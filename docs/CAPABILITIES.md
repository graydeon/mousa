# Supported local workflow

For source-build versus tagged-release availability,
see [development status](OVERVIEW.md#source-and-release-status).

Mousa is pre-alpha. The supported executable is `cmd/mousa`, built from source with
Go 1.25 or newer. The core packages are internal, not a stable SDK. Its CLI and
loopback HTTP service store and retrieve evidence; neither generates answers or
establishes factual truth. Mousa ships no MCP server.

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
| Opt-in revision text history | Yes | `access --source ID --history on\|off`; `recheck --trail ID --text`; `GET /v1/recheck/{trail_id}`; open-monitor request detail | Per-source logged setting defaulting off, never retroactive, kept text verified against its canonical segment and never searchable, `indexed`/`kept`/`not_kept` release statuses, denied or withdrawn sources release no text, dashboard revision diff |
| Per-source access decisions | Yes | `access <dir>|--source <id> allow|deny`; `sync --allow`; default deny with no event | Append-only events, default deny, no-op repeats, service-synced sources denied until native allow; see [Access](USAGE.md#access) |
| Byte-budget evidence selection | Yes | `query --budget-bytes <positive>` | First-fit byte accounting, duplicate omission, and history reads. |
| Canonical query trails | Yes | `query`; `trail` ID | Stable result digests, atomic leaf writes, corruption checks and caller-view masking. |
| Durable notes | Yes | `remember --source <id>`, `notes export --source <id>`, `notes import --source <id>`; `POST /v1/notes` on a served store | Immutable canonical records with typed trail/segment lineage; idempotent writes, exact stdin/argument bytes (CRLF/BOM preserved), byte-exact export, lexical recall with stale reasons, denied/withdrawn enforcement, write guards on document sources, served lineage redaction for citations outside the caller's read grants |
| Item paths and moves | Yes | `remember --path`, JSONL record `path`, `move --note|--item|--prefix --to`; `POST /v1/moves` on a served store | Validated lowercase paths, `unsorted` default for notes, literal legacy item IDs preserved, atomic `path_move` log leaves, no-op and denied enforcement, monitor move detail |
| Note supersession | Yes | `remember --supersedes NOTE` (repeatable), `query --include-superseded`; native `supersedes`, `include_superseded` | Immutable same-source edges, default exclusion with include marking, verified trail effects, export/import in any line order |
| Embedding inference, answer generation | No | No | Not implemented |
| Loopback JSON request service | Yes | Opt-in `serve --config FILE`; `agent add NAME --config FILE --source ID`; client by `-endpoint`, `MOUSA_ENDPOINT` or client configuration, token by `MOUSA_TOKEN`, `token_file` or `token_command` | Durable admission/outcome per request including unauthorized and canceled attempts, typed errors with request IDs, verified operator history and detail, unknown-outcome transport failures; see [Local served store](USAGE.md#local-served-store-opt-in) |
| Live stage stream and dashboard | Yes | `GET /v1/events`, `GET /` | Bounded frame ring with explicit gap on overflow/restart, `Last-Event-ID` resume without silent misses, operator-only, measured stage timings that are not archived; reconnects read verified history |
| Public ingress, multi-tenant serving, remote automation relay | No | No | Not implemented; `serve` binds loopback only |
| Stable SDK, general connectors, public pairing relay | No supported interface | No | Not implemented as supported interfaces |

The [Git documentation example](../examples/docs/README.md) is a separate Python CLI
example. It returns source passages and does not generate an answer.

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
lexical index rows change in one transaction. Activation records the store clock
for a new, changed, or restored revision; deleting and re-adding an item records
a fresh time even when the bytes are identical. A no-change re-sync or path-only
move keeps the prior activation time. Schema-9 stores keep the time unset unless
a committed served document outcome proves it:

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

By default, canonical revision history retains identities, digests, and
provenance, not retired text. A source that opts into [revision
history](USAGE.md#history) retains each segment's verified text when an update
or deletion retires it. Retained text is not indexed. Text retired before
opt-in cannot be recovered from its digest.

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

`remember --source <id>` writes one immutable
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
must currently allow access and not be withdrawn. A note without lineage is unsourced (`origin: "unsourced"`,
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
sources or per-note grants. The CLI author is the operator identity; on a served store the
author is the authenticated principal, which needs a write grant on the source.
A new note can declare `--supersedes NOTE_ID` edges to existing same-source
notes; the edges are immutable, there is no un-supersede, and queries exclude
superseded passages unless asked to include them. Document and note sources
stay disjoint: `sync` refuses a source holding notes,
and `remember` refuses a source holding document items, active or not.
Notes cannot be edited or retracted. Moving and superseding leave the stored
text unchanged.

## Store schema and older stores

A store has one schema. Mousa marks it in the SQLite header (`application_id`
and `user_version`) and checks that header before it reads or verifies any record.
The current schema is 10. A store written by schema 7, 8 or 9 is refused on
open with an `incompatible_schema` error that names `mousa migrate`; that
command upgrades the store in place inside one transaction, preserving notes,
note citations, query trails and request history, prints `{"schema": 10}`,
and `verify` confirms the result. Stores older than schema 7 are refused
without a migration path. Export notes
with the old binary before creating a new store, then import them under the
same source ID and re-sync document sources. A SQLite file that is not a Mousa
store and a store from a newer binary are refused the same way. A refused
store is not modified.

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

A query returns passages and records one `trail` (`mousa.trail.v1`). The CLI's
default compact output carries the trail ID and passage text, path, kind, and
note summary fields. The compact `date` is the note's creation date or the
current document revision's activation date, in the CLI's local time zone.
Documents without proof of activation omit `date`. Rich passage metadata is
available through CLI `--full` or the HTTP query API.

Paths starting with `_` are for meta documents. Collection queries leave them
out. Query the source directly with `--source` to read one.

The trail is an append-only log leaf. It records the request, per-source
access, snapshot index, allowed source heads, candidate scores and ranks, and
packing decisions.
`trail.id` identifies this record; `trail.result_sha256` compares results across
equivalent requests against the same state. Lexical is the only supported mode.

`--budget-bytes` defaults to 8,192. It covers released text, not JSON or model
tokens. First-fit packing skips passages that do not fit and omits exact-content
copies of already packed passages. The trail records `packing.used`, `packed`,
`omitted`, and `duplicate_of` as applicable. A denied query has empty `passages`
and no retrieval, candidates, or packing. An allowed query with no matches has
an empty candidate list.

`mousa -store STORE trail TRAIL_ID` reads the full canonical record by `trail.id`
without writing. This is an owner interface, not a way to retrieve historical text.
The caller view carried by a served query reply omits `as_of`,
head sequence numbers, branch drop counts, and denied-source details other than
`source_id` and `outcome`.

### Verify a selected passage's location

In a `--full` CLI result or HTTP query reply, each selected passage includes:

| Field | Meaning |
|---|---|
| `representation_id` | Canonical identity of the immutable normalized representation that owns the segment. |
| `representation_sha256` | SHA-256 of the entire normalized representation, encoded as lowercase hexadecimal. |
| `range.start` | Zero-based inclusive offset in normalized UTF-8 bytes. |
| `range.end` | Exclusive end offset in normalized UTF-8 bytes. |
| `segment_policy` | `fixed-v1`, `passage-v1` or `note-v1`, read from that representation's canonical parameters. |

Passage coordinates are measured in normalized bytes. Trails do not contain
passage text.

The log records the initial path of each new item in its version 3 item change.
Later moves are logged. For older items, the first logged move provides the
initial path. If neither record exists, that item's historical path is unknown.
Trail verification and recheck reject contradicted collection meta-path
omissions and report unresolved omissions as `unverifiable_meta_path_omissions`
when nonzero.

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

Owner inspection uses `trail TRAIL_ID`. Withdrawal does not remove data or
mutate recorded trails; it prevents future retrieval.

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
