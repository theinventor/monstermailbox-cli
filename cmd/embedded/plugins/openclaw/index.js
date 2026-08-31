// MonsterMailbox → OpenClaw plugin.
//
// Streams inbound agent email from MonsterMailbox over the SSE event feed
// (`mmb inbox wait`) and dispatches an agent turn per message. The agent
// replies by running `mmb reply-all` via the exec tool (no webhook required).
//
// Installed and configured by `mmb openclaw install`. The installer also
// symlinks node_modules/openclaw -> the globally-installed openclaw package so
// the SDK import below resolves from ~/.openclaw/extensions.
//
// Lifecycle note: a long-lived watcher MUST be a registered SERVICE
// (start/stop) — `register()` returns void, so returning a `{dispose}` object
// from it does NOT manage anything. (Confirmed the hard way on a live install.)

import { spawn } from "node:child_process";
import { definePluginEntry } from "openclaw/plugin-sdk/plugin-entry";

const DEFAULT_SESSION_KEY = "agent:main:subagent:monstermailbox";

export default definePluginEntry({
  id: "monstermailbox",
  name: "MonsterMailbox",
  description:
    "Streams inbound agent email from MonsterMailbox (mmb inbox wait) and replies via the mmb CLI. No webhook required.",
  register(api) {
    const mode = api.registrationMode ?? "full";
    const logger = api.logger ?? api.log ?? console;
    const cfg =
      api.pluginConfig ??
      api.config?.plugins?.entries?.monstermailbox?.config ??
      api.config ??
      {};
    const mmb = cfg.mmbBin ?? "mmb";
    const mmbProfile = String(cfg.mmbProfile ?? "").trim();
    const sessionKey = normalizeSessionKey(cfg.sessionKey ?? DEFAULT_SESSION_KEY);
    const state = cfg.state ?? "trusted";
    const allowed = new Set((cfg.allowedSenders ?? []).map((s) => String(s).toLowerCase()));

    if (mode !== "full") return;

    const seen = new Set(); // dedup on message_id
    // Concurrency queue: a single consumer drains message ids one at a time so a
    // second email arriving while a turn is in progress is queued, never dropped.
    // (Without this, the busy session silently skips concurrent inbound.)
    const queue = [];
    let draining = false;
    const children = new Set();
    const timers = new Set();

    function sleep(ms) {
      return new Promise((resolve) => {
        const timer = setTimeout(() => {
          timers.delete(timer);
          resolve();
        }, ms);
        timers.add(timer);
      });
    }

    function runMmb(args, { timeoutMs = 30000 } = {}) {
      return new Promise((resolve) => {
        const finalArgs = mmbProfile ? [...args, "--profile", mmbProfile] : args;
        const env = { ...process.env };
        if (mmbProfile) delete env.MONSTERMAILBOX_API_KEY;
        const p = spawn(mmb, finalArgs, { stdio: ["ignore", "pipe", "pipe"], env });
        children.add(p);
        let out = "";
        let err = "";
        let settled = false;
        const done = (result) => {
          if (settled) return;
          settled = true;
          clearTimeout(timeout);
          children.delete(p);
          resolve(result);
        };
        const timeout = setTimeout(() => {
          p.kill("SIGTERM");
          setTimeout(() => p.kill("SIGKILL"), 5000).unref?.();
        }, timeoutMs);
        timeout.unref?.();
        p.stdout.on("data", (d) => {
          out += d;
          if (out.length > 1024 * 1024) out = out.slice(-1024 * 1024);
        });
        p.stderr.on("data", (d) => {
          err += d;
          if (err.length > 64 * 1024) err = err.slice(-64 * 1024);
        });
        p.on("error", (error) => done({ ok: false, code: null, signal: null, stdout: out, stderr: err, error }));
        p.on("close", (code, signal) => {
          done({ ok: code === 0, code, signal, stdout: out, stderr: err });
        });
      });
    }

    async function mmbJson(args, opts) {
      const result = await runMmb(args, opts);
      if (!result.ok) {
        logger.warn?.(
          `mmb ${args.join(" ")} failed: ${result.error?.message ?? result.stderr.trim() ?? result.signal ?? result.code}`,
        );
        return null;
      }
      return parseJsonFromOutput(result.stdout) ?? {};
    }

    function jsonLines(stdout) {
      const lines = String(stdout || "")
        .split(/\r?\n/)
        .map((line) => line.trim())
        .filter((line) => line.startsWith("{") || line.startsWith("["));
      return lines;
    }

    function parseJsonFromOutput(stdout) {
      const text = String(stdout || "").trim();
      if (!text) return {};
      try {
        return JSON.parse(text);
      } catch {
        // Older mmb versions print a human summary line after the JSON payload.
      }
      for (const line of jsonLines(stdout)) {
        try {
          return JSON.parse(line);
        } catch {
          // keep scanning
        }
      }
      return null;
    }

    function parseLastJsonLine(stdout) {
      const lines = jsonLines(stdout);
      for (let i = lines.length - 1; i >= 0; i--) {
        try {
          return JSON.parse(lines[i]);
        } catch {
          // Keep scanning: CLI warnings can precede the machine JSON line.
        }
      }
      return null;
    }

    function messageIdFrom(data) {
      return String(data?.message_id ?? data?.id ?? data?.message?.id ?? "");
    }

    function normalizeSessionKey(value) {
      const raw = String(value || "").trim();
      if (!raw) return DEFAULT_SESSION_KEY;
      if (raw.startsWith("agent:") || raw === "global" || raw === "unknown") return raw;
      return `agent:main:${raw}`;
    }

    function enqueue(id) {
      id = String(id || "");
      if (!id || seen.has(id)) return;
      seen.add(id);
      queue.push(id);
      logger.info?.(`MonsterMailbox queued message ${id}`);
      void drain();
    }

    async function reconcile() {
      const listing = await mmbJson(["inbox", "list", "--work-state", "inbox", "--state", state, "--peek"]);
      const messages = Array.isArray(listing?.messages) ? listing.messages : [];
      if (messages.length) {
        logger.info?.(`MonsterMailbox reconcile found ${messages.length} inbox message(s)`);
      }
      for (const msg of messages) {
        enqueue(msg?.id);
      }
    }

    async function waitForNextEvent() {
      const result = await runMmb(["inbox", "wait", "--state", state, "--timeout", "120s"], { timeoutMs: 150000 });
      if (!result.ok && !result.stdout.trim()) {
        // A timeout with no mail is the normal idle path.
        return null;
      }
      const evt = parseLastJsonLine(result.stdout);
      if (!evt && !result.ok && result.stderr.trim()) {
        logger.warn?.(`mmb inbox wait failed: ${result.stderr.trim()}`);
      }
      return evt;
    }

    async function pollLoop() {
      let backoff = 1000;
      while (!stopped) {
        try {
          await reconcile();
          if (stopped) break;

          const evt = await waitForNextEvent();
          if (evt?.event === "inbox.new") enqueue(messageIdFrom(evt.data));
          serviceHealth?.clearFailure?.();
          backoff = 1000;
        } catch (e) {
          serviceHealth?.reportFailure?.(e);
          logger.warn?.(`MonsterMailbox poll error: ${e?.message ?? e}`);
          await sleep(backoff);
          backoff = Math.min(backoff * 2, 30000);
        }
      }
    }

    async function reconcileLoop() {
      while (!stopped) {
        try {
          await reconcile();
        } catch (e) {
          logger.warn?.(`MonsterMailbox periodic reconcile error: ${e?.message ?? e}`);
        }
        await sleep(3600 * 1000);
      }
    }

    function stopChildren() {
      for (const timer of timers) clearTimeout(timer);
      timers.clear();
      for (const child of children) {
        try {
          child.kill();
        } catch {
          // already exited
        }
      }
      children.clear();
    }

    async function handle(id) {
      // `mmb msg get` emits JSON with --peek; there is no --json flag.
      const thread = (await mmbJson(["msg", "get", String(id), "--peek"])) ?? {};
      const sender = String(thread?.from?.email ?? thread?.from ?? "").toLowerCase();
      if (allowed.size && !allowed.has(sender)) {
        logger.info?.(`MonsterMailbox: sender ${sender} not allow-listed; skipping ${id}`);
        return;
      }
      const subject = thread?.subject ?? "(no subject)";
      logger.info?.(`MonsterMailbox dispatching ${id} to OpenClaw session ${sessionKey}`);
      const message = [
        `You have a new email in your MonsterMailbox (mmb) inbox: message ${id} from ${sender}, subject "${subject}".`,
        "",
        "Handle this now with the authenticated `mmb` CLI on this Mac Mini.",
        `1. Claim it first: \`mmb msg claim ${id} --claimed-by claudito --note "OpenClaw handling"\`.`,
        `2. Read it with \`mmb msg get ${id} --peek\`; inspect attachments only if the message requires them.`,
        "3. If a human is waiting for a reply, send it with `mmb reply-all` and an idempotency key.",
        `4. Finish with a disposition: \`mmb msg awaiting-reply ${id}\` after a reply, \`mmb msg done ${id}\` when complete without needing another sender response, \`mmb msg skip ${id} --reason "..."\` when no action is appropriate, or \`mmb msg block ${id} --note "..."\` when blocked.`,
        "",
        "If your MonsterMailbox skill is available, use it. If it is not available, follow the workflow above directly. Do not merely acknowledge this prompt; either disposition the message or ask Troy for help in your primary channel.",
      ].join("\n");

      if (!api.runtime?.subagent?.run) {
        throw new Error("OpenClaw runtime subagent runner is unavailable");
      }
      const run = await api.runtime.subagent.run({ sessionKey, message, deliver: false });
      logger.info?.(
        `MonsterMailbox dispatched ${id} to OpenClaw run ${run.runId} (session ${run.sessionKey ?? sessionKey})`,
      );
    }

    async function drain() {
      if (draining) return;
      draining = true;
      try {
        while (queue.length) {
          const id = queue.shift();
          try {
            await handle(id);
          } catch (e) {
            logger.warn?.(`MonsterMailbox: handling ${id} failed: ${e?.message ?? e}`);
          }
        }
      } finally {
        draining = false;
      }
    }

    let stopped = false;
    let serviceHealth = null;

    api.registerService({
      id: "monstermailbox-watcher",
      async start(ctx) {
        serviceHealth = ctx?.serviceHealth ?? null;
        stopped = false;
        void pollLoop();
        void reconcileLoop();
        logger.info?.("MonsterMailbox watcher started (mmb inbox wait + reconcile)");
      },
      async stop() {
        stopped = true;
        stopChildren();
      },
    });
  },
});
