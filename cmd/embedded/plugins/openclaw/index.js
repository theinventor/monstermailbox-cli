// MonsterMailbox → OpenClaw plugin.
//
// Streams inbound agent email from MonsterMailbox over the SSE event feed
// (`mmb inbox watch`) and dispatches an agent turn per message. The agent
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

export default definePluginEntry({
  id: "monstermailbox",
  name: "MonsterMailbox",
  description:
    "Streams inbound agent email from MonsterMailbox (mmb inbox watch) and replies via the mmb CLI. No webhook required.",
  register(api) {
    const cfg = api.config ?? {};
    const mmb = cfg.mmbBin ?? "mmb";
    const sessionKey = cfg.sessionKey ?? "openclaw_main";
    const state = cfg.state ?? "trusted";
    const allowed = new Set((cfg.allowedSenders ?? []).map((s) => String(s).toLowerCase()));

    const seen = new Set(); // dedup on message_id
    // Concurrency queue: a single consumer drains message ids one at a time so a
    // second email arriving while a turn is in progress is queued, never dropped.
    // (Without this, the busy session silently skips concurrent inbound.)
    const queue = [];
    let draining = false;

    function mmbJson(args) {
      return new Promise((resolve) => {
        const p = spawn(mmb, args);
        let out = "";
        p.stdout.on("data", (d) => (out += d));
        p.on("error", () => resolve(null));
        p.on("close", (code) => {
          if (code !== 0) return resolve(null);
          try {
            resolve(JSON.parse(out || "{}"));
          } catch {
            resolve({});
          }
        });
      });
    }

    async function handle(id) {
      // `mmb msg get` emits JSON with --peek; there is no --json flag.
      const thread = await mmbJson(["msg", "get", String(id), "--peek"]);
      const sender = String(thread?.from?.email ?? thread?.from ?? "").toLowerCase();
      if (allowed.size && !allowed.has(sender)) {
        api.log?.info?.(`MonsterMailbox: sender ${sender} not allow-listed; skipping ${id}`);
        return;
      }
      const subject = thread?.subject ?? "(no subject)";
      const body = thread?.body_text ?? "";
      const message =
        `A new email arrived via MonsterMailbox.\n` +
        `From: ${sender}\nSubject: ${subject}\nMessage-ID: ${id}\n\n${body}\n\n` +
        `Handle it via the exec tool with the mmb CLI:\n` +
        `  ${mmb} msg claim ${id}        # mark in-progress when you start\n` +
        `  ${mmb} reply-all ${id} --body-html "<your reply>"   # reply\n` +
        `  ${mmb} msg done ${id}         # mark handled when finished`;

      await api.runtime.subagent.run({ sessionKey, message, deliver: false });
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
            api.log?.warn?.(`MonsterMailbox: handling ${id} failed: ${e?.message ?? e}`);
          }
        }
      } finally {
        draining = false;
      }
    }

    let child = null;
    let stopped = false;
    let buf = "";

    function startWatch() {
      if (stopped) return;
      child = spawn(mmb, ["inbox", "watch", "--json", "--state", state]);
      buf = "";
      child.stdout.on("data", (chunk) => {
        buf += chunk.toString();
        let nl;
        while ((nl = buf.indexOf("\n")) >= 0) {
          const line = buf.slice(0, nl).trim();
          buf = buf.slice(nl + 1);
          if (!line) continue;
          let evt;
          try {
            evt = JSON.parse(line);
          } catch {
            continue;
          }
          if (evt.event !== "inbox.new") continue;
          const id = String(evt.data?.message_id ?? "");
          if (!id || seen.has(id)) continue;
          seen.add(id);
          queue.push(id);
          void drain();
        }
      });
      child.on("error", (e) => api.log?.warn?.(`MonsterMailbox watcher error: ${e?.message ?? e}`));
      // `mmb inbox watch` auto-reconnects internally; if the process itself
      // exits (e.g. auth failure), back off and respawn unless we're stopping.
      child.on("close", () => {
        child = null;
        if (!stopped) setTimeout(startWatch, 2000);
      });
    }

    api.registerService({
      id: "monstermailbox-watcher",
      async start() {
        stopped = false;
        startWatch();
        api.log?.info?.("MonsterMailbox watcher started (mmb inbox watch)");
      },
      async stop() {
        stopped = true;
        if (child) {
          child.kill();
          child = null;
        }
      },
    });
  },
});
