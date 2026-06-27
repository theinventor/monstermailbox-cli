"""MonsterMailbox -> Hermes gateway adapter.

Streams inbound agent email over the MonsterMailbox SSE feed by shelling out to
`mmb inbox watch --json --state trusted` (reuses the CLI's existing auth), turns
each inbox.new into a Hermes agent turn, and delivers the agent's reply via
`mmb reply-all`. No webhook, no public endpoint.

Installed by `mmb hermes install`, which also pins the toolset so the turn has a
terminal tool:

    # ~/.hermes/config.yaml
    platform_toolsets:
      monstermailbox: [hermes-cli]
    command_allowlist:
      - "mmb *"

Without that toolset entry the platform would default to a non-existent
`hermes-monstermailbox` toolset and the agent would have no shell — the exact
failure the plain webhook channel has.
"""

import asyncio
import json
import os
from typing import Optional

from gateway.platforms.base import BasePlatformAdapter, MessageEvent, MessageType, SendResult
from gateway.config import Platform


def _mmb_bin() -> str:
    return os.getenv("MMB_BIN", "mmb")


def check_monstermailbox_requirements() -> bool:
    """Enabled only when the mmb CLI is present AND authenticated."""
    import subprocess

    try:
        r = subprocess.run([_mmb_bin(), "whoami"], capture_output=True, timeout=10)
        return r.returncode == 0
    except Exception:
        return False


class MonsterMailboxAdapter(BasePlatformAdapter):
    MAX_MESSAGE_LENGTH = 50_000

    def __init__(self, config):
        # "monstermailbox" is minted dynamically via Platform._missing_().
        super().__init__(config, Platform("monstermailbox"))
        self._watch_task: Optional[asyncio.Task] = None
        self._stop = asyncio.Event()
        self._seen: set[str] = set()
        self._reply_target: dict[str, str] = {}  # chat_id -> latest message_id
        allow = os.getenv("MMB_ALLOWED_SENDERS", "").strip()
        self._allowed = {a.strip().lower() for a in allow.split(",") if a.strip()}

    async def connect(self) -> bool:
        self._stop.clear()
        self._watch_task = asyncio.create_task(self._watch_loop())
        return True

    async def disconnect(self):
        self._stop.set()
        if self._watch_task:
            self._watch_task.cancel()
            try:
                await self._watch_task
            except asyncio.CancelledError:
                pass

    async def _watch_loop(self):
        backoff = 1.0
        while not self._stop.is_set():
            proc = None
            try:
                proc = await asyncio.create_subprocess_exec(
                    _mmb_bin(), "inbox", "watch", "--json", "--state", "trusted",
                    stdout=asyncio.subprocess.PIPE,
                    stderr=asyncio.subprocess.DEVNULL,
                )
                backoff = 1.0
                assert proc.stdout is not None
                async for raw in proc.stdout:
                    if self._stop.is_set():
                        break
                    line = raw.decode("utf-8", "replace").strip()
                    if not line:
                        continue
                    try:
                        evt = json.loads(line)
                    except json.JSONDecodeError:
                        continue
                    if evt.get("event") == "inbox.new":
                        await self._on_inbox_new(evt.get("data") or {})
            except asyncio.CancelledError:
                raise
            except Exception as e:
                self.logger.warning("MonsterMailbox watch error: %s", e)
            finally:
                if proc and proc.returncode is None:
                    proc.terminate()
            if self._stop.is_set():
                break
            await asyncio.sleep(backoff)
            backoff = min(backoff * 2, 30.0)

    async def _on_inbox_new(self, data: dict):
        msg_id = str(data.get("message_id") or "")
        if not msg_id or msg_id in self._seen:
            return
        self._seen.add(msg_id)

        thread = await self._mmb_json("msg", "get", msg_id, "--peek", "--json")
        thread = thread or {}
        sender = str((thread.get("from") or {}).get("email") or data.get("from") or "").lower()
        if self._allowed and sender not in self._allowed:
            self.logger.info("MonsterMailbox: sender %s not allow-listed; ignoring", sender)
            return
        subject = thread.get("subject") or data.get("subject") or "(no subject)"
        body = thread.get("body_text") or ""

        chat_id = sender or msg_id
        self._reply_target[chat_id] = msg_id

        source = self.build_source(
            chat_id=chat_id,
            chat_name=sender,
            chat_type="dm",
            user_id=sender,
            user_name=sender,
            thread_id=str(thread.get("thread_root_message_id") or msg_id),
            message_id=msg_id,
        )
        text = f"Subject: {subject}\nFrom: {sender}\n\n{body}"
        event = MessageEvent(
            text=text, message_type=MessageType.TEXT, source=source,
            message_id=msg_id, raw_message=thread or data,
        )
        await self.handle_message(event)

    async def send(self, chat_id, content, reply_to=None, metadata=None) -> SendResult:
        msg_id = (metadata or {}).get("message_id") or self._reply_target.get(chat_id)
        if not msg_id:
            return SendResult(success=False, error="no MonsterMailbox message to reply to")
        res = await self._mmb_json("reply-all", str(msg_id), "--body-html", content)
        if res is None:
            return SendResult(success=False, error="mmb reply-all failed")
        return SendResult(success=True, message_id=str(res.get("outbound_id") or msg_id))

    async def send_typing(self, chat_id):
        return None

    async def send_image(self, chat_id, image_url, caption=None, **kwargs) -> SendResult:
        return await self.send(chat_id, caption or "", metadata={})

    async def get_chat_info(self, chat_id) -> dict:
        return {"name": chat_id, "type": "dm", "chat_id": chat_id}

    async def _mmb_json(self, *args) -> Optional[dict]:
        proc = await asyncio.create_subprocess_exec(
            _mmb_bin(), *args,
            stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE,
        )
        out, err = await proc.communicate()
        if proc.returncode != 0:
            self.logger.warning("mmb %s failed: %s", args[0], err.decode("utf-8", "replace"))
            return None
        try:
            return json.loads(out.decode("utf-8", "replace") or "{}")
        except json.JSONDecodeError:
            return {}


