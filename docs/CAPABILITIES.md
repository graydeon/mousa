# Supported local workflow

Mousa is pre-alpha. The supported executable is `cmd/mousa`, built from source with
Go 1.25 or newer. The core packages are internal, not a stable SDK. The CLI stores
and retrieves evidence; it does not generate answers or establish factual truth.

## Capability matrix

“Core” means an implementation exists. “CLI” means an operator can use it through
`cmd/mousa`. Tests and measurements describe different kinds of evidence: passing
behavior tests do not establish retrieval quality or performance on other workloads.
The [research record](RESEARCH.md) documents published measurements and limitations.

| Capability | Core | CLI | Behavioral coverage | Published measurements |
|---|---|---|---|---|
| UTF-8 directory import and resync | Yes | `sync <dir>` | Import, repeated no-op, update, revert, delete, restore, restart | Synthetic cold-CLI lifecycle comparison in the research record |
| Directory preview and selection controls | Yes | `sync --preview`, include/exclude globs, explicit overrides and limits | Actual-CLI scope changes, preview without store creation, failed validation preserving activation, rooted link rejection | Synthetic preview and sync observations; no performance claim from boundary smoke tests |
| JSONL item input | Yes | `sync --source <id>` | Retry, replacement, tombstone, malformed input, committed prefix, input limits | Small evolving CLI example; not a throughput benchmark |
| Raw-content identity and normalized text | Yes | Both input forms | CRLF, BOM, empty text, exact retries | Core evaluation includes normalization; no separate normalization ablation |
| Versioned passage boundaries | Yes | `sync --segment-policy passage-v1` | Exact source slices, policy replacement, retries, interrupted activation, historical identities and access exclusion | Bounded documentation and synthetic comparison in the research record |
| Atomic current-item activation | Yes | Both input forms | Transaction failure, reader snapshots, abrupt exit, retry | Included in the directory lifecycle comparison; no isolated transaction ablation |
| Legacy directory recovery | Yes | Complete directory sync | Migration preserves history and requires source replay | None |
| Source-scoped lexical retrieval | Yes | `query` | Cross-source isolation and current-only evidence | BEIR core evaluation and synthetic native-CLI comparison; no general CLI quality claim |
| Source lifecycle and retrieval policy decisions | Yes | Fresh query evaluation; `access` and `withdraw` | CLI deny/allow, source withdrawal, and cross-source isolation | Evolving CLI example and warm current-query costs; not an isolated policy ablation |
| Byte-budget evidence selection | Yes | `query --budget-bytes <positive>` | UTF-8 byte boundary and skip-oversized-then-continue packing | Core budget ablations and synthetic CLI coverage at three byte budgets |
| Explicit query-term policy | Yes | Default `original`; explicit `--policy dedup` | Repetition changes ranking; shared expression capping | Published BEIR original/dedup results, not a new CLI quality claim |
| Durable Source Trails and context packet IDs | Yes | Every query; `trail` inspection | Actual-CLI ID round-trip, current authorization, retired revisions; transaction rollback and rejected metadata filtering | Cold CLI and separate warm traced/current-query observations |
| Classification records | Yes | No administration command | Canonical storage and validation | None; not automatic classification or classification-based authorization |
| Semantic/hybrid retrieval, model inference, answer generation | No | No | Not implemented | None |
| MCP, HTTP service, stable SDK, general connectors | No supported interface | No | Not implemented as supported interfaces | None |

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

Canonical preparation can commit before activation. A preparation failure may
therefore leave unactivated historical records, but it does not replace the previous
searchable revision. Failure or abrupt exit during activation rolls back both index
replacement and the current-item pointer. Retrying the same input is supported.
A multi-item sync is not one transaction: earlier successful items remain committed
when a later item fails. Concurrent syncs of the same source are not a supported
source-wide snapshot protocol; serialize them.

Canonical history retains identities, digests, and provenance, not a complete text
archive. After deindexing, Mousa cannot reconstruct an old revision's text from its
digest. Retain original source material separately if historical text is required.

`status` distinguishes `active_items` from historical `observations` and reports
`needs_recovery`. Source collection state is separate: an active source can contain
zero active items.

For sync reports, `total_items` counts selected directory files or processed JSONL
records, not the source's active item count. `store_bytes` samples the main database
file before close and excludes WAL and other files; it is not total disk usage.
Use the separately reported post-exit file sizes in the lifecycle experiment when
comparing those measured stores.

## Ingestion segment policies

Both input forms accept `--segment-policy fixed-v1|passage-v1`. The default is
`fixed-v1`; query-term `--policy` is a separate option. For example:

