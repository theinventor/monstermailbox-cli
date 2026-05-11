# MonsterMailbox `mmb` workflows

## Auth

Existing key:

```sh
mmb auth save \
  --profile <profile> \
  --api-key "$MONSTERMAILBOX_API_KEY" \
  --api-url https://api.monstermailbox.com \
  --agent-address <local-part>@monstermailbox.com \
  --storage=file
mmb auth use <profile>
mmb auth status --human
mmb whoami
```

New inbox:

```sh
mmb auth login --address <local-part> --email <owner@example.com>
mmb auth status --human
```

## Inbox triage

```sh
mmb inbox list --peek
mmb inbox list --all --peek
mmb inbox list --state quarantined --peek
mmb msg get <id> --peek
```

If supported by the current `agent-context`, move work state while handling:

```sh
mmb msg work-state <id> in_progress
mmb msg work-state <id> awaiting_reply
mmb msg work-state <id> done
mmb msg work-state <id> blocked
```

Command names/flags may change; verify with `mmb agent-context` before relying on this exact spelling.

## Safe outbound

Reply-all is the default safe reply mode:

```sh
mmb reply-all <message-id> --body-file reply.txt
```

New outbound thread:

```sh
mmb new-email \
  --to <recipient@example.com> \
  --subject "Subject" \
  --body-file body.txt
```

Use idempotency keys when retrying mutation commands if the command supports `--idempotency-key`.

## Trust setup

Expected inbound:

```sh
mmb expect --from sender@example.com --subject "Invoice" --ttl 24h
```

Whitelist, broader and riskier:

```sh
mmb whitelist create example.com
```

## Webhooks

Most agents want a narrow subscription:

```sh
mmb webhook create \
  --name "agent inbox" \
  --url https://example.com/mmb/webhook \
  --event inbox.new
```

Use `mmb webhook events` to inspect available events before subscribing.
