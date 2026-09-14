# Paper outline — Mousa (working)

Grounded strictly in work that exists. Sections without evidence are marked **pending**
and must not be filled with claims until the cited measurement exists.

1. **Introduction** — evidence engines for agent memory: inspectable selection, durable
   provenance, bounded context. Positioning as engineering contribution, not a
   benchmark-competitiveness claim. *Grounded: engine exists, README/RESEARCH record.*
2. **Design** — nine-stage pipeline; canonical records; deterministic identities;
   immutable policy decisions; Source Trails; byte-budget packets. *Grounded: phases
   11A–11D, contracts, tests.*
3. **Integrity model** — fail-closed verification: per-row verification on write,
   corpus-wide verification at startup, canonical re-encode checks. *Grounded: store test
   suite incl. tamper classes; migration verification.*
4. **Evaluation methodology** — protocol, metrics, environments (AVX-less floor; worker),
   subset labeling discipline, dev/held-out policy. *Grounded: RESEARCH.md method section.*
5. **Retrieval quality results (BEIR development subsets)** — SciFact/NFCorpus/ArguAna
   measured values. *Grounded: phase-13 runs.* Competitive comparison vs published BM25 /
   hybrid baselines: **pending** (requires protocol-identical runs of competitors).
6. **Performance results** — ingest scaling before/after the O(N²) fix (with profile
   attribution); latency distributions; index size; integrity cost decomposition
   (verification ~0.6% vs BM25 ~60% on long queries). *Grounded: profiling evidence.*
7. **Mode ablations** — verified vs enforced vs traced. Verified==enforced completed;
   traced and pack-budget curves **pending** (blocked on per-run request namespaces).
8. **Limitations and threats to validity** — subset protocol, single-corpus scope no-op,
   expression term-cap protocol, shared-host worker timings, no tuning performed.
   *Grounded: RESEARCH.md limitations.*
9. **Roadmap** — decision freshness, token-aware budgeting, hybrid ranking, then
   FreshStack when documentation retrieval is ready; LongMemEval/MemoryAgentBench and
   TREC RAG behind explicit capability milestones. *Grounded: NEXT.md.*

## Claim map

| claim | benchmark | metric | baseline | capability needed |
|---|---|---|---|---|
| lexical retrieval quality | BEIR subsets | nDCG@10/Recall@100/MRR@10 | published BM25 + in-protocol competitor | implemented |
| integrity costs latency not quality | mode ablation | ranking identity + latency | verified path | implemented |
| ingest scales linearly | scaling probe | docs/s vs corpus size | pre-fix baseline | implemented (fixed) |
| pack budget ↔ selection quality | traced curves over budgets | nDCG@10 vs budget | full-budget pack | traced mode runnable per-run |
| long-query handling | expression-size sweep | latency vs terms | uncapped expression | bounded expression construction |
| memory across sessions | LongMemEval / MemoryAgentBench | session metrics | adapter baseline | session memory features (**pending capability**) |
| documentation/code retrieval | FreshStack | official protocol metrics | official baseline | documentation retrieval features (**pending capability**) |
| end-to-end grounded answering | TREC RAG | answer quality | constant answering config | answering pipeline (**pending capability**) |
