# Inbound triggers: start work from events (Pro)

A trigger starts a recipe, or an agent as a background task, when something happens outside AgentOven: a signed webhook arrives, a message lands in a mailbox, a file appears in a folder or an object-store prefix.

What every trigger does, and what it cannot:

- It runs as a **service account of its kitchen, and the account belongs to a person**. It is never anonymous. If the person leaves the kitchen, the account stops (`owner_unbound`) and so does the trigger, until a Head Chef gives the account a new owner.
- It starts work **through the same API a call from that account would use**, so the account's role, environment gating, guardrails, budgets and the audit trail all apply. It can start nothing the account could not start itself.
- It **cannot skip an approval**. A human gate inside a recipe waits for a person. An environment that requires approval (always, in a GxP kitchen) refuses the run, because skipping it is a signed decision and a trigger cannot sign.
- Creating one needs `trigger:manage` (Chef and above) and a service account no higher than your own role. A Baker, Taster and Auditor can read triggers; only Chef and above can fire a real test.

## Webhook

```bash
agentoven trigger create invoice-arrived invoice-flow --service-account <id> \
  --mapping '{"invoice":"$.invoice.id","note":"{{ $.invoice.note }}"}' --require '$.invoice.id'
# prints the URL path (/hooks/<kitchen>/invoice-arrived) and the signing secret, once
```

The sender signs every call. The scheme is the one AgentOven's own outbound webhooks use:

| Header | Value |
|---|---|
| `X-AgentOven-Timestamp` | unix seconds |
| `X-AgentOven-Signature` | `sha256=` and the hex HMAC-SHA256, with the secret, of `<timestamp>.<body>` |
| `X-AgentOven-Delivery` | an id, the same on every retry of one delivery |

`agentoven.sign_webhook(secret, body)` (Python) and `signWebhook(secret, body)` (TypeScript) make them. A call older than five minutes (`timestamp_tolerance_seconds`, 30 to 3600) or from the future is refused. For senders that cannot do this (GitHub and those that copy it) set `signature_scheme` to `hmac-body`: the signature is `sha256=<hex HMAC of the body>` in `X-Hub-Signature-256`, and a delivery id header (`X-GitHub-Delivery`) is **required**, because that scheme has no clock and the id is its only replay protection.

| Answer | Meaning |
|---|---|
| `202` `{"status":"accepted","run_id":…}` | a run or task was started |
| `200` `{"status":"duplicate"}` | this delivery id was seen in the last 24 hours; nothing started (answered as success so the sender stops retrying) |
| `401` `unauthorized` | no signature, a wrong one, a stale timestamp, a missing delivery id, an unknown trigger: always the same answer, so a stranger learns neither which headers a trigger expects nor which names exist. The trigger's delivery log says which it was |
| `409` `trigger_disabled`, `413`, `400 payload_not_json`, `422` | switched off; body larger than the trigger's limit (1 MiB by default, 8 MiB at most); not JSON; the payload lacks a required value or the run was refused (the answer carries a code, never the reason: that is in the delivery log) |
| `429` / `503` with `Retry-After` | over the trigger's rate limit (60 a minute by default); the control plane or the account's standing is failing now: the same delivery id can be sent again |

Rotating the secret (`agentoven trigger rotate-secret`) takes effect at once; there is no overlap period.

Two rules about secrets and replays:

