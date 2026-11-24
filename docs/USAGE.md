# Using Mousa

For source-build versus tagged-release availability,
see [development status](OVERVIEW.md#source-and-release-status).

[← Back to Mousa](../README.md) · [Capability matrix](CAPABILITIES.md) · [How Mousa works](OVERVIEW.md)

Full CLI and MCP usage. Consumer examples are linked where they exist. Run commands from
the repository root; build the executable from source before using the examples.

## Local usage (cmd/mousa)

The local CLI synchronizes text items into a canonical store and returns
source-linked evidence under a byte budget. Build with Go 1.25 or newer:

```sh
CGO_ENABLED=0 go build -o mousa ./cmd/mousa
./mousa -store local.sqlite sync --preview ./documents
./mousa -store local.sqlite sync --allow ./documents
./mousa -store local.sqlite status ./documents
./mousa -store local.sqlite query --budget-bytes 4096 ./documents 'zebra habitat'
```

Commands print JSON on stdout. Directory item identity is the
relative POSIX path. Updates and reverts atomically replace current search evidence for the same item ID;
deletion removes current evidence without deleting canonical history. Identical
content restored after deletion becomes searchable again. `status` reports active
items separately from historical observations.

Directory defaults select Markdown/plain-text extensions and skip hidden and
generated entries. Preview shows selected names and byte lengths without opening
the store. Use repeatable `--include`/`--exclude` globs to constrain scope and
`--all-text` only as a deliberate override. Selection flags are not saved between
invocations; successful scope changes remove old items outside the selected set.
See [directory selection and limits](CAPABILITIES.md#directory-selection-and-input-limits).

The default ingestion policy, `fixed-v1`, uses segments of at most 4,096 UTF-8 bytes.
To opt into bounded passage boundaries, use
`./mousa -store local.sqlite sync --segment-policy passage-v1 --allow ./documents`.
This policy groups document blocks into at most 1,024-byte source slices, with
explicit fallback for oversized blocks. Repeat the flag on resync; a policy change
replaces current evidence even when source bytes are unchanged. Smaller passages
can fit smaller evidence budgets but change lexical ranking and increase segment
count. See [policy identity, boundaries and limitations](CAPABILITIES.md#ingestion-segment-policies).

The source-built BEIR evaluator exposes the same choice with
`-segment-policy fixed-v1|passage-v1`; its default remains `fixed-v1`.
Use separate output directories for the policies because each dataset has one
indexed store per directory. Repeat the flag on reuse and resume.
This option does not change the runtime ranking or packing defaults.

For an explicit item stream:

```sh
printf '%s\n' '{"id":"note@draft","text":"The harbor inspection is on Friday."}' |
  ./mousa -store local.sqlite sync --source inspection-notes --allow
./mousa -store local.sqlite query --source inspection-notes 'harbor inspection'
```

JSONL input is bounded to 1 MiB per line, 64 MiB per invocation, and 10,000 records.
Deletion is explicit; omission does not delete. Failed input retains its committed
prefix and emits no success result. See the [input and recovery contract](CAPABILITIES.md)
before upgrading an existing directory store or retaining sensitive material.

A query returns `passages` and one `trail` (`mousa.trail.v1`). The trail is an
append-only log leaf. It records the request, per-source access, snapshot index,
allowed source heads, candidate scores and ranks, and packing decisions. `trail.id`
identifies this record; `trail.result_sha256` compares results across equivalent
requests against the same state. Lexical is the only supported mode.

`--budget-bytes` defaults to 8,192. It covers released text, not JSON or model
tokens. First-fit packing skips passages that do not fit and omits exact-content
copies of already packed passages. The trail records `packing.used`, `packed`,
`omitted`, and `duplicate_of` as applicable. A denied query has empty `passages`
and no retrieval, candidates, or packing. An allowed query with no matches has
an empty candidate list.

`mousa -store STORE trail TRAIL_ID` reads the full canonical record by `trail.id`
without writing. This is an owner interface, not a way to retrieve historical text.
MCP trail reads check current access to every originally allowed source. Missing
and unauthorized IDs both return `not_found`. The caller view omits `as_of`,
head sequence numbers, branch drop counts, and denied-source details other than
`source_id` and `outcome`.

Use `mousa -store STORE trail list [--source ID] [--segment ID] [--since SEQ]`
to stream owner-visible trail metadata as JSONL without writing. Each line
contains the trail `id`, its log `seq`, `requested_at_usec`, and `result_sha256`.
`--source` selects trails whose scope named that JSONL source; `--segment`
selects only trails that released that segment in full or in part. `--since`
excludes log entries at or before the supplied sequence. Filters combine.

### Query a collection or source list

```sh
mousa -store local.sqlite collection create fleet
mousa -store local.sqlite collection add --source dispatch fleet
mousa -store local.sqlite collection add --source inspection fleet
mousa -store local.sqlite collection list
mousa -store local.sqlite query --collection fleet 'harbor inspection'
mousa -store local.sqlite query --source dispatch --source inspection 'harbor inspection'
mousa -store local.sqlite collection remove --source dispatch fleet
```

Add and remove are idempotent. Add requires an existing source and accepts a
directory argument in place of `--source`. Collection names are lowercase,
start with a letter or digit, and may include digits, dots, hyphens, and
underscores (up to 64 characters). Collections and explicit lists hold at
most 128 sources. Empty collections return an empty audited result.

The trail records the resolved source IDs and each access decision. BM25
uses only the statistics of the candidate's source ancestry, and a shard
is searched only when every source in it allows retrieval. A collection
does not grant access. For MCP, pass a `sources` array or `collection` in
place of `source`; all members must be configured for that caller.

### Verify a store

Open checks only store identity and schema compatibility; it does not scan the
contents. Run `mousa -store STORE verify` for a full check, `verify --log` for
log sequence, hashes and per-kind coverage, `verify --trail TRAIL_ID` for a
named record, or `verify --stdin` for whitespace-delimited trail IDs. All modes
open the source read-only, return JSON `record_counts` on success, and fail
with a non-zero exit on damage. Whole-store verification checks an isolated
SQLite page copy because the FTS5 integrity command requires write permission.

### CLI client example

Two Python consumers show the CLI contract:

- The [versioned Git documentation example](../examples/docs/README.md) imports
  pinned public manuals and returns passages with verified byte ranges.
- The [SQLite backup checklist](../examples/backup/README.md) verifies saved
  passages and accepts caller-authored fact judgments.

The maintained client workflow [`eval/local/workflow.py`](../eval/local/workflow.py)
(`--example-only`) is checked by its acceptance suite
[`eval/local/workflow_test.py`](../eval/local/workflow_test.py).

## Access

A source with no access event is denied to every caller: queries return no
passages. Two commands record access. `sync --allow` records allow after the sync, but
only when the source has no access event yet; it never reverses an earlier
allow or deny. `access <dir> allow|deny`, or `access --source <id> allow|deny`,
changes access afterwards. A plain `sync ./documents` without `--allow` leaves a
new source denied, with reason `access_default`. Each change appends a new access event with its time
and actor; earlier events are never edited or removed. Repeating the current
access is a no-op reported as `changed: false`. Access is per source and
applies to every caller.

```sh
./mousa -store local.sqlite access ./documents deny
./mousa -store local.sqlite access ./documents allow
./mousa -store local.sqlite access --source inspection-notes deny
```

`./mousa -store local.sqlite withdraw ./documents` (or `withdraw --source <id>`)
withdraws the source from retrieval; there is no resume command, and withdrawal
is not erasure (see [retention](#what-the-store-does-not-protect-against)).

A denied query still appends its trail. It reports `deny` with `access_default`
or `access_denied`, or a lifecycle reason such as `source_withdrawn`, and no
passages. Withdrawal blocks retrieval even after a later `allow`; `sync` does
not resume it.

A source synced by an agent through MCP stays denied until the operator runs
`mousa -store STORE access --source ID allow`. MCP has no allow option, on
purpose: a client that can write a source cannot also release it.

## Notes

`remember` stores one durable note (a fact, decision or preference) as an
immutable canonical record on an external JSONL source. The source needs no
synced items: the operator bootstraps it with
`access --source <id> --create allow`, a single command that registers the
source and records the allow event. It is not one atomic operation: the
registration is safe to retry, and without the access event the source stays
denied. Without `--create` the command keeps the old missing-source behavior;
access is never granted silently. Every note write is transactional and
idempotent, and the first valid write wins: a valid retry with the same text
returns the first note's ID, author, time and lineage without appending anything.
Invalid citations still reject every time.

```sh
./mousa -store local.sqlite access --source memory --create allow
./mousa -store local.sqlite remember --source memory --from TRAIL_ID 'The harbor inspection uses dry dock 3.'
./mousa -store local.sqlite query --source memory 'dry dock'
./mousa -store local.sqlite notes export --source memory
```

`--from TRAIL[:SEG,SEG]` cites a committed query trail (repeat for several
trails); an omitted segment list cites every segment the trail selected. A
cited trail must have allowed retrieval, each listed segment must have been
selected, and the cited source must currently allow access and not be
withdrawn. A citation identifies what the trail released; it does not prove
the note is true, and it does not prove the note's citations are complete.
Note text is untrusted content: never store secrets or transcripts. A note
without lineage is unsourced (`origin: "unsourced"`). One positional argument
stores its exact bytes; with no positional argument, stdin supplies the bytes
up to 4,096, unmodified. Both forms preserve CRLF and BOM exactly.
`notes export --source <id>` streams each canonical note record plus its exact
text as JSONL, opened read-only. After registering and allowing the same source
in a new store, use `notes import --source ID < notes.jsonl`. Import preserves
the record and exact text, rejects foreign sources or bad digests, is atomic,
and treats identical imports as no-ops. If an imported citation's trail is
absent, recall reports `citation_unverifiable`.

Lexical query surfaces a stored note as ordinary evidence plus a `note` object
carrying its ID, author, creation time, lineage and a `stale` array. Each
stale entry names the affected `trail_id` (and `segment_id` when a single
segment retired) with a reason of `source_denied`, `source_withdrawn` or
`segment_retired`; no cited text is released. Notes follow the same per-source
access rules as document evidence: a denied or withdrawn note source releases
nothing, and later access changes do not redact note bytes already released.
`sync` and `mousa_sync` refuse a source holding notes, and `remember` refuses
a document source, including one whose items are all inactive.

Notes share the source's static identity and access grant; there are no
agent-specific sources or per-note grants yet. Over MCP, `mousa_remember`
takes `source`, `text` and an optional `derived_from` array of
`{trail_id, segment_ids?}` objects; the trusted `--caller` is the author, and
the write needs the source's `--ingest-source` grant plus `mousa:write` over
HTTP. Cited sources must also be configured with `--source`. Not supported:
editing or retracting a note, recalling across multiple sources in one query,
a citation change cursor or standalone recheck, a daemon, and any change to
lexical ranking.

## Changes and recheck

```sh
mousa -store local.sqlite changes --since 0 --source memory
mousa -store local.sqlite changes --since SEQ --collection fleet
mousa -store local.sqlite recheck --source memory NOTE_ID
mousa -store local.sqlite recheck --trail TRAIL_ID
```

`changes` returns up to 256 entries in log order, with no passage text.
Save `next_index` and pass it as the next `--since` for the same scope.
If `more` is true, call again to finish the page sequence. Without a scope
flag the CLI reads all allowed sources; MCP reads all configured and allowed
sources. The MCP `mousa_changes` tool takes `since` and optionally `source`
or `collection`.

An `updated` item entry names both the retired and newly current revision.
A denied source contributes nothing; a withdrawn source that still has
an allow event contributes withdrawal notices only. The cursor is a
global log index, not a count of visible changes.

`recheck` returns `fresh`, `stale`, `unsourced` (note without citations),
or `empty` (trail without released passages). Stale reasons include
withdrawn or denied citation sources, retired segments, and citations
that this store cannot verify. Rechecking does not rewrite a note or trail.

## What the store does not protect against

Integrity. Each ingest receipt, access event, withdrawal, query trail and note
appends a hashed leaf to an append-only log in the same transaction. Until signed checkpoints
exist, anyone who can write the SQLite file can forge a consistent history.
The log detects accidental damage and partial edits, not a deliberate rewrite.

Retention. Trails keep the query text, caller and prepared terms. Item replacement and explicit deletion remove the indexed
text row, but its bytes can remain in SQLite free pages until `VACUUM`
reclaims them. Withdrawal
removes nothing: withdrawn text stays indexed in the store. Removal is not
secure erasure: canonical records keep digests rather than old text, but copies
can remain in free pages, WAL files, backups and previously released output.
Retention limits and erasure are not handled yet.

## Local served store (opt-in)

`mousa -store STORE serve --config FILE [--listen 127.0.0.1:PORT]` owns the
store and serves JSON request endpoints, an operator request stream, and an
embedded dashboard over loopback HTTP only. It is a separate opt-in surface
for local automation: the offline CLI and MCP keep working unchanged on stores
that are not served.

A served store must be created by this binary's current schema. Every command
refuses a store written by an earlier Mousa schema **without modifying it** —
there is no migration. To roll back, keep using the prior binary against the
prior store; to serve, create a fresh store and re-sync it from its sources.

### Sources and credentials

Grant external sources offline before starting the daemon; `serve` has no
allow option:

```sh
./mousa -store STORE access --source inspection-notes --create allow
./mousa -store STORE access --source memory --create allow
```

The serve config names principals by SHA-256 verifier, so raw tokens never
enter the config, the store, or the log:

```json
{
  "principals": [
    {"id": "writer", "token_sha256": "…64 lowercase hex…",
     "read_sources": ["inspection-notes", "memory"], "write_sources": ["inspection-notes", "memory"]},
    {"id": "reader", "token_sha256": "…", "read_sources": ["inspection-notes"]},
    {"id": "monitor", "token_sha256": "…", "read_sources": [], "operator": true}
  ]
}
```

Keep write, read and monitor (operator) credentials separate; `operator: true`
grants metadata-only monitor reads and never bypasses per-source read or write
denial. Mint each token into the caller's environment rather than argv or a
file — for an interactive shell `read -rs TOKEN` then export it — and record
only its verifier: `printf '%s' "$TOKEN" | sha256sum`. A nonoperator principal
gets a denied outcome for requests outside its grants and `403` on the monitor
endpoints; the daemon never reveals whether another principal exists.

Save the verifiers to a private config file, then run the daemon in a
separate terminal. It prints its bound loopback URL on startup.

```sh
./mousa -store STORE serve --listen 127.0.0.1:8787 --config PRINCIPALS.json
```

Open the printed URL in your browser and attach with the monitor token.
Use a caller token, not the monitor token, for the smoke commands.

### Requests and replies

`POST /v1/query`, `POST /v1/notes` and `POST /v1/documents` accept one JSON
object each with typed fields:

- `/v1/query`: `{"scope":{"source"|"sources"|"collection"},"text","budget_bytes"}`
- `/v1/notes`: `{"scope":{"source"},"text","citations":[{"trail_id","segment_ids"}]}`
- `/v1/documents`: `{"source","record":{"id","text"|"deleted"},"segment_policy"}`

Any request may add a `client` object (`harness`, `agent`, `model`, `run_id`).
These are caller-reported labels recorded for the audit trail — never place
secrets or sensitive metadata in them. Every reply carries the request's own
ID in `request_id` (and the `X-Mousa-Request-ID` header), and errors are typed
(`unauthorized`, `forbidden`, `invalid_request`, `source_denied`, `too_large`,
`busy`, `conflict`, `not_found`, `canceled`, `internal`,
`coverage_unavailable`).

A note whose `citations` name trails the caller cannot read is denied, and a
replay of note text whose original citations are inaccessible is denied the
same way — a caller authorized for the note's source sees the shared text but
cited trails and stale reasons outside its read grants are omitted from the
note evidence rather than revealed.

### CLI over the endpoint

The same binary talks to a served store without holding it open:

```sh
export MOUSA_TOKEN="$TOKEN"
printf '%s\n' '{"id":"inspection","text":"The harbor inspection moved to Friday."}' |
  ./mousa -endpoint http://127.0.0.1:8787 sync --source inspection-notes
./mousa -endpoint http://127.0.0.1:8787 query --source inspection-notes 'harbor inspection'
./mousa -endpoint http://127.0.0.1:8787 remember --source memory 'note text'
```

`-endpoint` supports only `query`, `remember`, and `sync --source`; the sync
form posts each JSONL line on stdin as one request. The HTTP envelope carries
the request ID; the CLI prints the result or a typed endpoint error with
its request ID. If audit recording fails, `coverage_unavailable` indicates
that the daemon cannot attest complete coverage. A transport failure means
the outcome is **unknown** — the request may or may not have landed, so
the client never retries silently. Check
`GET /v1/requests/{request_id}` (operator) when in doubt.

### Audit history and live stages

Every served request is admitted to the durable audit loop before work —
unauthenticated, malformed, oversized, busy and canceled attempts included —
and normally finished with one terminal outcome (`committed`, `no_op`,
`denied`, `invalid`, `failed`, `canceled`, `interrupted`). Monitor endpoints (`/v1/requests`,
`/v1/events`), `healthz`, dashboard assets and unknown routes are deliberately
**not** audited. `GET /v1/requests?after=SEQ&limit=N` pages the verified
history with `next_after`/`more`; `GET /v1/requests/{request_id}` returns one
entry's verified admission, outcome and evidence reference. Both are
operator-only and carry metadata across all sources, never request or note
bodies.

`GET /v1/events` streams live stage observations (`receive` through
`response`) as SSE for up to 8 concurrent operator viewers, identified
`{epoch}:{seq}` per service run. The ring is a bounded buffer of recent
frames, not a permanent record: on overflow or restart the stream emits an
explicit `gap` event and the client must re-read the durable history via
`/v1/requests` — terminal records there are the authoritative record a viewer
reconnects against. Reconnecting with `Last-Event-ID` resumes retained frames
without duplicates; an unknown or foreign cursor produces a `gap`, never
silent misses. Stage timings are measurements, not archive: after a gap the
dashboard shows measured stages only for newly observed requests.

The dashboard at `/` is same-origin, holds the operator token only in page
memory, and renders the live ledger plus verified per-request detail. It is a
local surface — keep the daemon on loopback and do not publicize its address.

### Stop, verify, and back up

Stop the daemon with terminal Ctrl-C (SIGINT/SIGTERM); it cancels and drains
in-flight requests. On restart, admissions left without outcomes are marked
interrupted. After stopping run
`mousa -store STORE verify` (and `verify --log` for sequence coverage) exactly
as for an offline store.

For backups use SQLite's online Backup API or the `.backup` command of the
`sqlite3` interpreter into an entirely new path, never a raw copy of the
database or WAL file:

```sh
sqlite3 STORE ".backup 'STORE.snap'"
./mousa -store STORE.snap verify
sqlite3 STORE.snap ".backup 'STORE.restored'"
./mousa -store STORE.restored verify
```

Use the restored path only after verification passes. The
[SQLite backup checklist](../examples/backup/README.md) exercises the
same API against fixture passages.

Automation can consume the verified history without the dashboard; see
[examples/automation/README.md](../examples/automation/README.md) for a
loopback-only revision forwarder.

## Local MCP usage

The same executable serves MCP `2025-11-25` over stdio, backed directly by the
canonical Go/SQLite operations. Configure an MCP client's executable and
arguments, for example:

```json
{
  "mcpServers": {
    "mousa": {
      "command": "/absolute/path/mousa",
      "args": [
        "-store", "/absolute/path/evidence.sqlite",
        "mcp", "--caller", "cli",
        "--source", "inspection-notes",
        "--ingest-source", "inspection-notes"
      ]
    }
  }
}
```

The client launches the process and performs initialization and tool discovery.
The six tools are `mousa_sync`, `mousa_status`, `mousa_query`, `mousa_trail`,
`mousa_remember`, and `mousa_changes`. MCP has no access tool and no
allow option: a source synced through `mousa_sync` stays denied until the
operator runs `mousa -store STORE access --source ID allow` natively (see
[Access](#access)). `mousa_remember` writes one durable note to a configured
ingest source (see [Notes](#notes)) — the trusted `--caller` becomes the note
author, never a caller-supplied value; optional `derived_from` citations use
the same `trail_id`/`segment_ids` shape as the CLI.
Store, permitted sources and trusted caller are fixed at startup. Item ingestion
is disabled unless that source also has `--ingest-source`.
The default stdio mode has no listener, directory import, shell, access administration or withdrawal tool.


Pass `{"trail_id":"..."}` to `mousa_trail`. Reads do not write; an unavailable
ID returns `not_found` whether absent or not currently authorized.

### Bounded project-history consumer

[`examples/memory/memory.py`](../examples/memory/memory.py) uses native stdio MCP
with an explicit history source and a bundled 28-document synthetic fixture. It
releases attributed passages, not answers, and captures request/response
receipts per invocation. `ingest` writes items through `mousa_sync`, but MCP has
no allow option, so `ask` returns an empty passage list until
the operator records `access --source memory allow` natively.

The [frozen protocol, measured source and exact receipts](https://github.com/graydeon/mousa-benchmarks/tree/b1be28ac9f12134a7513c1e28fe884e7f657c988/results/2026-09-30-agent-memory)
are pinned in the benchmarks repository. That run predates default deny.
