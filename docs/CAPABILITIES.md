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
| JSONL item input | Yes | `sync --source <id>` | Retry, replacement, tombstone, malformed input, committed prefix, input limits | None |
| Raw-content identity and normalized text | Yes | Both input forms | CRLF, BOM, empty text, exact retries | Core evaluation includes normalization; no separate normalization ablation |
| Atomic current-item activation | Yes | Both input forms | Transaction failure, reader snapshots, abrupt exit, retry | Included in the directory lifecycle comparison; no isolated transaction ablation |
| Legacy directory recovery | Yes | Complete directory sync | Migration preserves history and requires source replay | None |
| Source-scoped lexical retrieval | Yes | `query` | Cross-source isolation and current-only evidence | BEIR core evaluation; not a CLI quality claim |
| Source lifecycle and retrieval policy decisions | Yes | Query evaluates the installed deployment/source policy | Core allow/deny, withdrawal and source-scope tests | Core verified/enforced mode comparisons |
| Byte-budget evidence selection | Yes | Fixed 8,192-byte released-text budget | CLI byte accounting; core packing boundaries | Core budget ablations |
| Durable Source Trails and context packet IDs | Yes | Not yet exposed by the CLI | Core immutable trail and packet tests | Core traced mode; CLI does not use that mode yet |
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
| Active item | Identical raw content | `unchanged` | Unchanged |
| Active item | Different content, including an older revision | `updated` | Input revision only |
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

## Current selection and retrieval limits

Directory sync currently skips `.git`, non-regular entries, and non-UTF-8 files.
UTF-8 validity is not a confidentiality check: other hidden files, configuration,
credentials, generated output, and unrelated text can be included. Use a dedicated
input directory containing only material you intend to retain. Directory preview,
configurable exclusions, and explicit root-boundary controls are not yet exposed.

The current CLI query builder lowercases its terms, folds repeated terms, quotes
them, OR-joins them, and drops trailing terms when needed to meet the expression
limit. This is the `dedup` policy, not the published BEIR `original` protocol that
preserves repeats. The CLI considers at most 100 source-scoped candidates and
releases whole selected segments under an 8,192-byte text budget. Bytes are not
model tokens; JSON/provenance overhead is outside that text budget.

The CLI currently reports selected evidence and a decision identifier, not a durable
Source Trail identifier. It does not yet distinguish all no-match, policy-excluded,
and budget-omitted outcomes. Core tracing exists, but is not evidence that this CLI
path has been traced. Integrity checks, authorization, current activation, and factual
correctness are separate properties; none substitutes for the others.
