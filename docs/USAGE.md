# Using Mousa

For source-build versus release status and the proposed v10 composition boundary,
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

## Native token-aware context packing

`query --context-tokens N` selects whole evidence passages against the token
count of the exact returned `context` string, not a client-side trim of a
byte-packed result. Directory and JSONL selectors support lexical, vector-only
and hybrid modes, with either `original` or `exact-v1` packing.

### Prepare the optional runtime

The standard Go executable and ordinary queries need no Python. Token mode uses
a caller-selected local interpreter, default `python3`, with official
`tiktoken==0.12.0`. Prepare its vocabulary explicitly while network access is
available. These Unix commands install only into a dedicated venv and cache:

```sh
TOKEN_PY="$HOME/.local/share/mousa-tokenizer/bin/python"
TOKEN_CACHE="$HOME/.cache/mousa/tiktoken"
python3 -m venv "$HOME/.local/share/mousa-tokenizer"
"$TOKEN_PY" -m pip install 'tiktoken==0.12.0'
TIKTOKEN_CACHE_DIR="$TOKEN_CACHE" "$TOKEN_PY" -c \
  'import tiktoken; print(tiktoken.get_encoding("o200k_base").name)'
```

Preparation may download public package/vocabulary files. Queries do not:
the embedded helper refuses a missing or corrupt prepared vocabulary before
receiving source text, with download and socket entry points disabled.
No provider credentials or model/service calls are used. The backend name is an
encoding declaration, not an assertion about an unverified model's tokenizer.
Pass `--tokenizer-cache` for a nondefault cache; otherwise Mousa uses
`mousa/tiktoken` beneath the operating system's user cache directory.

### Query and inspect

After the quick-start directory sync:

```sh
./mousa -store local.sqlite query --context-tokens 512 \
  --tokenizer-python "$TOKEN_PY" --tokenizer-cache "$TOKEN_CACHE" \
  --packing-policy exact-v1 ./documents 'harbor inspection'
```

An independently runnable JSONL example:

```sh
printf '%s\n' '{"id":"harbor","text":"The harbor inspection is on Friday."}' |
  ./mousa -store token-example.sqlite sync --source token-notes
./mousa -store token-example.sqlite query --context-tokens 256 \
  --tokenizer-python "$TOKEN_PY" --tokenizer-cache "$TOKEN_CACHE" \
  --source token-notes 'harbor'
```

The result has schema `mousa.token_result.v1`, `context`, selected `evidence`,
`token`, `trail_id` and `packet_id`. `token.measured_tokens` covers all UTF-8 bytes
of `context`, including headers/separators; `token.rendered_sha256` hashes those
exact UTF-8 bytes, before JSON string escaping. `used_bytes` still counts only
selected evidence text, not rendering headers, separators or JSON overhead.
`token.limit` is the effective allowance, and `token.budget` retains declarations.
The released `token.candidate_ids` aligns `proposed_tokens` and lexical `lexical`
facts with accepted candidates, including unselected budget/duplicate omissions.
Lexical ranks count only accepted order, not original search positions. A null
proposed count means that accepted passage was not counted (for example, a
duplicate); a lifecycle-rejected candidate has no slot at all. Query and history
use this filtered view, not the full canonical v7/v8/v10 producing record.

