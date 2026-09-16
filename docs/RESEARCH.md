# Mousa research record

Raw benchmark reports and optional comparison dependencies are maintained in
[mousa-benchmarks](https://github.com/graydeon/mousa-benchmarks).
Historical report links below identify the byte-preserving migration commit.
Product regression tests, required client acceptance, and evaluation tools that
depend on Mousa internals remain in this repository. Moving reports does not
change their original source identities, protocols, or conclusions.

This record tracks Mousa's empirical evaluation: hypotheses, methods, raw results,
limitations, and reproduction. It is the only source of measured claims about Mousa.
Development subsets and modified protocols are labeled as such; none is described as a
full-suite result. Results are grouped by measurement environment; timings from different
machines are never combined into one software comparison.

## Status

Pre-alpha engine (verified lexical retrieval with FTS5 BM25, ingest, segmentation,
lifecycle verification, policy decisions, Source Trails, byte-budget packets). No
answering pipeline exists yet, so no RAG/answer-quality benchmark is applicable. Memory
benchmarks (LongMemEval, MemoryAgentBench) and FreshStack await the corresponding
retrieval capabilities; see the roadmap in the documented roadmap.

## Opt-in passage segmentation

A bounded development comparison on 2026-09-16 evaluated `fixed-v1` against
`passage-v1` through the supported sync/query CLI. The default remains `fixed-v1`.
The [report](https://github.com/graydeon/mousa-benchmarks/blob/0d5b3c64194bc8dd2876687ad5ded3d85c625ad9/results/2026-09-16/passage-policy.json) records the frozen
questions, required source passages, fixture hashes, candidate runtime hashes,
individual timing samples, coverage screening and manual judgments.

The documentation corpus was README.md and docs/CAPABILITIES.md from `6e84d5e`:
32,826 bytes, unchanged from the earlier exercise. The original ten natural
questions and three separate keyword reformulations were retained. Before
measurement, five synthetic fixtures added a command runbook, a list/table
reference, plain prose, Unicode text and a deliberately oversized fenced block.
Their required spans were 121, 184, 116, 98 and 3,001 bytes respectively. Only the
first four were declared feasible at 1,024 bytes; all five were feasible at 8,192.
Feasibility does not imply that lexical ranking selects the required passage.

| Corpus and measure | Budget | Fixed | Passage |
|---|---:|---:|---:|
| Documentation: complete required facts | 1,024 | 0/10 | 7/10 |
| Documentation: complete required facts | 8,192 | 7/10 | 10/10 |
| Documentation: entire declared source span selected | 1,024 | 0/10 | 5/10 |
| Documentation: entire declared source span selected | 8,192 | 6/10 | 7/10 |
| Synthetic: complete feasible source spans | 1,024 | 0/4 | 3/4 |
| Synthetic: complete source spans | 8,192 | 5/5 | 5/5 |

Required-fact judgments retain the original semantic-equivalence rule rather than
requiring identical phrases. For example, the selected README says that omission
does not delete an item; the JSONL example separately supplies `deleted:true`.
At 1,024 bytes the JSONL question remains partial, and upload-retry and trail
questions remain insufficient. The separate keyword diagnostics recover retry
and historical-text limits at 1,024, but not the complete JSONL answer. At 8,192,
both policies supply all three keyword-diagnostic answers. Those diagnostics do
not replace the natural-question scores.

There are losses that the aggregate required-fact score does not show. At 8,192,
the recovery question loses the previously selected preview-guidance paragraph:
after separation, that segment has no lexical match to the question. The trail
question loses its separate access-instruction paragraph: the 719-byte segment
ranks 29 and is omitted; the packet uses 8,149 bytes. Neither loss was removed
from the full-span measure. In the synthetic Unicode question at 1,024 bytes, the
989-byte required passage ranks second; an unrelated 994-byte reference passage
is selected first and leaves 30 bytes. No query-specific or document-specific
policy adjustment followed these results.

The documentation fixed policy had five cuts between alphanumeric characters;
the passage policy had none. This is a narrow boundary metric, not a Markdown
validity score. The 3,001-byte synthetic fence becomes three fragments without
reconstructed delimiters. Both policies supply the whole source span at 8,192,
neither at 1,024. Repeated filler in the synthetic sources produces 11 repeated
selected content hashes across the five passage-policy queries at 8,192, versus
zero with fixed segments. Segmentation introduces no overlapping source ranges,
but packing still does not remove repeated content. Documentation queries had
no exact duplicate selected hashes. At 8,192, selected bytes outside each
declared documentation span total 75,721 for fixed and 75,576 for passage across
ten questions. This is a context-size proxy, not a semantic redundancy score:
equivalent evidence in another source also falls outside that span.

Measurements used three repetitions, alternating policy order, in one shared
six-vCPU EPYC-IBPB VM on a Ryzen 7 2700X host, with 5.5-core and 8-GiB job limits.
Both policies used the same candidate executable, Go 1.27.1, `CGO_ENABLED=0`,
`GOAMD64=v1` and `GOMAXPROCS=6`. Each query copied an initialized closed store
before timing, then launched a fresh CLI process. Filesystem caches were warm,
not flushed. Timings include open, retrieval, durable trail, JSON output and exit;
builds and fixture copies are excluded. Query medians pool three repetitions of
each question: 30 samples per documentation arm/budget and 15 per synthetic arm.
Peak RSS is the maximum observed with GNU `time`.

| Corpus | Measure | Fixed | Passage |
|---|---|---:|---:|
| Documentation | Segments | 9 | 49 |
| Documentation | Initial closed store bytes | 479,232 | 507,904 |
| Documentation | Lexical index page bytes | 90,112 | 94,208 |
| Documentation | Initial sync median ms | 163.45 | 330.59 |
| Documentation | Unchanged sync median ms | 49.33 | 55.92 |
| Documentation | Query median ms, 1,024 / 8,192 | 55.99 / 67.26 | 99.79 / 104.41 |
| Documentation | Query peak RSS KiB, 1,024 / 8,192 | 17,616 / 17,612 | 20,440 / 20,596 |
| Synthetic | Segments | 5 | 16 |
| Synthetic | Initial closed store bytes | 434,176 | 442,368 |
| Synthetic | Lexical index page bytes | 45,056 | 45,056 |
| Synthetic | Initial sync median ms | 197.02 | 226.98 |
| Synthetic | Unchanged sync median ms | 49.26 | 51.33 |
| Synthetic | Query median ms, 1,024 / 8,192 | 52.78 / 53.05 | 67.89 / 64.76 |
| Synthetic | Query peak RSS KiB, 1,024 / 8,192 | 15,572 / 15,532 | 17,500 / 18,044 |

Before measurement, resource ceilings were 12 times the segment count, four
times initial store bytes, six times median sync cost, query median no greater
than the larger of three times baseline or baseline plus 50 ms, and peak RSS no
greater than the larger of twice baseline or baseline plus 8,192 KiB. Both corpora
met these opt-in engineering ceilings. They are not speedup criteria: documentation
ingestion took about twice as long, and queries were 55–78% slower.

All 279 measurement CLI calls exited successfully. Selected evidence matched
canonical ranges and hashes, stayed within byte budgets, and repeated queries
selected identical evidence. An executable built from the original revision
created an existing store; the candidate reopened it without changing canonical
records and retained those records after policy replacement. Focused policy,
boundary, lifecycle, interruption, reader-snapshot and access tests passed, as
did formatting, module, vet, full CGo-free, full race and required Python client
acceptance checks.

These results support an opt-in way to retrieve useful small source passages.
They do not establish general relevance superiority, complete answers under tiny
budgets, or a default-policy change. Full-span context loss, repeated context,
the Unicode ranking miss and higher runtime costs remain measured limitations.

## Normalized passage-location metadata

A matched CLI check on 2026-09-16 compared `33ee5a4` with the additive location
output in `dc47c9e`. Both binaries queried copies of the same store created by
the older binary. A single synthetic item contained 300 repetitions of
`Amber café 東京.` separated by CRLF blank lines and prefixed with a UTF-8 BOM.
The normalized representation was 6,300 bytes. The query was `amber`, with a
16,384-byte evidence budget, under each segmentation policy.

Each policy used one warmup pair followed by 12 measured pairs, alternating
binary order. Each invocation was a fresh process; timings include startup,
query, tracing, JSON output and close, but not compilation. Filesystem caches
were warm and were not dropped. The shared Linux worker had six virtual CPUs,
an AMD Ryzen 7 2700X host, an 8-GiB job memory limit and Go 1.27.1.

| Policy | Selected passages | Median before / after | Serialized bytes before / after |
|---|---:|---:|---:|
| fixed-v1 | 2 | 67.98 / 65.38 ms | 8,331 / 8,888 |
| passage-v1 | 7 | 75.88 / 79.06 ms | 9,966 / 11,937 |

Selected segment IDs, text, ranks and content hashes matched in every pair.
The new coordinates and representation digests matched the normalized source,
including when reading representations stored before the extension. Both arms
released 6,300 evidence-text bytes. Metadata added 557 bytes for two fixed
segments and 1,971 bytes for seven passages; this overhead is outside the text
budget. These small shared-host latency differences are descriptive, not
speedup targets or evidence of improved retrieval relevance.

## Exact-content packing investigation: deferred

A focused CLI fixture reproduced duplicate displacement under both segmentation
policies. Two distinct items contained `amber amber` (11 bytes each); a third
contained `amber repair code ZX17` (22 bytes). For query `amber` and a 33-byte
budget, the two duplicate texts ranked first and consumed 22 bytes, leaving
insufficient space for the distinct repair code. Their representation and segment
identities differed despite identical text digests.

A diagnostic walk of the authorized 100-byte response retained the first ranked
copy and the repair item within 33 bytes. This was a hypothetical selection over
released bytes, not an implemented packing policy. Separate-source trail access
was rejected; denied and withdrawn queries returned no evidence; the peer source
remained independently retrievable.

The frozen passage fixtures also contain repeated selected bytes. Analysis of
the saved first repetition found five repeated passages (4,860 bytes) for the
storage-class question and six (5,832 bytes) for the orchard question, both at
8,192 bytes under passage-v1. The frozen documentation responses contained no
exact duplicate selected text. These observations do not change the original
results or establish a general retrieval-quality improvement.

Implementation is deferred because a truthful explanation needs a versioned
canonical Source Trail change. The current v1 record has selection flags and
lifecycle reasons, but no packing-policy identity, duplicate omission reason or
retained-segment relationship. Treating a duplicate omission as a budget omission
would be incorrect; removing candidates before tracing would lose provenance.

The proposed contract keeps authorization and lifecycle filtering first, retains
the first fitting exact byte string in verified rank order, and records each
later duplicate's own segment identity plus its retained-segment relationship.
An unselected oversized candidate does not reserve content. Hashes may narrow
comparisons, but bytes must match. Different source identities are never merged.
The existing packet identity can continue to describe the selected segments,
ranks and budget; the new trail identity must also bind packing policy and
omission explanations. Supporting both historical v1 records and that versioned
contract is a prerequisite. No deduplication flag or changed default ships here.

## Scoped verification-statement reuse: inconclusive, not adopted

A bounded experiment on 2026-09-15 compared the supported CLI at `b98e15c`
with a prototype that prepared six canonical-read SQL statements per opening
verification pass. It reused statements for sources, observations, artifacts,
representations, representation inputs, and segments across repeated reads.
The pinned modernc SQLite driver retains a compiled single-statement handle
and resets it when its rows close. Each pass owned and closed its statements,
including partial initialization. The prototype retained both opening passes,
all record and projection checks, writable FTS integrity checking, and existing
connection and snapshot semantics. It did not cache evidence or validity.

The [measurement report](https://github.com/graydeon/mousa-benchmarks/blob/0d5b3c64194bc8dd2876687ad5ded3d85c625ad9/results/2026-09-16/prepared-statements.json) retains
source and binary hashes, fixture hashes, individual samples, semantic query
results, acceptance criteria, and lifecycle timings. The primary fixture was a
closed 96-document store after three content generations and the existing
update/delete/restore/query schedule. The second fixture used an equivalent
newly generated store with six additional complete content generations. Each
arm queried an identical copy within each fixture; the fixtures had different
source identities.

There were 12 alternating baseline/prototype pairs per fixture, after one
untimed pair. Each invocation was a fresh process with full opening, query,
output, and exit costs. Builds and fixture copying were outside the timer.
Filesystem caches were not dropped: these are process-cold, not disk-cold,
measurements. Both binaries used Go 1.27.1, `CGO_ENABLED=0`, `GOAMD64=v1`,
`GOMAXPROCS=6`, and `-trimpath -buildvcs=false`, in one shared six-vCPU EPYC-IBPB
VM on a Ryzen 7 2700X host, with 5.5-core and 8-GiB job limits.

| Fixture | Arm | Median ms | Range ms | p90 ms | Median / maximum RSS KiB |
|---|---|---:|---:|---:|---:|
| Three generations | Baseline | 798.9 | 756.0–862.2 | 823.5 | 23,650 / 25,780 |
| Three generations | Prototype | 692.5 | 556.5–782.4 | 723.9 | 21,474 / 22,108 |
| Nine generations | Baseline | 1,729.1 | 1,645.6–1,881.3 | 1,827.9 | 24,382 / 26,164 |
| Nine generations | Prototype | 1,452.9 | 1,341.2–1,496.2 | 1,494.8 | 22,292 / 24,520 |

Primary median latency improved 13.3%; its two six-pair halves improved 12.6%
and 17.0%. The history fixture improved 16.0%. The prototype was faster in all
24 timed pairs. No latency sample was a Tukey 1.5-IQR outlier; RSS outliers are
retained in the report. All 52 query responses, including warmups, agreed on
semantic invariants within their fixture. Fresh request, decision, trail,
packet, and timestamp fields were not required to be identical.

Before measurement, adoption required a 10% primary median improvement,
repeatability across both halves and at least 10 of 12 primary pairs, a positive
history-fixture improvement, and correctness. Median and maximum RSS could
increase by no more than the greater of 10% or 2,048 KiB. Query p90, maximum
latency, and lifecycle-stage medians could not regress by more than 10%.
No extra repetitions were allowed to resolve a mixed result.

The fixed-query criteria passed, but three lifecycle-stage medians exceeded
the declared tolerance across three alternating evolving-example pairs:

| Stage | Baseline median ms | Prototype median ms | Increase |
|---|---:|---:|---:|
| Update | 56.5 | 73.4 | 29.9% |
| Inspect denied trail | 57.0 | 63.4 | 11.1% |
| Allow after withdrawal | 49.5 | 58.9 | 19.0% |

These small samples do not isolate the cause of the increases. They prevent
adoption under the declared rule despite the repeatable fixed-query improvement.
The result is inconclusive overall; the experimental implementation was removed.
No additional timing runs or broader optimization followed.

All 260 workflow commands passed their assertions, including 194 Mousa commands
and 66 minimal-SQLite comparison commands. Candidate verification also passed
the focused store, CLI, model, and evaluation suites; formatting, module
tidiness and verification, vet, full CGo-free tests, and race tests. Store tests
cover tampering, failed opens, cancellation, interrupted recovery, read-only
operation, and concurrent retries/snapshots. Focused prototype tests exercised
fresh reads after reuse and initialization failure. Those tests were removed
with the prototype. This is a synthetic self-comparison, not evidence of
competitive superiority or a deployed performance improvement.

### Lifecycle diagnosis: no implementation adjustment

A separately bounded investigation on 2026-09-15 recovered the exact prototype
and baseline rather than extending the original acceptance run. The original
failed gate and non-adoption decision above remain unchanged. The
[diagnostic report](https://github.com/graydeon/mousa-benchmarks/blob/0d5b3c64194bc8dd2876687ad5ded3d85c625ad9/results/2026-09-16/prepared-diagnosis.json) preserves
the protocol, hashes, individual responses, timings, memory, and instrumentation.

The hypothesis was that eager preparation and cleanup imposed fixed costs on
small lifecycle stores. All three disputed commands call `Open`, including
denied inspection and an already-active allow policy. The earlier experiment
alternated complete workflows, with independently generated request identities
and timestamps. It did not compare adjacent commands from identical snapshots.

The new protocol captured six closed snapshots during one baseline execution
of the existing evolving example. Every invocation restored the same database
path from its stage's byte-identical snapshot, with unchanged arguments and input.
There were 24 adjacent alternating baseline/prototype pairs per stage after one
warmup pair; stage order rotated between repetitions. The three controls were
an initial query, allowed-trail inspection, and an unaffected-source query.
Processes were fresh; filesystem caches were not dropped. Toolchain, binary
flags, and worker resource limits matched the earlier experiment.

| Stage | Baseline / prototype median ms | Difference ms | Change | Prototype faster pairs |
|---|---:|---:|---:|---:|
| Update | 69.389 / 68.590 | −0.799 | −1.2% | 11/24 |
| Inspect denied trail | 64.823 / 66.135 | +1.311 | +2.0% | 10/24 |
| Allow after withdrawal | 54.946 / 59.970 | +5.024 | +9.1% | 11/24 |
| Initial query | 54.405 / 46.832 | −7.573 | −13.9% | 16/24 |
| Inspect allowed trail | 56.702 / 55.815 | −0.887 | −1.6% | 14/24 |
| Query unaffected source | 83.028 / 75.274 | −7.754 | −9.3% | 17/24 |

The medians of paired differences for the disputed stages were +0.247, +2.645,
and +0.944 ms, respectively; these are distinct from differences of medians.
Baseline/prototype p90 values were 72.620/74.484, 84.751/78.795, and
68.103/69.126 ms. Their maxima were 75.068/74.963, 104.911/84.008, and
69.377/77.451 ms. Median peak RSS fell from 17,268/19,368/19,814 KiB to
15,116/17,628/17,760 KiB; maximum RSS also fell for each disputed stage.
The report includes ranges, inclusive quartiles, all paired differences,
nearest-rank p90, and retained outliers for every stage. In particular, one
prototype unaffected-source query took 1,295.834 ms, versus a baseline maximum
of 97.746 ms. That tail event is not discarded or explained by the median gains.

Separate instrumented runs executed each stage three times. Every invocation
ran two verification scopes, and every scope used all six statements. The
disputed stages made 33, 42, and 42 scoped statement calls per pass. Preparation
plus cleanup across both passes had median costs of 0.162, 0.222, and 0.255 ms,
respectively, far below the original 6–17 ms increases. An empty-store
initialization prepared six unused statements, costing 0.171 ms including
cleanup; this was not one of the regressing lifecycle states.

The first job stopped after its first warmup pair because the diagnostic
comparison incorrectly included the output's `latency_micros` field. The single
permitted harness repair excluded that timing field, retained both samples,
and resumed using the saved binaries and snapshots. No completed invocation
was repeated. All 300 uninstrumented responses then passed paired semantic
comparison; the 21-command setup passed its existing assertions, and all
19 instrumented commands succeeded. Raw failure evidence remains in the report.
Both uninstrumented binary hashes and runtime source manifests match the
previously tested versions; their earlier correctness evidence is reused.
No changed runtime implementation required a new full test suite.

No disputed-stage median exceeded the new diagnostic protocol's 10% limit,
and the measured costs do not support lazy preparation as a fix for these
paths. No implementation adjustment was made, and no adjusted-candidate
acceptance run was triggered. The original 10% cold-query improvement target
and 10% lifecycle tolerance were not relaxed. The original regressions' cause
remains unresolved: ordering, generated state, and shared-host variability were
potential confounders, not measured causal explanations. The large tail event
also remains unexplained. This optimization investigation is closed without
adoption or a deployed performance improvement.

## Evolving evidence and native lexical CLI comparison

`eval/local/workflow.py` is an executable synthetic example and report generator.
Its JSONL scenario checks source isolation, update, omission without deletion,
explicit deletion, identical restore, byte omission, trail inspection, policy
denial, withdrawal, and an unaffected peer source. Directory preview must select
the expected files without creating a store.

The comparison uses the same generated Markdown corpus for Mousa, QMD's native
lexical CLI, and a minimal Python/SQLite FTS5 implementation. Each repetition starts
fresh stores; engine order rotates. It measures initial sync, no-op syncs, three
content generations, current and stale queries, deletion, and restoration. Queries
use 128-, 256-, and 512-byte released-text budgets. Every returned item must have
the current fixture text, belong to the query's relevant set, and fit the budget.
The raw report retains command exits, responses, timings, RSS, selected items,
fixture coverage, and post-exit storage size.

This is a whole-route comparison, not an equal-feature or equal-ranking benchmark:

- Mousa uses verified source-scoped retrieval, fresh policy evaluation, native
  byte packing, and durable trails. Its store retains canonical history.
- QMD 2.8.3 uses native lexical search, including prefix matching, its tokenizer,
  and field-weighted BM25. The fixture uses single ASCII terms without stemming
  or prefix collisions. Scores are not compared across engines.
- Minimal SQLite uses body-only FTS5 and one transaction per sync. It has no
  canonical history, authorization, trails, or directory-safety controls.
- QMD and SQLite return candidate bodies before the evaluator applies greedy
  byte packing. Their native output is **not** byte-limited. Native command time,
  JSON decoding, and external packing time are recorded separately.

All topic matches are relevant by construction. Recall under the byte budget
measures coverage of this small fixture, not semantic relevance or answer quality.
No embedding, reranking, generation, model download, or model-backed QMD route is
run. There is no equivalent implemented Mousa route to compare with those modes.

Comparison timing includes startup, store opening, output, and exit; builds and
fixture generation are excluded. JSONL example rows also include input-file
preparation and are not throughput measurements. Filesystem caches are not dropped. GNU `time`
RSS is a maximum, not summed simultaneous process-tree memory; this matters for
QMD's Node launcher and child. Post-exit storage includes each route's retained
data, not an equal-retention index-size comparison.

`eval/local/warm.go` separately measures preparation, warm verified retrieval,
new historical tracing with pre-evaluated requests, and fresh current queries.
Each stage copies the same closed single-source CLI store after restoration.
Copying, store opening, request construction, warmup, and historical pre-evaluation
are excluded. Traces use new requests rather than retries. Candidate counts and
packed bytes must agree with the cold CLI reference. The stages are not additive;
subtracting their timings does not isolate a component's cost. Runtime allocation
deltas are process-wide counters, not RSS.

### Measured results

The [24-document report](https://github.com/graydeon/mousa-benchmarks/blob/0d5b3c64194bc8dd2876687ad5ded3d85c625ad9/results/2026-09-16/workflow-24.json) and
[96-document report](https://github.com/graydeon/mousa-benchmarks/blob/0d5b3c64194bc8dd2876687ad5ded3d85c625ad9/results/2026-09-16/workflow-96.json) each passed with 326
commands, including 225 comparison-query checks. Each size has three repetitions.
The reports bind all measured Go/evaluation sources, the Mousa binary, and the QMD
dependency lockfile to SHA-256 hashes. QMD reported `2.8.3 (facd35e)` and Node
reported `v24.21.0`. No model-backed route was run.

Both sizes used the shared Ryzen 7 2700X host, an EPYC-IBPB Linux VM with six vCPUs
and 12 GiB RAM, job limits of 5.5 cores and 8 GiB, and shared NVMe storage.
Mousa used Go 1.27.1 with `CGO_ENABLED=0 -trimpath -buildvcs=false`. Three repetitions
on a shared host do not support precise speedup estimates; the raw samples and
minimum/maximum command times are retained.

Median cold-command milliseconds:

| Documents | Route | Initial sync | No-op at generation three | Final `cedar` query, 128 B |
|---:|---|---:|---:|---:|
| 24 | Mousa | 102.5 | 235.3 | 244.1 |
| 24 | QMD | 250.6 | 213.9 | 209.8 |
| 24 | Minimal SQLite | 75.4 | 77.7 | 52.6 |
| 96 | Mousa | 354.8 | 790.4 | 773.5 |
| 96 | QMD | 303.9 | 248.7 | 229.9 |
| 96 | Minimal SQLite | 81.3 | 71.5 | 59.2 |

Mousa was slower on the larger cold workflow, despite returning the same ordered
selected items in all 75 aligned query groups at each size. All engines excluded
stale and deleted fixture text. Mean initial topic coverage at 128/256/512 bytes
was 0.2083/0.4167/0.6667 for 24 documents and 0.0521/0.1042/0.1667 for 96 documents,
identical across the three routes. This deliberately simple fixture establishes
currentness and byte-budget behavior, not a general quality advantage.

At 96 documents, maximum observed cold-command RSS was 25,780 KiB for Mousa,
80,624 KiB for QMD, and 22,472 KiB for minimal SQLite. Median post-exit retained
storage was 1,695,744, 340,313, and 110,592 bytes respectively. These numbers
include different histories and guarantees; they are not equal-feature efficiency
ratios.

Warm-stage medians below pool 30 calls from each of three independent fixture
copies per stage. The query is the same final `cedar` query and 128-byte budget
as the cold reference above.

| Documents | Preparation | Verified retrieval | New historical trace | Fresh current query |
|---:|---:|---:|---:|---:|
| 24 | 0.000376 ms | 3.157 ms | 7.336 ms | 8.725 ms |
| 96 | 0.000361 ms | 11.296 ms | 23.997 ms | 26.306 ms |

At 96 documents, mean allocated bytes per measured call were 24, 1,333,438,
1,643,613, and 1,738,762 respectively. Cold CLI cost remains substantially larger
than these already-open paths. The result supports investigating startup and
historical verification costs; it does not justify weakening verification or
treating the stage timings as an additive profile.

### Cost relative to the preceding Mousa CLI

A separate [five-revision report](https://github.com/graydeon/mousa-benchmarks/blob/0d5b3c64194bc8dd2876687ad5ded3d85c625ad9/results/2026-09-16/current-cli.json) compares
the complete increment with `cf2d9db1dbac73e90e5e3adf3bb2302b0f065713`.
It reuses the lifecycle protocol below: 24/96 documents, five revisions, three
repetitions, alternating arm order, and identical single-term queries. Both arms
passed all 132 correctness checks. The candidate also records canonical query
trails and uses the new directory path; this is not an isolated tracing ablation.

| Documents | Operation | Preceding CLI median ms | Current CLI median ms |
|---:|---|---:|---:|
| 24 | Initial sync | 98.0 | 97.1 |
| 24 | No-op at revision five | 284.7 | 324.1 |
| 24 | Current query at revision five | 274.3 | 301.6 |
| 96 | Initial sync | 341.2 | 362.4 |
| 96 | No-op at revision five | 938.9 | 1,022.1 |
| 96 | Update to revision five | 1,141.0 | 1,292.6 |
| 96 | Current query at revision five | 1,054.5 | 1,135.7 |

The new contracts have measurable cost on this workload. Startup still verifies
retained history, so overall cold operation is not history-independent. The
record preserves the regressions rather than treating additional functionality
as a free optimization. Reproduce with `eval/local/lifecycle.py`, building its
baseline from the commit above and its candidate from the report's source
manifest. Do not combine these samples with the three-engine fixture: its text,
history depth, and query schedule differ.

### Reproduce the workflow

Use Linux x86-64, Python 3.9 or newer, the module's supported Go toolchain, GNU
`time`, and Node 24.21.0 for the pinned QMD comparison. QMD is optional evaluation
software, not a Mousa runtime dependency. Its MIT-licensed published package is
2.8.3, release source `facd35e01359e59d938bc9418e93fb9318addee3`.
The nested lockfile pins the downloaded dependency graph, including the Linux
SQLite vector binary needed by QMD's store initialization. Installation below
disables lifecycle scripts and omits optional model binaries; no model is needed.

```sh
CGO_ENABLED=0 go build -trimpath -buildvcs=false -o ../mousa-workflow ./cmd/mousa
CGO_ENABLED=0 go build -trimpath -buildvcs=false -o ../mousa-warm ./eval/local
git clone https://github.com/graydeon/mousa-benchmarks.git ../mousa-benchmarks
git -C ../mousa-benchmarks checkout 0d5b3c64194bc8dd2876687ad5ded3d85c625ad9
npm ci --prefix ../mousa-benchmarks/comparisons/qmd --ignore-scripts --omit=optional --no-audit --no-fund
python3 eval/local/workflow.py --mousa ../mousa-workflow --warm ../mousa-warm --qmd ../mousa-benchmarks/comparisons/qmd/node_modules/@tobilu/qmd/bin/qmd --dependency-lock ../mousa-benchmarks/comparisons/qmd/package-lock.json --documents 24 --repeats 3 --output ../workflow-24.json
```

Repeat with `--documents 96` for the larger fixture. Use `--node` to select an
explicit Node executable and `--environment` to attach a JSON hardware record.
Without QMD or the warm probe, the report labels that route NOT RUN. A command
timeout terminates its process group; any failed check produces a nonzero exit
and a partial FAIL report. Source and binary hashes identify the measured inputs.
Ephemeral paths are redacted, while canonical identifiers remain unchanged.

## Local CLI current-item lifecycle

The [machine-readable report](https://github.com/graydeon/mousa-benchmarks/blob/0d5b3c64194bc8dd2876687ad5ded3d85c625ad9/results/2026-09-16/current-items.json) compares the
baseline built from `fd2f2d5493fed88ed932b0b8b031d732d31e7089` with
explicit current-item activation. Candidate source-file hashes, both binary hashes,
build flags, environment, all command samples, correctness results, and summary
statistics are included. This is a synthetic lifecycle experiment, not a retrieval
quality benchmark or a general performance claim.

Protocol: 24 and 96 generated UTF-8 documents, five revisions, three repetitions.
Each arm gets a fresh store and the same absolute source-root identity. Arm order
alternates by repetition. The CLI imports, repeats no-op syncs, updates every item,
queries current and stale terms, deletes an item, restores identical content, and
queries the restored item. The evaluator checks exact returned item sets, revision
text, sync action counts, and released-text byte accounting. Single-term queries
avoid differences between repeat-preserving and deduplicated query policies.

Measurement includes process startup, store opening and verification, command work,
JSON output, and process exit. Builds and evaluator-side JSON validation are outside
the timer. Every command starts a new process; this is **cold CLI**, not cold disk:
filesystem caches are not dropped. GNU `time` reports maximum process RSS. Store
file size is sampled after process exit. The pilot bounded expansion to these two
sizes; commands have a 15-second timeout. No larger corpus or history claim follows.

Environment: shared Ryzen 7 2700X host; Linux VM exposing EPYC-IBPB, six vCPUs,
12 GiB RAM, job limits of 5.5 cores and 8 GiB, shared NVMe storage. Go 1.27.1,
module Go version 1.25.0, `CGO_ENABLED=0`, `-trimpath -buildvcs=false`. Host
contention and three repetitions limit timing precision. Worker wall-clock skew is
irrelevant to elapsed times, which use a monotonic clock.

Median elapsed milliseconds:

| Documents | Operation | Baseline | Current-item activation |
|---:|---|---:|---:|
| 24 | Initial sync | 128.1 | 99.1 |
| 24 | No-op after initial sync | 126.1 | 120.3 |
| 24 | No-op at revision five | 470.6 | 301.9 |
| 24 | Update to revision five | 479.4 | 338.7 |
| 96 | Initial sync | 598.1 | 311.1 |
| 96 | No-op after initial sync | 832.0 | 309.1 |
| 96 | No-op at revision five | 4,211.0 | 980.1 |
| 96 | Update to revision five | 3,605.4 | 1,165.0 |

All 132 candidate command samples passed their correctness checks. The baseline
failed 60 of 132 samples: repeated no-op classification, stale evidence exclusion,
deletion, restoration classification, or restored retrieval. Restoration is checked
both by action reporting and by actual query results; its new action name alone
does not establish a retrieval fix. These timings therefore compare an incorrect
baseline with the corrected workflow, not equivalent correct implementations.
Maximum command duration was 4.273 seconds baseline and 1.189 seconds candidate;
maximum observed process RSS was 26,444 KiB and 25,764 KiB respectively.

Removing per-item history scans lowers the measured sync cost, but startup still
verifies historical records. At 96 documents, candidate no-op latency grows from
309.1 ms after the initial import to 980.1 ms at revision five. Overall cold sync
is not history-independent. The extra current-item projection and conservative
legacy-store replay requirement are correctness tradeoffs, not free optimizations.

### Reproduction

Use a disposable checkout of the commit containing this report and verify the
candidate source hashes recorded in the JSON. Later source changes are a new
comparison, not an exact reproduction. Python 3.9 or newer is required; install GNU
`time` for RSS measurement. Without it, the script labels RSS as not measured.

```sh
git worktree add --detach ../mousa-lifecycle-baseline fd2f2d5493fed88ed932b0b8b031d732d31e7089
(cd ../mousa-lifecycle-baseline && CGO_ENABLED=0 go build -trimpath -buildvcs=false -o ../mousa-baseline ./cmd/mousa)
CGO_ENABLED=0 go build -trimpath -buildvcs=false -o ../mousa-candidate ./cmd/mousa
python3 eval/local/lifecycle.py --baseline ../mousa-baseline --candidate ../mousa-candidate --output ../lifecycle.json
```

The script generates the corpus, prints progress, writes raw rows and summaries,
and exits nonzero if any candidate correctness check fails. Baseline failures are
retained rather than stopping the comparison. The saved report also includes
environment and source/build fingerprints. Absolute timings and fresh request IDs
are not expected to repeat byte-for-byte.

JSONL validation, crash recovery, CRLF/BOM handling, unusual item names, and source
isolation have separate behavioral tests. This directory timing experiment does
not measure JSONL throughput, warm-core latency, durable CLI tracing, model-token
efficiency, or another search system.

## Hypotheses under test

- H1 (identity of ranking): the verified, policy-enforced, and traced retrieval paths
  return the same ranking as raw lexical search; policy gating costs latency, not quality.
- H2 (ingest scaling): per-document indexing should be linear in corpus size. The initial
  implementation was quadratic; the fix moved corpus-wide integrity checking to startup
  verification while preserving per-row verification and fail-closed behavior.
- H3 (retrieval quality): FTS5 BM25 with unicode61 tokenization over title+text is the
  quality baseline for the implemented lexical path. Measured on BEIR development subsets;
  comparisons to published BM25 numbers are context only, not replication.
- H4 (long-query cost): BM25 scoring cost grows with the number of OR terms in the query
  expression. Measured to size the query-construction decision (term capping) honestly.

## Measurement environments

| id | hardware | notes |
|---|---|---|
| sandbox | Intel Xeon L5640 (x86-64, **no AVX/AVX2**), 8 threads, Debian 6.12 | primary floor; GOAMD64=v1 |
| worker | AMD Ryzen 7 2700X host, VM exposes EPYC-IBPB host-model, 6 vCPU quota, shared desktop host | cross-checks and determinism only; not a dedicated benchmark host |

Software: Go 1.27.1 toolchain, go.mod `go 1.25.0`, `modernc.org/sqlite v1.57.0`,
`CGO_ENABLED=0`. Datasets: BEIR SciFact (SHA-256 `536e1444…e0165`), NFCorpus
(`efe5be03…671b0b`), ArguAna (`cfdf79ad…f7557b`), fetched from the BEIR public host;
corpora are not redistributed by this repository. Licenses checked at fetch time; results
are published as measurements, not dataset redistribution.

## Method

`cmd/beir` (harness) ingests each corpus document through the canonical engine (one
Source per dataset, one Observation/Artifact/Representation per document, canonical text
segments, FTS5 index), then runs each judged query through a selected retrieval mode:
verified (`SearchVerifiedLexical`), enforced (`SearchEnforcedLexical`, one stored allow
decision per query), or traced (`TraceEnforcedLexical`). Document ranking is best-segment
Metrics (corrected 2026-09-15): `ndcg_at_10` follows the reference BEIR protocol —
graded linear gains, `gain/log2(rank+1)` discounting, ideal from positive judgments
only, and BEIR's `ignore_identical_ids` default (retrieved documents whose ID equals the
query ID are removed first) — pinned to BEIR
`af4a85e7f601a697039c88ab83c1dc88dc975b3f` and trec_eval `m_ndcg_cut.c`
`dc0c991c80bae2087de774ed76278e11d9d9f4c6`. `recall_at_100` divides by positive
judgments only. The earlier binary-gain nDCG survives as the explicitly named custom
metric `binary_ndcg_at_10`, and MRR@10 is project-defined (not part of BEIR's
`evaluate()`). Differential fixtures cover graded vs zero judgments, empty results,
duplicate IDs, missing results, and both settings of the self-ID rule
(`eval/beir/metrics_reference_test.go`). Reports written before 2026-09-15 are binary
and protocol-legacy; saved rankings can be re-scored with `beir -report rescore`.

Development vs evaluation data: the inspected test splits (SciFact, NFCorpus, ArguAna)
have informed repeated development decisions (successive query-policy evaluations chose query policies after
inspecting test-split outcomes); they are **development evidence**, not untouched
held-out evaluation, and cannot serve as confirmatory evidence for a future adoption
decision. The ±0.001 figures below are an engineering tolerance, not an established
statistical noise band. If tuning is introduced later, SciFact's dev split becomes
tuning data and new data must be reserved before the next confirmatory evaluation.

## Results — sandbox environment

Full test-split runs, one Source per dataset, limit 100:

| dataset | docs | queries | nDCG@10 | Recall@100 | MRR@10 | p50 latency | index |
|---|---|---|---|---|---|---|---|
| SciFact | 5183 | 300 | 0.6681 | 0.8859 | 0.6345 | 205 ms | 30.5 MB |
| NFCorpus | 3633 | 323 | 0.3063 | 0.2334 | 0.5124 | 144 ms | 22.0 MB |
| ArguAna | 8674 | 1401 | 0.3534 | 0.9615 | 0.2319 | 2982 ms | 43.4 MB |

Mode ablation (same stores, same query order): enforced == verified byte-identically on
SciFact and NFCorpus (see `ablations.md`). Policy gating changes latency, not ranking (H1
supported on these subsets).

Determinism: SciFact metrics reproduced identically on the worker and on rerun; NFCorpus
rerun rankings identical (latency percentiles differ, as expected). ArguAna was indexed
once and searched via the reuse path; no second full-run replicate yet.

### Ingest scaling (H2)

Before the fix (sandbox): throughput collapsed 74 → 18.7 docs/s from 50 → 400 synthetic
docs (doubling ratios 2.93 → 3.10 → 3.52, trending to 4). Profile: 89.6% of CPU inside
`IndexTextRepresentation` — 56.2% full `verifyLexicalRecords` per document, 23.3% the FTS5
structural `integrity-check` per document. The structural verification check had been removed;
it now runs once per writable open.

After the fix (same probe): 172 → 185 docs/s flat across 50/100/200/400. Full-corpus
indexing: SciFact 798 s (6.5 docs/s), ArguAna 1263 s (6.9 docs/s) — dominated by
per-document transaction commits with `synchronous=FULL` and per-row verification, not by
corpus-wide work. Integrity guarantees preserved: per-row `verifyLexicalRow` on write,
per-candidate verification on read, corpus-wide content verification at every open, the
FTS5 structural `integrity-check` once per writable open (it is the only layer that
catches same-length docsize drift, which `quick_check` and content verification both
miss and which silently skews BM25 scores; cost ~78 ms at 2000 docs, once per open),
tamper tests unchanged and passing (`TestSearchLexicalIntegrityAndReadOnlyParity`,
`TestLexicalFTSStructuralTamperDetectedOnOpenStore`, migration verification, all 100+
store tests, `-race` clean).

### Long-query cost (H4)

Fixed 12-query ArguAna diagnostic against the existing index: query latency correlates
with expression size (10.9 s @ 4090-byte expression vs 0.75 s @ 791 bytes). Profile:
66.6% of CPU in the single SQL ranking query, 60.6% inside FTS5's BM25
(`_fts5ApiInstCount`/`_fts5CacheInstArray` instance counting over OR terms); candidate
verification is 0.6%. This motivates the next engineering decision: bounded expression
construction (deduplicate terms, rank or cap terms) measured for quality impact before
adoption.

### Pack byte-budget curve (retrieval-mode evaluation, reproduced by the harness in packet-coverage evaluation)

SciFact test split, 300 queries, limit 100, traced mode on a reused store, per-run
request namespaces (`run-<digest>` recorded in every report). Tracing is ranking-neutral
at every budget: nDCG@10 0.6681, Recall@100 0.8859, MRR@10 0.6345 for all nine budgets
below — ranked document lists are byte-identical across budgets and to the verified
baseline. The budget only changes what the context packet carries.

Gold-document coverage within budget: the fraction of each query's gold documents that
the engine's own packed selection carries, |relevant ∩ selected| / |relevant|, macro-averaged
over judged queries; a document counts as covered when at least one of its candidates was
selected. Coverage is computed by the harness from the Source Trail's ordered candidate
rows (`-report coverage` joins per-budget reports into the curve); the earlier
retrieval-mode evaluation table was computed by an standalone Python simulation over document
granularity and is superseded by these harness measurements. Agreement is within 0.06
absolute at every budget (largest gap at 1 KiB, where the simulation over-estimated
small budgets by packing whole documents that the engine's per-candidate greedy pass
skips):

| budget | gold coverage | selected docs | used bytes | utilisation |
|---|---|---|---|---|
| 512 B | 0.007 | 0.0 | 130 | 25.4% |
| 1 KiB | 0.061 | 1.0 | 830 | 81.1% |
| 2 KiB | 0.410 | 1.0 | 1826 | 89.2% |
| 8 KiB | 0.735 | 5.0 | 7976 | 97.4% |
| 16 KiB | 0.784 | 10.0 | 16160 | 98.6% |
| 32 KiB | 0.830 | 21.0 | 32528 | 99.3% |
| 64 KiB | 0.869 | 42.0 | 65293 | 99.6% |
| 128 KiB | 0.879 | 84.0 | 130614 | 99.7% |
| 256 KiB | 0.886 (= Recall@100) | 99.0 | 156836 | 59.8% |

Readings: coverage saturates at Recall@100 exactly at 256 KiB (the harness and the
ceiling agree to four decimals); half the achievable coverage needs ~4 KiB; the
64–128 KiB region reaches 84–88% of documents for 87–88% coverage. This gives 11D's
pack stage its first measured cost/benefit anchor (H3), with the dev-subset caveats above.

### Expression term deduplication (retrieval-mode evaluation, H4 verdict)

ArguAna queries duplicate 38.5% of their terms (median 177 terms/query; duplicates are
almost exclusively stopwords). BM25 sums per matched term, so duplication multiplies a
term's contribution and dominates FTS5's instance-counting cost. A `dedup` expression
policy was prototyped in the harness (`BuildExpressionWithPolicy`) and measured on full
test splits, identical hardware and stores, limit 100:

| dataset | nDCG@10 | Recall@100 | MRR@10 | p50 latency |
|---|---|---|---|---|
| SciFact | 0.6681 → 0.6683 (+0.0002) | 0.8859 → 0.8859 | 0.6345 → 0.6348 | 205 ms → 205 ms |
| NFCorpus | 0.3063 → 0.3069 (+0.0006) | 0.2334 → 0.2344 | 0.5124 → 0.5128 | 144 ms → 148 ms |
| ArguAna | **0.3534 → 0.3233 (−0.0301)** | 0.9615 → 0.9336 (−0.0279) | 0.2319 → 0.2091 (−0.0228) | **2982 ms → 723 ms (−76%)** |
| ArguAna p99 | 12.3 s → 1.5 s (−87.5%) | | | |

Deduplication is a retrieval-policy change, not a semantics-preserving optimization:
on 50 real ArguAna queries the deduplicated top-10 differed for every query. The
measured verdict: a large latency win on long queries, but a material ArguAna quality
regression (nDCG@10 −0.0301, far beyond the ±0.001 engineering tolerance observed on SciFact/NFCorpus),
while short-query datasets are unaffected in both dimensions. Decision rule applied
(quality non-regression beyond the engineering tolerance AND material latency win) **rejects dedup as the
default production policy**; the negative result is recorded and the original protocol
remains the published baseline. Both policies stay available in the harness
(`-policy original|dedup`) for future policy experiments; no engine change was made.

### Semantics-preserving expression reduction (zero-posting evaluation, H4 closed)

The one remaining expression-level candidate that could be latency-neutral in ranking was
dropping phrases with zero postings: a phrase that matches no document contributes nothing
to BM25 and nothing to the OR candidate set under FTS5. Measured on the first 50 ArguAna
test queries against the retrieval-mode evaluation store, median of two post-warmup repeats, same hardware.

Ranking identity and both arms of the latency comparison come from one probe run
(`probes/arguana-prune-match-oracle.json`), so they share warm-up state:

| variant (one probe run) | p50 latency | ranking identity |
|---|---|---|
| engine shape (`bm25` + segment_id tiebreaker, LIMIT 100) | 1.832 s | reference |
| zero-posting phrases removed (1–4 per query, 16/50 queries affected) | 1.818 s | **byte-identical top-100 on all 50 queries** |

An independent probe series (`runs/arguana-diagnosis.json`) measured the same shape at
1.841 s and the pruned expression at 1.717 s (−6.7%), plus two shape variants that were not
adopted: `ORDER BY rank` without the tiebreaker 2.065 s and LIMIT 1000 1.928 s. Both series
put the win well below the ≥10% bar, and the LIMIT result shows cost scales with matched
rows rather than with the limit, so no top-N early termination is active for this query
shape.

Zero-posting phrases are rare once document frequency is checked against the index instead
of against vocabulary strings: 25 removable phrase instances out of 7,860 (0.3%), spread
over 16 queries. The cost is the high-document-frequency stopword phrases both policies
must keep, and the per-query effect is not systematic: over the 16 affected queries the
delta ranges from −8.7% to +20.9%, with 6 of 16 slower or equal after pruning.

Decision rule applied (byte-identical rankings AND ≥10% p50 win on ArguAna) **rejects
zero-posting elimination**: no engine or harness change. This closes the
semantics-preserving expression space at H4 — duplicate folding changes rankings (implementation), zero-posting dropping does not pay (zero-posting evaluation) — leaving only ranking-semantics levers
(stopword dropping, df cutoffs, weighting) or deferred engine-level rewrites (e.g.
`detail=`).

Two measurement lessons are recorded for the next harness change: raw term strings are an
unsound document-frequency oracle under `unicode61 remove_diacritics 2` — the vocabulary
check read 2.2% of phrase instances as absent and reported spurious ranking differences for
40 of 50 queries, while a MATCH-existence check found 0.3% absent and no ranking difference
— and LIMIT does not bound scan cost for this query shape.

### Floor-weight expression reduction (floor-weight evaluation, H4 lever measured)

The remaining expression lever from zero-posting evaluation was the class of query terms whose postings
cover at least half the indexed rows. On this corpus FTS5 assigns such terms an inverse
document frequency clamped to a small floor (~1e-6 before the length/tf factor, so at most
2.2e-6 per matched row), meaning they contribute essentially no evidence while owning most
of the scanned postings: on the 50-query ArguAna slice, 2222 of 7860 term instances
(28.2%) are in this class yet the class dominates the 407k-postings scan. The policy drops
exactly those terms (`-policy drop-floor`, `eval/beir/reduce.go`): a cached read-only probe
measures per distinct term how many rows contain it (MATCH-existence, the sound oracle
from zero-posting evaluation), the term is dropped when `df * 2 >= indexed rows`, and the classification
is guarded at run start by `VerifyFloorPremise`, which probes terms on both sides of the
boundary and refuses the run if the boundary does not separate evidence — so a future
SQLite build without the clamp degrades to keeping terms, not to silently changing
rankings. Probe cost is instrumentation, reported separately: 26.2 s of probes for the
full 1401-query run, outside every measured latency.

Full ArguAna test split, 1401 queries, verified mode, limit 100, identical store and
hardware, both arms on the same machine (sandbox, saved evaluation results):

| arm | p50 | p90 | p99 | nDCG@10 | Recall@100 | MRR@10 |
|---|---|---|---|---|---|---|
| original (published protocol) | 2.895 s | 7.937 s | 12.481 s | 0.3534 | 0.9615 | 0.2319 |
| drop-floor | 0.506 s | 0.825 s | 1.117 s | 0.3534 | 0.9615 | 0.2319 |

p50 −82.5%, p99 −91.0%. Aggregate metrics are identical because the rankings are: the
ordered top-100 document lists are byte-identical on 1398 of 1401 queries. The 3
exceptions swap two adjacent non-relevant documents in the tail (ranks 41–49; the single
relevant document sits at the same rank in both arms for all three, so no metric moves).
The per-document score perturbation is bounded by the removed terms' floor contribution
(≤ 2.2e-6 per matched instance) against score gaps orders of magnitude larger; the three
swaps are adjacent-equal pairs whose gap is within that bound. Per-query latency ratio:
min 0.050, max 0.682, mean 0.189.

Short-query controls with the same configuration (drop-floor vs published baseline): SciFact
300 queries nDCG@10 0.6681 == baseline, Recall@100 0.8859 == baseline; NFCorpus 323
queries nDCG@10 0.3063 == baseline, Recall@100 0.2333 vs 0.2334 (−0.0001, one document
moved across the Recall@100 boundary by the floor perturbation; within the ±0.001 noise
band). NFCorpus had 25 of 323 queries where every term was at the floor (e.g. two-word
queries like "airport scanners"); those queries fall back to the baseline expression and
are counted in the report's `fallback_queries`.

**Correction (2026-09-15): the pre-registered decision rule FAILED.**
The rule required every ordered ranking to match; 1398 of 1401 matched, so the strict
identity criterion was not met and the policy was **not adopted under the registered
rule**. The paragraph above preserves the original record; the correct reading is that
drop-floor is a **promising experimental approximate policy** (`-policy drop-floor`
remains available in the harness), not an optimization adopted under the rule. The
published baseline protocol stays `-policy original`, no engine change was made, and any
future quality-preserving acceptance rule must be specified before new confirmatory
evaluation. The metric protocol was also corrected: the `ndcg@10` figures above
were the harness's binary-gain form. Under the corrected reference protocol (graded
gains, positive-only denominators; see Evaluation protocol) the saved drop-floor
rankings re-score to NFCorpus nDCG@10 0.306698 (binary form: 0.306322); SciFact and
ArguAna are unchanged within floating-point rounding.

Limitations: one corpus family (BEIR subsets), verified mode only, harness-side policy.
The three tail swaps show the reduction is bounded-score-changing rather than exactly
score-preserving; they are recorded, not rounded away. Premise guard evidence and
per-query accounting are in every drop-floor report (`reduction` block). Reported query
latency in this evaluation measured the store call only (search-only); the harness now also
measures the full request path including expression probing and reduction.

## Failed approaches and incomplete runs (recorded honestly)

- Ingest O(N²): first SciFact run killed at 30m12s (~2.8k/5.2k docs). Superseded by the
  fix above; partial store preserved as evidence.
- First ArguAna reuse run killed at 22m44s with no progress instrumentation; store
  preserved. Led to progress reporting, per-query timeouts, and the reuse path.
- Reuse-path identity bug: opening a store under a different dataset name derived a
  different Source ID and unmapped segments. Fixed by validating that the store's stored
  Source matches the derived identity and failing closed (`reuse validation failed`).
  Worker-verified: reuse vs fresh rankings byte-identical on the small fixture; mismatched
  name rejected nonzero.
- Traced-mode ablation: was blocked by request-identity conflicts when reusing a store
  already exercised by an enforced run. Resolved in retrieval-mode evaluation with per-run request
  namespaces (`RunConfig.Namespace`): enforced-then-traced on one store completes, retries
  keep identity, and distinct configurations never collide (fixture-verified, plus the
  four full SciFact traced runs above).
- Pack byte-budget curves: measured in retrieval-mode evaluation (curve above).

## Limitations

- All numbers are development-subset protocol results with the term-cap protocol noted
  above; not full BEIR and not comparable as system benchmarking.
- One Source per corpus makes enforcement scope a no-op by construction; scope effects
  under many Sources are untested by these runs.
- Latency includes per-query ancestry verification by design; the profile separates it
  (small) from BM25 scoring (dominant on long queries).
- Worker timings are from a shared desktop host; used only for determinism cross-checks.
- No competing-system comparison yet: this evaluation measures Mousa's own implemented
  capability and regressions, when the required capabilities are available.

## Reproduction

```
eval/beir/fetch.sh <data-dir>
go build -o /tmp/beir ./cmd/beir
/tmp/beir -data <data-dir>/scifact -dataset scifact -mode verified -out out.json
/tmp/beir -data <data-dir>/scifact -dataset scifact -mode traced -budget 2048 -reuse -out out-t-2048.json
# pack byte-budget curve: run the traced command above per budget, then
/tmp/beir -report coverage -reports out-t-512.json,out-t-1024.json,... -out curve.json
/tmp/beir -data <data-dir>/scifact -dataset scifact -mode enforced -reuse -out out-e.json
go test ./eval/beir/ ./internal/...   # scoring self-test + engine gates
```

Per-query reports contain ranked document identifiers, metrics, and timing measurements.
Run the commands above to generate reports for the selected dataset. Historical raw
reports are retained separately and are not distributed in this repository.
