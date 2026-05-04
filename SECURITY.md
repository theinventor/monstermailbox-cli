# Security policy

## Reporting a vulnerability

Please **do not** open a public GitHub issue for security problems.

Email **security@monstermailbox.com** with:

- A description of the issue and its impact.
- Steps to reproduce (or a proof-of-concept), if you have one.
- The `mmb` version (`mmb --version`) and OS/arch.

You should get an acknowledgement within 72 hours. We aim to ship a fix
or mitigation within 14 days for high-severity issues.

## Scope

In scope:

- The `mmb` CLI in this repository.
- The credential storage on disk (`~/.config/mmb/config.json`).
- The auto-updater (binary download, signature/checksum verification, atomic replace).

Out of scope (report to the relevant team instead):

- The monstermailbox web app or HTTP API surface — `security@monstermailbox.com` still routes correctly, but please mark it `[server]` in the subject so we can route faster.
- Third-party dependencies — please report upstream first; we will track once a CVE is published.

## What we treat as a security issue

- Anything that could exfiltrate or expose a stored API key.
- Path traversal, race conditions, or world-writable file modes around the config or binary.
- Auto-updater attacks: downgrade attacks, MITM, checksum bypass, malicious release replacement.
- Local privilege escalation through CLI behavior.

Bug reports that are functional issues (a flag misbehaves, output is wrong) belong in regular GitHub Issues.
