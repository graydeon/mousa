![Mousa — an experimental memory instrument](assets/mousa_readme_banner_v2_1600x480.png)

> **Status:** Pre-alpha. Build `cmd/mousa` from source for local UTF-8 directory sync, bounded JSONL input, source-scoped lexical queries, and authorized Source Trail inspection. Query policy and released-text byte budgets are explicit. Packages remain internal, with no stable SDK. See the [capability matrix](docs/CAPABILITIES.md) for supported behavior and the [research record](docs/RESEARCH.md) for measured results and limitations.

Mousa is a local-first retrieval and memory backbone for agents and other software that need useful context over long periods. It is intended to recover relevant source material, preserve what changed, and assemble compact context packets that can be inspected before use. The result is memory with a source trail rather than an opaque answer.

## Why Mousa?

*Mousa* is the Ancient Greek singular of *Muse*. In Greek mythology, Mnemosyne—the titan Goddess of Memory—is the mother of the nine Muses. The name gives Mousa a practical organizing idea: durable memory is not one act. Sources move through nine explicit stages before they become usable context.

## The problem

Long-lived knowledge does not only grow. It becomes stale, duplicated, contradictory, and detached from its sources. Search can return matching text without showing why it was selected, whether it remains current, or what it replaced. Agent systems also pay a direct cost when retrieval sends more context than the task requires.

Mousa is being designed to address those problems with source-linked retrieval, explicit lifecycle and policy metadata, inspectable ranking, and byte-budget context assembly; token-aware budgeting is planned.

## The nine-stage retrieval backbone

Mousa's retrieval backbone deliberately echoes the nine Muses. Stages 1–5 are implemented in the Go core; stages 6–9 are implemented in narrower form and the deferred parts are listed:

| Stage | Responsibility | Status |
|---|---|---|
| **1. Ingest** | Register source material with its origin, identity, and policy metadata. | Implemented. |
| **2. Normalize** | Convert input into a stable representation while preserving the original source. | Implemented for UTF-8 text. |
| **3. Segment** | Produce deterministic, addressable chunks. | Implemented for byte-range text segments. |
| **4. Index** | Build portable lexical and optional semantic search structures. | Lexical (SQLite FTS5) implemented; semantic retrieval is planned, not implemented. |
| **5. Match** | Retrieve candidate evidence for a request. | Implemented for lexical matching. |
| **6. Rank** | Order candidates using inspectable relevance and policy signals. | Implemented for BM25 order plus source-lifecycle policy; hybrid ranking is planned. |
| **7. Verify** | Check authority, freshness, sensitivity, conflicts, and supersession. | Partially implemented: source lifecycle and deployment policy decisions; freshness, supersession, and conflict checks are deferred. |
| **8. Trace** | Record how evidence moved through retrieval in a durable Source Trail. | Implemented for enforced lexical retrieval; see the narrower field list below. |
| **9. Pack** | Assemble the selected evidence within an explicit context budget. | Implemented as an explicit byte budget; token-aware selection is planned. |

Each stage has one responsibility and a visible boundary. The pipeline can be tested stage by stage, and no single model, provider, or harness owns the result.

## Source Trails

A Source Trail records how a context packet was produced. The default `mousa.source_trail.v1` binds the policy request and decision, outcome, search expression, byte budget, packet identity, and ordered candidates with segment identity, content hash, final rank, byte count, disposition, lifecycle reasons, and selection flag. Opt-in exact-content packing writes `mousa.source_trail.v2`, which also binds the packing policy, omission reason and retained-segment relationship. Historical v1 bytes and identities remain unchanged. Ranking-stage explanations, transforms and supersession decisions remain planned.

The goal is not to present a score as an explanation. The goal is to retain enough evidence to inspect what was selected, what was rejected, and why.

## Intended callers

Mousa is intended to expose one portable core through narrow interfaces for:

- agent harnesses and model-independent automation;
- command-line tools;
- application and SDK integrations;
- MCP clients;
- documented HTTP clients where a service boundary is justified;
- ordinary JSON and JSONL import and export.

The first shipped interface is the local vertical slice, `cmd/mousa`.

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
relative POSIX path. Updates and reverts atomically replace current search evidence;
deletion removes current evidence without deleting canonical history. Identical
content restored after deletion becomes searchable again. `status` reports active
items separately from historical observations.