For externally supplied vectors, use the same exported passages, immutable
space and query-vector JSON from the [vector workflow](#caller-supplied-vector-workflow):

```sh
printf '%s\n' '{"schema":"mousa.vector_query.v1","values":[1,0]}' |
  ./mousa -store local.sqlite query --context-tokens 512 \
    --tokenizer-python "$TOKEN_PY" --tokenizer-cache "$TOKEN_CACHE" \
    --vector-space "$SPACE_ID" ./documents
printf '%s\n' '{"schema":"mousa.vector_query.v1","values":[1,0]}' |
  ./mousa -store local.sqlite query --context-tokens 512 \
    --tokenizer-python "$TOKEN_PY" --tokenizer-cache "$TOKEN_CACHE" \
    --hybrid-space "$SPACE_ID" ./documents 'harbor inspection'
```

The two-coordinate example requires a prepared two-dimensional space. Substitute
the caller's actual vector for other dimensions; Mousa does not generate it.

Normal inspection needs no tokenizer, including after restart:

```sh
./mousa -store token-example.sqlite trail --source token-notes TRAIL_ID
./mousa -store token-example.sqlite trail --verify-tokens \
  --tokenizer-python "$TOKEN_PY" --tokenizer-cache "$TOKEN_CACHE" \
  --source token-notes TRAIL_ID
```

Replace `TRAIL_ID` with the query's identifier. The second form explicitly recounts
the selected rendering when every original selected passage is still retained.
It refuses retired, unarchived bytes rather than substituting today's text.
Both forms reauthorize the source; denied inspection discloses no history.
Normal `token_verification` distinguishes verified retained rendering digests
from unavailable retired text; the stored count remains producer evidence.
Only `official_recount_verified` reports an actual new tokenizer recount.

Token query `outcome` uses this precedence: a denied request is
`policy_excluded`; selected evidence is `evidence`; no canonical candidates is
`no_candidates`; candidates with no verified accepted disposition is
`lifecycle_excluded`. Otherwise, accepted candidates existed but none fit the
byte or token constraints, so the result is `budget_omitted`. In a mixed
accepted/rejected list, lifecycle rejection does not turn a genuine accepted
budget omission into `lifecycle_excluded`. Rejected identities, ranks and count
slots remain absent from the released token arrays in every case.

### Rendering, reservations and limits

`mousa.context_text.v1` renders each selected passage as:

```text
[source=SOURCE_ID segment=SEGMENT_ID sha256=CONTENT_SHA256]
UNCHANGED_PASSAGE_TEXT
```

The IDs/digest are lowercase 64-character hexadecimal strings. The header ends
with one LF; passage text is unchanged, with no added trailing LF. Two LF bytes
separate passages in selected candidate order. No selected passage means the
empty string. Literal special-token-looking text is ordinary evidence text.
The renderer is a counting/provenance format, not an instruction-escaping format.

Optional `--context-window W --prompt-reserve P --output-reserve O` combines the
explicit cap with `W-P-O` using overflow-safe subtraction. Cap range is
1..524288; window/reservations are 0..1000000000. Reservations require a positive
window and must fit it; an allowance of zero produces an empty context.
For example, `--context-tokens 512 --context-window 1024 --prompt-reserve 400
--output-reserve 300` allows at most 324 context tokens. These are caller
declarations: application prompt/tool/JSON framing outside the returned string
is not counted, and no universal model-window safety is claimed.

Token mode caps the released-text budget at 262144 bytes and candidate count at
100, including withheld candidates. Each accepted passage remains at most 4096
bytes. Aggregate accepted candidate text after exact supersession suppression
is capped at 262144 bytes, before budget/duplicate omissions; withheld bytes
remain verified but do not consume survivor capacity. Rendered text is capped
at 524288 bytes, aggregate counter input at 32 MiB, and counter lifetime at 15 seconds
(including writer-lock waits after initialization). Every attempted addition
counts the entire proposed rendering; passage counts are never summed.
Oversized passages are skipped and later candidates are tried. `exact-v1`
deduplicates against selected byte-equal passages only; skipped passages consume
no released budget and do not suppress later copies.

Token-specific options without `--context-tokens` reject. Token mode rejects
explicit association flags, even empty. Supersession true writes lexical v8
or vector/hybrid v10; omission and explicit false keep v7. Explicit false
vector/hybrid token flags, previously rejected, are now accepted as omission.
Vector-only mode still rejects explicit query-term policy; hybrid permits it.
Missing/wrong-version runtime, missing/corrupt cache, invalid backend output,
timeout or cancellation fail closed without a partial successful response or
partially committed query decision/trail. MCP queries expose the same token core
with explicit startup configuration and optional token fields described below;
OpenAI mention/resources remain byte-only.


## Lexical supersession with token budgets

After [preparing the external tokenizer](#prepare-the-optional-runtime), build
the executable and run the synthetic example from the repository root:

```sh
CGO_ENABLED=0 go build -o mousa ./cmd/mousa
python3 examples/token_supersession.py --mousa ./mousa \
  --tokenizer-python "$TOKEN_PY" --tokenizer-cache "$TOKEN_CACHE"
```

The example imports JSONL, discovers exact revision pins, writes real native
declaration and activation records, and queries both packers through native and
MCP clients. Each printed result has one withheld predecessor, one retained
successor, and an independent official count of the complete returned context.
It checks the exact token boundary and independently opted-in v8 history.

For a synced directory, the same composition uses:

```sh
./mousa -store local.sqlite query --supersession --context-tokens 512 \
  --tokenizer-python "$TOKEN_PY" --tokenizer-cache "$TOKEN_CACHE" \
  --packing-policy exact-v1 ./documents 'harbor inspection'
```

Prepare declaration/activation using that directory's own source and exact
revision pins as in the [directory example](#directory-supersession-example).
MCP uses a configured JSONL alias and operator-only startup backend; a query
adds `"supersession": true` to its existing token arguments. Query results add
`supersession_excluded`, including zero. All-withheld empty context reports
`supersession_excluded`; an unsuppressed survivor that cannot fit reports
`budget_omitted`. Withheld candidates remain accepted metadata with positive
size/rank and null proposed count, not rejected slots or replacement passages.

Vector/hybrid composition is also supported as v10; association queries still
reject supersession. Normal/recount native history uses the recorded selection,
not today's activation. MCP history independently
accepts `supersession: true` and `token_details: true`, returning producing
observations without recounting. Retired selected text cannot be reconstructed;
application/model framing is unknown and remains outside these counts.
This synthetic example makes no semantic-quality, performance or full-window claim.

## Byte packing and associations

`--packing-policy exact-v1` separately enables exact-content deduplication:

```sh
./mousa -store local.sqlite query --packing-policy exact-v1 --budget-bytes 4096 ./documents 'harbor inspection'
```

It omits byte-equal copies of already selected passages without changing ranking.
Authorization and lifecycle filtering run first; text that does not fit reserves
nothing. Each duplicate keeps its own provenance and references the retained
segment in the trail. Equal text is not independent corroboration or shared
authorization. Default `--packing-policy original` retains repeated passages.

For a directory or JSONL source with a separately stored and activated declaration:

```sh
./mousa -store local.sqlite query --supersession ./documents 'harbor inspection'
./mousa -store local.sqlite query --source inspection-notes --supersession 'harbor inspection'
./mousa -store local.sqlite query --source inspection-notes --supersession --packing-policy exact-v1 'harbor inspection'
```

Flags precede query text. Opted-in responses add `supersession_excluded`, including
zero; defaults omit it. Suppression is distinct from lifecycle, budget and duplicate
omissions, and an empty result reports budget omission before supersession exclusion,
then lifecycle exclusion. An `--associations` combination is rejected before the store
opens. Directory queries use their own source namespace, not the JSONL `--source`
namespace; passing the directory path to `--source` queries a different source.
A valid query uses writable open and can create or migrate a store before a later operation fails;
rollback does not undo that open boundary. Only the opted-in query prepares and
size-checks its expression before opening the store: a default or
`--supersession=false` query keeps the established post-open expression error, and
the store's own limit rejects an oversized expression after the store is opened.

`--associations <file>` opts a query into declared associated context:

```sh
./mousa -store local.sqlite query --associations associations.json --budget-bytes 4096 ./documents 'harbor inspection'
```

The file declares author-attributed relationships between items of the queried
source, for example that a procedure item is incomplete for its declared use
without the qualification documented in another item. Mousa never infers these
relationships; the author and basis stay attached to every passage the
declaration releases. A declaration is honored only when the declaring item
contributed selected primary evidence, so an unrelated query releases nothing.
Depth is one and at most four distinct targets are read per query, up to 256
target passages in total; a larger target is read only up to that bound and its
remaining passages are reported as an omission. Associated
passages pack into the remaining budget after primary evidence, keep their own
item, segment identity, byte coordinates and content digest, and carry the
declaration that included them. A missing, inactive or unfired declaration is
recorded as a visible omission, never a silent gap or a completeness claim. A
relationship is not an access grant: resolution stays inside the one allowed
source decision and only reaches the current active revision. The stored trail
records the complete association stage; decoding stays strict across trail
versions, and reading a stored trail re-verifies every released passage against
canonical store content. See the
[associated context contract](CAPABILITIES.md#declared-associated-context).

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
with vector queries. Explicit query-term policy and associations reject.
Supersession with byte or token budgets is an opt-in described below. MCP has a separate
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
Associations and vector-only flags cannot be combined with hybrid mode;
supersession with byte or token budgets is supported. The [capability contract](CAPABILITIES.md#native-hybrid-lexical-and-vector-retrieval)
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

### Vector and hybrid supersession

Build the executable, then run the synthetic external-vector import, selection
and recorded-history example:

```sh
CGO_ENABLED=0 go build -o mousa ./cmd/mousa
python3 examples/vector_supersession.py --mousa ./mousa
```

It imports supplied two-dimensional vectors, deliberately leaves the successor
without a vector, and compares ordinary versus enforced queries through native
JSONL and configured MCP. Under both packers, the ordinary predecessor wins;
enforcement withholds it and selects later surviving guidance within the same
byte budget. Producing history records the original ranks and consultation.
These synthetic values establish protocol/arithmetic, not semantic quality.

After preparing a directory or JSONL source and importing external vectors, add
`--supersession` to either byte query:

```sh
printf '%s' '{"schema":"mousa.vector_query.v1","values":[1,0]}' |
  ./mousa -store local.sqlite query --vector-space SPACE_ID --supersession ./documents
printf '%s' '{"schema":"mousa.vector_query.v1","values":[1,0]}' |
  ./mousa -store local.sqlite query --hybrid-space SPACE_ID --supersession --source SOURCE_ID 'Drain'
```

Replace placeholders with the imported space and configured source. Administer
the exact declaration and activation through the native commands first; merely
recording them changes no default query. MCP uses `supersession: true` on
`mousa_vector_query` or `mousa_hybrid_query`. Both report
`supersession_excluded`, including zero. Omission/false retains ordinary response
properties and v5/v6 history; true writes v9 even without history or on denial.
All-withheld is `supersession_excluded`, not a budget omission. Coverage remains
the admitted producing source coverage, not the survivor count.

Inspect natively with `trail`, or request MCP `mousa_trail` with independent
`supersession: true` and `retrieval_details: true` to see both verified recorded
members. Later activation, deactivation and revision changes do not rerank history.
Fresh authorization can deny it and can append audit evidence. Retired text is
not reconstructed. Add `--context-tokens N --tokenizer-python "$TOKEN_PY"
--tokenizer-cache "$TOKEN_CACHE"` before positional arguments to combine either
mode with measured tokens and retain the independent byte budget. True records
v10; omitted/false records v7. Full vector-source admission remains before masking.

The same runnable example has an optional token mode:

```sh
python3 examples/vector_supersession.py --mousa ./mousa \
  --tokenizer-python "$TOKEN_PY" --tokenizer-cache "$TOKEN_CACHE"
```

It independently counts the exact returned rendering with official
`encode_ordinary`, checks exact-fit and one-token-short contexts, and verifies
v10 history. Without these options, the byte-only run needs no tokenizer.
Configured MCP uses `context_tokens` and the existing reservations alongside
`supersession: true`; history's `token_details` remains independent of the
consultation and retrieval-details opt-ins. No MCP recount or byte fallback occurs.
No automatic embeddings, retrieval-quality, semantic-truth or model-window claim
follows. MCP also exposes explicit vector/hybrid tools; resource search stays lexical.

### CLI client example

For a persistent documentation lookup, see the
[versioned Git documentation example](../examples/docs/README.md). It imports
pinned public manuals and their direct include fragments, then returns passages
with verified byte ranges, normalized line locations and upstream links. Its
optional `--context-tokens` mode renders cited passages within a
`tiktoken==0.12.0`/`o200k_base` prompt-content cap; the raw packet still
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

`access ./documents deny` blocks this CLI caller's retrieval and trail inspection;
`access ./documents allow` restores that policy permission. Both also accept
`--source <id>`. `withdraw ./documents` changes source lifecycle state: policy allow
does not undo withdrawal, and the CLI has no resume command. These controls are not
secure erasure or a replacement for filesystem access controls.

`supersession declaration put` appends one strict revision-pinned declaration read
from stdin, and `supersession declaration get <declaration-id>` reads one stored
declaration back. Both print the canonical stored declaration JSON:

```sh
./mousa -store local.sqlite supersession declaration put < declaration.json
./mousa -store local.sqlite supersession declaration get DECLARATION_ID
```

A declaration names its source plus the exact predecessor and successor item and
representation revisions, so item labels alone cannot pin a revision. It is
immutable history, not an activation pointer and not an access grant: storing one
does not filter queries, suppress the predecessor or authorize anything. The input
is one `mousa.supersession_declaration.v1` object limited to 64 KiB before decoding;
the codec rejects unknown or duplicate fields, trailing values, malformed identities
and an identity that disagrees with its content. The declared source and both pinned revisions must already exist; the command
does not create or retarget sources, items or revisions, or repair records.
Writable open may create an absent store or upgrade an older supported store
before target validation rejects a declaration. Such a rejection appends no
declaration; it does not undo store creation or migration. An exact retry is idempotent; a reused identity with different
content is a conflict. `get` opens the store read-only, so an invalid identity, a
missing record, an absent store and a corrupt stored record all fail without
creating, migrating or repairing anything.

`supersession activation put` applies one strict activation transition read from
stdin, and `supersession activation get <activation-id>` reads one stored event
back. Both print the canonical stored event JSON, and
`supersession activation state <source-id>` reports the verified current state for
one source as a projection object:

```sh
./mousa -store local.sqlite supersession activation put < activation.json
./mousa -store local.sqlite supersession activation get ACTIVATION_ID
./mousa -store local.sqlite supersession activation state SOURCE_ID
```

The input is one `mousa.supersession_activation.v1` object limited to 64 KiB before
decoding, with the same strict codec rules. The caller states the transition: the
event identity, the nullable expected predecessor event and the nullable selected
declaration. A null predecessor with a declaration is a first activation, a named
predecessor with a declaration is a replacement or reactivation, and a named
predecessor with a null declaration is a deactivation. Nothing infers the current
predecessor or invents a transition. The transition identity covers only the source,
expected predecessor and selected declaration, so actor, time and reason are
recorded labels: replaying an identity with different metadata is a conflict, not an
exact retry, and recording them authenticates nothing.

The store enforces the transition semantics inside one writer transaction: a first
activation must be a root, a replacement or deactivation must name the exact current
event, a transition that would not change the active declaration is refused, an exact
retry succeeds only while its event is still current, and a stale or competing
expectation is a conflict. A rejected transition transaction appends no event and
changes no activation, declaration or canonical record. Writable open may still
create an absent store or upgrade an older supported store before target validation
rejects the transition; rejection does not undo that creation or migration. Malformed
or oversized input is rejected before the store opens.
`get` opens the store read-only and keeps historical reads: an event stays readable
after later transitions, while a retry of that event is rejected. Activation is
recorded local administration; it grants no access, moves no
item pointer, mutates no declaration and changes no default query. Only an explicitly
opted-in query applies it.

`state` opens the store read-only and prints one JSON object with exactly
`source_id`, `current_activation_id` and `active_declaration_id`. The identity
strings are the same canonical lowercase hexadecimal values the event and declaration
records carry, and a deactivated source reports an explicit JSON null declaration.
The object is a current-state projection: it carries no identity, schema or hash of
its own and is not stored. A source with no activation history reports the store's
`not_found` error, while history whose current projection is missing or inconsistent
with the event it names fails integrity verification and is never rebuilt or repaired
by the read. The state is a verified snapshot rather than a reservation: a caller that
transitions next still states its own expected predecessor and can still conflict if
another caller wins first. Use `state` for the current projection and `get` for one historical event. There is
no activation listing or per-source history command. MCP `mousa_query` and `mousa_trail`
accept an optional `supersession` boolean; declaration and activation administration
remain unavailable over MCP.

The declaration and activation command groups are trusted local store administration,
and none of them claims to authenticate a recorded actor or author. See the
[supersession boundary](CAPABILITIES.md#supersession-core-boundary).

### Directory supersession example

Build `./mousa` as above. This Python 3 example runs native commands against a fresh
temporary directory and store. It discovers exact item and representation pins from
default query evidence, then submits explicit declaration and activation JSON through
the existing administration commands. No database access or model is involved.

The CLI accepts complete records, not identity-free drafts. For these records, each
identity is SHA-256 over an ordered tuple: each field has an unsigned 8-byte big-endian
byte-length prefix. Schema and label fields are UTF-8; identity fields are raw 32-byte
digests, not their hexadecimal display strings. A directory source uses the tuple
`("mousa.source.v1", "mousa-local", absolute_directory_path)`; JSONL uses
`"mousa-jsonl"` instead. Do not resolve symlinks or substitute the stream namespace.
The preparation below implements only these existing encodings for this example;
the native commands validate both the identities and stored pin ancestry.

```sh
python3 - <<'PY'
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile

binary = str(Path("./mousa").absolute())
with tempfile.TemporaryDirectory(prefix="mousa-directory-example-") as temporary:
    root = Path(temporary) / "documents"
    root.mkdir()
    store = Path(temporary) / "evidence.sqlite"
    (root / "old.md").write_text("cedar predecessor evidence", encoding="utf-8")
    (root / "new.txt").write_text("cedar successor evidence", encoding="utf-8")

    def command(*args, record=None):
        result = subprocess.run(
            [binary, "-store", str(store), *map(str, args)],
            input=None if record is None else json.dumps(record),
            text=True, capture_output=True, check=True)
        return json.loads(result.stdout)

    def identity(*fields):
        digest = hashlib.sha256()
        for field in fields:
            value = field.encode("utf-8") if isinstance(field, str) else field
            digest.update(len(value).to_bytes(8, "big"))
            digest.update(value)
        return digest.hexdigest()

    command("sync", root)
    baseline = command("query", root, "cedar")
    pins = {hit["item"]: hit["representation_id"] for hit in baseline["evidence"]}
    source = identity("mousa.source.v1", "mousa-local", os.path.abspath(root))
    declaration = dict(
        schema="mousa.supersession_declaration.v1", source_id=source,
        predecessor_item_id="old.md", predecessor_representation_id=pins["old.md"],
        successor_item_id="new.txt", successor_representation_id=pins["new.txt"],
        author="example.operator", basis="new.txt explicitly replaces old.md")
    declaration["id"] = identity(
        declaration["schema"], bytes.fromhex(source), "old.md",
        bytes.fromhex(pins["old.md"]), "new.txt", bytes.fromhex(pins["new.txt"]),
        declaration["author"], declaration["basis"])
    command("supersession", "declaration", "put", record=declaration)
    activation = dict(
        schema="mousa.supersession_activation.v1", source_id=source,
        expected_previous_activation_id=None, declaration_id=declaration["id"],
        actor_id="example.operator", actor_version="1", occurred_at_usec=1,
        reason="explicit directory replacement")
    activation["id"] = identity(
        activation["schema"], bytes.fromhex(source), b"",
        bytes.fromhex(declaration["id"]))
    command("supersession", "activation", "put", record=activation)
    disabled = command("query", "--supersession=false", root, "cedar")
    assert disabled["evidence"] == baseline["evidence"]
    for packing in ("original", "exact-v1"):
        selected = command("query", "--supersession", "--packing-policy", packing,
                           root, "cedar")
        assert [hit["item"] for hit in selected["evidence"]] == ["new.txt"]
        inspected = command("trail", root, selected["trail_id"])
        print(json.dumps(inspected["historical"]["supersession"], indent=2))
    (root / "old.md").write_text("cedar revised predecessor evidence", encoding="utf-8")
    command("sync", root)
    revised = command("query", "--supersession", root, "cedar")
    assert revised["supersession_excluded"] == 0
    assert {hit["item"] for hit in revised["evidence"]} == {"old.md", "new.txt"}
    historical = command("trail", root, selected["trail_id"])
    assert historical["historical"]["supersession"]["activation_id"] == activation["id"]
PY
```

Each printed v4 member names the producing activation, declaration, predecessor and
successor pins. Declaration alone changes no selection; activation affects only
opted-in queries. An updated predecessor has a different representation and is not
withheld by the old pin. Removing or updating the successor does not revoke the
declaration. A later deactivation names the current activation as
`expected_previous_activation_id` and sets `declaration_id` to null; reactivation
names that deactivation and the declaration. Null identity tuple fields are empty
bytes. Read `supersession activation state SOURCE_ID` before preparing a subsequent
transition; a competing transition can still cause a conflict. Historical `trail`
inspection reauthorizes access and preserves the producing pins, not today's state.

Integrity checks, authorization, current activation, and factual truth are separate
properties. Classification records are not automatic categorization or
classification-based authorization.

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
The eight tools are `mousa_sync`, `mousa_status`, `mousa_query`, `mousa_trail`,
`mousa_vector_query`, `mousa_hybrid_query`, `mousa_vector_inputs` and `mousa_vector_import`.
Store, permitted sources and trusted caller are fixed at startup. Item ingestion
and embedding import are disabled unless that source also has `--ingest-source`.
The default stdio mode has no listener, directory import, shell, policy administration or withdrawal tool.

`mousa_trail` reauthorizes each inspection. Denial succeeds with a fresh committed
authorization decision ID and reasons, but no history or existence disclosure.
Unknown and foreign identities return `not_found` without committing the attempted
decision. An already-open inspector also returns `not_found` for a trail whose missing
decision parent prevents source ownership verification; fresh-open verification instead
rejects that damaged store as `integrity`. Neither path repairs history.

Native `query --source <id> --associations <file>` can produce v3 history that the
configured same-source MCP inspector reads. Omitted, false and true `supersession`
inputs all preserve its association metadata and producing identities without adding
a supersession member.

`--caller cli` uses the existing CLI retrieval policy, including source-scoped
denials and withdrawal. This is trusted local configuration, not caller
authentication or protection from someone who can modify the store. Other
caller IDs require matching policy provisioned separately; they do not inherit
CLI access.

Tool calls return structured, versioned results. Queries require an explicit
query-term policy and released-text byte budget, and return evidence bytes,
normalized coordinates, digests, packet IDs and Source Trail IDs. Retrieved text
is data, not instructions. See [tool schemas, limits, errors and lifecycle](CAPABILITIES.md#local-stdio-mcp).

Local stdio MCP exposes the same opt-in explicitly: `mousa_query` accepts an optional
`supersession` boolean, and an opted-in response adds `supersession_excluded`, including
zero. `mousa_trail` accepts the same boolean and returns the consultation member a
`mousa.source_trail.v4` record carries: whether supersession was consulted, the nullable
activation and declaration identities and the withholding rows, so a client can tell a
consulted absence of history from a deactivation without reading the store. Omitting the
field or passing `false` keeps the default retrieval and the filtered inspection, and a
legacy trail carries no member. The records must be prepared by the
native `supersession declaration put` and `supersession activation put` commands for the
same configured source. See [MCP supersession opt-in](MCP_SUPERSESSION.md).

The actual-executable MCP client regressions run with:

```sh
CGO_ENABLED=0 go test ./cmd/mousa -run TestMCP -count=1 -v
```

MCP acceptance is deterministic client/server verification, not a model-driven
agent or retrieval-quality measurement. A stable public SDK and general
connectors remain unsupported.

### MCP vector and hybrid example

Build `./mousa`, then run this synthetic standard-library Python example on Linux:

```sh
CGO_ENABLED=0 go build -o mousa ./cmd/mousa
python3 examples/mcp_retrieval.py --mousa ./mousa
```

The example creates a fresh temporary store and uses only MCP tools to sync three
JSONL items, export current normalized passage bytes/pins, import hand-assigned
two-coordinate vectors, retry that exact import, query and inspect history.
The vector-only hit is `vector.md`; the overlapping `overlap.md` wins hybrid
equal-weight RRF(k60) despite ranking second in each branch. Printed JSON includes
selected evidence and matching history identities. Temporary data and receipts
are removed on exit. The vectors are arithmetic-only: no semantic quality, model
integration, performance or model-window safety is established.

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
rerank the original record. The independent `supersession` opt-in still works.
Native v7 vector/hybrid facts can also be inspected. The independent
`token_details` opt-in returns safe recorded token facts without recounting.

Only exact configured JSONL aliases are permitted, including when a label looks
like a path. No directory read permission follows. Native bounds and precise
space/dimension/current-coverage errors apply, without lexical fallback.
Supersession/association combinations on these query tools reject rather than
being ignored. Refresh cached closed schemas through `tools/list`.
OpenAI mentions/resources retain their lexical contract.


### MCP token-budget example

Prepare the official environment and cache using the
[tokenizer preparation commands](#native-token-aware-context-packing), then configure
both `--tokenizer-python "$TOKEN_PY"` and `--tokenizer-cache "$TOKEN_CACHE"` in
the MCP process arguments. Only the operator chooses these paths. Ordinary MCP
startup and byte queries need neither Python nor these options.

Run the same synthetic client with token mode enabled:

```sh
python3 examples/mcp_retrieval.py --mousa ./mousa \
  --tokenizer-python "$TOKEN_PY" --tokenizer-cache "$TOKEN_CACHE"
```

It exercises lexical, vector and hybrid queries, independently counts the exact
returned context with official `encode_ordinary`, checks its SHA256 and the exact
fit/one-token-short boundary, and reads matching producing token history.
No model or embedding service is called. For your own client, add the following
to any query tool's existing required arguments:

```json
{"context_tokens":512,"window_tokens":2048,"prompt_reserve_tokens":1024,"output_reserve_tokens":512}
```

The effective allowance here is 512 tokens. The required `budget_bytes` also
limits released passage text. `context` is the exact `mousa.context_text.v1`
rendering, including source/segment headers and separators; `token` reports its
measured count, digest, bytes, backend/rendering identity and budget facts.
Count that string, not the JSON serialization or a sum of passage counts.
Client/model framing and unknown prompt content are outside the count;
reservations do not establish full model-window safety.

Omitting `context_tokens` preserves the existing byte response property set.
Window/reservation fields require it even when zero. Caps are positive integers
up to 524288; declarations are integers 0..1000000000. Nonzero reservations
require a positive window and cannot exceed it together. Null, booleans,
fractional/overflow numbers, duplicate and unknown fields reject before mutation.
All three token modes accept supersession: true records lexical v8 or
vector/hybrid v10; byte-only vector/hybrid remains v9. Associations are unsupported.
Tool inputs cannot set interpreter, cache, store or caller paths/identity.

Without a configured usable backend, token mode returns `tokenizer_unavailable`;
byte tools remain available. The query-scoped offline worker is bounded and
reaped before successful v7 persistence. Counting failure and cancellation leave
no successful producing trail/packet. Startup/migration/policy writes and the
fresh authorization audit on a legitimate inspection are not rolled back by
that guarantee.

Inspect with `mousa_trail` arguments `source`, `trail_id`, `token_details: true`.
It returns the accepted-only recorded v7 view after fresh authorization, not
canonical rejected metadata or a new measurement. Default/false hides it; legacy
records have no token member. `retrieval_details` remains independent. Counts and
digests survive restart and source replacement under existing retained-text limits;
MCP provides no historical recount. OpenAI mention/resources stay byte-only.

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

### ChatGPT desktop and Codex CLI

The integration follows [OpenAI MCP Extensions](https://github.com/openai/mcp-extensions):
composer evidence mentions, authorized resource reads and plugin onboarding.
Extensions do not establish transport, authentication or directory publication.
The four core tools remain the fallback for hosts without desktop extensions.

Build Mousa, ingest only data you intend to share, then create a new local
marketplace. This example uses the maintained synthetic fixture:

```sh
CGO_ENABLED=0 go build -o ./mousa ./cmd/mousa
./mousa -store ./evidence.sqlite sync --source fixture < eval/local/openai-fixture.jsonl
./mousa -store ./evidence.sqlite plugin --out ./mousa-marketplace \
  --source fixture --consent-to-share
codex plugin marketplace add ./mousa-marketplace
codex plugin add mousa@mousa-local
codex plugin list --marketplace mousa-local --json
```

The package includes root `plugin.json`, `mcp.json`, setup/evidence skills and
a contained executable. Its store path and source permissions are bound to
this user. Regenerate configuration for another user; do not distribute private
store paths, stores or credentials. The executable targets the platform used
to build it. Existing output directories are refused. Ingestion remains disabled
unless separately requested with `--ingest-source`.

In ChatGPT desktop, install the local marketplace plugin and run its onboarding.
The setup skill reuses explicit user authorization for the configured sources
and sharing with the configured host/provider; otherwise it requests confirmation
before retrieval. Installation alone does not establish that authorization.
Then search for Cedar using the composer mention picker. Codex CLI can use the
evidence skill and core tools without a mention picker. The directory is local, not a public listing.
Actual desktop interaction and model-driven CLI use require the corresponding
supported app/account and remain separate from credential-free acceptance.

The host/provider receives selected text, labels, identifiers, coordinates,
digests and provenance metadata. Mousa does not filter secrets. See
[extension, privacy, HTTP and publication boundaries](CAPABILITIES.md#openai-mcp-extensions).
Local stdio requires no tunnel. Hosted/private connectivity may require a
separately configured Secure MCP Tunnel; it is not public directory deployment.