```sh
./mousa -store local.sqlite sync --segment-policy passage-v1 ./documents
./mousa -store local.sqlite query --budget-bytes 1024 ./documents 'restart procedure'
./mousa -store notes.sqlite sync --source notes --segment-policy passage-v1 < items.jsonl
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
No schema migration or historical ID rewrite is needed. Returning to an earlier
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
matching segments. The [research record](RESEARCH.md) reports the bounded
development comparison and its costs, not general superiority.

## Upgrading an existing directory store

Schema version 9 and earlier did not record a reliable current-item relationship.
Migration 10 preserves canonical history, removes the ambiguous directory search
projection, and marks each existing directory source as requiring recovery. The CLI
refuses queries for that source until a complete directory sync succeeds:

```sh
./mousa -store local.sqlite sync ./documents
./mousa -store local.sqlite status ./documents
```

Use the same absolute root identity and restore the desired source files before
syncing. Recovery reconstructs the current item set from those files; it does not
guess chronology from stored hashes or modification times. If original source input
is unavailable, current activation cannot be reconstructed automatically. Keep a
backup before upgrading. A failed recovery sync can be retried; the recovery flag
remains set until a full sync finishes.

Preview selection before recovery. The current defaults are narrower than older
CLI versions that attempted every UTF-8 file. Use explicit include patterns or
`--all-text` when the intended recovery set needs other file types or hidden files;
review the selection before committing it.

## JSONL input

Each nonblank line contains one object with `id` and exactly one of `text` or
`deleted`. For deletion, `deleted` must be `true`.

```json
{"id":"notes/review@draft","text":"The harbor inspection is scheduled for Friday."}
{"id":"notes/withdrawn","deleted":true}
```

```sh
./mousa -store local.sqlite sync --source inspection-notes < items.jsonl
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
./mousa -store local.sqlite sync --include '*.md' --exclude 'drafts' ./documents
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

```sh
./mousa -store local.sqlite query --policy original --budget-bytes 4096 ./documents 'harbor inspection'
./mousa -store local.sqlite query --policy dedup --source inspection-notes 'harbor harbor inspection'
```

Put flags before positional arguments. `original` is the default and preserves
repeated terms, as in the published BEIR protocol. `dedup` folds repeated prepared
terms before expression capping. It can change both ranking and which terms fit;
it is a retrieval-policy choice, not an equivalent optimization. Earlier CLI
versions implicitly used deduplication.

The CLI and evaluation harness share preparation: split at characters other than
ASCII alphanumerics or non-ASCII runes, lowercase and trim terms, quote them as
literal FTS5 phrases, then OR-join them. This is not a complete implementation of
the index's `unicode61` tokenizer. Keep the leading terms whose expression fits
4,096 bytes. An oversized first term is retained and rejected by the store rather
than silently removed.

The candidate limit is 100, not an exhaustive match count. Packing walks verified
rank order, skips an accepted segment that exceeds the remaining budget, and
continues. It releases whole segments without truncation, deduplication, or
redundancy removal. `--budget-bytes` must be positive and defaults to 8,192.
The budget counts normalized UTF-8 evidence text only, not model tokens or JSON,
identifier, or provenance overhead.

With the default `fixed-v1` policy, text is segmented into at most 4,096 UTF-8 bytes,
ending at a code-point boundary, not a sentence, paragraph, or Markdown boundary.
A small budget can therefore return `budget_omitted` even when the relevant sentence is short: its entire
segment must fit. Inspect the trail's candidate `text_bytes` and `selected` fields
to distinguish this from no matches. Increase `--budget-bytes` to admit the
segment; 4,096 bytes can admit one full-sized segment but does not guarantee that
the selected text answers the question. Related context may be in another segment,
and broad lexical matches can consume the budget before the needed passage.
The opt-in ingestion passage policy reduces the maximum to 1,024 bytes using the
boundary rules above. Query packing does not split either kind of segment further.

Each invocation uses a fresh request identity. Current policy evaluation, verified
retrieval, canonical packing, and decision/trail storage share one writer
transaction. Failure while writing the trail rolls back the decision too. The core's
historical evaluation and tracing APIs retain their exact-retry semantics; the new
current-query path rejects an already-used request identity.

Responses include full `request_id`, `decision_id`, `trail_id`, `packet_id`, and
selected `segment_id` values. Each evidence hit includes its text digest.
`matched_candidates` counts considered candidates before packing, bounded by 100;
use the length of `evidence` for the selected count. The old misleading
`total_matches` field has been removed.

### Verify a selected passage's location

