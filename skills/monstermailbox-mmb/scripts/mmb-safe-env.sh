#!/usr/bin/env bash
set -euo pipefail

if ! command -v mmb >/dev/null 2>&1; then
  echo "mmb not found on PATH" >&2
  exit 127
fi

mmb auth status --human
mmb whoami
