---
name: mousa-development
description: Implement or review Mousa changes against its canonical identity, authorization, SQLite, retrieval and public evidence contracts.
---

# Develop Mousa

Read the current task, repository documentation and operator-supplied workflow before work. Follow the authorized scope and execution environment. This skill supplies product-specific guidance; it does not authorize publication, model calls, source access or infrastructure changes.

## Find the affected contract

Read [CAPABILITIES](../../docs/CAPABILITIES.md) for supported interfaces and evidence limits, and the relevant implementation and callers before editing. For supersession, read [SUPERSESSION_ENFORCEMENT](../../docs/SUPERSESSION_ENFORCEMENT.md): native directory/JSONL and MCP queries support explicit opt-in, including lexical associations as v12/v13 and proposed vector/hybrid associations as v14/v15 for byte/token mode. Defaults remain uncurated; MCP administration remains unsupported.

Trace changed symbols and record callers and compatibility impact using available local code intelligence. Keep policy authorization, lifecycle rejection, supersession selection and packing dispositions distinct. A source-scoped decision must not silently broaden to another source or caller.

## Preserve canonical behavior

Keep earlier schema versions, public signatures, canonical encodings and identity goldens unchanged unless the task explicitly authorizes a compatibility change. A hash binds fields; it does not prove stored existence, ancestry, authorization, byte equality or historical timing. Compare canonical bytes where exact-content packing requires equality; a digest is only a comparison shortlist.

Reuse the supplied SQLite transaction for candidate, administrative and provenance reads. Calling a public method that opens another transaction can mix snapshots. Distinguish verified no history from damaged recorded state: a missing event named by an existing projection is corruption; an unknown identity requested directly from a historical reader can be not-found. Fail without manufacturing, repairing or committing partial records.

Historical trail reads verify recorded immutable evidence and current authorization without reinterpreting selection under today's activation projection. Preserve canonical candidate sizes and ranks when withholding; suppressed candidates consume no packing budget and cannot become retained duplicates. Keep unavailable historical text verification limits explicit.

For declared context, native directory/JSONL and configured MCP queries accept byte
or measured token budgets. MCP decodes the complete inline
association_declarations.v1 document from its original nested bytes. Vector/hybrid
support uses the same post-primary composition stages, not a second retrieval.
Resolve only primary passages surviving both budgets in the same writer snapshot;
append associated text using the one query counter, then confirm and Finish once.
Byte-only stages retain v3, or v12 with supersession, and the 256-target-passage consideration bound. Token
composition uses v11, or v13 with supersession, and packet v3, with at most100 total primary/associated rows
and256KiB admitted text before duplicate/budget omissions. Suppressed predecessors spend no admitted text budget. Unfired declarations
retain ordinary history. Keep associated IDs/counts separately aligned from
compacted accepted-primary lexical facts. Verify both stages against recorded
ancestry, not today's targets; normal history needs no tokenizer.
Caller author/basis strings are untrusted assertions, not authenticated identities.
The runnable examples/mcp_associations.py checks selection/provenance, not
semantic sufficiency. Fresh history authorization may append audit evidence.

For classification administration, use the
[native draft contract](../../docs/USAGE.md#native-classification-administration).
`DecodeClassificationInput` requires explicit facts and derives the existing
classification.v1 identity; preserve typed subject parsing and ordered basis.
Put/get use existing store validation and transactional parents, including
cross-source basis. Keep parser tests separate from store rollback/damage evidence
and native-client observations. Labels are attribution, not access authority or
automatic classification; revision pins do not relabel replacement evidence.

For vector work, read [caller-supplied exact vector retrieval](../../docs/CAPABILITIES.md#caller-supplied-exact-vector-retrieval).
Native `vector inputs`, `vector import` and `query --vector-space` form one supported
workflow; embedding inference stays external and never holds a canonical transaction.
The source/segment/space slot is immutable; vector identities bind supplied values
and canonical revision pins. Fresh authorization/current-pointer selection and v5
trace persistence share one writer snapshot. Historical verification reads recorded
producing embeddings rather than current vectors. Keep cosine separate from BM25,
report coverage and reject explicit vector query-term policy. Byte-only explicit
supersession uses v9 with the existing producing facts and consultation; source
admission precedes masking and ranks/coverage stay unchanged. MCP
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
v7 and packet v3, v8 for lexical supersession or v10 for vector/hybrid supersession.
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
For combined token/supersession queries, consult through the same writer
transaction after authorization. Pack only survivors; verify every withheld
candidate's original canonical size/content and ancestry. Lexical v8 and
vector/hybrid v10 bind token facts and recorded consultation using packet v3's
full-selection wrapper, not released text alone. Project token ranking through
suppression-aware v4 or v9 respectively. Historical reads rederive membership
from recorded events/declarations, not current activation. Vector/hybrid full-source
admission still precedes masking; survivor accounting cannot relax it.
Byte-only lexical association/supersession uses v12 only for recorded supporting
stages; non-firing calls retain v4. Reuse one consultation and suppress exact
target predecessors before retained-byte/dedup accounting, verifying withheld
content and ancestry too. Recorded history must rederive both stages from immutable
pins, not today's projection. Token triples use v13 only for recorded association
facts; no firing facts retains v8. Apply target supersession before admission,
deduplication and either budget, including oversized and duplicate targets.
Byte-only vector/hybrid uses v9 and packet v1's released-selection binding.
Vector/hybrid association facts use closed v14 byte or v15 token profiles with
exactly one retrieval component and optional supersession. No-fire paths retain
v5/v6/v7/v9/v10. Admit the entire current source (100 items, 100 passages, 256 KiB)
before suppression, including unembedded rows. Resolve support in that same writer
snapshot from the admitted universe; an embedding is not required for support.
Keep support separate from primary evidence, rank arrays and coverage counts.
Preserve v1–v13 identities. The retrieval example independently counts complete
returned contexts; synthetic vectors establish protocol/arithmetic only.
Omitted/false token supersession retains v7 unless producing association facts
require lexical v11 or vector/hybrid v15.


## Prove and deliver the change

Use focused regression checks that fail for the intended behavior before the fix and pass afterward. Missing symbols are compile-stage evidence, not behavioral proof. Use real temporary SQLite stores for storage and transaction claims, explicit ordering for snapshot tests, and recomputed identities for corruption tests when a stale ID would obscure the invariant. Retain independent golden expectations and negative observations.

Apply installed simplification, Go compatibility, documentation and quality-review guidance when relevant. Inspect supported Go versions and CI before choosing APIs. Run the exact checks required by the current publication workflow on the final tree; preserve logs and distinguish local tests, published-head CI and post-merge CI.

Update public capability and usage docs with the actual supported behavior. Use Conventional Commit subjects and PR titles. Submit a reviewable PR and stop when the operator's workflow assigns merge to an independent reviewer. Report final tested/public tree identities, evidence and unresolved findings; silence or a prior-head review is not final-head approval.

At each handoff assess [mousa-benchmarks](https://github.com/graydeon/mousa-benchmarks) separately. New native interfaces, retrieval behavior or empirical claims need independently reproducible public observations at the matching milestone. Internal contract tests do not establish native or semantic acceptance. Preserve historical archive pins; do not relabel administration-only observations as enforcement results. Publish companion evidence before linking product claims to it. Design-only changes need accurate status, not fabricated measurements.
