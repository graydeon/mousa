# Using Mousa

For source-build versus tagged-release availability,
see [development status](OVERVIEW.md#source-and-release-status).

[← Back to Mousa](../README.md) · [Capability matrix](CAPABILITIES.md) · [How Mousa works](OVERVIEW.md)

Full CLI and service usage. Consumer examples are linked where they exist. Run commands from
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

A query prints one JSON object on one line by default. The object has `trail`,
the ID of the recorded `mousa.trail.v1` record, and `passages` in packed order.
Each passage has `path`, `kind`, and `text`. A note's `date` is its creation
date; a document's `date` is when its current revision was activated. Updating
or restoring a revision records a fresh activation time, while a no-change
re-sync or path-only move keeps the existing one. Dates use the local time zone
of the CLI process and are omitted when the store has no activation proof.
Notes also have `author` and `note` (the note ID). `stale` and `superseded`
appear only when true. `kind` is `note`, `document`, or `item`.

```json
{"trail":"<trail-id>","passages":[{"path":"notes/cedar","date":"2026-01-02","kind":"note","author":"agent","note":"<note-id>","text":"Cedar note.","stale":true}]}
```

Compact output does not HTML-escape `<`, `>`, or `&`.

`access:"deny"` appears only when at least one source resolves and all resolved
sources deny retrieval. It does not appear when a query merely has no matches,
when a collection is empty, or when some sources allow access. The full trail is
recorded for either output form, and `trail.result_sha256` compares results
across equivalent requests against the same state. Lexical is the only
supported mode.

`--full` prints the previous indented query result, including passage
coordinates, digests and note lineage. A document passage carries optional
integer `activated_at_usec` when its activation time is recorded or proven;
the HTTP response uses the same field. Local queries include the canonical
trail. Served queries include the HTTP caller view. The stored trail is an
append-only log leaf. It records the request, per-source access, snapshot
index, allowed source heads, candidate scores and ranks, and packing decisions.
`trail.id` identifies this record.

`--budget-bytes` defaults to 8,192. It covers released text, not JSON or model
tokens. First-fit packing skips passages that do not fit and omits exact-content
copies of already packed passages. The trail records `packing.used`, `packed`,
`omitted`, and `duplicate_of` as applicable. A denied query has empty `passages`
and no retrieval, candidates, or packing. An allowed query with no matches has
an empty candidate list.

Queries consider up to 200 lexical candidates per query text. Use `--limit`
(HTTP `limit`) to set a cap from 1 to 200; zero uses the default. Shared BM25
uses the same cap for each shard's shortlist. The trail records the cap used,
even if fewer candidates match. Verification accepts older trails. Older
binaries reject trails whose recorded request limit exceeds 100; fix forward
after writing one.

`mousa -store STORE trail TRAIL_ID` reads the full canonical record by `trail.id`
without writing. This is an owner interface, not a way to retrieve historical text.
Use `recheck --trail TRAIL_ID` to check whether its evidence is still current;
a served store requires `--text`.
The caller view carried by a served query reply omits `as_of`,
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
does not grant access.

When a query's eligible ancestry spans more than one shard, each shard
still proposes its top hits by its own BM25, and every proposal is
rescored with the same FTS5 arithmetic against statistics summed over all
eligible shards: row count, token count and each phrase's document
frequency, including shards that returned no hits. A single text merges
by that shared score with ties by segment ID; several texts keep their
per-text rankings and fuse with the existing reciprocal-rank rule. Each
branch names the scoring `bm25-shared-v1` and records the shared
statistics; each candidate score records the token length and per-phrase
occurrences it was recomputed from. Multi-text fusions are named
`bm25-shared-rrf-k60-v1`. A query whose eligible ancestry spans zero or
one shard keeps the earlier ranking, names and evidence, and no query
accepts weights. Verification replays ranking and packing from recorded
score bits and accepts a recomputed score within
`1e-9 * max(1, abs(recorded))` of the recorded value so library rounding
changes do not invalidate stored trails.

### Verify a store

Open checks only store identity and schema compatibility; it does not scan the
contents. Run `mousa -store STORE verify` for a full check, `verify --log` for
log sequence, hashes and per-kind coverage, `verify --trail TRAIL_ID` for a
named record, or `verify --stdin` for whitespace-delimited trail IDs. All modes
open the source read-only, return JSON `record_counts` on success, and fail
with a non-zero exit on damage. Whole-store verification checks an isolated
SQLite page copy because the FTS5 integrity command requires write permission.

A store written by a schema-7, schema-8, or schema-9 binary is refused on
open with a hint to run `mousa -store STORE migrate`. Migration rewrites the
store in place to schema 10; stop any daemon holding it first and keep a
verified backup. It preserves every note, citation, trail and request record,
and prints `{"schema": 10}`; run `verify` afterwards. Stores older than schema 7
are still refused without modification and must be rebuilt by re-syncing
from their sources.

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

A source synced through the served store stays denied until the operator runs
`mousa -store STORE access --source ID allow`. The service has no allow
endpoint, on purpose: a client that can write a source cannot also release it.

## History

A source can retain the text each `sync` or `sync --source` update or delete
retires. The setting is per source and off by default, including for stores
carried through a migration, where retirement before opt-in is never
recovered. `access --source ID --history on|off` changes the setting on an
existing source; like any access change it runs offline against an explicit
store, so stop a serving daemon first and keep a verified backup. The setting
is logged. Repeating a recorded setting reports `changed: false`.

```sh
mousa -store local.sqlite access --source docs --history on
mousa -store local.sqlite access --source docs --history off
mousa -store local.sqlite recheck --trail TRAIL_ID --text
mousa recheck --trail TRAIL_ID --text
```

History is never retroactive: turning it on keeps only later retirements,
and turning it off again leaves the existing archive readable. There is no
retention limit or purge. Archived text is verified against its canonical
segment on every read and is never searchable: retired terms return no hits.

`recheck --trail TRAIL_ID --text` adds a `released` list with each packed
segment's status: `indexed` (still live), `kept` (served from the archive)
or `not_kept` (retired before opt-in or while off). `--text` requires
`--trail` and is the only form a served store accepts; the caller needs a
current read grant on every allowed source the trail touched, so a denied or
withdrawn source releases no text. Plain `recheck` reports staleness
unchanged and never reads the archive.

On a served store a document request's monitor detail also carries the
revision text it touched: `previous` and `current` when they are kept,
`previous_not_kept` when a former revision was never archived, and an empty
content object when the source is denied or withdrawn. The requested
revision's own text is shown, never a newer one in its place; an empty
revision stays distinguishable from a missing one. `--open-monitor` needs no
principal for monitor reads, but document text still requires the source to
be allowed and not withdrawn. Open an update in the dashboard to see the
line diff.

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

Lexical query's compact output reports a stored note's ID, author, local
creation date, and true `stale` or `superseded` flags. A document's compact
date is its current revision activation date, omitted when unknown. `--full`
surfaces a note as ordinary evidence plus a `note` object carrying its ID,
author, creation time, lineage and a `stale` array. Each stale entry names
the affected `trail_id` (and `segment_id` when a single segment retired) with
a reason of `source_denied`, `source_withdrawn` or `segment_retired`; no cited
text is released. Notes follow the same per-source access rules as document
evidence: a denied or withdrawn note source releases nothing, and later access
changes do not redact note bytes already released.
`sync` refuses a source holding notes, and `remember` refuses
a document source, including one whose items are all inactive.

Notes share the source's static identity and access grant; there are no
agent-specific sources or per-note grants yet. Notes cannot be edited or
retracted. Moving and superseding leave the stored text unchanged.

## Paths

Every local item carries one location path alongside its item ID. A path is at
most 256 bytes of lowercase ASCII segments joined by `/`
(`^[a-z0-9_][a-z0-9._-]*(/[a-z0-9_][a-z0-9._-]*)*$`); uppercase letters, empty
segments, `.` and doubled slashes are refused. Notes default to `unsorted`;
`remember --path workspace/decisions` places a new note, and a repeat of the
same note keeps its stored path. Documents default to their literal item ID —
`docs/one` lives at `docs/one`, `.env` at `.env` — whether they were synced
before or after paths existed, and that ID stays the path until it is moved.
Every explicit destination is validated, and passing `--path ''` is an error
rather than a request for the default. A JSONL `sync` record may carry a
`"path"` for a document item: the same text with a different path relocates it
(`"action": "moved"`) without rebuilding its representation, a record with
both changes updates the text and moves it in the same commit, and omitting
`"path"` on re-sync preserves the current path. Delete records reject a path.

Queries report each passage's current `path`. Paths describe locations only:
they never rename an item ID or a note ID.

Paths starting with `_` are for meta documents. Collection queries leave them
out. Query the source directly with `--source` to read one.

## Move

`mousa move` relocates stored items without re-syncing them. One selector is
required: `--note NOTE_ID`, `--item ITEM_ID`, or `--prefix OLD/`, and `--to`
names the new path (a prefix move appends each item's suffix after the new
prefix). Directory sources move offline; JSONL sources move offline through
`-store` or against a served store through the endpoint client.

```sh
./mousa -store local.sqlite move --source notes --note NOTE_ID --to workspace/decisions
./mousa -store local.sqlite move --source docs --prefix team/ --to shelf/
./mousa -endpoint http://127.0.0.1:8787 move --source docs --item docs/a --to shelf/cli
```

The served endpoint accepts the same JSON:

```sh
curl -X POST "$ENDPOINT/v1/moves" -H "authorization: Bearer $TOKEN" \
  -d '{"source": "docs", "item": "docs/a", "to": "shelf/cli"}'
```

A move is one immutable `path_move` log leaf covering every entry, attributed
to the operator or the authenticated principal (which needs a write grant on
the source). A note or item ID that names nothing errors; a prefix that
matches nothing, or only items already at the destination, is a no-op: the
served request still records an admission and a no-op outcome, but no move
leaf. Moving preserves IDs, bytes, representations and access accounting;
earlier `trail` records still resolve, and `verify` stays green. The monitor
shows each move's old and new paths in the open request's content as `Moved
paths`; closed requests show metadata only. The destination's lifecycle rules
apply: a denied or withdrawn source cannot be moved into.

## Supersede

`remember --supersedes NOTE_ID` records that the new note replaces an older
note of the same source; repeat the flag for several targets. Every target
must already exist in that source — self, missing, foreign-source and repeated
IDs are refused, and a note already superseded can still be named. The edge is
immutable and lands with the note atomically: there is no un-supersede.
Repeating the same text returns the stored note; only an explicitly different
`--supersedes` set conflicts.

```sh
./mousa -store local.sqlite remember --source memory \
  --supersedes OLD_NOTE_ID 'The harbor inspection uses dry dock 5.'
```

Default queries omit candidates whose note is superseded (`omitted:
"superseded"`); `--include-superseded` (or a native query's
`"include_superseded": true`) packs them and marks each passage's note with
`superseded_by`. The query trail records the `supersession` effects it used,
so a trail taken before a replacement still verifies afterwards. Export and
import carry the edges inside each canonical note record, in any line order.

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
flag the CLI reads all allowed sources.

An `updated` item entry names both the retired and newly current revision.
A denied source contributes nothing; a withdrawn source that still has
an allow event contributes withdrawal notices only. The cursor is a
global log index, not a count of visible changes.

`recheck` returns `fresh`, `stale`, `unsourced` (note without citations),
or `empty` (trail without released passages). Stale reasons include
withdrawn or denied citation sources, retired segments, citations
that this store cannot verify, and `bm25_statistics_changed` when a
shared-statistics trail's eligible shards changed its recorded counts —
including one that grew without releasing anything. Rechecking does not
rewrite a note or trail.

## What the store does not protect against

Integrity. Each ingest receipt, access event, withdrawal, query trail and note
appends a hashed leaf to an append-only log in the same transaction. Until signed checkpoints
exist, anyone who can write the SQLite file can forge a consistent history.
The log detects accidental damage and partial edits, not a deliberate rewrite.

Retention. Trails keep the query text, caller and prepared terms. Item
replacement and deletion remove the indexed text row. Sources with
[history](#history) enabled also keep a separate copy of retired text.
Withdrawal does not remove indexed or archived text. Removing a row is not
secure erasure: copies can remain in free pages, WAL files, backups and
previously released output. There is no retention limit or purge command.

## Local served store (opt-in)

`mousa -store STORE serve --config FILE [--listen 127.0.0.1:PORT]` owns the
store and serves JSON request endpoints, an operator request stream, and an
embedded dashboard over loopback HTTP only. It is a separate opt-in surface
for local automation: the offline CLI keeps working unchanged on stores
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

Add one principal per agent. `agent add` mints the token, records only its
SHA-256 verifier in the serve config (creating the file if needed), and
writes the token to a new private file:

```sh
./mousa agent add codex --config PRINCIPALS.json --source memory --token-file ~/.config/mousa/tokens/codex
./mousa agent add reader --config PRINCIPALS.json --read inspection-notes --token-file ~/.config/mousa/tokens/reader
./mousa agent add monitor --config PRINCIPALS.json --read memory --operator --token-file ~/.config/mousa/tokens/monitor
```

`--source` grants read and write; `--read` and `--write` grant one direction.
Without `--token-file` the token is printed once in the JSON result, for a
secret manager. Raw tokens never enter the config, the store, or the log.
`serve` reads the config at startup, so restart it after adding an agent.

`operator` grants metadata-only monitor reads and never bypasses per-source
read or write denial. A nonoperator principal gets a denied outcome for
requests outside its grants and `403` on the monitor endpoints; the daemon
never reveals whether another principal exists.

Run the daemon in a separate terminal. It prints its bound loopback URL on startup.

```sh
./mousa -store STORE serve --listen 127.0.0.1:8787 --config PRINCIPALS.json
```

Open the printed URL in your browser. Enter the monitor token if the page asks for one.
Use a caller token, not the monitor token, for the smoke commands.

### Requests and replies

`POST /v1/query`, `POST /v1/notes` and `POST /v1/documents` accept one JSON
object each with typed fields:

- `/v1/query`: `{"scope":{"source"|"sources"|"collection"},"text","budget_bytes","limit"}`
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

The same binary talks to a served store without holding it open. Write a
client configuration once, at `mousa/client.json` under the user configuration
directory (`~/.config` on Linux) or the path in `MOUSA_CONFIG`:

```json
{"endpoint": "http://127.0.0.1:8787", "token_file": "~/.config/mousa/tokens/{harness}"}
```

After that an agent needs no flags and no token in its environment:

```sh
export MOUSA_HARNESS=codex MOUSA_MODEL=MODEL_ID
./mousa remember --source memory 'note text'
./mousa query --source memory 'note'
printf '%s\n' '{"id":"inspection","text":"The harbor inspection moved to Friday."}' |
  ./mousa sync --source inspection-notes
```

That is the whole agent integration: put those two commands in the agent's
instructions. Mousa ships no MCP server.

| Setting | Order of precedence |
|---|---|
| Endpoint | `-endpoint URL`, then `MOUSA_ENDPOINT`, then `endpoint` in the client configuration |
| Token | `MOUSA_TOKEN`, then `token_file`, then `token_command` |

`token_command` is an argv array whose standard output is the token, for a
secret manager: `["pass", "show", "mousa/{harness}"]`. `{harness}` in
`token_file` or `token_command` expands to `MOUSA_HARNESS`, so one
configuration serves several agents, each with its own principal. Passing
`-store` ignores the client configuration.

An agent in a sandbox needs two things from it: a connection to the loopback
endpoint, and read access to the client configuration and its token source.
A sandbox that blocks either one must be configured to allow it.

A served store supports `query`, `remember`, `sync --source`, `move`, and
`recheck --trail TRAIL_ID --text`; the sync
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
bodies. `GET /v1/recheck/{trail_id}` is the served form of
`recheck --trail --text`: any authenticated caller gets released segment
text only for sources inside its read grants and `not_found` otherwise,
and like the other reads it appends no audit record.

`serve --open-monitor` serves these monitor reads, the stage stream and the
dashboard without a credential, for a private deployment where everyone who
can reach the listener may see every request. In this mode a request's
detail also carries its query text with the passages returned, or its note
text, across all sources; a document request's detail carries the revision
text it touched. An allowed source releases the indexed current revision
even with [history](#history) off, plus retained earlier text when it was
kept; a denied or withdrawn source releases neither. Query, note
and document endpoints still require their bearer credential.

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

The dashboard at `/` is same-origin, holds any operator token only in page
memory, and shows each pipeline stage lighting up as requests pass, the
agents calling, and recent requests with their verified detail. Clicking a
request opens everything recorded for it, including each returned passage in
full when the monitor is open. It is a
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
