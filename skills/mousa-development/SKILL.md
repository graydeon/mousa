---
name: mousa-development
description: Implement or review Mousa changes against its canonical identity, authorization, SQLite, retrieval and public evidence contracts.
---

# Develop Mousa

Read the current task, repository documentation and operator-supplied workflow before work. Follow the authorized scope and execution environment. This skill supplies product-specific guidance; it does not authorize publication, model calls, source access or infrastructure changes.

## Find the affected contract

Read [CAPABILITIES](../../docs/CAPABILITIES.md) for supported interfaces and evidence limits, and the relevant implementation and callers before editing.

Trace changed symbols and record callers and compatibility impact using available local code intelligence. Keep policy authorization, lifecycle rejection and packing dispositions distinct. A source-scoped decision must not silently broaden to another source or caller.

## Preserve canonical behavior

Keep earlier schema versions, public signatures, canonical encodings and identity goldens unchanged unless the task explicitly authorizes a compatibility change. A hash binds fields; it does not prove stored existence, ancestry, authorization, byte equality or historical timing. Compare canonical bytes where exact-content packing requires equality; a digest is only a comparison shortlist.

Reuse the supplied SQLite transaction for candidate, administrative and provenance reads. Calling a public method that opens another transaction can mix snapshots. Distinguish verified no history from damaged recorded state: a missing event named by an existing projection is corruption; an unknown identity requested directly from a historical reader can be not-found. Fail without manufacturing, repairing or committing partial records.

Historical trail reads verify recorded immutable evidence and current authorization. Keep unavailable historical text verification limits explicit.

