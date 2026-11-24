# Verified revision forwarding (local automation)

`forward_revision.py` is a stdlib-only, loopback-only adapter that pages a
served Mousa store's verified request history and posts one event per
committed external revision to a local webhook receiver. It exists so local
automation can consume the durable audit stream without re-implementing
verification, cursors, or credential handling.

It forwards only entries that are, in order:

1. an authenticated, committed `document` request in the verified history,
2. confirmed by fetching `GET /v1/requests/{request_id}` — the detail outcome
   must equal the history entry and carry the same `item_change` evidence ID,
3. a real revision for exactly the configured `--source` — `current` and
   `previous` representation IDs both present and different. Adds, deletes,
   restores, no-ops, denials and every other source are skipped.

## Run

Both credentials come from the process environment and must be distinct:

```sh
export MOUSA_OPERATOR_TOKEN="$OPERATOR_TOKEN"   # daemon operator principal
export MOUSA_WEBHOOK_TOKEN="$RECEIVER_TOKEN"    # credential the receiver checks
python3 examples/automation/forward_revision.py \
  --mousa http://127.0.0.1:MOUSA_PORT \
  --receiver http://127.0.0.1:RECEIVER_PORT/webhook \
  --source inspection-notes \
  --cursor ./forward.cursor
```

Each run is one shot and bounded (8 pages × 64 outcomes). Rerun it explicitly
from cron, a file watcher, or your orchestrator; there is no worker loop.

Deliveries are `POST`ed with `Authorization: Bearer <webhook token>`,
`Content-Type: application/json`, and `Idempotency-Key: <event_id>`, body:

```json
{
  "type": "mousa.document.revised.v1",
  "event_id": "mousa.document.revised.v1:<canonical item-change ID>",
  "request_id": "<26-char request ID>",
  "source": "<external source ID>",
  "item_id": "<item ID>",
  "current_representation_id": "<representation ID>",
  "previous_representation_id": "<representation ID>"
}
```

Only a 2xx acknowledges a delivery; the adapter retries a bounded three
attempts on network failure, 429 and 5xx, and exits nonzero leaving the
cursor behind when delivery cannot be confirmed. The receiver must treat the
idempotency key as the dedupe key because an acknowledged-lost delivery is
reposted verbatim.

## Cursor semantics

The cursor file holds one integer: the last fully handled audit sequence. It
is written atomically (tempfile, fsync, `os.replace`, mode 0600) and is
dedicated to exactly one service, one external source and one receiver — do
not share it between configurations, do not edit it, and do not reset it
automatically. To start over, delete it and run once; to move it to another
daemon, start a new cursor file. The cursor's parent directory must exist and
be writable by the operator only, like the serve config.

## Safety boundaries

- Both URLs are validated before any request: `http` scheme, loopback IP
  literal, explicit port, no credentials, no query or fragment; the service
  URL must be the origin only and the receiver must have a nonempty path.
- Redirects are never followed, so neither credential can be forwarded
  elsewhere; proxies are bypassed.
- The operator credential authorizes only the monitor reads it needs; the
  webhook credential is the only thing sent to the receiver. Nothing printed
  by the adapter contains either credential, request bodies, or note text.
- A nonoperator or missing credential fails against the daemon; the adapter
  surfaces a fixed refusal, never the server's reply.

## Wiring into n8n (untested sketch)

The intended receiver shape is a small authenticated webhook. In n8n that
maps to:

- a **Webhook** node (path e.g. `/webhook`, response mode "last node"),
- **Header Auth** credential checking `Authorization: Bearer <receiver token>`
  — a distinct credential from the Mousa operator token,
- an **IF** or dedupe step keyed on `Idempotency-Key` (or
  `body.event_id`) so a retried delivery applies its effect once, and
- **Respond to Webhook** only after the effect is durable: the adapter takes
  a 2xx as acknowledgement, so replying before the effect lands loses work.

Subsequent Mousa work inside the workflow should use an HTTP Request node
against `POST /v1/query` and friends with a scoped nonoperator token — do not
hand the Mousa operator credential to a workflow.

n8n has not been run against this adapter. What has been exercised is the
deterministic local receiver in
[forward_revision_test.py](forward_revision_test.py), which runs the adapter
against a real daemon on a real store, proves 2-attempts/1-effect idempotency,
cursor resume, redirect refusal, non-loopback refusal, and that only the
selected source's revisions are emitted.

```sh
CGO_ENABLED=0 go build -o mousa ./cmd/mousa
python3 examples/automation/forward_revision_test.py --mousa ./mousa
```