Each selected evidence hit also includes these additive fields:

| Field | Meaning |
|---|---|
| `representation_id` | Canonical identity of the immutable normalized representation that owns the segment. |
| `representation_sha256` | SHA-256 of the entire normalized representation, encoded as lowercase hexadecimal. |
| `byte_start` | Zero-based inclusive offset in normalized UTF-8 bytes. |
| `byte_end` | Exclusive end offset in normalized UTF-8 bytes. |
| `segment_policy` | `fixed-v1` or `passage-v1`, read from that representation's canonical parameters. |

Existing evidence fields retain their meanings. Consumers that reject unknown
JSON fields must accept these additions before upgrading. The fields are released
only with selected, authorized text, not for denied, rejected or budget-omitted
candidates. Historical trail inspection does not gain these fields.

To locate a passage from original source bytes, first validate UTF-8, remove one
initial UTF-8 BOM (`EF BB BF`), then replace CRLF with LF and remaining CR with LF.
No Unicode normalization or whitespace trimming is performed. For JSONL, the
source bytes are the UTF-8 encoding of the decoded `text` value, not the JSONL
record's serialization. Verify the entire normalized byte sequence against
`representation_sha256` **before** applying `[byte_start:byte_end]`. The slice
must equal the UTF-8 encoding of `text`, its length must equal `byte_length`,
and its SHA-256 must equal `content_sha256`.

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

| `outcome` | Meaning |
|---|---|
| `evidence` | At least one accepted segment fits. Other candidates may be omitted. |
| `no_matches` | Authorized lexical search found no candidates. |
| `policy_excluded` | Source policy denied the request; no lexical search was performed. |
| `lifecycle_excluded` | Source lifecycle blocked retrieval, or every considered candidate was lifecycle-rejected. |
| `budget_omitted` | Accepted candidates existed, but none fit the text budget. |

`budget_omitted` and `lifecycle_excluded` counters describe considered candidates.
They can both be nonzero. If accepted candidates exist but none fit, the outcome
is `budget_omitted`. A source-level gate runs before search, so its counters are
zero; that does not assert the corpus has no matches. `decision_reasons` records
the source-level evaluation reasons without candidate metadata.

The decision authorizes the query's transaction snapshot, not all future access.
The trail records a packet plan, not proof that stdout reached its recipient.
`latency_micros` includes store opening and query work but excludes JSON output and
store close; external process timing is needed for full cold-CLI cost. Existing
published measurements remain bound to their recorded source manifests, not to
later implementations.

## Authorized trail inspection and source controls

Replace `TRAIL_ID` with the identifier returned by a query.

```sh
./mousa -store local.sqlite trail ./documents TRAIL_ID
./mousa -store local.sqlite trail --source inspection-notes TRAIL_ID
./mousa -store local.sqlite access --source inspection-notes deny
./mousa -store local.sqlite access --source inspection-notes allow
./mousa -store local.sqlite withdraw --source inspection-notes
```

Use the `trail_id` returned by a query. Inspection evaluates fresh source access
before looking up history. A deny response contains authorization IDs/reasons but
no historical payload, even for a nonexistent trail. An allowed request cannot
inspect another source's trail.

The `historical` payload is a filtered view, not a complete canonical Source Trail
record from which its ID can be recomputed. It contains the original decision
outcome/reasons, effective expression, byte accounting, packet ID, and accepted
candidate metadata. Rejected candidates are counted but their identities, hashes,
and other metadata are omitted. The trail stores the effective expression, not
the raw query or the name of its preparation policy; inspection cannot reconstruct
those missing fields.

`selected` describes the historical packet. `indexed_now` describes index
membership in the inspection snapshot. Retired revisions can have historical
metadata without current index membership; neither property authorizes a new text
release. Inspection never returns historical text.

`access` manages a source-scoped allow/deny binding for the fixed CLI caller and
retrieval purpose. Concurrent changes use the core compare-and-swap activation
contract; conflicts are errors, not silent overwrites. This is an operator control,
not caller authentication or protection against someone who can directly read or
modify the store.

`withdraw` changes source lifecycle state without erasing index or canonical
records. Policy allow does not resume a withdrawn source. The CLI has no resume
command, and sync does not implicitly resume it. Item deletion/restoration is a
different operation from source withdrawal.

Directory identities remain usable by `query`, `status`, `trail`, `access`, and
`withdraw` after the physical directory disappears. `sync` still requires an
existing directory. Stored activation is not a claim about current filesystem
contents. Integrity checks, authorization, current activation, and factual
correctness are separate properties; none substitutes for the others.
