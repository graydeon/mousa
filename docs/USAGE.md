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

Each query stores a canonical Source Trail and returns full request, decision,
trail, packet, and selected segment IDs. Inspect the returned `trail_id` with
`./mousa -store local.sqlite trail ./documents TRAIL_ID`, or use
`trail --source inspection-notes TRAIL_ID` for JSONL input. Replace `TRAIL_ID` with
the returned identifier. Inspection evaluates
current source access before releasing historical metadata; it never returns old
text or rejected candidate identities.

The default query-term policy is `original`, which preserves repetition.
`--policy dedup` explicitly folds repeated terms and can change ranking. Earlier
CLI versions implicitly used deduplication. `--budget-bytes` sets a positive
released-text budget, defaulting to 8,192 bytes; JSON overhead and model tokens are
not covered. Queries distinguish no matches, policy/lifecycle exclusion, and budget
omission. See the [query and inspection contract](CAPABILITIES.md#query-policy-packing-and-tracing).

## Byte packing

`--packing-policy exact-v1` separately enables exact-content deduplication:

```sh
./mousa -store local.sqlite query --packing-policy exact-v1 --budget-bytes 4096 ./documents 'harbor inspection'
```

It omits byte-equal copies of already selected passages without changing ranking.
Authorization and lifecycle filtering run first; text that does not fit reserves
nothing. Each duplicate keeps its own provenance and references the retained
segment in the trail. Equal text is not independent corroboration or shared
authorization. Default `--packing-policy original` retains repeated passages.

A valid query uses writable open and can create a store before a later operation fails;
rollback does not undo that open boundary. The store's own limit rejects an
oversized expression after the store is opened.

### Caller-supplied vector workflow

Build `./mousa` as above and sync the source with `--allow` (see [Access](#access)).
Export the exact current normalized passages for your
external embedding producer, then import its vectors. Use the same source selector
for all three operations:

```sh
./mousa -store local.sqlite vector inputs ./documents > inputs.json
./mousa -store local.sqlite vector import ./documents < embeddings.json
./mousa -store local.sqlite query --vector-space SPACE_ID ./documents < query-vector.json
./mousa -store local.sqlite trail ./documents TRAIL_ID
```

Replace `SPACE_ID` with import's returned `space_id`, and `TRAIL_ID` with query's
returned `trail_id`. For a JSONL source, replace `./documents` with `--source ID`.
Put all flags before directory arguments. Vector queries take no text argument;
stdin contains `{"schema":"mousa.vector_query.v1","values":[1,0]}` for a
two-dimensional space. Those numbers alone assert no natural-language meaning.

The import object has `schema: "mousa.vector_import.v1"`, a `space` declaration
and an `embeddings` array. Each embedding copies exactly the exported `source_id`,
`segment_id`, `representation_id`, `representation_sha256` and `content_sha256`,
then adds `values`. Do not use raw file bytes when the export reports normalized
bytes. Import rejects stale/foreign pins, nonfinite or zero vectors, dimension
mismatch, duplicate segments, conflicting retries and malformed input. It does
not grant access. See [bounds, identity and v5 history](CAPABILITIES.md#caller-supplied-exact-vector-retrieval).

This runnable Python 3 example only drives native commands and constructs synthetic
vectors. Python is not a product runtime requirement; a real producer supplies its
own embeddings. The fixture proves protocol and cosine arithmetic, not model quality
or automatic natural-language semantic search. The useful runbook shares no query
term with `unplug`.

```sh
python3 - <<'PY'
import json
from pathlib import Path
import subprocess
import tempfile

binary = str(Path("./mousa").resolve())
with tempfile.TemporaryDirectory() as temporary:
    store = str(Path(temporary) / "vectors.sqlite")
    def call(*args, value=None):
        data = None if value is None else json.dumps(value)
        result = subprocess.run([binary, "-store", store, *args],
                                input=data, text=True, capture_output=True, check=True)
        return json.loads(result.stdout)
    call("sync", "--allow", "--source", "synthetic-vectors",
         value={"id": "runbook", "text": "Drain sockets before stopping the host."})
    lexical = call("query", "--source", "synthetic-vectors", "unplug")
    assert lexical["evidence"] == []
    exported = call("vector", "inputs", "--source", "synthetic-vectors")
    assert exported["decision_outcome"] == "allow"
    pins = ("source_id", "segment_id", "representation_id",
            "representation_sha256", "content_sha256")
    embeddings = [{**{key: passage[key] for key in pins}, "values": [1, 0]}
                  for passage in exported["passages"]]
    imported = call("vector", "import", "--source", "synthetic-vectors", value={
        "schema": "mousa.vector_import.v1",
        "space": {"schema": "mousa.vector_space.v1", "provider": "synthetic",
                  "model": "arithmetic-fixture", "version": "1",
                  "dimensions": 2, "metric": "cosine"},
        "embeddings": embeddings})
    query = {"schema": "mousa.vector_query.v1", "values": [1, 0]}
    args = ("query", "--vector-space", imported["space_id"],
            "--source", "synthetic-vectors")
    selected = call(*args, value=query)
    assert selected["evidence"][0]["text"] == exported["passages"][0]["text"]
    assert selected["evidence"][0]["cosine"] == 1
    history = call("trail", "--source", "synthetic-vectors", selected["trail_id"])
    assert history["historical"]["vector"]["query_vector_id"] == selected["query_vector_id"]
    call("sync", "--source", "synthetic-vectors",
         value={"id": "runbook", "text": "Use the replacement switch."})
    stale = call(*args, value=query)
    assert (stale["indexed"], stale["missing"], stale["evidence"]) == (0, 1, [])
    print(json.dumps({"selected": selected, "history": history,
                      "after_revision": stale}, indent=2))
PY
```

Each call reopens the persistent store. Source revisions/deletions deactivate old
vectors without rewriting them; re-export and import vectors for the new passage
pins. Partial coverage is explicit in `eligible`, `indexed` and `missing`; it never
triggers a lexical fallback. Both `--packing-policy original` and `exact-v1` work
with vector queries. Explicit query-term policy rejects.
MCP has a separate
`mousa_vector_query` tool and recorded retrieval-details inspection; resource search stays lexical.

### Hybrid lexical and vector query

After importing embeddings, combine text and a supplied vector explicitly:

```sh
./mousa -store local.sqlite query --hybrid-space SPACE_ID --policy dedup \
  --packing-policy exact-v1 ./documents "Drain sockets" < query-vector.json
./mousa -store local.sqlite query --hybrid-space SPACE_ID --source SOURCE_ID \
  "Drain sockets" < query-vector.json
```

`query-vector.json` uses the existing `mousa.vector_query.v1` object. Query text
does not attest which text generated that vector. Unknown/mismatched spaces and
invalid input reject; zero current vectors is reported as hybrid coverage, not a fallback.
Vector-only flags cannot be combined with hybrid mode. The [capability contract](CAPABILITIES.md#native-hybrid-lexical-and-vector-retrieval)
defines candidate bounds, rank domains, errors and historical verification limits.

This runnable example creates lexical-only `c`, vector-only `b` and overlapping
`a`. Each call starts the native executable afresh. The overlapping passage ranks
second in each branch but wins fusion: `2/62 = 0.03225806451612903`, versus `1/61`
for each branch's first hit. A 40-byte budget selects `a`, whereas lexical-only
selects `c` and vector-only selects `b`. Synthetic vectors prove arithmetic only.

```sh
python3 - <<'PY'
import json
from pathlib import Path
import subprocess
import tempfile

binary = str(Path("./mousa").resolve())
with tempfile.TemporaryDirectory() as temporary:
    store = str(Path(temporary) / "hybrid.sqlite")
    selector = ["--source", "synthetic-hybrid"]
    def call(*args, value=None):
        result = subprocess.run([binary, "-store", store, *args],
            input=None if value is None else json.dumps(value),
            text=True, capture_output=True, check=True)
        return json.loads(result.stdout)
    for item, text in {
        "a": "Drain sockets before stopping the host.",
        "b": "Radishes prefer damp soil.",
        "c": "Drain sockets."
    }.items():
        call("sync", "--allow", *selector, value={"id": item, "text": text})
    exported = call("vector", "inputs", *selector)
    pins = ("source_id", "segment_id", "representation_id",
            "representation_sha256", "content_sha256")
    embeddings = [{**{key: p[key] for key in pins},
                   "values": [1, 0] if p["item"] == "b" else [0, 1]}
                  for p in exported["passages"] if p["item"] != "c"]
    imported = call("vector", "import", *selector, value={
        "schema": "mousa.vector_import.v1",
        "space": {"schema": "mousa.vector_space.v1", "provider": "synthetic",
                  "model": "hybrid-arithmetic", "version": "1",
                  "dimensions": 2, "metric": "cosine"},
        "embeddings": embeddings})
    query = {"schema": "mousa.vector_query.v1", "values": [1, 0]}
    selected = call("query", "--hybrid-space", imported["space_id"],
                    "--budget-bytes", "40", *selector, "Drain", value=query)
    assert [p["item"] for p in selected["evidence"]] == ["a"]
    assert (selected["hybrid"]["eligible"], selected["hybrid"]["indexed"]) == (3, 2)
    history = call("trail", *selector, selected["trail_id"])
    assert history["historical"]["hybrid"] == selected["hybrid"]
    print(json.dumps({"selected": selected, "history": history}, indent=2))
PY
```

Native v6 history records both branch contributions and the fixed
`rrf-equal-k60-v1` policy. Both byte packers apply after fusion; exact-v1 handles
byte-equal passages with different identities separately from the identity union.

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
evidence, and trail inspection and `vector inputs` export release nothing.
Two commands record access. `sync --allow` records allow after the sync, but
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

A denied query still commits its trail and returns `decision_outcome: deny`,
reason `access_default` (or `access_denied` after an explicit deny), outcome
`policy_excluded`, zero candidate counts and empty evidence. A denied trail
inspection returns the fresh authorization decision ID and reasons with no
historical payload, and a denied `vector inputs` returns the decision metadata
with an empty `passages` array. Withdrawal is stronger than access: a withdrawn
source is denied with reason `source_withdrawn` and outcome
`lifecycle_excluded` even after `access ... allow`,
and `sync` does not resume it.

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
text as JSONL, opened read-only.

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
editing or retracting a note, importing existing notes,
recalling across multiple sources in one query, a citation change cursor or
standalone recheck, a daemon, and any change to lexical ranking. Like every
record, notes do not migrate across breaking store schema versions; export
before replacing an old-schema store.

## What the store does not protect against

Integrity. Each ingest receipt, access event, withdrawal and note appends a hashed
leaf to an append-only log in the same transaction. Until signed checkpoints
exist, anyone who can write the SQLite file can forge a consistent history.
The log detects accidental damage and partial edits, not a deliberate rewrite.

Retention. Trails keep the query's prepared search expression; hybrid trails
also keep the raw query text. The caller of every query is recorded on its
policy decision. Item replacement and explicit deletion remove the indexed
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
The nine tools are `mousa_sync`, `mousa_status`, `mousa_query`, `mousa_trail`,
`mousa_vector_query`, `mousa_hybrid_query`, `mousa_vector_inputs`,
`mousa_vector_import` and `mousa_remember`. MCP has no access tool and no
allow option: a source synced through `mousa_sync` stays denied until the
operator runs `mousa -store STORE access --source ID allow` natively (see
[Access](#access)). `mousa_remember` writes one durable note to a configured
ingest source (see [Notes](#notes)) — the trusted `--caller` becomes the note
author, never a caller-supplied value; optional `derived_from` citations use
the same `trail_id`/`segment_ids` shape as the CLI.
Store, permitted sources and trusted caller are fixed at startup. Item ingestion
and embedding import are disabled unless that source also has `--ingest-source`.
The default stdio mode has no listener, directory import, shell, access administration or withdrawal tool.


`mousa_trail` reauthorizes each inspection. Denial succeeds with a fresh committed
authorization decision ID and reasons, but no history or existence disclosure.
Unknown and foreign identities return `not_found` without committing the attempted
decision. An already-open inspector also returns `not_found` for a trail whose missing
decision parent prevents source ownership verification; fresh-open verification instead
rejects that damaged store as `integrity`. Neither path repairs history.

### MCP vector and hybrid example

[`examples/mcp_retrieval.py`](../examples/mcp_retrieval.py) is a synthetic
standard-library example that drives native and MCP vector/hybrid calls. MCP has
no allow option, so after its MCP sync it records `access --source ID allow`
natively.

For real use:

1. Call `mousa_vector_inputs` with `{"source":"inspection-notes"}`. It returns
   `mousa.embedding_inputs.v1` with fresh `decision_id`, `decision_outcome`,
   `decision_reasons` and `passages`. Each passage has exact normalized `text`,
   byte coordinates, item/segment policy and five canonical pins.
2. Compute embeddings externally over those exact bytes. Share text only with an
   authorized producer. Preserve `source_id`, `segment_id`, `representation_id`,
   `representation_sha256` and `content_sha256`, and the exact space declaration.
3. Call `mousa_vector_import` with `source` and `import`: one complete
   [`mousa.vector_import.v1` object](#caller-supplied-vector-workflow), not a filename
   or encoded JSON string. The result is `mousa.vector_import_result.v1` with
   canonical `space_id` and `imported`, the number of submitted embeddings accepted.
   This includes immutable retries, not just newly inserted rows.
4. Use that lowercase hex space identity with the existing query tools:

Export works on configured read sources under fresh retrieval authorization.
Denied/withdrawn exports have an empty `passages` array and no content counts or
pins. Import is administrative: it requires `--ingest-source` for that alias and,
over HTTP, both `mousa:read` admission and `mousa:write`. Retrieval denial does not
revoke this explicit write authority, but still masks export/query/history.
Withdrawal rejects import. Native import verifies lifecycle and every current
pin atomically; a stale, mismatched or foreign-source pin refuses the whole batch
without adding an embedding or space. Changed immutable values conflict; use a
different space for changed vectors. There is no external snapshot lease or
transaction during embedding computation: after source replacement, re-export
and recompute. No provider, shell, model endpoint or filesystem tool is exposed.

The codec retains its 8 MiB import bound, 1–100 embeddings and 1–2048 dimensions;
MCP's unchanged 2 MiB process-frame limit may reject a request before codec
validation. Unknown/duplicate members, nulls, invalid numbers, zero vectors,
dimension mismatches and repeated embeddings reject before mutation.

```json
{"name":"mousa_vector_query","arguments":{"source":"inspection-notes","space_id":"SPACE_ID","values":[1,0],"budget_bytes":4096}}
```

Replace `SPACE_ID` with the imported lowercase hex identity and use query values
matching that space's dimensions. For hybrid, use `mousa_hybrid_query` and add
`"query":"harbor inspection","policy":"original"` to the arguments. Text and vector
are independent caller inputs. Optional `packing_policy:"exact-v1"` deduplicates
byte-equal retained passages; omitted packing uses `original`.

Call `mousa_trail` with the same configured source, returned `trail_id` and
`retrieval_details:true` for verified producing vector/hybrid facts, including
native-created v5/v6 records. Omission/false keeps default history unchanged.
Current authorization can withhold history; later index/revision changes never
rerank the original record.

Only exact configured JSONL aliases are permitted, including when a label looks
like a path. No directory read permission follows. Native bounds and precise
space/dimension/current-coverage errors apply, without lexical fallback.


### Bounded project-history consumer

[`examples/memory/memory.py`](../examples/memory/memory.py) uses native stdio MCP
with an explicit history source and a bundled 28-document synthetic fixture. It
releases attributed passages, not answers, and captures request/response
receipts per invocation. `ingest` writes items through `mousa_sync`, but MCP has
no allow option, so `ask` returns `decision_outcome: deny` with no evidence until
the operator records `access --source memory allow` natively.

The [frozen protocol, measured source and exact receipts](https://github.com/graydeon/mousa-benchmarks/tree/b1be28ac9f12134a7513c1e28fe884e7f657c988/results/2026-09-30-agent-memory)
are pinned in the benchmarks repository. That run predates default deny.
