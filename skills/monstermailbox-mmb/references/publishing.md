# Publishing the official MonsterMailbox skill

This folder is intended to be public-safe. It must not contain API keys, private addresses, local deployment paths, customer data, or unpublished product plans.

## ClawHub summary

ClawHub is the public registry for OpenClaw skills and plugins. Skills publish from a folder containing `SKILL.md` plus optional support files.

Public skill page shape:

```text
https://clawhub.ai/<owner>/<slug>
```

Recommended official slug/name:

```text
slug: monstermailbox-mmb
name: MonsterMailbox mmb
```

Publish command once logged in to a publisher account/org:

```sh
clawhub skill publish ./skills/monstermailbox-mmb \
  --owner <publisher-owner> \
  --slug monstermailbox-mmb \
  --name "MonsterMailbox mmb" \
  --version 1.0.0 \
  --tags latest,email,monstermailbox,cli,agents \
  --changelog "Initial official MonsterMailbox mmb CLI skill"
```

If publishing via older ClawHub CLI docs, owner may be selected interactively rather than with `--owner`.

## Pre-publish checklist

- `SKILL.md` has required YAML frontmatter: `name` and `description`.
- Description includes trigger terms: MonsterMailbox, mmb CLI, inbox, email, quarantine, whitelist, webhook.
- No secrets: grep for `mmb_`, real emails, local-only paths, tokens, `.env`, and private URLs.
- Examples use placeholders only.
- Run the package validator if available:
  ```sh
  python /app/node_modules/openclaw/skills/skill-creator/scripts/package_skill.py ./skills/monstermailbox-mmb
  ```
- Optionally test local install by copying the folder into an OpenClaw workspace `skills/` directory and starting a new session.

## Versioning

Use semver. Bump patch for wording fixes, minor for new workflow coverage, major only for breaking behavioral changes.

Keep command details concise in `SKILL.md`; rely on `mmb agent-context` for live command flags/enums to avoid drift.
