# `mmb-bridge` — local Gmail → monstermailbox bridge daemon

`mmb-bridge` runs on your laptop. It watches your real Gmail, applies a
whitelist (the only senders you've approved), and forwards matching
mail to your agent's monstermailbox tenant.

The whole point: **your Gmail OAuth token never leaves your machine.**
monstermailbox is not holding god-mode credentials for your inbox.

```
Gmail ──▶ Pub/Sub topic   (gog gmail watch start registers this)
                │
                ▼
        Pull subscription   (mmb-bridge pulls — outbound only, no tunnel)
                │
                ▼
        mmb-bridge daemon
            ├─ decode push (emailAddress + historyId)
            ├─ gog gmail history --since=<id>     → new message IDs
            ├─ match against whitelist             (synced from /bridge/policy)
            ├─ on match: gog gmail get --format=raw  → raw MIME
            └─ POST /bridge/inbound                (Bearer mmb_…)
```

You'll talk to **three** systems. The setup below is in the right
order — paste it top-to-bottom and you'll have a running bridge.

---

## Prerequisites

You need:

1. **A monstermailbox account** with at least one agent.
2. **`gog` (gogcli)** — handles your Gmail OAuth + the Pub/Sub watch.
3. **`gcloud`** — to create the Pub/Sub topic + subscription.
4. A **Google Cloud project** with the Pub/Sub API enabled. The
   project does NOT have to be the same one your gog OAuth client
   lives in, but using one project for everything is simpler.

Install gog + gcloud:

```sh
# gog (Homebrew tap)
brew install steipete/tap/gogcli

# gcloud (Homebrew cask)
brew install --cask google-cloud-sdk

# log into gcloud (one time, opens a browser)
gcloud auth login
```

---

## One-time GCP setup

You don't need a new GCP project. Reuse the one your existing
gog OAuth client lives in — that's the project listed in your
gog `credentials.json`.

```sh
# Find the project ID your gog OAuth client lives in:
python3 -c "import json,sys; \
  c=json.load(open('$HOME/Library/Application Support/gogcli/credentials.json')); \
  print(c.get('installed', c.get('web', c)).get('project_id', '<unknown>'))"

# Set it + a topic + subscription name:
export PROJECT_ID="<the-id-printed-above>"   # e.g. gen-lang-client-0131531568
export TOPIC="gmail-events"
export SUBSCRIPTION="mmb-bridge-pull"

gcloud config set project "$PROJECT_ID"
```

> If `gcloud` reports a permission error on the project, you may need
> to run `gcloud auth login` first. Pub/Sub low-volume usage is on the
> free tier; you do NOT need to add a billing account just for the
> bridge.

### 1. Enable the Pub/Sub API

```sh
gcloud services enable pubsub.googleapis.com
```

Verify:

```sh
gcloud services list --enabled --filter=pubsub.googleapis.com
# Expect a row showing pubsub.googleapis.com
```

### 2. Create the topic + grant Gmail's service account publish rights

This is the easy-to-miss step. Gmail publishes via a fixed
service-account email; if it can't publish, the watch will silently
drop notifications.

```sh
gcloud pubsub topics create "$TOPIC"

gcloud pubsub topics add-iam-policy-binding "$TOPIC" \
  --member=serviceAccount:gmail-api-push@system.gserviceaccount.com \
  --role=roles/pubsub.publisher
```

Verify:

```sh
gcloud pubsub topics get-iam-policy "$TOPIC"
# Look for: members: ['serviceAccount:gmail-api-push@system.gserviceaccount.com']
#           role: roles/pubsub.publisher
```

### 3. Create the PULL subscription

mmb-bridge pulls — no public URL needed. **Do NOT make this a push
subscription** — push subscriptions need a public HTTPS endpoint and
defeat the whole point of staying outbound-only.

```sh
gcloud pubsub subscriptions create "$SUBSCRIPTION" \
  --topic="$TOPIC" \
  --ack-deadline=60
```

Verify:

```sh
gcloud pubsub subscriptions describe "$SUBSCRIPTION"
# pushConfig: {} → confirms it's a pull subscription, NOT push.
```

---

## One-time gog setup

You almost certainly already have gog working. The bridge needs gog's
OAuth to also cover **Pub/Sub**, which gog doesn't request by default.
This is a one-line edit on the OAuth consent screen + a single re-auth.

### 1. Add `pubsub` to your OAuth consent screen scopes

Visit: <https://console.cloud.google.com/apis/credentials/consent>

- Confirm the dropdown at the top is your `$PROJECT_ID`.
- Click your OAuth consent screen → **Edit App** → next to
  **Scopes** click **Save and Continue** until the **Add or Remove
  Scopes** dialog appears (or click the **Add or Remove Scopes** button).
- Search for `pubsub`. Tick **`https://www.googleapis.com/auth/pubsub`**.
- **Update**, then **Save**.

> **Why this step?** The consent screen pins the maximum set of
> scopes the OAuth client is *allowed to request*. Without the
> Pub/Sub scope listed here, gog's `--extra-scopes` flag will fail
> with `invalid_scope` at re-auth time.

### 2. Re-auth gog with the pubsub scope

```sh
gog login your-email@gmail.com \
  --extra-scopes=https://www.googleapis.com/auth/pubsub \
  --force-consent
```

A browser will pop. Approve. The new token replaces the old one and
carries Gmail + Pub/Sub.

Verify:

```sh
gog auth list -j | python3 -c "import json,sys; \
  print('has pubsub:', any('pubsub' in s for s in json.load(sys.stdin)['accounts'][0]['scopes']))"
# Expect: has pubsub: True
```

---

## Install `mmb-bridge`

```sh
go install github.com/theinventor/monstermailbox-cli/bridge@latest
# This places `mmb-bridge` in $(go env GOPATH)/bin — make sure it's on $PATH.

mmb-bridge --help
# Expect the help screen to list: init, start, status, stop, logs, whitelist, rotate-key
```

Or build from source:

```sh
git clone https://github.com/theinventor/monstermailbox-cli.git
cd monstermailbox-cli
go build -o mmb-bridge ./bridge
sudo mv mmb-bridge /usr/local/bin/
```

---

## Mint an enrollment token in the dashboard

In your monstermailbox dashboard:

1. Sign in.
2. **Bridge** → click **Generate setup token** next to the agent you
   want to bridge into.
3. Copy the entire `mmb-bridge init …` block shown on the page.

The token is valid for 24h, single-use. The dashboard will NOT
re-show it; if you lose it, mint a new one.

---

## Run `mmb-bridge init`

Paste the `mmb-bridge init` line from the dashboard, **and add the
GCP flags**:

```sh
mmb-bridge init \
  --enrollment-token bre_<from-dashboard> \
  --api-base-url https://app.monstermailbox.com \
  --account your-email@gmail.com \
  --gcp-project "$PROJECT_ID" \
  --pubsub-topic "$TOPIC" \
  --pubsub-subscription "$SUBSCRIPTION"
```

What this does (it tells you as it goes):

```
✓ gog is installed
✓ gog is authenticated
✓ gog OAuth has pubsub scope
→ redeeming enrollment token at https://app.monstermailbox.com …
✓ enrolled as alpha@monstermailbox.com (api_base_url=https://app.monstermailbox.com)
→ registering gmail Pub/Sub watch on projects/<proj>/topics/gmail-events …
✓ Gmail will publish to projects/<proj>/topics/gmail-events
✓ wrote /Users/<you>/.mmb-bridge/config.json (mode 0600)

Next: run `mmb-bridge start` to begin forwarding mail.
```

---

## Add senders to your whitelist

By default, the whitelist is **empty** — the bridge will drop every
incoming message. That's the safe default; you opt in to senders.

In the dashboard: **Policy → Whitelist → Add entry**.

Examples:

| Goal                                            | Entry                                                                     |
| ----------------------------------------------- | ------------------------------------------------------------------------- |
| GitHub CI / PR notifications                    | `notifications@github.com`                                                |
| Stripe receipts only (not all stripe)           | sender `billing@stripe.com`, subject regex `^Receipt for`                  |
| All of `*@subdomain.example.com`                | sender_regex `^.+@subdomain\.example\.com$`                               |

Whitelist edits propagate to the running bridge within ~30s
(`/bridge/policy` is polled). Confirm the version bump in the
dashboard's Bridge view.

---

## Start the daemon

```sh
mmb-bridge start --detach
# "Bridge started (pid 12345, detached). Tail logs with `mmb-bridge logs -f`."
```

Verify:

```sh
mmb-bridge status
# daemon:    running
# pid:       12345
# config:    /Users/you/.mmb-bridge/config.json
# agent:     alpha@monstermailbox.com
# gmail:     you@gmail.com
# pubsub:    projects/<proj>/subscriptions/mmb-bridge-pull
# policy:    version=3  2 entries  fetched 1s
# local-only: false
```

Tail logs:

```sh
mmb-bridge logs -f
```

---

## End-to-end smoke test

1. Send yourself an email from `notifications@github.com` (or any
   address you've whitelisted) — easiest: trigger a CI run on a repo
   you own.
2. `mmb-bridge logs -f` should show:
   ```
   forward msg=<gmail-id> from="notifications@github.com" subject="…" → inbound=<id> (matched exact sender=notifications@github.com)
   ```
3. The dashboard's **Bridge** view should flip to **● Connected ·
   last forward N seconds ago**.
4. The agent's inbox should have the message (treated like any other
   inbound — runs through the trust pipeline).

---

## Troubleshooting

### `gog OAuth has pubsub scope` fails

Re-run step 2 of *One-time gog setup* — `gog login --extra-scopes=…
--force-consent`. The error message includes the exact command.

### `gog gmail watch start` errors with "publisher not found"

You forgot step 3 of *One-time GCP setup*. Re-run:

```sh
gcloud pubsub topics add-iam-policy-binding "$TOPIC" \
  --member=serviceAccount:gmail-api-push@system.gserviceaccount.com \
  --role=roles/pubsub.publisher
```

### `mmb-bridge logs` shows `pubsub pull: ... 403`

Your gog OAuth account doesn't have the **Pub/Sub Subscriber** role
on the subscription. Grant it:

```sh
gcloud pubsub subscriptions add-iam-policy-binding "$SUBSCRIPTION" \
  --member=user:your-email@gmail.com \
  --role=roles/pubsub.subscriber
```

### Bridge shows ● Connected then ⚠ Unreachable

The daemon stopped. `mmb-bridge status` will say `daemon: stopped` or
`stale`. Check the log: `mmb-bridge logs -n 50`. Common causes:

- Gmail's watch expired (re-run `gog gmail watch start --topic …`,
  Gmail forces re-registration every 7 days).
