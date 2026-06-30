#!/usr/bin/env bash
# Upstream-compatibility check: does `mmb hermes install` still work against the
# LATEST Hermes upstream (github.com/nousresearch/hermes-agent)?
#
# Public + secret-free: installs Hermes from its public git repo and exercises our
# installer against a throwaway HERMES_HOME. No MonsterMailbox account/key/email —
# tests installer mechanics + Hermes's own config/cron tooling, not delivery.
#
# Exit codes: 0 = compatible · 1 = our installer broke against working upstream ·
# 78 = upstream itself could not be installed/run (neutral, not our fault).
set -uo pipefail
MMB="${MMB:-mmb}"
NEUTRAL=78
HERMES_REF="${HERMES_REF:-git+https://github.com/nousresearch/hermes-agent}"

echo "== installing Hermes from ${HERMES_REF} into a venv (best-effort, timeout ${HERMES_PIP_TIMEOUT:-900}s) =="
# Isolated venv (no system clobber, portable across Ubuntu 22/24 PEP-668). Wrapped
# in `timeout` so a heavy/slow upstream install degrades to NEUTRAL rather than
# hanging the runner or looking like our failure.
# hermes-agent requires Python 3.11–3.13; pick a compatible interpreter.
PYBIN=""
for p in python3.13 python3.12 python3.11; do command -v "$p" >/dev/null 2>&1 && { PYBIN="$p"; break; }; done
if [ -z "$PYBIN" ]; then echo "::warning::no Python 3.11–3.13 found (hermes-agent requires it); neutral skip"; exit $NEUTRAL; fi
echo "using $PYBIN ($($PYBIN --version 2>&1))"
VENV="$(mktemp -d)/venv"
"$PYBIN" -m venv "$VENV" || { echo "::warning::could not create venv with $PYBIN; neutral skip"; exit $NEUTRAL; }
"$VENV/bin/pip" install --quiet --upgrade pip >/dev/null 2>&1 || true
if ! timeout "${HERMES_PIP_TIMEOUT:-900}" "$VENV/bin/pip" install --quiet "${HERMES_REF}" >/tmp/hermes_pip.log 2>&1; then
  echo "::warning::pip install hermes-agent failed/timed out (upstream heavy/broken); neutral skip"
  tail -25 /tmp/hermes_pip.log || true
  exit $NEUTRAL
fi
export PATH="$VENV/bin:$PATH"
command -v hermes >/dev/null 2>&1 || { echo "::warning::hermes CLI not on PATH after install; neutral skip"; exit $NEUTRAL; }
echo "hermes: $(hermes --version 2>&1 | head -1)"

HERMES_HOME="$(mktemp -d)"
export HERMES_HOME
printf 'plugins: {}\n' > "$HERMES_HOME/config.yaml"

echo "== mmb hermes install =="
INST_LOG="$(mktemp)"
if ! "$MMB" hermes install --home "$HERMES_HOME" --force 2>&1 | tee "$INST_LOG"; then
  echo "::error::mmb hermes install failed against latest hermes"
  exit 1
fi

fail=0

# 0. The installer treats a failed `hermes cron create` as a non-fatal warning,
#    but for the compat check that IS a failure: it means Hermes changed its cron
#    CLI (e.g. v0.17.0 dropped `--schedule`/`--prompt`) and our backstop install
#    is broken against latest upstream.
if grep -qiE "hermes cron.{0,4} failed|unrecognized arguments" "$INST_LOG"; then
  echo "::error::backstop cron install broke against latest hermes (hermes cron CLI changed — see install output above)"
  fail=1
fi

# 1. Our config patch is valid YAML and carries every managed key with the right
#    type. (Catches our own regressions; the names must still match what Hermes
#    reads — verified live below.)
python3 - "$HERMES_HOME/config.yaml" <<'PY' || fail=1
import yaml, sys
c = yaml.safe_load(open(sys.argv[1]))
assert "monstermailbox" in (c.get("plugins", {}).get("enabled") or []), "plugins.enabled"
assert c.get("platform_toolsets", {}).get("monstermailbox") == ["hermes-cli"], "platform_toolsets"
mm = c.get("display", {}).get("platforms", {}).get("monstermailbox", {})
assert mm.get("long_running_notifications") is False, "long_running_notifications"
assert mm.get("interim_assistant_messages") is False, "interim_assistant_messages"
assert mm.get("tool_progress") == "off", "tool_progress"
assert c.get("compression", {}).get("codex_gpt55_autoraise") is False, "codex_gpt55_autoraise"
print("config.yaml managed keys OK")
PY
[ "$fail" = 1 ] && echo "::error::installed config.yaml missing/!=expected managed keys"

# 2. Gate script landed where Hermes resolves cron scripts (HERMES_HOME/scripts).
if [ -f "$HERMES_HOME/scripts/mmb_inbox_backstop_gate.sh" ]; then
  echo "gate script present at HERMES_HOME/scripts OK"
else
  echo "::error::gate script not at HERMES_HOME/scripts (Hermes scripts dir moved?)"
  fail=1
fi

# 3. THE upstream-contract checks: Hermes's own tooling must still accept the
#    config we wrote AND show what we registered. If a command ERRORS, our config
#    additions broke Hermes's parser (hard fail). If it runs but our entry is
#    absent, the cron/plugin path drifted (warn in v1 — promote to hard fail once
#    these are confirmed stable under a minimal CI config).
if hermes cron list >/tmp/hermes_cron.txt 2>&1; then
  grep -q "mmb-inbox-backstop" /tmp/hermes_cron.txt \
    && echo "hermes cron list shows mmb-inbox-backstop OK" \
    || echo "::warning::hermes cron list ran but mmb-inbox-backstop absent (cron CLI flags / scripts dir drift?)"
else
  echo "::error::'hermes cron list' errored on our post-install config"; cat /tmp/hermes_cron.txt; fail=1
fi

if hermes plugins list >/tmp/hermes_plugins.txt 2>&1; then
  grep -q "monstermailbox" /tmp/hermes_plugins.txt \
    && echo "hermes plugins list shows monstermailbox OK" \
    || echo "::warning::hermes plugins list ran but monstermailbox absent (plugin discovery drift?)"
else
  echo "::error::'hermes plugins list' errored on our post-install config"; cat /tmp/hermes_plugins.txt; fail=1
fi

[ "$fail" = 0 ] && echo "RESULT: Hermes compatible"
exit $fail
