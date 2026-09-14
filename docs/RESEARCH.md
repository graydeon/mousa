# Mousa research record

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
per document. Metrics: binary-gain nDCG@10, Recall@100, MRR@10, computed in
`eval/beir` with hand-computed unit tests (`go test ./eval/beir/`). Query expressions
tokenize to quoted OR terms, capped at the store's 4096-byte expression bound by dropping
trailing terms; the cap is part of the protocol, not engine behavior.

Development vs evaluation data: no parameter tuning was performed against any test split
during the initial baseline measurement; the tokenizer, k1/b, and limit are engine defaults. If tuning is
introduced later, SciFact's dev split becomes tuning data and test splits stay held out.

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
regression (nDCG@10 −0.0301, beyond the ±0.001 noise band observed on SciFact/NFCorpus),
while short-query datasets are unaffected in both dimensions. Decision rule applied
(quality non-regression beyond noise AND material latency win) **rejects dedup as the
default production policy**; the negative result is recorded and the original protocol
remains the published baseline. Both policies stay available in the harness
(`-policy original|dedup`) for future policy experiments; no engine change was made.

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
