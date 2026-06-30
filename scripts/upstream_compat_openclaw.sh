#!/usr/bin/env bash
# Upstream-compatibility check: does `mmb openclaw install` still work against the
# LATEST public OpenClaw release?
#
# Public + secret-free: installs OpenClaw from public npm and exercises our
# installer against a throwaway home. No MonsterMailbox account, API key, or real
# email — this tests installer/plugin *mechanics*, not delivery (that lives in the
# private watchdog).
#
# Exit codes: 0 = compatible · 1 = our installer/plugin broke against working
# upstream · 78 = upstream itself could not be installed (neutral, not our fault).
set -uo pipefail
MMB="${MMB:-mmb}"
NEUTRAL=78
OC_VERSION="${OC_VERSION:-latest}"

echo "== installing openclaw@${OC_VERSION} from public npm =="
if ! npm install -g "openclaw@${OC_VERSION}" >/tmp/oc_npm.log 2>&1; then
  echo "::warning::npm install -g openclaw@${OC_VERSION} failed (upstream); neutral skip"
  tail -20 /tmp/oc_npm.log || true
  exit $NEUTRAL
fi
command -v openclaw >/dev/null 2>&1 || { echo "::warning::openclaw CLI not on PATH after install; neutral skip"; exit $NEUTRAL; }
echo "openclaw: $(openclaw --version 2>&1 | head -1)"

OPENCLAW_HOME="$(mktemp -d)"
export OPENCLAW_HOME
printf '{"plugins":{}}\n' > "$OPENCLAW_HOME/openclaw.json"

echo "== mmb openclaw install =="
if ! "$MMB" openclaw install --home "$OPENCLAW_HOME" --force; then
  echo "::error::mmb openclaw install failed against openclaw@${OC_VERSION}"
  exit 1
fi

fail=0
ext="$OPENCLAW_HOME/extensions/monstermailbox"

# 1. Backstop job written with the schema OpenClaw's scheduler expects.
python3 - "$OPENCLAW_HOME/cron/jobs.json" <<'PY' || fail=1
import json, sys
d = json.load(open(sys.argv[1]))
jobs = [j for j in d.get("jobs", []) if j.get("name") == "MonsterMailbox inbox backstop"]
assert jobs, "backstop job missing from jobs.json"
j = jobs[0]
assert j.get("sessionTarget") == "isolated", f"sessionTarget={j.get('sessionTarget')!r}"
assert j.get("schedule", {}).get("kind") == "every", f"schedule={j.get('schedule')!r}"
assert j.get("payload", {}).get("kind") == "agentTurn", f"payload={j.get('payload')!r}"
print("jobs.json schema OK")
PY
[ "$fail" = 1 ] && echo "::error::backstop job missing or schema drifted in jobs.json"

# 2. openclaw.json patched: plugin enabled + path registered.
python3 - "$OPENCLAW_HOME/openclaw.json" "$ext" <<'PY' || fail=1
import json, sys
d = json.load(open(sys.argv[1])); ext = sys.argv[2]
e = d.get("plugins", {}).get("entries", {}).get("monstermailbox", {})
assert e.get("enabled") is True, f"entries.monstermailbox.enabled={e.get('enabled')!r}"
paths = d.get("plugins", {}).get("load", {}).get("paths", [])
assert ext in paths, f"{ext} not in load.paths={paths}"
print("openclaw.json patch OK")
PY
[ "$fail" = 1 ] && echo "::error::openclaw.json not patched as expected (config schema changed?)"

# 3. THE key upstream-contract check: our plugin module still imports against the
#    latest SDK (catches definePluginEntry / plugin-sdk subpath API drift). Run
#    from the plugin dir so it resolves via the node_modules/openclaw symlink the
#    installer created — exactly how OpenClaw loads it at runtime.
if [ -d "$ext/node_modules/openclaw" ]; then
  if ( cd "$ext" && node --input-type=module --eval 'await import("./index.js"); console.log("plugin import OK")' ); then
    :
  else
    echo "::error::our OpenClaw plugin no longer imports against latest SDK (openclaw/plugin-sdk/plugin-entry drift?)"
    fail=1
  fi
else
  echo "::warning::SDK not linked into the plugin (npm root -g unavailable?); skipped import check"
fi

# 4. Best-effort: openclaw's own inspector. Don't fail on it (may need more setup).
openclaw plugins inspect monstermailbox >/dev/null 2>&1 \
  && echo "openclaw plugins inspect: OK" \
  || echo "::warning::openclaw plugins inspect monstermailbox did not pass (informational)"

[ "$fail" = 0 ] && echo "RESULT: OpenClaw compatible with openclaw@${OC_VERSION}"
exit $fail