- **Every secret a trigger names is `vault://<credential>` of its own kitchen.** A webhook's `secret_ref`, a mailbox's `password_ref` and an object store's `access_key_ref` and `secret_key_ref` cannot be `env://` or `k8s://` (those would read the server's own environment and hand it to the host the trigger names); create the credential first (`agentoven secret create`). One credential signs one trigger: a second trigger naming it is refused (`409 secret_in_use`), and the generated secret has an unguessable name.
- **A signed request is one delivery, whatever id it carries.** The delivery id header is not covered by the signature, so a captured request cannot be made new by changing it: a repeat of the same signed request is a `duplicate`. A sender that retries must resend the same signed request, or sign again and send the same `X-AgentOven-Delivery` (AgentOven's own sender does that). With `hmac-body`, an identical body inside 24 hours is a duplicate too.

Anyone can open a connection to the webhook address, so a few places are rationed before a signature is checked: one address holds at most 8 requests at once and one kitchen's path 24, a body must arrive within 15 seconds, and a refused request is answered `503` with `Retry-After`.

## Mapping the payload to the input

`input_mapping` is JSON. A string that is `$.a.b[0]` is replaced by that value and keeps its type; `"text {{ $.a.b }}"` puts values into text; `$meta.delivery_id`, `$meta.source`, `$meta.trigger`, `$meta.kitchen`, `$meta.received_at` name the delivery; `$$` starts a literal `$`. Nothing else is evaluated: no functions, no scripts, no environment. A bad path is refused when the trigger is saved. With no mapping a recipe gets `{"payload": <the payload>}` and an agent a message that shows it, marked as untrusted data from an outside sender (it is still text an outsider wrote: guardrails apply to what a trigger sends an agent, as to any input).

For an agent target the mapping has `message` and optionally `metadata`. Files (below) become content parts of the task.

## E-mail

`kind: email`, settings: `host`, `port`, `security` (`tls`, `starttls`, `none` for a private host only), `username`, `password_ref` (a reference such as `vault://mailbox-password`, never the password), `folder`, filters `from`, `to`, `subject_contains`, `processed` (`flag`: mark the message `\Seen` and with the keyword `AgentOvenProcessed`; or `move` to `processed_folder`), `poll_seconds` (15 to 3600), `max_message_bytes`, `max_attachments`.

Each new message is one delivery, keyed by its `Message-ID`, so the same message starts one run however many times it is seen. Its text, headers and attachments become the payload (`from`, `to`, `subject`, `text`, `message_id`, `date`, `attachments`: `[{blob_ref, mime_type, name, size}]`); each attachment is stored in the kitchen's blob store. For an agent target the images, PDFs, audio and video go to the model as content parts, and other types are named in the message. The server must be reachable, offer AUTH=PLAIN or LOGIN, and the host must be public, or listed by the installation in `AGENTOVEN_TRIGGER_PRIVATE_HOSTS`. OAuth2 (Microsoft 365 with basic auth disabled) is not supported.

## File arrival

`kind: file`, settings: `source: dir` with `path` (a folder inside the kitchen's own directory under `AGENTOVEN_TRIGGER_FILE_ROOT`; unset, local folders are off), or `source: s3` with `s3: {endpoint, region, bucket, prefix, access_key_ref, secret_key_ref}` (AWS, MinIO or any S3-compatible store). `pattern` (`*.pdf`), `stable_seconds` (default 30: size and modification time must not change for that long), `processed: move` (to `.processed/` or `processed/<time>-<name>`) or `delete`, `poll_seconds`, `max_file_bytes` (25 MiB, the blob store's limit). Only plain files directly in the folder are taken: no links, no sub-folders, no names starting with a dot. Delivery key: content digest and name.

## Trying a trigger, and the log

`agentoven trigger fire <id> --payload @sample.json` shows the input the run would get and starts nothing; `--run` starts a real run as the trigger's account, recorded as a test and attributed to you. `agentoven trigger poll <id>` looks at a mailbox or folder now. `agentoven trigger deliveries --status failed` lists what happened: **every** delivery is recorded as `accepted`, `rejected`, `duplicate` or `failed` with a code, a reason, the run it started, its size and a digest of its payload. The payload itself is never stored, in the log or in the audit trail. Refusals of unauthenticated callers (bad signatures) are logged at most once every ten seconds per trigger and say how many they stand for, so a flood cannot fill the log.

The dashboard has the same under **Build, Recipes, Triggers**.

## Operating it

| Setting | Meaning |
|---|---|
| `AGENTOVEN_TRIGGERS=off` | no trigger routes, no `/hooks/`, no polling |
| `AGENTOVEN_TRIGGER_POLLING=off` | this replica does not poll mailboxes and folders (run the poller on one replica) |
| `AGENTOVEN_TRIGGER_FILE_ROOT` | the folder local-folder triggers live under, one directory per kitchen |
| `AGENTOVEN_TRIGGER_PRIVATE_HOSTS` | host names, addresses or CIDR ranges triggers may reach although they are private (an on-premises mail server, an internal object store) |
| `AGENTOVEN_TRIGGER_DELIVERY_RETENTION_DAYS` | how long the delivery log is kept (90) |

Behind a proxy the log's `remote_addr` is the proxy's address. Rate limits are per replica. Several replicas may poll the same mailbox: the delivery claim keeps it to one run per message.