- Pub/Sub IAM was edited.
- Network outage on the host machine.

### Whitelist edits aren't taking effect

`mmb-bridge status` shows the version + fetch time. If the version is
stale, your bridge's API key has been revoked. Re-mint an enrollment
token in the dashboard and re-run `mmb-bridge init`.

### Need to rotate the bridge API key

```sh
mmb-bridge rotate-key
# rotated. previous last4=XXXX, new last4=YYYY
# If the daemon was running, restart it: `mmb-bridge stop && mmb-bridge start --detach`.
```

---

## Files

`mmb-bridge` keeps everything under `~/.mmb-bridge/`:

| File              | Mode | Contents                                                       |
| ----------------- | ---- | -------------------------------------------------------------- |
| `config.json`     | 0600 | bridge-scoped API key + agent + gcp project + subscription     |
| `state.json`      | 0600 | last Gmail historyId + dedup ring of last 1000 message IDs     |
| `bridge.log`      | 0600 | line-oriented log (`mmb-bridge logs` reads this)               |
| `bridge.pid`      | 0600 | live daemon's pid (used by `start`/`stop`/`status`)            |
| `whitelist.json`  | 0600 | local-only whitelist (only used in `--local-only` mode)        |

Loss of the directory just means re-running `mmb-bridge init` with a
fresh enrollment token — nothing is irrecoverable.

---

## Security model

- **Gmail OAuth never leaves your machine.** monstermailbox cannot
  read your inbox even if compromised; it only sees what your local
  whitelist forwards.
- **The bridge API key is scoped.** It carries `bridge:write`,
  `bridge:policy:read`, and `bridge:rotate` — NOT the agent's primary
  key. A stolen `~/.mmb-bridge/config.json` cannot read the agent's
  inbox or send mail as the agent.
- **Pub/Sub is pull, not push.** No public URL, no inbound port.
- **Enrollment tokens are single-use, 24h TTL.** A leaked token mid-
  setup can only enroll one machine before it's burned.
- **Bridge can be paused.** `mmb-bridge stop` halts forwarding
  immediately; mail stays in your real Gmail. Resuming with `start`
  picks up from the persisted historyId — no loss.

---

## `--local-only` mode

Don't want monstermailbox to see your whitelist? Pass `--local-only`
at `init` and the bridge will read `~/.mmb-bridge/whitelist.json`
instead of polling `/bridge/policy`. Manage with:

```sh
mmb-bridge whitelist add notifications@github.com
mmb-bridge whitelist list
```

Trade-off: you give up dashboard-side editing + audit logs of
whitelist changes.