async def _standalone_send(pconfig, chat_id, message, *, thread_id=None,
                           media_files=None, force_document=False):
    """Out-of-process delivery (cron / `hermes send`) via the mmb CLI."""
    proc = await asyncio.create_subprocess_exec(
        _mmb_bin(), "new-email", "--to", chat_id, "--subject", "Hermes Agent",
        "--body-html", message,
        stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE,
    )
    out, err = await proc.communicate()
    if proc.returncode != 0:
        return {"error": err.decode("utf-8", "replace")}
    return {"success": True, "platform": "monstermailbox", "chat_id": chat_id}


def _build_adapter(config):
    return MonsterMailboxAdapter(config)


def _is_connected(config) -> bool:
    return check_monstermailbox_requirements()


def register(ctx) -> None:
    ctx.register_platform(
        name="monstermailbox",
        label="MonsterMailbox",
        adapter_factory=_build_adapter,
        check_fn=check_monstermailbox_requirements,
        is_connected=_is_connected,
        required_env=[],
        install_hint="Requires the authenticated `mmb` CLI on PATH (run `mmb whoami`).",
        allowed_users_env="MMB_ALLOWED_SENDERS",
        allow_all_env="MMB_ALLOW_ALL_SENDERS",
        cron_deliver_env_var="MMB_HOME_ADDRESS",
        standalone_sender_fn=_standalone_send,
        max_message_length=50_000,
        pii_safe=True,
        emoji="📬",
        platform_hint=(
            "You are handling email via MonsterMailbox. Your FINAL message is sent "
            "automatically as the email reply — write it as a complete, plain-language "
            "reply. Do NOT run `mmb reply-all` yourself (that double-sends). Use the "
            "terminal tool with `mmb` only for workflow actions: `mmb msg claim <id>`, "
            "`mmb msg done <id>`, `mmb whitelist`, quarantine handling."
        ),
        allow_update_command=True,
    )
