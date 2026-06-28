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
import logging
import os
import shutil
from typing import Optional

from gateway.platforms.base import BasePlatformAdapter, MessageEvent, MessageType, SendResult
from gateway.config import Platform

_PLUGIN_DIR = os.path.dirname(os.path.abspath(__file__))

# Turn OFF Hermes' per-sender pairing/authz for this platform. The MonsterMailbox
# server already gates inbound by trust-state (we only ingest --state trusted), so
# the gateway's pairing handshake is redundant and would email pairing codes to
# unknown senders instead of processing their mail. Authorizing all senders for
# THIS platform makes the server's trust-state the single gate. Override by setting
# MMB_ALLOW_ALL_SENDERS=false (and/or an MMB_ALLOWED_SENDERS allowlist) before start.
os.environ.setdefault("MMB_ALLOW_ALL_SENDERS", "true")

# Suppress Hermes' one-time "📬 No home channel is set" prompt, which it emails on
# the first message of every new thread when no home-channel env var is set. This
# platform is reply-only and intentionally has no home channel — and send() drops
# any home-channel delivery anyway (no message to reply to) — so setting the env
# var simply silences the prompt by design (run.py: `if not os.getenv(env_key)`).
os.environ.setdefault("MONSTERMAILBOX_HOME_CHANNEL", "disabled")


def _mmb_bin() -> str:
    # Resolve the mmb binary robustly. The gateway's PATH often does NOT include
    # the mmb install dir, so don't rely on a bare "mmb": prefer an explicit
    # MMB_BIN, then the absolute path the installer recorded next to this plugin
    # (mmb_path), then a PATH lookup, then bare "mmb".
    env = os.getenv("MMB_BIN")
    if env:
        return env
    try:
        with open(os.path.join(_PLUGIN_DIR, "mmb_path")) as f:
            recorded = f.read().strip()
        if recorded and os.path.exists(recorded):
            return recorded
    except OSError:
        pass
    return shutil.which("mmb") or "mmb"


def _mmb_env() -> dict:
    # mmb resolves its file-backed profile under $HOME. A supervised gateway may
    # run with a different HOME than the install/interactive shell (→ mmb 401).
    # Use MMB_HOME, else the HOME the installer recorded next to this plugin
    # (mmb_home), else the current environment unchanged.
    env = dict(os.environ)
    home = os.getenv("MMB_HOME")
    if not home:
        try:
            with open(os.path.join(_PLUGIN_DIR, "mmb_home")) as f:
                home = f.read().strip()
        except OSError:
            home = ""
    if home:
        env["HOME"] = home
    return env


def check_monstermailbox_requirements() -> bool:
    """Enabled only when the mmb CLI is present AND authenticated."""
    import subprocess

    try:
        r = subprocess.run([_mmb_bin(), "whoami"], capture_output=True, timeout=10, env=_mmb_env())
        return r.returncode == 0
    except Exception:
        return False


# Non-reply surfaces (the "⏳ Working…" heartbeat, tool progress, interim
# chatter, lifecycle notices) are suppressed BY DESIGN, not by content matching:
# `mmb hermes install` registers this platform at Hermes's minimal/non-interactive
# display tier (display.platforms.monstermailbox) so the gateway never generates
# them for us. send() therefore just emails the agent's real reply.


