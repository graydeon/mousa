# Using Mousa

For source-build versus tagged-release availability,
see [development status](OVERVIEW.md#source-and-release-status).

[← Back to Mousa](../README.md) · [Capability matrix](CAPABILITIES.md) · [How Mousa works](OVERVIEW.md)

Full CLI, MCP and consumer examples. Run commands from the repository root;
build the executable from source before using the examples.

## Local usage (cmd/mousa)

The local CLI synchronizes text items into a canonical store and returns
source-linked evidence under a byte budget. Build with Go 1.25 or newer:

```sh
CGO_ENABLED=0 go build -o mousa ./cmd/mousa
./mousa -store local.sqlite sync --preview ./documents
./mousa -store local.sqlite sync ./documents
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
`./mousa -store local.sqlite sync --segment-policy passage-v1 ./documents`.
This policy groups document blocks into at most 1,024-byte source slices, with
explicit fallback for oversized blocks. Repeat the flag on resync; a policy change
replaces current evidence even when source bytes are unchanged. Smaller passages
can fit smaller evidence budgets but change lexical ranking and increase segment
count. See [policy identity, boundaries and limitations](CAPABILITIES.md#ingestion-segment-policies).

The source-built BEIR evaluator exposes the same choice with
`-segment-policy fixed-v1|passage-v1`; its default remains `fixed-v1`.
Use separate output directories for the policies because each dataset has one
indexed store per directory. Repeat the flag on reuse and resume.
See [evaluator provenance and commands](RESEARCH.md#reproduction) for store
compatibility and explicit segmentation comparisons. This option does not
change the runtime ranking or packing defaults.

For an explicit item stream:

```sh
printf '%s\n' '{"id":"note@draft","text":"The harbor inspection is on Friday."}' |
  ./mousa -store local.sqlite sync --source inspection-notes
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

## Current-source manifests

Build a revision containing `manifest`, then export the current indexed state:

```sh
./mousa -store local.sqlite manifest ./documents
./mousa -store local.sqlite manifest --source inspection-notes
./mousa -store local.sqlite manifest --expect MANIFEST_ID ./documents
./mousa -store local.sqlite manifest --source inspection-notes --expect MANIFEST_ID
python3 examples/source_manifest.py --mousa ./mousa
```

Replace `MANIFEST_ID` with the returned `manifest.id`. Keep that baseline in
caller-owned state; Mousa does not persist a manifest or work-completion record.
Authorized export exits 0 and returns decision metadata plus `manifest`.
An expected-ID check additionally returns `matches`; only an authorized complete
match exits 0. Mismatch, denial and operational failure exit 1; invalid command
syntax exits 2. Denial omits `manifest` and `matches`, including on export. Treat
every nonzero exit as refusal to resume, not as an empty or matching source.
Expected IDs must be nonzero lowercase 64-hex strings.

For a configured MCP JSONL alias, discover `mousa_source_manifest` and call:

```json
{"name":"mousa_source_manifest","arguments":{"source":"inspection-notes","expected_manifest_id":"REPLACE_WITH_SAVED_MANIFEST_ID"}}
```

Omit `expected_manifest_id` to export a new baseline. A mismatch is a normal
structured result with `matches: false`, not a malformed-request error. Require
`decision_outcome: "allow"`, `manifest`, and `matches: true` before resuming.
Denial returns only existing decision metadata. The expected ID grants no access.
Unknown-source denials have fresh decision IDs but no persisted receipt: the
decision's storage foreign key requires an existing source. Known-source decisions
are persisted only when inspection completes; failed inspection rolls them back.
Null, coerced, duplicate, unknown and malformed argument fields are rejected.
This read needs no ingestion flag;
HTTP retains validated-principal and `mousa:read` admission. MCP accepts configured
JSONL aliases, not directory paths. Native directory roots and JSONL aliases have
different source identities; identical text does not make them interchangeable.

The example uses temporary stores and shows export, match, a changed file without
sync (still a match), sync (mismatch and a new ID), explicit deletion, JSONL
omission versus deletion, and MCP authorized/denied checks. It refuses to resume
on mismatch or denial. No tokenizer, embedding producer or model call is needed.

### Scope and bounds

The manifest describes **current indexed local-adapter content**, not live files.
It contains ordered active-item identities and current observation, artifact and
representation pins, raw/normalized digests and lengths, normalizer/segmentation
identity, and a count/digest of each exact ordered segment inventory. It contains
no passage text or retired bodies. Both `fixed-v1` and `passage-v1` are supported.
JSONL omission leaves an item unchanged; delete it explicitly with
`{"id":"note@draft","deleted":true}`.

Fresh authorization and all current-content verification share one writer
snapshot. A match is not a lease or an atomic guard on a later query. It neither
records action completion nor guarantees query-result stability, semantic truth,
authenticated authorship or full model-window safety. Embeddings
and authorization receipts are outside the
content identity. An allowed policy change need not change the ID.

Limits apply together: 1,000 active items, 10,000 current segments, 16 MiB of
normalized indexed bytes inspected, 512 KiB of canonical encoded manifest, and
4,096 bytes per item identifier. Fewer than 1,000 items can exceed another limit.
Excess, corruption, incomplete current indexes and unsupported recovery state fail
closed: no truncation, partial digest or successful match. A valid active empty
source has an empty inventory; denial or an unknown source is not such a source.
Synchronizing an untouched empty directory does not create an active source.
A previously ingested source remains valid after all its items are deleted.
MCP also bounds the serialized result, including its duplicated text block, below
2 MiB with framing space reserved. Escaping can make an otherwise valid canonical
manifest exceed that transport bound; it returns `resource_limit`, not a partial
manifest. Manifest calls require a JSON-encoded JSON-RPC request ID of at most
1,024 bytes. Oversized IDs close stdio with a diagnostic or receive HTTP 400,
before inspection; these transport rejections do not echo the oversized ID.
No raw archive, migration or new persisted manifest state is introduced.

Exact retries and input-order permutations preserve identity. Revision,
segmentation and active-membership changes alter it; restoring identical pins may
restore an earlier ID. This is a state fingerprint, not a monotonic event counter.
Fresh stores reproduce full IDs only with the same source identity and current
pins; separate temporary directory roots must not be normalized into equality.

### Canonical identity recipe

The closed payload has fields in this order: `schema`, `id`, `source_id`, `items`.
The schema is `mousa.source_manifest.v1`. Items are strictly ordered by their
UTF-8 item-identifier bytes. Each item has exactly these fields, in order:
`item`, `observation_id`, `artifact_id`, `representation_id`, `raw_sha256`,
`raw_byte_length`, `normalized_sha256`, `normalized_byte_length`, `processor_id`,
`processor_version`, `parameters_sha256`, `segmentation_policy`, `segment_count`,
`segments_sha256`. IDs and SHA-256 values are lowercase 64-hex strings; lengths
and counts are JSON integers.

Let `J(x)` be compact UTF-8 JSON with the listed object-member order, no spaces,
no HTML escaping, U+2028/U+2029 escaped, and one trailing LF. Arrays retain order
and empty inventories encode as `[]`, not `null`. Let
`T(a,b) = u64be(len(a)) || a || u64be(len(b)) || b`, with lengths in bytes.
Then `manifest.id` is lowercase hex SHA-256 of:

```text
T(UTF8("mousa.source_manifest.v1"), J({"source_id":SOURCE_ID,"items":ITEMS}))
```

The manifest ID excludes its own `schema`/`id` members from the JSON payload;
the schema is the tuple's domain separator. Fresh decision metadata is outside
this payload. Canonical encoded-manifest size includes its final LF.

For each item, reconstruct every current segment in contiguous byte-range order.
The segment-inventory entry is `{"segment_id":ID,"selector":SELECTOR,
"content_sha256":DIGEST}` in that member order. `SELECTOR` is
`{"schema":"mousa.selector.text_byte_range.v1","start":START,"end":END}`,
with half-open normalized UTF-8 byte coordinates. The segment ID and content
digest are the existing canonical segment identity and SHA-256 of that exact
normalized slice. `segments_sha256` is lowercase hex SHA-256 of:

```text
T(UTF8("mousa.source_manifest_segments.v1"), J(ORDERED_SEGMENT_ENTRIES))
```

The normalizer is `mousa.text.normalize_utf8`, version `1`. Parameters are
SHA-256 of empty bytes for `fixed-v1`, or of the exact UTF-8 bytes
`{"segmentation":"passage-v1"}` (without LF) for `passage-v1`. This recipe binds
actual provenance and selectors, not just names and counts.


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

A valid query uses writable open and can create or migrate a store before a later operation fails;
rollback does not undo that open boundary. The store's own limit rejects an
oversized expression after the store is opened.

### Caller-supplied vector workflow

Build `./mousa` as above. Export the exact current normalized passages for your
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
    call("sync", "--source", "synthetic-vectors",
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
        call("sync", *selector, value={"id": item, "text": text})
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

For a persistent documentation lookup, see the
[versioned Git documentation example](../examples/docs/README.md). It imports
pinned public manuals and their direct include fragments, then returns passages
with verified byte ranges, normalized line locations and upstream links. Its
optional `--context-tokens` mode renders cited passages within a
prompt-content cap counted by the example itself; the raw packet still
contains the full byte-packed response. The example does not generate answers.

For an explicit caller decision over those retrieval contracts, see the
[SQLite backup checklist](../examples/backup/README.md). It verifies saved passages
from pinned Python documentation, accepts caller-authored fact judgments, and
reports coverage, unresolved facts, or one requested follow-up retrieval.
Citation consistency is separate from the caller's assessment of semantic support.

Run the maintained Python standard-library example against the built CLI:

```sh
CGO_ENABLED=0 go build -o mousa ./cmd/mousa
python3 eval/local/workflow.py --example-only --mousa ./mousa \
  --output client-results.json --timeout 5
```

Prerequisites: Go 1.25 or later to build Mousa, and Python 3.9 or later
with its standard-library `sqlite3` module on Linux. Process-group timeout
handling and executable test fixtures are Linux-supported; other operating
systems are not validated. GNU `time` is optional for peak-RSS measurements;
if `time` is on `PATH`, it must support GNU `-f` and `-o` options.
No QMD, Node, models, network access, or third-party Python packages are needed
to run the client example or acceptance suite. Building requires the Go module
dependencies, downloaded beforehand for an offline build.
The example creates an isolated temporary store and removes it after the run;
`client-results.json` retains the responses and command results.

The client passes argument arrays and JSONL stdin without shell interpolation.
It runs the same lifecycle checks with `fixed-v1` and `passage-v1`, repeating the
chosen policy on resync. It ingests two bulletins, a separate peer source and a
multibyte directory fixture, then checks update, deletion, restoration, denial,
withdrawal and cross-source trail rejection. It distinguishes `no_matches`,
`budget_omitted`, `policy_excluded`, and `lifecycle_excluded`. Denied trail
inspection releases no historical metadata.
It also verifies exact-content packing and the retained-segment explanation under
both policies, including the small-budget duplicate-displacement regression.

The example retains original source bytes and uses `verify_evidence` to check
the normalized representation digest before slicing the returned byte range.
It verifies the selected text and its content digest, rejects changed bytes for
an old saved response, and checks policy changes on identical source bytes.
The directory fixture exercises multiple passages, UTF-8, BOM, CRLF and CR.
See [normalized passage coordinates](CAPABILITIES.md#verify-a-selected-passages-location)
for the additive output fields, normalization rules and limits of saved evidence.

Success exits zero and writes `"result": "PASS"`. An unexpected child exit,
invalid JSON object, or failed behavioral check stops the workflow with exit
one and a partial `"result": "FAIL"` report. Each child has the `--timeout`
deadline; expiration kills and reaps its process group and records exit 124.
Missing prerequisites or invalid arguments can fail before a report is created.
Inspect the report's `error`, `rows`, and `summary`; timings are single-invocation
diagnostics, not a speedup or retrieval-quality claim.

This is a supported example of the CLI, not a stable SDK or a general client
library. It does not implement retrieval or packing. The same
[evolving workflow](RESEARCH.md#evolving-evidence-and-native-lexical-cli-comparison)
can separately run native lexical comparisons when `--example-only` is omitted.
Run the required client acceptance suite from the repository root:

```sh
CGO_ENABLED=0 go build -o mousa ./cmd/mousa
python3 eval/local/workflow_test.py --mousa ./mousa
```

The command requires an explicit readable executable and runs the actual CLI
workflow plus nonzero-exit, malformed-JSON, wrong-shaped-output, and timeout
consumer tests. It also runs the versioned documentation and caller-reviewed
backup consumer suites. It exits nonzero on a failed top-level check, missing
prerequisite, or skipped top-level test. The optional tokenizer test inside the
documentation suite skips if `tiktoken` is absent; that nested skip does not
fail this command. Run `python3 examples/docs/docs_test.py --mousa ./mousa`
with `tiktoken==0.12.0` installed to exercise the opt-in projection. Failures
include captured workflow output for diagnosis. Each run uses temporary
stores; the original workflow's CLI calls have five-second deadlines (0.2
seconds for the intentional timeout), documentation and backup calls have
30-second deadlines, and each workflow subprocess has a 120-second deadline.
The suite does not run comparisons or benchmarks. Ad hoc `unittest`
discovery may still skip the real-CLI test when `MOUSA_EXECUTABLE` is unset;
it is not a substitute for this required command.

`access ./documents deny` blocks retrieval and trail inspection of that source for
every caller; `access ./documents allow` restores it. A source is allowed until
denied. Both also accept `--source <id>`. `withdraw ./documents` changes source
lifecycle state: access allow
does not undo withdrawal, and the CLI has no resume command. These controls are not
secure erasure or a replacement for filesystem access controls.

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
`mousa_vector_query`, `mousa_hybrid_query`, `mousa_vector_inputs`, `mousa_vector_import`
and `mousa_source_manifest`.
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

Build `./mousa`, then run this synthetic standard-library Python example on Linux:

```sh
CGO_ENABLED=0 go build -o mousa ./cmd/mousa
python3 examples/mcp_retrieval.py --mousa ./mousa
```

The source-build example creates separate fresh stores for native directory,
native JSONL and configured MCP JSONL workflows. Each syncs two items, exports
normalized passage pins, imports two hand-assigned vectors, and queries vector
and hybrid retrieval with both packers. The MCP form also queries lexical
retrieval. Assertions check released bytes against the packet accounting and
inspect producing history. Temporary stores and wire receipts are removed on
exit; printed results remain available for inspection. Synthetic vectors
establish protocol/arithmetic only, not semantic quality, model integration,
performance or model-window safety.

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
with an explicit history source. The default is an independently authored,
28-document synthetic fixture. It releases attributed passages, not answers.
Python 3 on Linux is optional; no package, model, service or second store is required.

Each command starts a fresh consumer and Mousa process, negotiates MCP, captures
decoded JSON request/response receipts, then closes and reaps the process.
Receipts are not original wire framing. Use an isolated
store and a distinct receipt file for each command:

```sh
python3 examples/memory/memory.py --mousa ./mousa --store memory.sqlite --receipts ingest.jsonl ingest
python3 examples/memory/memory.py --mousa ./mousa --store memory.sqlite --receipts before.jsonl ask 'How does saving an edit survive the computer losing power?' --budget-bytes 2048
python3 examples/memory/memory.py --mousa ./mousa --store memory.sqlite --receipts change.jsonl change
python3 examples/memory/memory.py --mousa ./mousa --store memory.sqlite --receipts after.jsonl ask 'How long may delivery attempts continue after the first failure?' --budget-bytes 2048
```

`ingest` applies explicit item identities; `change` applies the fixture's same-item
correction and explicit tombstone. Omission never deletes an item. The tombstone
deactivates that item, not its source. `inspect TRAIL_ID` inspects an original
query's historical selection under fresh authorization without releasing text.
Do not run `ingest` again after `change` unless restoring the initial fixture is
intentional. Receipt files must not already exist.

Custom histories are validated before receipt creation or server startup.
Originals and corrections require nonempty UTF-8 author, date and URI strings,
string text and its raw UTF-8 SHA-256 digest. Item IDs contain 1–4096 UTF-8 bytes
without NUL. Text, serialized items and complete sync requests must fit the
native MCP limits (262144, 1048576 and 2097152 bytes respectively).
The declared source must be nonempty lossless UTF-8 without NUL (the process argv
boundary). Startup permits only that source; ingestion permission is added only
for `ingest`, `change` or `apply`. The v1 fixture retains `documents` and distinct
existing-item `changes`, containing corrected text or `deleted: true`, never both.
Empty change commands are rejected before startup.

For multiple lifecycle epochs, use `mousa-project-history-v2`:

```json
{
  "schema": "mousa-project-history-v2",
  "source": "project-alpha",
  "revisions": [
    {
      "revision": "initial",
      "id": "note",
      "text": "",
      "sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      "author": "Synthetic author",
      "date": "epoch-1",
      "uri": "synthetic://project/note"
    }
  ],
  "epochs": [
    {"epoch": "create", "operations": [{"id": "note", "revision": "initial"}]},
    {"epoch": "delete", "operations": [{"id": "note", "deleted": true}]},
    {"epoch": "restore", "operations": [{"id": "note", "revision": "initial"}]}
  ]
}
```

Save this as `history.json`, then run with a fresh store and distinct receipts:

```sh
python3 examples/memory/memory.py --mousa ./mousa --history history.json --store chronology.sqlite --receipts create.jsonl apply create
python3 examples/memory/memory.py --mousa ./mousa --history history.json --store chronology.sqlite --receipts delete.jsonl apply delete
python3 examples/memory/memory.py --mousa ./mousa --history history.json --store chronology.sqlite --receipts restore.jsonl apply restore
python3 examples/memory/memory.py --mousa ./mousa --history history.json --store chronology.sqlite --receipts status.jsonl status
python3 examples/memory/memory.py --mousa ./mousa --history history.json --store chronology.sqlite --receipts query.jsonl ask note --policy dedup --packing-policy exact-v1 --budget-bytes 2048
```

`apply EPOCH` sends only that epoch's ordered operations. It neither replays
predecessors nor checks a Python progress ledger. A revert explicitly references
old content; a restore also supplies the referenced content. Exact replay is
allowed. Each epoch contains 1–128 distinct item operations; histories contain
1–128 distinct epoch labels and 1–4096 distinct revision labels. Array order is
fixture chronology, not authenticated time. The store may already be ahead of
the requested epoch. `requested_epoch` describes the invocation, while native
actions and `source_status` describe observed state.

Native sync commits each item separately. A later failure retains its committed
prefix, reported by `native_error.error.completed_items` and `failed_item`.
There is no automatic retry, batch splitting or rollback. Repair requires a new
explicit invocation. Known input and frame bounds are preflighted before launch.

`ask` accepts native query policies `original` (default) and `dedup`, and packing
policies `original` (default) and `exact-v1`. Dedup folds repeated query terms;
exact packing omits byte-identical passages after the first fitting selection.
Neither mode supplies semantic support. Requested and returned identities must
agree. Evidence budgets are 1–65536 bytes; queries contain 1–4096 UTF-8 bytes.

JSON reports distinguish `operation_status`, overall `status`, `capture_complete`
and process teardown. Consumer `PASS` means completed operation/capture/cleanup,
not scenario acceptance. `assertion_status` and `agent_acceptance` remain `NOT RUN`
in consumer output. Preflight failures emit a report without creating receipts.

`--timeout` must be finite and positive. It bounds each request's pipe writes
and response wait, including unrelated notifications; history and executable
hashing, receipt-file I/O, JSON parsing and process teardown are separate
boundaries. Partial writes are completed before a request receipt is recorded.
Each response frame is limited to 32 MiB, excluding its newline.
Responses already buffered before a request cannot fulfill it, even if their
IDs predict that request.
Native text and structured tool results must agree, including their source.
Transport, tool and receipt errors fail the command rather than produce
successful empty evidence. Reports distinguish incomplete receipt capture and
process cleanup diagnostics from the causal error.

The consumer checks released text, digests and normalized byte coordinates
against the attributed fixture revision. Revision matching removes a leading
UTF-8 BOM and normalizes CRLF or bare CR to LF, as ingestion does. Revisions
with equal normalized bytes must have identical author/date/URI attribution;
conflicts fail preflight and rendering. Equivalent revisions with identical
attribution share the evidence identity and expose all matching `revision_labels`,
not an arbitrarily chosen epoch. Corrected, restored and reverted selections use
their own revision's metadata.
These synthetic attribution labels are not signatures or
authenticated authorship. The rendered context retains the exact passages and
identities; its byte count and digest are separate from the evidence-text budget.
No tokenizer or whole-model context limit is supplied.

Twenty frozen development cases retrieved all 21 required initial passages,
including separately attributed multi-document passages. Correction, tombstone,
restart and original-trail checks passed. All three unsupported questions also
released unrelated passages; `evidence_available` does not establish semantic
support. Across 25 initial/post-change observations, 309 of 334 released passages
did not support the frozen requirements. Literal OR-term retrieval can release
misleading near matches. This is deterministic retrieval/lifecycle evaluation,
not held-out relevance, semantic memory or successful model-driven agent use.

The [frozen protocol, measured source and exact receipts](https://github.com/graydeon/mousa-benchmarks/tree/b1be28ac9f12134a7513c1e28fe884e7f657c988/results/2026-09-30-agent-memory)
retain the measured consumer's original identity. Later transport and validation
fixes are not relabeled as that run or as native-agent acceptance.