For current-source manifests, follow the [closed identity recipe](../../docs/USAGE.md#canonical-identity-recipe).
Keep content inspection and fresh authorization in the same writer transaction.
Use transaction-local verified readers with canonical-blob length admission before
allocation; reconstruct through the existing segmenter and verify complete current
index membership. Historical canonical rows are not current index rows.
Manifest limits are separate from vector retrieval limits and conjunctive.
Check actual MCP serialization, including escaping, duplicated content and request
ID framing. Denial omits the entire manifest and match result. Unknown-source
denial is fresh but unpersisted because no source foreign-key target exists.
Treat an expected-ID match as snapshot evidence, never a lease on later work.

For classification administration, use the
[native draft contract](../../docs/USAGE.md#native-classification-administration).
`DecodeClassificationInput` requires explicit facts and derives the existing
classification.v1 identity; preserve typed subject parsing and ordered basis.
Native put/get preserve cross-source basis and readonly get. For the
[MCP interface](../../docs/USAGE.md#mcp-classification-administration), prove all
subject and basis ancestry roots belong to the selected source in the owning
transaction. Scope projections conservatively before canonical release; forged
local projections must not authorize foreign payloads. Denied inspection authorizes
before lookup, omits payload and hides existence/corruption. Foreign, mixed or
unprovable scope is generic not_found. Fresh authorization, scope, verified readback
and receipt share one writer snapshot. Explicit classification administration grants
no ingestion or read rights; HTTP writes retain principal/scope gates.
Use canonical historical ancestry, not current index/vector coverage. Preserve
confidence 0, exact retries and refusal without repair. Keep parser, actual-store,
HTTP and stdio evidence distinct. Labels are attribution, not factual truth,
calibrated confidence, access authority or automatic classification; revision pins
do not relabel replacement evidence.

For vector work, read [caller-supplied exact vector retrieval](../../docs/CAPABILITIES.md#caller-supplied-exact-vector-retrieval).
Native `vector inputs`, `vector import` and `query --vector-space` form one supported
workflow; embedding inference stays external and never holds a canonical transaction.
The source/segment/space slot is immutable; vector identities bind supplied values
and canonical revision pins. Fresh authorization/current-pointer selection and v5
trace persistence share one writer snapshot. Historical verification reads recorded
producing embeddings rather than current vectors. Keep cosine separate from BM25,
report coverage and reject explicit vector query-term policy.
MCP
`mousa_vector_query` reuses that native transaction for configured JSONL sources.

For hybrid changes, read [native hybrid retrieval](../../docs/CAPABILITIES.md#native-hybrid-lexical-and-vector-retrieval).
`query --hybrid-space` combines prepared lexical text and an explicitly supplied
query vector in one immediate snapshot. v6 records separate BM25/cosine branch
ranks, canonical pins and equal-weight RRF(k60); both packers consume fused order.
Preserve v1–v5 bytes and vector-only behavior. Verify immutable producing facts
in history without rerunning lexical search or asserting past coverage completeness.
MCP `mousa_hybrid_query` reuses this transaction. Its retrieval-details history
opt-in projects the verified producing view without reranking. Preserve default
lexical query and trail property sets, canonical hex identities and closed schemas;
OpenAI mention/resource search stays lexical and byte-only. MCP exports current
authorized passage inputs through `mousa_vector_inputs` and imports exact pinned
external values through `mousa_vector_import`. Reuse `ExportEmbeddingInputs`,
`DecodeVectorImport` and atomic `ImportEmbeddings`; preserve their native bounds
and the smaller transport frame bound. Export needs retrieval authorization, not
ingestion permission. Import needs the alias's `--ingest-source` and runtime HTTP
`mousa:write` scope, not a new native retrieval-policy gate. Withdrawal/current
pins still reject in native import. Exact retries count accepted submissions,
not new rows; conflicts/stale/foreign pins cannot partially insert a space or batch.
No external snapshot lease exists. Keep tokenizer paths out of tool arguments.
v7 retrieval details use verified safe vector/hybrid fields.

For token packing changes, read [native token-aware packing](../../docs/CAPABILITIES.md#native-token-aware-packing)
and its runtime preparation/limits in the usage guide. Native token mode writes
v7 and packet v3.
Count the complete proposed versioned rendering through the bounded official offline process, after fresh
authorization and within the same writer snapshot. Reap/verify the worker before
canonical insertion. Skipped passages neither spend released budget nor seed
duplicates. Keep byte equality separate from the retrieval identity union.
Normal canonical reads need no Python: measured counts are recorded producer
evidence, while retained rendering digests are rederived. Explicit authorized
recount needs every original selected byte; retired text remains unarchived.
Run optional-feature checks with a pinned prepared backend and report skips
separately. MCP context_tokens uses these same native transactions with trusted
startup-only tokenizer-python/cache; token_details independently releases recorded
safe facts without recounting. Omitted token fields preserve byte defaults and
Python independence; invalid budgets reject before mutation.
Native query/history expose `TokenSelectionView`: accepted candidate identities
align compact proposed-count and lexical arrays, with accepted-order lexical ranks.
Keep rejected slots and original search ordinals out of release views. Accepted
budget/duplicate omissions still have legitimate metadata. Internal canonical
`TokenSelection` retains all producing facts; never mutate or rehash it to filter
an inspection. Fresh inspection may append authorization evidence.

## Prove and deliver the change

Use focused regression checks that fail for the intended behavior before the fix and pass afterward. Missing symbols are compile-stage evidence, not behavioral proof. Use real temporary SQLite stores for storage and transaction claims, explicit ordering for snapshot tests, and recomputed identities for corruption tests when a stale ID would obscure the invariant. Retain independent golden expectations and negative observations.

Apply installed simplification, Go compatibility, documentation and quality-review guidance when relevant. Inspect supported Go versions and CI before choosing APIs. Run the same checks CI runs on the final tree before committing, and distinguish local results from CI results.

Update public capability and usage docs with the actual supported behavior. Use Conventional Commit subjects. Maintainer work is committed directly to `main`, without issues or pull requests; outside contributions use pull requests. Report what was tested, the evidence and any unresolved findings.

Behaviour is proven by tests in this repository. [mousa-benchmarks](https://github.com/graydeon/mousa-benchmarks) publishes retrieval quality, quality per token budget, and ingest and query speed at scale; per-feature protocol captures are no longer produced. Preserve historical archive pins and do not fabricate measurements.