class MonsterMailboxAdapter(BasePlatformAdapter):
    MAX_MESSAGE_LENGTH = 50_000

    def __init__(self, config):
        # "monstermailbox" is minted dynamically via Platform._missing_().
        super().__init__(config, Platform("monstermailbox"))
        # The base adapter doesn't reliably provide self.logger across Hermes
        # versions; ensure one exists so warning/error paths don't AttributeError
        # (which would silently kill the watcher loop).
        if getattr(self, "logger", None) is None:
            self.logger = logging.getLogger("monstermailbox")
        self._watch_task: Optional[asyncio.Task] = None
        self._stop = asyncio.Event()
        self._seen: set[str] = set()
        self._reply_target: dict[str, str] = {}  # chat_id -> latest message_id
        allow = os.getenv("MMB_ALLOWED_SENDERS", "").strip()
        self._allowed = {a.strip().lower() for a in allow.split(",") if a.strip()}

    async def connect(self, **kwargs) -> bool:
        # Hermes passes lifecycle kwargs (e.g. is_reconnect) — accept and ignore.
        self._stop.clear()
        if self._watch_task and not self._watch_task.done():
            return True  # already running; never spawn a duplicate watcher
        self._watch_task = asyncio.create_task(self._poll_loop())
        return True

    async def disconnect(self, **kwargs):
        self._stop.set()
        if self._watch_task:
            self._watch_task.cancel()
            try:
                await self._watch_task
            except asyncio.CancelledError:
                pass

    async def _poll_loop(self):
        # Drive inbound via repeated `mmb inbox wait` (one event per call) plus an
        # inbox reconcile each iteration. This is robust where a long-lived
        # `mmb inbox watch` goes silent, and it self-heals after `mmb update`:
        # each iteration spawns a fresh process on the CURRENT binary instead of
        # holding a replaced/deleted executable open.
        backoff = 1.0
        while not self._stop.is_set():
            # 1) Reconcile: pick up anything already queued (covers the gap
            #    between successive wait calls). _on_inbox_new dedups via _seen.
            try:
                listing = await self._mmb_json(
                    "inbox", "list", "--work-state", "inbox", "--state", "trusted", "--peek"
                )
                for m in (listing or {}).get("messages", []):
                    if self._stop.is_set():
                        break
                    await self._on_inbox_new({"message_id": m.get("id")})
            except asyncio.CancelledError:
                raise
            except Exception as e:
                self.logger.warning("MonsterMailbox reconcile error: %s", e)

            if self._stop.is_set():
                break

            # 2) Block for the next event with `wait` (one event per invocation).
            proc = None
            try:
                proc = await asyncio.create_subprocess_exec(
                    _mmb_bin(), "inbox", "wait", "--state", "trusted", "--timeout", "120s",
                    stdout=asyncio.subprocess.PIPE,
                    stderr=asyncio.subprocess.DEVNULL,
                    env=_mmb_env(),
                )
                out, _ = await proc.communicate()
                backoff = 1.0
                if self._stop.is_set():
                    break
                line = out.decode("utf-8", "replace").strip()
                if line:
                    try:
                        evt = json.loads(line.splitlines()[-1])
                        if evt.get("event") == "inbox.new":
                            await self._on_inbox_new(evt.get("data") or {})
                    except json.JSONDecodeError:
                        pass
                # wait exits non-zero on timeout with no event — just loop.
                continue
            except asyncio.CancelledError:
                raise
            except Exception as e:
                self.logger.warning("MonsterMailbox wait error: %s", e)
            finally:
                if proc and proc.returncode is None:
                    proc.terminate()
            # Reached only on exception: back off before retrying.
            if self._stop.is_set():
                break
            await asyncio.sleep(backoff)
            backoff = min(backoff * 2, 30.0)

    async def _on_inbox_new(self, data: dict):
        msg_id = str(data.get("message_id") or "")
        if not msg_id or msg_id in self._seen:
            return
        self._seen.add(msg_id)

        # `mmb msg get` emits JSON with --peek; there is no --json flag.
        thread = await self._mmb_json("msg", "get", msg_id, "--peek")
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
        # Don't email an empty body. Status/progress surfaces are suppressed at
        # the source by the minimal display tier (see install), so anything that
        # reaches here with content is a real reply.
        if not content or not str(content).strip():
            return SendResult(success=True, message_id="suppressed-empty")
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
            env=_mmb_env(),
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
        env=_mmb_env(),
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
        # Intentionally NOT a cron/home-delivery channel: this is a receive-and-
        # reply platform only. Registering a cron_deliver_env_var made Hermes treat
        # it as a home channel and error "no home channel" when no home address was
        # set, and routed proactive/system notices over email. Omitting it keeps
        # the platform reply-only.
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
