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
The five tools are `mousa_sync`, `mousa_status`, `mousa_query`, `mousa_trail`
and `mousa_remember`. MCP has no access tool and no
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
