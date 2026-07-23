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


# The plugin is a THIN TRIGGER. It does NOT read, reply to, claim, or dispose
# email. On each inbound it wakes the agent and points it at the agent's OWN
# email-handling skill (see _on_inbox_new). That skill owns everything: triage,
# the reply (`mmb reply-all`, sent ONLY when a human is awaiting a response), and
# disposition (`mmb msg claim/done/block`). send() therefore never emails anything
# (see send()), which is what makes over-replying, double-replies, and status-
# chrome leaks structurally impossible — there is no auto-send path to misfire.


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
        self._reconcile_task: Optional[asyncio.Task] = None
        self._stop = asyncio.Event()
        self._seen: set[str] = set()  # msg ids we've already woken the agent for
        allow = os.getenv("MMB_ALLOWED_SENDERS", "").strip()
        self._allowed = {a.strip().lower() for a in allow.split(",") if a.strip()}

    async def connect(self, **kwargs) -> bool:
        # Hermes passes lifecycle kwargs (e.g. is_reconnect) — accept and ignore.
        self._stop.clear()
        if self._watch_task and not self._watch_task.done():
            return True  # already running; never spawn a duplicate watcher
        self._watch_task = asyncio.create_task(self._poll_loop())
        # Independent safety-net task — does NOT share fate with the watcher, so
        # a wedged/dead _poll_loop can't take the reconcile down with it.
        if self._reconcile_task is None or self._reconcile_task.done():
            self._reconcile_task = asyncio.create_task(self._reconcile_loop())
        return True

    async def disconnect(self, **kwargs):
        self._stop.set()
        for task in (self._watch_task, self._reconcile_task):
            if task:
                task.cancel()
                try:
                    await task
                except asyncio.CancelledError:
                    pass

    async def _reconcile(self):
        """Wake the agent for any undispositioned trusted inbox mail the SSE
        watcher didn't deliver. Dedups via _seen, so it never double-wakes a
        message already handled in real time. Safe to run concurrently with the
        watcher and the periodic loop."""
        listing = await self._mmb_json(
            "inbox", "list", "--work-state", "inbox", "--state", "trusted", "--peek"
        )
        for m in (listing or {}).get("messages", []):
            if self._stop.is_set():
                break
            await self._on_inbox_new({"message_id": m.get("id")})

    async def _reconcile_loop(self):
        # Independent hourly safety net. The real-time path is the SSE watcher;
        # this exists solely to guarantee a hard floor if that path ever fails
        # silently (server-side delivery wedge, a wedged watcher, etc.) — mail is
        # then caught within the interval instead of sitting forever. Runs once
        # immediately (flushes any backlog on connect) then every hour. An error
        # here must never kill the loop — that silent-death is the whole bug class
        # we're defending against.
        while not self._stop.is_set():
            try:
                await self._reconcile()
            except asyncio.CancelledError:
                raise
            except Exception as e:
                self.logger.warning("MonsterMailbox periodic reconcile error: %s", e)
            # Interruptible sleep: wakes immediately on disconnect.
            try:
                await asyncio.wait_for(self._stop.wait(), timeout=3600)
            except asyncio.TimeoutError:
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
            #    between successive `wait` calls — the SSE resume cursor does
            #    NOT persist across separate `wait` processes). _seen dedups.
            try:
                await self._reconcile()
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
                # Hard wall-clock guard. `mmb inbox wait` self-bounds at 120s,
                # but NEVER let a wedged child freeze this loop — the reconcile
                # above shares it, and an unguarded communicate() on a hung
                # `wait` is exactly what stalled a live agent for 7 days.
                out, _ = await asyncio.wait_for(proc.communicate(), timeout=150)
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

        chat_id = sender or msg_id
        source = self.build_source(
            chat_id=chat_id,
            chat_name=sender,
            chat_type="dm",
            user_id=sender,
            user_name=sender,
            thread_id=str(thread.get("thread_root_message_id") or msg_id),
            message_id=msg_id,
        )
        # Thin trigger: wake the agent and point it at its OWN email skill. We pass
        # id + sender + subject; the skill reads the body (`mmb msg get`), triages,
        # replies (`mmb reply-all`) only when warranted, and dispositions the
        # message. The plugin does none of that. If the agent has no matching skill,
        # the prompt tells it to escalate to its human rather than guess or reply.
        text = (
            f'You have a new email in your MonsterMailbox (mmb) inbox: message {msg_id} '
            f'from {sender}, subject "{subject}". Find your skill for handling '
            f"MonsterMailbox email and run it. Hint: it should include guidance for how "
            f"to triage and how to add dispositions to the messages with mmb. If you "
            f"can't find a relevant skill for this, do not guess or reply — ask your "
            f"human what to do in your primary channel with them."
        )
        event = MessageEvent(
            text=text, message_type=MessageType.TEXT, source=source,
            message_id=msg_id, raw_message=thread or data,
        )
        await self.handle_message(event)

    async def handle_message(self, event) -> None:
        # Hermes injects gateway-internal lifecycle events — background-process
        # completion notices ("[IMPORTANT: Background process … exited (exit code
        # N)]"), CLI-handoff prompts — as synthetic MessageEvent(internal=True)
        # down the SAME inbound path as real email. On a reply-only email platform
        # each one spawns a fresh agent turn and a real reply-all: a DUPLICATE
        # email with no actual inbound (the root cause of "extra emails within a
        # minute"). Drop internal events here so ONLY genuine inbound email
        # (internal=False, which is what _on_inbox_new builds) can produce a reply.
        # Startup-resume uses a separate dispatch path (not handle_message) and is
        # unaffected; the backstop cron re-picks any inbox mail this might skip.
        if getattr(event, "internal", False):
            preview = (getattr(event, "text", "") or "").replace("\n", " ")[:80]
            self.logger.info("MonsterMailbox: dropped internal gateway event (no email reply): %s", preview)
            return
        return await super().handle_message(event)

    async def send(self, chat_id, content, reply_to=None, metadata=None) -> SendResult:
        # The plugin never auto-sends email. Each inbound wakes the agent to run
        # its own email skill, and THAT skill sends replies via `mmb reply-all`
        # (only when a reply is warranted) and dispositions the message. So
        # whatever the turn produces and hands here — a reply the skill already
        # sent, "no action needed" narration, or gateway status chrome — is simply
        # not emailed. This is what makes over-replying / double-replies / chrome
        # leaks impossible: there is no auto-send path.
        return SendResult(success=True, message_id="not-auto-sent")

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
            "MonsterMailbox delivers inbound email by waking you with a short prompt "
            "per message; it does NOT auto-send anything you write. Handle email by "
            "running your own email-handling skill, which triages, replies via "
            "`mmb reply-all <id>` ONLY when a human is awaiting a response, and "
            "dispositions the message with `mmb msg claim/done/block <id>`. Your turn "
            "text is never emailed."
        ),
        allow_update_command=True,
    )