Directory defaults select Markdown/plain-text extensions and skip hidden and
generated entries. Preview shows selected names and byte lengths without opening
the store. Use repeatable `--include`/`--exclude` globs to constrain scope and
`--all-text` only as a deliberate override. Selection flags are not saved between
invocations; successful scope changes remove old items outside the selected set.
See [directory selection and limits](docs/CAPABILITIES.md#directory-selection-and-input-limits).

The default ingestion policy, `fixed-v1`, uses segments of at most 4,096 UTF-8 bytes.
To opt into bounded passage boundaries, use
`./mousa -store local.sqlite sync --segment-policy passage-v1 ./documents`.
This policy groups document blocks into at most 1,024-byte source slices, with
explicit fallback for oversized blocks. Repeat the flag on resync; a policy change
replaces current evidence even when source bytes are unchanged. Smaller passages
can fit smaller evidence budgets but change lexical ranking and increase segment
count. See [policy identity, boundaries and limitations](docs/CAPABILITIES.md#ingestion-segment-policies).

For an explicit item stream:

```sh
printf '%s\n' '{"id":"note@draft","text":"The harbor inspection is on Friday."}' |
  ./mousa -store local.sqlite sync --source inspection-notes
./mousa -store local.sqlite query --source inspection-notes 'harbor inspection'
```

JSONL input is bounded to 1 MiB per line, 64 MiB per invocation, and 10,000 records.
Deletion is explicit; omission does not delete. Failed input retains its committed
prefix and emits no success result. See the [input and recovery contract](docs/CAPABILITIES.md)
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
omission. See the [query and inspection contract](docs/CAPABILITIES.md#query-policy-packing-and-tracing).

`--packing-policy exact-v1` separately enables exact-content deduplication:

```sh
./mousa -store local.sqlite query --packing-policy exact-v1 --budget-bytes 4096 ./documents 'harbor inspection'
```

It omits byte-equal copies of already selected passages without changing ranking.
Authorization and lifecycle filtering run first; text that does not fit reserves
nothing. Each duplicate keeps its own provenance and references the retained
segment in the trail. Equal text is not independent corroboration or shared
authorization. Default `--packing-policy original` retains repeated passages.

### CLI client example

For a persistent, usable documentation lookup, see the
[versioned Git documentation example](examples/docs/README.md). It imports two
pinned public manuals and returns bounded passages with verified byte ranges,
normalized line locations and upstream links. It does not generate answers.

For an explicit caller decision over those retrieval contracts, see the
[SQLite backup checklist](examples/backup/README.md). It verifies saved passages
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
See [normalized passage coordinates](docs/CAPABILITIES.md#verify-a-selected-passages-location)
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
[evolving workflow](docs/RESEARCH.md#evolving-evidence-and-native-lexical-cli-comparison)
can separately run native lexical comparisons when `--example-only` is omitted.
Run the required client acceptance suite from the repository root:

```sh
CGO_ENABLED=0 go build -o mousa ./cmd/mousa
python3 eval/local/workflow_test.py --mousa ./mousa
```

The command requires an explicit readable executable and runs the actual CLI
workflow plus nonzero-exit, malformed-JSON, wrong-shaped-output, and timeout
consumer tests. It also runs the versioned documentation and caller-reviewed
backup consumer suites. It exits nonzero on a failed test, unmet prerequisite, or skipped test.
Failures include captured workflow output for diagnosis. Each run uses temporary
stores; the original workflow's CLI calls have five-second deadlines (0.2 seconds
for the intentional timeout), documentation and backup calls have 30-second deadlines, and
each workflow subprocess has a 120-second deadline.
The suite does not run comparisons or benchmarks. Ad hoc `unittest` discovery
may still skip the real-CLI test when `MOUSA_EXECUTABLE` is unset; it is not a
substitute for this required command.

`access ./documents deny` blocks this CLI caller's retrieval and trail inspection;
`access ./documents allow` restores that policy permission. Both also accept
`--source <id>`. `withdraw ./documents` changes source lifecycle state: policy allow
does not undo withdrawal, and the CLI has no resume command. These controls are not
secure erasure or a replacement for filesystem access controls.

Integrity checks, authorization, current activation, and factual truth are separate
properties. Classification records are not automatic categorization or
classification-based authorization.

SDK, MCP, HTTP, and human-facing application interfaces remain planned. They are not
part of the supported CLI workflow.

## Design principles

- **Local-first:** the baseline should work without a hosted service.
- **Source-linked:** retrieved context should retain durable links to its evidence.
- **Lean:** use the smallest reliable components and avoid mandatory services.
- **Portable:** start with ordinary files, SQLite, documented protocols, and a tested CPU-only baseline.
- **Inspectable:** ranking, filtering, transformation, and packet assembly should be traceable.
- **Deterministic where possible:** stable identities, hashes, manifests, and truncation rules should make results reproducible.
- **Token-aware:** retrieval quality and context cost should be measured together.
- **Provider-independent:** no model provider, accelerator, agent harness, or database service should own the canonical store.

Compatibility will be documented as a tested matrix. Mousa will not claim support for an operating system, architecture, accelerator, model backend, or agent harness until that combination has reproducible public verification.

## Foundation

Implemented and planned capabilities, marked per item:

- SQLite-first local storage (implemented);
- lexical retrieval through SQLite FTS5 or an equally portable built-in mechanism (FTS5 implemented);
- optional semantic retrieval behind a narrow provider interface (planned);
- inspectable hybrid ranking (planned);
- deterministic ingestion, chunk identity, source hashing, and manifests (ingestion and identity implemented; manifests planned);
- authority, freshness, sensitivity, status, and supersession metadata (lifecycle status and deployment policy implemented; freshness, supersession, and conflicts deferred);
- secret filtering and non-indexable sensitivity classes (planned);
- byte-budget-aware selection with opt-in exact-content deduplication (implemented); truncation, approximate redundancy removal and token-aware budgeting remain planned;
- source-linked context packets with durable Source Trail identifiers (implemented in the core and exposed by `cmd/mousa`);
- documented export formats that other tools can read without Mousa (planned).

Items marked planned are design targets. They are not a release checklist or a statement of current functionality, and nothing in this document is a measured claim.

## Credits

README banner portrait source: Dante Gabriel Rossetti, *Mnemosyne* (c. 1876–1881), Delaware Art Museum, accession 1935-22. [Public-domain reproduction via Wikimedia Commons](https://commons.wikimedia.org/wiki/File:Mnemosyne_DAM.jpg). Materially transformed for Mousa.

## License

Mousa is licensed under the GNU Affero General Public License v3.0. See [`LICENSE`](LICENSE).


## Benchmark results

Raw measurements, optional comparison dependencies, and reproduction guidance are
published in [mousa-benchmarks](https://github.com/graydeon/mousa-benchmarks).
[Research methods and limitations](docs/RESEARCH.md) remain here. Product tests
and required CLI client acceptance do not depend on the benchmark repository.
