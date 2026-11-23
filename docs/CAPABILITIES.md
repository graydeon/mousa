# Supported local workflow

For source-build versus tagged-release availability,
see [development status](OVERVIEW.md#source-and-release-status).

Mousa is pre-alpha. The supported executable is `cmd/mousa`, built from source with
Go 1.25 or newer. The core packages are internal, not a stable SDK. Its CLI and
MCP interfaces store and retrieve evidence; none generates answers or establishes
factual truth.

## Capability matrix

“Core” means an implementation exists. “CLI” means an operator can use it through
`cmd/mousa`. Passing behavior tests do not establish retrieval quality or performance
on other workloads. Measurements are published separately in
[mousa-benchmarks](https://github.com/graydeon/mousa-benchmarks); the pre-rework
research record is archived at [archive/research-pre-rework.md](archive/research-pre-rework.md)
and does not describe the current code. Scale at 100k documents and 1M retrieval
units is a [development target](OVERVIEW.md#scale-targets), not a property of today's
binary, until the scale probe passes at both sizes.

| Capability | Core | CLI | Behavioral coverage |
|---|---|---|---|
| UTF-8 directory import and resync | Yes | `sync <dir>` | Import, repeated no-op, update, revert, delete, restore, restart |
| Directory preview and selection controls | Yes | `sync --preview`, include/exclude globs, explicit overrides and limits | Actual-CLI scope changes, preview without store creation, failed validation preserving activation, rooted link rejection |
| JSONL item input | Yes | `sync --source <id>` | Retry, replacement, tombstone, malformed input, committed prefix, input limits |
| Query scope | Yes | `query --source ID` (repeatable), `query --collection NAME` | Persistent membership, per-source decisions and denied-source score isolation |
| Raw-content identity and normalized text | Yes | Both input forms | CRLF, BOM, empty text, exact retries |
| Versioned passage boundaries | Yes | `sync --segment-policy passage-v1`; source-built BEIR `-segment-policy fixed-v1|passage-v1` | Exact source slices, policy replacement, retries, interrupted activation, historical identities and source exclusion; evaluator identity/reuse/resume and explicit comparison axis |
| Atomic current-item activation | Yes | Both input forms | Transaction failure, reader snapshots, abrupt exit, retry |
| Source-scoped lexical retrieval | Yes | `query` | Cross-source isolation and current-only evidence |
| Source lifecycle (withdrawal) | Yes | `withdraw` | Withdrawn sources release no evidence; cross-source isolation |
| Per-source access decisions | Yes | `access <dir>|--source <id> allow|deny`; `sync --allow`; default deny with no event | Append-only events, default deny, no-op repeats, MCP-synced sources denied until native allow; see [Access](USAGE.md#access) |
| Byte-budget evidence selection | Yes | `query --budget-bytes <positive>` | First-fit byte accounting, duplicate omission, and history reads. |
| Canonical query trails | Yes | `query`; `trail` ID | Stable result digests, atomic leaf writes, corruption checks and caller-view masking. |
| Durable notes | Yes | `remember --source <id>`, `notes export --source <id>`, `notes import --source <id>`; MCP `mousa_remember` for configured ingest JSONL | Immutable canonical records with typed trail/segment lineage; idempotent writes, exact stdin/argument bytes (CRLF/BOM preserved), byte-exact export, lexical recall with stale reasons, denied/withdrawn enforcement, write guards on document sources |
| Embedding inference, answer generation | No | No | Not implemented |
| Local stdio MCP | Yes | `mcp --caller <id> --source <id>` | Real SDK client/executable round trips, persistence, configured boundaries, committed prefix, framing, cancellation and shutdown |
| OAuth-protected Streamable HTTP | Yes | Opt-in `mcp --http-config FILE` | Local TLS introspection fixture covers subject, audience, issuer, expiry, scopes, revocation and host/origin boundaries |
| Stable SDK, general connectors, public pairing relay | No supported interface | No | Not implemented as supported interfaces |

The [Git documentation example](../examples/docs/README.md) is a separate Python CLI
example. It returns source passages and does not generate an answer.

## Local stdio MCP

`mousa -store STORE mcp --caller cli --source inspection-notes
--ingest-source inspection-notes` serves one local client over stdin/stdout.
Repeat `--source` for each permitted JSONL source, and repeat `--ingest-source`
only for sources that may accept items or notes. Writes are disabled by default. Source
IDs are exact, case-sensitive opaque identities in namespace `mousa-jsonl`,
not paths to open. This interface cannot import directory sources.

The store path, source allowlist and trusted caller ID come only from process
arguments. Unknown sources, paths used as unconfigured source IDs, and tool
fields such as `store`, `root` or `caller` are rejected before store mutation.
An operator may configure an opaque source ID that looks like a path; it still
does not grant filesystem access. Protect the executable invocation, store,
WAL, backups and released output with local filesystem controls.

Access is per source and applies to every caller, including MCP. MCP exposes no
access tool and `mousa_sync` has no allow option, so an MCP-synced source stays
denied until the operator runs `access ... allow` natively; see
[Access](USAGE.md#access). MCP does not authenticate the startup identity.

### Protocol and tools

The supported protocol is [MCP `2025-11-25`](https://modelcontextprotocol.io/specification/2025-11-25/basic/lifecycle),
implemented with the official [Go SDK v1.8.0](https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.8.0).
Initialization advertises the tools capability. If the client requests another
version, the server offers `2025-11-25`; a client that cannot use it should
disconnect. `tools/list` exposes the input and output JSON Schemas. The default
stdio mode exposes no prompts, resources, roots, sampling, tasks or remote transport.

| Tool | Required arguments | Optional arguments | Success `result` |
|---|---|---|---|
| `mousa_sync` | `source`, `items` | `segment_policy`: `fixed-v1` (default) or `passage-v1` | Existing sync report: action lists, `total_items`, segment policy, store-size sample and elapsed seconds |
| `mousa_status` | `source` | None | Existing status report: collection state, active items and observations |
| `mousa_query` | Exactly one of `source`, `sources`, `collection`; `query`, `budget_bytes` | `mode`: `lexical` (default) | Caller-view trail and selected passages. Every requested member must be configured for the caller. |
| `mousa_trail` | `trail_id` | None | Caller-view trail, without passage text. |
| `mousa_remember` | `source`, `text` | `derived_from`: array of `{trail_id, segment_ids?}` | Canonical `mousa.note.v1` record: ID, content digest, author (trusted caller), time and stored lineage; exact retries return the first note |

For example, call `mousa_sync` with:

```json
{
  "source": "inspection-notes",
  "items": [
    {"id": "notes/review@draft", "text": "The harbor inspection is scheduled for Friday."},
    {"id": "notes/withdrawn", "deleted": true}
  ]
}
```

`mousa_sync` records no access event, so the source is denied until the operator
runs `mousa -store STORE access --source inspection-notes allow` natively. Then
call `mousa_query` with:

```json
{"source":"inspection-notes","query":"harbor inspection","budget_bytes":4096}
```

Use the returned `result.trail.id` with `mousa_trail`. Items use the same strict
record validation, normalization, immutable revision identities and atomic
current-item activation as [JSONL input](#jsonl-input). There are no new
packing rules. Evidence coordinates refer to
normalized UTF-8 bytes; use the [same digest and range checks](#verify-a-selected-passages-location)
as for CLI evidence. Treat every source text as untrusted data, not protocol,
shell commands or instructions to the client.

### Structured results and failure state

Every completed known-tool call returns `structuredContent` with schema
`mousa.mcp_result.v1`. A text content block contains the same serialized JSON
for clients that do not consume structured content. Its object has:

| Field | Meaning |
|---|---|
| `schema` | Always `mousa.mcp_result.v1`. |
| `operation` | The invoked tool name. |
| `source` | Configured source label, or an empty string for `mousa_trail` and when arguments failed decoding. |
| `result` | Success payload described above; absent on a tool error. Its full schema is discoverable in that tool's `outputSchema`. |
| `error` | Tool error object; absent on success. |

`error` has `code`, a diagnostic `message` of at most 512 UTF-8 bytes,
`completed_items`, and an optional one-based `failed_item` index. `completed_items`
counts successfully applied item records, including no-op retries; their prefix
remains committed. It is zero for errors outside item application. No partial
success report is emitted on an ingestion error. Correct and replay the call:
unchanged content remains a no-op and repeated tombstones report `absent`.

Argument-envelope errors, unknown/unconfigured sources, disabled ingestion,
and excessive item counts are checked before applying items. Item validation
and size limits are checked in order, so a later invalid item does not undo
earlier activations. Failure during one item's preparation may retain immutable
history but does not activate that failed revision. This is not whole-batch
atomicity. Omitted items are unchanged; deletion must be explicit.

Tool errors set MCP `isError: true`. Boundary codes include
`invalid_arguments`, `source_not_permitted`, `ingestion_not_permitted`,
`invalid_item`, `resource_limit`, `busy` and `cancelled`; canonical storage errors
retain their existing codes, with `operation_failed` for other failures.
Query denial is a successful result with no passages. Trail reads are read-only
and return `not_found` for absent and unauthorized IDs.
Unknown tools/methods and malformed protocol parameters use SDK JSON-RPC errors.
Invalid JSON or an oversized inbound frame terminates the stdio session with a
bounded diagnostic on stderr, without a success message. Earlier completed
requests/items remain durable; inspect status after reconnecting.

### Limits, concurrency and shutdown

| Bound | Maximum |
|---|---|
| Inbound JSON-RPC frame | 2 MiB |
| Configured sources | 128 |
| Item records per sync call | 128 |
| Serialized JSON bytes per item | 1 MiB |
| Decoded UTF-8 text per item | 256 KiB |
| Item ID | 4,096 UTF-8 bytes; nonempty, no NUL |
| Query text | 4,096 UTF-8 bytes; nonempty |
| Released-text budget | Integer 1–65,536 bytes |
| Lexical candidates | 100, preserving the existing query contract |
| Concurrent tool calls admitted | 8; excess calls return `busy` |
| Tool-call deadline, including time waiting for the store | 30 seconds |

JSON Schema string lengths count characters; byte bounds above are additionally
enforced at runtime. Frame overhead and JSON escaping also consume the framing
and per-item bounds. A released-text budget is not a cap on protocol output,
metadata, model tokens, or a complete model context window.

The process owns one canonical store connection and serializes complete
application calls, including each whole multi-item sync. Separate processes
still do not form a supported concurrent source-wide synchronization protocol.
Coordinate external ingestion and administrative changes; SQLite conflicts are
errors, not an implicit retry mechanism.

Cancellation is propagated into the current operation, including queued calls.
Already completed item activations are not rolled back by cancellation.
SQLite's existing one-second busy timeout remains unchanged; cancellation is
not a promise of instantaneous interruption of its busy wait.
[Stdio shutdown](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports#stdio)
uses stdin closure/disconnect or SIGINT/SIGTERM, not a shutdown tool. The server
cancels pending work, drains handlers and closes its owned store. Stdout contains
only protocol messages; diagnostics go to stderr.

The deterministic actual-client regression command is
`CGO_ENABLED=0 go test ./cmd/mousa -run TestMCP -count=1 -v`. It launches the built
executable with the official SDK client and temporary SQLite stores, validates
discovered output schemas and logs client/server receipts. It is not a
model-driven agent run or a comparative measurement.

### Authenticated HTTP and deployment

HTTP is a separate transport, not an extension. `mcp --http-config FILE` serves
stateless JSON Streamable HTTP at `/mcp`. The same startup caller/source/store
bindings and source-deny precedence apply. Each process accepts one configured
OAuth subject; deploy separate processes, OS users and stores for separate
accounts. There is no shared multi-tenant writable store or client-selected path.
This is a dedicated-account, operator-hosted deployment, not a public pairing relay.

The JSON configuration requires:

| Field | Contract |
| --- | --- |
| `listen` | Numeric loopback address and port 1–65535; no LAN/public bind |
| `resource` | Stable absolute HTTPS URL ending in `/mcp`; no credentials/query/fragment |
| `issuer` | Exact HTTPS OAuth authorization-server issuer |
| `subject` | Exact expected user subject; all other subjects are rejected |
| `introspection_url` | Operator-configured HTTPS RFC 7662 token-introspection endpoint |
| `client_id` | Confidential resource-server introspection client |
| `client_secret_env` | Environment variable containing its secret; no secret in package/config |

Start it with the same `-store`, `--caller`, `--source` and optional ingestion
flags as stdio. Supply an established OAuth 2.1 authorization server
with authorization-code/PKCE S256 and client registration appropriate for the
host, plus an operator-managed HTTPS reverse proxy. Mousa is the resource server,
not an OAuth authorization server. The proxy must preserve the configured public
Host or exact loopback authority. Forwarded identity/host headers confer no trust.
Untrusted Origins are rejected for MCP; public protected-resource metadata supports
cross-origin authentication discovery. No unauthenticated tool listener is started.

Every HTTP request introspects the bearer token without caching. The response
must contain active, exact `iss`/`sub`, matching `aud` (string or array), future
`exp`, acceptable `nbf` if present, and space-separated `scope`. Missing/invalid
claims fail closed. `mousa:read` is required for all MCP requests; item ingestion
and note writes also require `mousa:write` and startup source permission. This is not an API-key login
fallback. Introspection uses HTTP Basic service authentication, a five-second
timeout, bounded response, TLS verification and no redirects. Provider errors
release no token/secret details. The configured IdP must support these claims.

The unauthenticated metadata endpoint is
`/.well-known/oauth-protected-resource/mcp`; 401 responses advertise it through
`WWW-Authenticate`. Request bodies are limited to 2 MiB, headers to 16 KiB and
admission to eight concurrent requests including token verification. Stateless
requests retain no session identity/replay buffer. Shutdown drains handlers before
closing the canonical store.

## Item identity and lifecycle

A directory source uses namespace `mousa-local` and its absolute root path as its
external identity. An item is identified by its relative POSIX path. Moving a root
creates a different source. A renamed file is a deletion plus an addition.

A JSONL source uses namespace `mousa-jsonl` and the exact `--source` value as its
external identity. That namespace is distinct from directory sources, even if the
external value equals a directory path. JSONL item IDs are opaque, case-sensitive
strings; `@` and Unicode are supported.

Raw content identifies an immutable revision. Normalization may change the bytes
used for retrieval, so a raw digest is not a normalized representation ID. Replaying
a revision reuses its accepted delivery receipt, including the original capture time.
Timestamps and hash ordering do not select the current revision.

The current-item relationship is separate from revision history. Its activation and
lexical index rows change in one transaction:

| Prior state | Input | Reported action | Searchable state after success |
|---|---|---|---|
| Unknown item | Text, including empty text | `added` | Input revision |
| Active item | Identical raw content and segment policy | `unchanged` | Unchanged |
| Active item | Different content or segment policy, including an older revision | `updated` | Input representation only |
| Deleted item | Text | `restored` | Input revision only |
| Active item | Deletion | `deleted` | No evidence for that item |
| Unknown or already deleted item | Deletion | `absent` | No evidence for that item |

Empty text has no segments and produces no search result. Removing an index entry
is not secure erasure: canonical records remain as history, and old content may
remain in SQLite pages, WAL files, backups, or previously released output.

Sync prepares canonical records and activates them in the same transaction.
Batches contain at most 16 items. A failed item rolls back to its savepoint
without replacing the previous searchable revision; earlier completed items
remain committed. An interruption rolls back the in-flight batch. Retrying the
same input is supported. Concurrent syncs of the same source are not a
source-wide snapshot protocol; serialize them.

Canonical history retains identities, digests, and provenance, not a complete text
archive. After deindexing, Mousa cannot reconstruct an old revision's text from its
digest. Retain original source material separately if historical text is required.

`status` distinguishes `active_items` from historical `observations`. Source collection state is separate: an active source can contain
zero active items.

For sync reports, `total_items` counts selected directory files or processed JSONL
records, not the source's active item count. `store_bytes` samples the main database
file before close and excludes WAL and other files; it is not total disk usage.

## Ingestion segment policies

Both input forms accept `--segment-policy fixed-v1|passage-v1`. The default is
`fixed-v1`. For example:

```sh
./mousa -store local.sqlite sync --segment-policy passage-v1 --allow ./documents
./mousa -store local.sqlite query --budget-bytes 1024 ./documents 'restart procedure'
./mousa -store notes.sqlite sync --source notes --segment-policy passage-v1 --allow < items.jsonl
```

Repeat the selected policy on every sync. It is not saved as a source default.
Omitting the flag selects `fixed-v1`, including when the current items use passages.
Changing policy on identical raw bytes reports `updated`, reuses the original
observation, artifact and receipt, and atomically replaces the item's active
representation and index. It does not leave both segment sets searchable.
JSONL omission still leaves an item unchanged, so a partially resynced source can
contain items with different policies. Sync JSON includes `segment_policy`.

The policy is bound into the normalized representation's existing
`parameters_sha256` identity field. `fixed-v1` retains SHA-256 of empty bytes;
`passage-v1` uses SHA-256 of the exact UTF-8 bytes
`{"segmentation":"passage-v1"}`. Normalizer processor ID and version stay unchanged.
Segment IDs still bind representation, byte-range selector and content digest.
No schema change or historical ID rewrite is needed. Returning to an earlier
policy and content reuses those historical identities. Use a passage-aware binary
for future syncs; older binaries do not understand the selected policy.

`fixed-v1` emits up to 4,096 bytes per segment at UTF-8 code-point boundaries.
`passage-v1` emits up to 1,024 bytes per segment using these deterministic rules:

- Split normalized text into LF-delimited lines. Whitespace-only lines delimit
  paragraphs and stay with the preceding block; initial whitespace stays with
  the first block.
- ATX headings (one to six `#` characters, followed by whitespace or end of line,
  after at most three spaces) start a new passage group. Following blocks join
  the heading when they fit.
- List markers are `-`, `*`, `+`, or one to nine digits followed by `.` or `)`,
  then a space or tab. Consecutive list lines and nonblank continuation lines
  form a block. Blank lines end that block, including in loose lists.
- Consecutive nonblank lines containing a literal `|` form a table-like block.
  Escaped pipes and column syntax are not interpreted.
- Backtick or tilde runs of at least three characters after at most three spaces
  open a fenced block. A run of the same character at least as long closes it
  only when followed by spaces or tabs, or the line ending. Internal blank lines
  and headings do not end the fence. An unclosed fence extends to end of input.
- Greedily group complete consecutive blocks while they fit. For a block larger
  than 1,024 bytes, emit fragments ending at the last LF within the limit,
  otherwise the last ASCII space or tab, otherwise a UTF-8 code-point boundary.
  Emit the final remainder separately before grouping subsequent blocks.

These are line-level rules, not a Markdown parser. Nested container semantics,
setext headings and HTML blocks are not recognized. A fragment can be an
incomplete command, list, table or code block; code-point safety does not imply
grapheme safety. No heading, fence delimiter or other text is reconstructed.
Every segment is an exact contiguous normalized source slice, with no overlap,
duplication or omitted bytes. Normalization removes an initial UTF-8 BOM and
converts CRLF and CR to LF, so selectors refer to normalized bytes, not raw-file
offsets. Empty text emits no segments. A representation exceeding 65,536 passages
is rejected before activation, leaving its previous searchable revision intact.

Passages make smaller evidence units eligible for packing, not necessarily more
relevant. They change lexical document frequencies and ranking. A complete
structure larger than the budget cannot fit; smaller budgets can still omit all
matching segments.

## Durable notes

`remember --source <id>` and `mousa_remember` write one immutable
`mousa.note.v1` record per note: a canonical record carrying the note ID
(record digest), content digest, author, creation time, source and typed
lineage, plus one internal note-policy item holding the exact bytes. Every
write is transactional and idempotent: the first valid write wins, so
repeating the same text returns the first note's ID, author, time and lineage
without appending anything, regardless of the retry's author or submitted
lineage. Submitted citations are still validated on every write; an invalid
citation rejects.

Lineage is `derived_from`: an array of `{trail_id, segment_ids?}` objects.
`trail_id` is required and must name a committed trail whose decision allowed
retrieval; `segment_ids` omitted or empty means every segment that trail
selected, and each listed segment must have been selected. Each cited source
must currently allow access and not be withdrawn, and for MCP must be a
configured source. A note without lineage is unsourced (`origin: "unsourced"`,
empty `derived_from`). A checked citation identifies what the trail released,
not whether the note is true and not whether its citations are complete. Note
text is untrusted content: never store secrets or transcripts.

Lexical query emits selected notes as ordinary evidence plus a `note` object
(`id`, `author`, `created_at_usec`, `derived_from`, `origin`, `stale`).
`stale` is an array of `{trail_id, segment_id?, reason}` entries; reasons are
`source_denied`, `source_withdrawn` and `segment_retired`, describing what
changed under the citation without rewriting the note. Notes follow the same
access, lifecycle and budget rules as document evidence: a denied or
withdrawn note source releases nothing, and access changes do not redact note
bytes that were already released. `notes export --source <id>` streams each
canonical record plus exact text as JSONL from a read-only open. After
registering and allowing the same source in a new store, use
`notes import --source ID < notes.jsonl`. Import preserves the record and exact
text, rejects foreign sources or bad digests, is atomic, and treats identical
imports as no-ops. If an imported citation's trail is absent, recall reports
`citation_unverifiable`.

Notes share their source's normal access grant; there are no agent-specific
sources or per-note grants. The CLI author is the operator identity, the MCP
author is the trusted `--caller`, and an MCP note write requires the source's
`--ingest-source` grant (plus `mousa:write` over HTTP). Document and note
sources stay disjoint: `sync` and `mousa_sync` refuse a source holding notes,
and `remember` refuses a source holding document items, active or not.
Not supported: editing or retracting a note,
recalling across multiple sources, a citation change cursor or standalone
recheck, a daemon, and any change to ranking.

## Store schema and older stores

A store has one schema. Mousa marks it in the SQLite header (`application_id`
and `user_version`) and checks that header before it reads or verifies any record.
There are no migrations. A store written before the schema was collapsed, or by
an earlier schema version, is refused with an `incompatible_schema` error that
says to create a new store and re-sync it from its sources. A SQLite file that is
not a Mousa store and a store from a newer binary are refused the same way. A
refused store is not modified.

Schema 4 refuses old stores without modifying them. Export notes with the old
binary before creating a new store, then import them under the same source ID.
Re-sync document sources.

Open checks `application_id`, `user_version` and the store ID. Writes and
queries verify records they touch. Use `verify` explicitly to scan stored
records, the log and lookup projections, and FTS integrity. See
[verification](USAGE.md#verify-a-store).

## JSONL input

Each nonblank line contains one object with `id` and exactly one of `text` or
`deleted`. For deletion, `deleted` must be `true`.

```jsonl
{"id":"notes/review@draft","text":"The harbor inspection is scheduled for Friday."}
{"id":"notes/withdrawn","deleted":true}
```

```sh
./mousa -store local.sqlite sync --source inspection-notes --allow < items.jsonl
./mousa -store local.sqlite query --source inspection-notes 'harbor inspection'
```

Limits per invocation:

- 1 MiB per input line, excluding its line ending.
- 64 MiB total input, including whitespace and line endings.
- 10,000 nonblank records. Split larger inputs across invocations.
- 4,096 UTF-8 bytes per item ID. IDs must be nonempty and cannot contain NUL.

Unknown fields, duplicate JSON keys, null values, invalid UTF-8, unpaired Unicode
surrogates, trailing JSON values, and repeated item IDs within one invocation are
errors. Blank lines are ignored. Record order is not item identity or delivery
sequence evidence. Omission from a JSONL stream never deletes an item; send an
explicit tombstone.

On malformed input or an exceeded limit, the command exits 1, identifies the failure
on stderr, and emits no success JSON on stdout. The successfully applied prefix
remains committed. Correct the input and replay it: unchanged current content is a
no-op, and repeated tombstones are reported as `absent`.

## Directory selection and input limits

```sh
./mousa -store local.sqlite sync --preview ./documents
./mousa -store local.sqlite sync --preview --include '*.md' --exclude 'drafts' ./documents
./mousa -store local.sqlite sync --include '*.md' --exclude 'drafts' --allow ./documents
```

Preview uses the same selection and content validation as sync, but does not open,
create, or mutate the store. It reports relative item names, byte lengths, skipped
entries with reasons, and total selected bytes, never file contents. Preview does
not reserve a filesystem snapshot for a later sync.

Defaults select `.md`, `.markdown`, `.txt`, and `.rst` files, case-insensitively.
Hidden entries and directories named `node_modules`, `vendor`, `build`, `dist`, or
`target` are excluded. Observed symlinks, non-regular files, and `.git` entries
beneath the root are skipped even with `--all-text`.

- Repeatable `--include` patterns replace the default extension selection.
- Repeatable `--exclude` patterns apply to files and directories and always win.
- Patterns use Go's `path.Match` syntax, not recursive `**` globbing. A pattern
  without `/` matches a basename at any depth; a pattern with `/` matches the
  relative POSIX path. Excluding a directory skips its descendants.
- `--all-text` overrides hidden/generated and extension defaults. Include/exclude
  patterns still apply. For example, `--all-text --include '.notes'` deliberately
  selects that hidden filename rather than every file.

Selection flags are invocation-local, not saved as source configuration. Use the
same flags on subsequent syncs to retain the same scope. A successful sync
deactivates previously indexed items outside its selected set, including items
excluded by a changed filter. `skipped` now contains `{item, reason}` objects with
relative paths rather than the older list of absolute path strings.

| Limit | Default | Override |
|---|---|---|
| Selected bytes per file | 1 MiB | `--max-file-bytes` |
| Total selected file bytes | 64 MiB | `--max-bytes` |
| Visited entries, including directories and skipped entries | 10,000 | `--max-entries` |

Limits must be positive; byte limits must be below the maximum signed 64-bit value.
These bound accepted input, not process RSS or the memory needed to enumerate a
directory. An excluded directory counts as one visited entry. All selected content
is read once and validated within the aggregate byte bound before the store is
opened. A read error, invalid UTF-8 in a selected file, or exceeded limit fails the
command without changing prior activation. This differs from older directory
sync, which silently skipped invalid UTF-8. The explicit `--all-text` override does
not make binary files valid input.

The root must be an existing directory, not a symlink. Reads use `os.Root` to
constrain resolution beneath the opened root; the tested native Linux path rejects
escaping replacement links as well as skipping links found during enumeration.
This is not a concurrent filesystem snapshot, mount boundary, or hard-link
isolation mechanism. Use a stable, dedicated input tree. Root confinement does not
make arbitrary mounted filesystems or device content safe.

UTF-8 validity and filename defaults are not secret filtering. An ordinary
Markdown file can contain credentials or unrelated private text. Preview and retain
only material intended for this store; protect the store, WAL, backups, and released
output separately. Directory selection flags are not accepted with JSONL `--source`
input, which has its own explicit record bounds.

## Query policy, packing, and tracing

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

### Verify a selected passage's location

Each selected passage includes:

| Field | Meaning |
|---|---|
| `representation_id` | Canonical identity of the immutable normalized representation that owns the segment. |
| `representation_sha256` | SHA-256 of the entire normalized representation, encoded as lowercase hexadecimal. |
| `range.start` | Zero-based inclusive offset in normalized UTF-8 bytes. |
| `range.end` | Exclusive end offset in normalized UTF-8 bytes. |
| `segment_policy` | `fixed-v1`, `passage-v1` or `note-v1`, read from that representation's canonical parameters. |

Passage coordinates are measured in normalized bytes. Trails do not contain
passage text.

To locate a passage from original source bytes, first validate UTF-8, remove one
initial UTF-8 BOM (`EF BB BF`), then replace CRLF with LF and remaining CR with LF.
No Unicode normalization or whitespace trimming is performed. For JSONL, the
source bytes are the UTF-8 encoding of the decoded `text` value, not the JSONL
record's serialization. Verify the entire normalized byte sequence against
`representation_sha256` **before** applying `[range.start:range.end]`. The slice
must equal the UTF-8 encoding of `text` and its SHA-256 must equal
`content_sha256`.

These are not raw-file offsets, character positions, line numbers or editor
columns. The digest checks revision bytes, not factual truth, current filesystem
state or complete Markdown structure. Representation identity also binds
provenance and processing parameters: equal content digests do not make two
representations or sources interchangeable.

The maintained [`verify_evidence` client example](../eval/local/workflow.py)
retains original bytes, checks the digest and range, and rejects modified source
bytes for a saved response. Saved responses are historical releases. They cannot
revoke copies already delivered, reconstruct unavailable historical text, or
prove that a source is still current. Metadata and JSON serialization add output
bytes separately from the unchanged evidence-text budget.

## Authorized trail inspection and withdrawal

Owner inspection uses `trail TRAIL_ID`. MCP uses `mousa_trail` with `trail_id`
and configured read sources. Withdrawal does not remove data or mutate recorded
trails; it prevents future retrieval, and unauthorized MCP history reads are
masked as `not_found`.

Per-source access is an append-only event log with default deny; see
[Access](USAGE.md#access). It is an operator
control, not caller authentication, and it does not protect against someone who can read
or modify the store directly.

`withdraw` changes source lifecycle state without erasing index or canonical
records. The CLI has no resume command, and sync does not implicitly resume a withdrawn
source. Item deletion/restoration is a different operation from source withdrawal.

Directory identities remain usable by `query`, `status`, and
`withdraw` after the physical directory disappears. `sync` still requires an
existing directory. Stored activation is not a claim about current filesystem
contents. Integrity checks, authorization, current activation, and factual
correctness are separate properties; none substitutes for the others.

## Maintained agent skills

The repository-owned [mousa-development skill](../skills/mousa-development/SKILL.md) guides implementation and review of canonical records, transaction snapshots, compatibility and reproducible evidence. Development hosts can register the `skills/` directory with their skill loader and read this skill when working on Mousa.
