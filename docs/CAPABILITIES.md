# Supported local workflow

For source-build versus release status and the proposed v10 composition boundary,
see [development status](OVERVIEW.md#source-and-release-status).

Mousa is pre-alpha. The supported executable is `cmd/mousa`, built from source with
Go 1.25 or newer. The core packages are internal, not a stable SDK. Its CLI and
MCP interfaces store and retrieve evidence; none generates answers or establishes
factual truth.

## Capability matrix

“Core” means an implementation exists. “CLI” means an operator can use it through
`cmd/mousa`. Tests and measurements describe different kinds of evidence: passing
behavior tests do not establish retrieval quality or performance on other workloads.
The [research record](RESEARCH.md) documents published measurements and limitations.

| Capability | Core | CLI | Behavioral coverage | Published measurements |
|---|---|---|---|---|
| UTF-8 directory import and resync | Yes | `sync <dir>` | Import, repeated no-op, update, revert, delete, restore, restart | Synthetic cold-CLI lifecycle comparison in the research record |
| Directory preview and selection controls | Yes | `sync --preview`, include/exclude globs, explicit overrides and limits | Actual-CLI scope changes, preview without store creation, failed validation preserving activation, rooted link rejection | Synthetic preview and sync observations; no performance claim from boundary smoke tests |
| JSONL item input | Yes | `sync --source <id>` | Retry, replacement, tombstone, malformed input, committed prefix, input limits | Small evolving CLI example; not a throughput benchmark |
| Raw-content identity and normalized text | Yes | Both input forms | CRLF, BOM, empty text, exact retries | Core evaluation includes normalization; no separate normalization ablation |
| Versioned passage boundaries | Yes | `sync --segment-policy passage-v1` | Exact source slices, policy replacement, retries, interrupted activation, historical identities and access exclusion | Bounded documentation and synthetic comparison in the research record |
| Atomic current-item activation | Yes | Both input forms | Transaction failure, reader snapshots, abrupt exit, retry | Included in the directory lifecycle comparison; no isolated transaction ablation |
| Legacy directory recovery | Yes | Complete directory sync | Migration preserves history and requires source replay | None |
| Source-scoped lexical retrieval | Yes | `query` | Cross-source isolation and current-only evidence | BEIR core evaluation and synthetic native-CLI comparison; no general CLI quality claim |
| Caller-supplied exact vector retrieval | Yes | Native `vector inputs`/`vector import` and `query --vector-space`; MCP `mousa_vector_inputs`/`mousa_vector_import`/`mousa_vector_query` for configured JSONL | Synthetic cosine/tie arithmetic, strict imports/arguments, current-only source pins, explicit write gates, partial coverage, restart, both packing policies, v5 history | Synthetic vectors establish protocol/ranking arithmetic only, not model or retrieval quality |
| Hybrid lexical/vector retrieval | Yes | Native `query --hybrid-space`; MCP `mousa_hybrid_query` for configured JSONL | Equal-weight RRF(k=60), current snapshot, partial/zero coverage, both packers, v6 branch history | Synthetic arithmetic/protocol only; no model-quality acceptance |
| Source lifecycle and retrieval policy decisions | Yes | Fresh query evaluation; `access` and `withdraw` | CLI deny/allow, source withdrawal, and cross-source isolation | Evolving CLI example and warm current-query costs; not an isolated policy ablation |
| Byte-budget evidence selection | Yes | `query --budget-bytes <positive>`; opt-in `--packing-policy exact-v1` | Byte boundaries, skip-oversized selection, duplicate displacement, versioned explanations and historical compatibility | Bounded exact-content packing measurements; no general retrieval-quality claim |
| Rendered-context token packing | Yes | Native `query --context-tokens`; MCP queries with `context_tokens` and operator-prepared backend | Official offline local process, whole-rendering boundary fit, both packers, Unicode/special-looking text, v7 history, authorization and rollback; native-only recount | Synthetic counting/protocol evidence only; no model-window or retrieval-quality guarantee |
| Declared associated context | Yes | Native `query --associations <file>`; inline `associations` on lexical byte-only MCP `mousa_query` | Same-source authorization, selected-primary trigger, bounded fan-out/target passages, omission reasons and v3 trail readback | [Caller-reviewed backup example](../examples/backup/README.md) and [synthetic stdio example](USAGE.md#mcp-declared-supporting-context); no general retrieval-quality claim |
| Explicit query-term policy | Yes | Default `original`; explicit `--policy dedup` | Repetition changes ranking; shared expression capping | Published BEIR original/dedup results, not a new CLI quality claim |
| Durable Source Trails and context packet IDs | Yes | Every query; `trail` inspection | Actual-CLI ID round-trip, current authorization, retired revisions; transaction rollback and rejected metadata filtering | Cold CLI and separate warm traced/current-query observations |
| Classification records | Yes | No administration command | Canonical storage and validation | None; not automatic classification or classification-based authorization |
| Revision-pinned supersession declarations | Yes | `supersession declaration put`, `supersession declaration get <id>` | Strict identity/codec, immutable SQLite storage, same-source item/revision provenance, retry, restart and integrity failures; actual-CLI input bound, malformed/duplicate/unknown/trailing/oversized input, unpinned targets, missing identity, absent store, read-only read, rejected writes leaving stored bytes unchanged | None; declarations do not filter retrieval or establish factual truth |
| Supersession activation administration | Yes | `supersession activation put`, `supersession activation get <id>`, `supersession activation state <source-id>` | Strict identity/codec, immutable SQLite events and verified current projection, initial selection, replacement, deactivation, reactivation, exact retry, stale/competing/redundant rejection, metadata replay under an existing identity, historical event reads, current-state projection with canonical identity strings, explicit null declaration and per-source isolation, corrupt history/projection rejection without repair | None; recorded activation does not move item pointers, filter retrieval or establish factual truth |
| Transaction-local supersession consultation | Yes | Used by native opt-in queries | Verified current state and selected declaration read through the retrieval transaction; no-history, transitions, explicit snapshot ordering and corrupt recorded-state rejection | No performance measurement; verification is linear in the source's activation history |
| Supersession selection contract | Yes | `query --supersession DIRECTORY TEXT` or `query --source ID --supersession TEXT` | Exact predecessor-representation matching, source isolation, rank/size preservation, rejected and non-matching candidates, survivor packing | Synthetic native acceptance is separate from domain tests; no semantic-quality claim |
| Opt-in supersession trail and survivor packing | Yes | Directory/JSONL and configured MCP lexical, vector and hybrid queries, with independent byte and optional token budgets | v4 lexical bytes, v8 lexical tokens, v9 vector/hybrid bytes and v10 vector/hybrid tokens; both packers, exact pins, original ranks/coverage and survivor packing | Independent companion evidence is separate from product tests; no semantic-quality or latency claim |
| MCP supersession opt-in | Yes | `mousa_query`, `mousa_vector_query`, `mousa_hybrid_query`, and `mousa_trail` optional `supersession` boolean | Strict arguments, omitted/false/true properties, recorded consultation, native transaction reuse and independent supersession/retrieval/token history opt-ins | Independent stdio captures retain their producing source pins; synthetic client behavior is not retrieval quality or performance |
| Stored v4 trail content and administration verification | Yes | `trail --source <id> <trail-id>` | Canonical read/startup verification, recorded history after transitions/revisions, recomputed duplicate-to-selected and duplicate-to-budget corruption, missing named parents as integrity, unknown IDs and legacy readers unchanged, live-store rollback without repair | Retired indexed text is unarchived; compare bytes only where retained |
| Embedding inference, answer generation | No | No | Not implemented | None |
| Local stdio MCP | Yes | `mcp --caller <id> --source <id>` | Real SDK client/executable round trips, persistence, configured boundaries, committed prefix, framing, cancellation and shutdown | None; acceptance is not a performance or model-driven evaluation |
| OpenAI MCP Extensions | Yes | Opt-in `mcp --openai-extensions`; `plugin` packaging | Real executable mention/resource authorization and reconnect; native Codex install, component recognition, tool discovery and resource read; actual desktop rendering not verified | None; protocol/client checks are not model-driven evaluation |
| OAuth-protected Streamable HTTP | Yes | Opt-in `mcp --http-config FILE` | Local TLS introspection fixture covers subject, audience, issuer, expiry, scopes, revocation and host/origin boundaries | None; live identity-provider/deployment interoperability not verified |
| Stable SDK, general connectors, public pairing relay | No supported interface | No | Not implemented as supported interfaces | None |

The [Git documentation consumer](../examples/docs/README.md) is a separate
Python CLI example. Its opt-in `ask --context-tokens` counts its own rendered
prompt with pinned `o200k_base`; unlike native token packing, it does not change
the byte-packed canonical packet. Message framing and reserved output remain
outside that example's cap.
The [pinned development case](https://github.com/graydeon/mousa-benchmarks/tree/4a9a477907b7f0eb7af708118e50c047ba9b2a5d/results/2026-09-29-context-512)
keeps the fragment and verified parent passage at 511 of 512 content tokens
by omitting a duplicate path from citation headers; an unrelated `git-switch`
passage is not selected. It does not establish model-window safety, answer
quality or general relevance.

## Native token-aware packing

The [preparation and runnable queries](USAGE.md#native-token-aware-context-packing)
use official `tiktoken==0.12.0`, `o200k_base`, `encode_ordinary` through one bounded
local process per token query. The helper is embedded in the pure-Go executable;
Python is optional and invoked by argv, never a shell. The immutable encoder is
loaded and its exact backend identity handshaken before source text is sent.
The pinned loader's vocabulary SHA256 check remains active; missing/corrupt cache
cannot fall back to a download. No daemon, model service or provider credentials
are involved. Ordinary queries, startup and canonical reads need no tokenizer.

Token mode preserves existing lexical order, cosine order or hybrid identity
union/RRF order. Fresh authorization, current eligibility, retrieval, token
selection, insertion and canonical readback share one immediate writer snapshot.
Only freshly authorized verified text reaches the counter. Denial/withdrawal
enumerates no candidates and releases no context or source-derived count facts.
Current revisions and vector space/content pins retain their existing restrictions.

Each addition measures the entire proposed `mousa.context_text.v1` rendering,
including provenance headers and two-LF passage separators. The exact string,
UTF-8 byte digest and measured ordinary-text count are returned together.
The independent released-text byte budget excludes headers/separators.
Oversized passages are skipped without trimming; later candidates are tried.
Original packing retains byte-equal passages when they fit; exact-v1 compares
actual bytes against selected passages only. Identity union is separate from
byte equality. Unselected passages consume neither released-text nor rendered
token allowance and never seed the retained duplicate set.

`mousa.source_trail.v7` records mode, existing vector/hybrid producing facts or
aligned lexical rank/BM25 facts, backend/rendering identities, source, cap/window/
reservation declarations, effective allowance, ordered selections, measured
proposed-rendering counts, exact omissions and final digest/count/byte accounting.
Omissions distinguish `bytes`, `tokens`, `rendered_bytes` and `duplicate`; lifecycle
rejection remains separate. Packet v3 binds the complete token selection under
its own domain; v1–v6 encodings, default responses and packet identities do not
change. No context text is archived and no parallel canonical store is introduced.
A denied v7 trail carries no candidates, proposed source counts or selected text;
canonical validation rejects candidate facts even when identities are recomputed.

Lexical token queries additionally accept explicit supersession enforcement:
native directory/JSONL `query --supersession --context-tokens N` and MCP
`mousa_query` with `supersession: true` and `context_tokens`. Both packers skip
exact pinned predecessors before byte/token counting and duplicate retention.
Withheld rows retain positive canonical sizes and accepted ranks, with null
proposed counts; all-withheld empty context reports `supersession_excluded`,
not budget omission. Successor availability never reinstates a pinned predecessor.
Token admission retains the 100-candidate and 4,096-byte per-passage limits.
The 262,144-byte aggregate text limit applies to accepted candidates remaining
after exact suppression, before budget or duplicate omissions. Withheld bytes
do not consume that capacity; their content, ancestry, sizes and ranks are
still verified on construction and canonical readback.
Vector/hybrid composition uses v10 and retains full-source admission before masking,
as specified [below](#byte-only-vectorhybrid-supersession). Association combinations reject.

The composition writes `mousa.source_trail.v8`, binding the token facts and the
closed recorded consultation. Omitted/false lexical supersession retains v7;
byte-only supersession retains v4. Valid v1–v7 bytes and identities are unchanged.
The v8 trail identity hashes its complete canonical record with ID zeroed, so its
schema is an explicit version binding. Packet v3 has no separate strict packet
decoder: its existing constructor hashes a wrapper containing the complete
producing trail with ID zeroed and request/decision/packet fields empty. The v8
variant keeps that construction, including schema and consultation, rather than
hashing only returned text. Equal returned context can therefore have different
v7/v8 packet identities. The v7 hash path is unchanged.

Canonical readback/startup verifies v8 through the existing trail scan, with no
new tables. Recorded event/declaration chains and exact suppression membership
are checked through the supplied queryer, not today's projection; withheld
candidates remain canonical-content verified. Packing and retained-rendering
verification use that recorded mask. Normal history records producing counts;
native explicit recount uses selected retained bytes only. MCP's independent
`supersession` and `token_details` inspection opt-ins release safe recorded
members after fresh authorization, never recounting.

Canonical reads/startup validate identities, projections, policy/source parents,
retrieval facts, packing accounting and the rendered digest when all selected
bytes remain retained, without Python. A recorded measured count is producer
evidence, not proof of tokenizer execution. Native `trail --verify-tokens`
reauthorizes and independently recounts retained selected bytes with a ready
backend; retired original bytes make that explicit recount unavailable.
Neither current content nor a hash can replace those missing historical bytes.
Historical BM25 and past coverage completeness retain their existing limits.

Native query and normal/recount history release a `TokenSelectionView`, not the
canonical `TokenSelection`. Its `candidate_ids`, `proposed_tokens` and (for lexical
mode) `lexical` arrays align only with historically accepted candidates, including
accepted budget/duplicate omissions. Lexical ranks are consecutive within this
accepted order; BM25 and proposed counts retain their accepted producing values.
Rejected candidates leave no identity, null slot or original ordinal in those
arrays. The deliberate history `lifecycle_excluded` aggregate remains available.
Vector/hybrid producing arrays already require accepted candidates only. Measured
rendering/count/digest and budget declarations do not change. Canonical internal
reads retain every producing fact; projection never edits the record or its
identity. Fresh inspection still records its authorization audit evidence.

Token query outcomes distinguish authorized all-lifecycle-rejected candidates
(`lifecycle_excluded`) from no candidates (`no_candidates`) and accepted
candidates omitted by a byte or token budget (`budget_omitted`). Policy denial
takes precedence as `policy_excluded`; any selected evidence yields `evidence`.
Mixed accepted/rejected lists follow the accepted selection or budget outcome,
without releasing rejected positional details.

Caps, process/work limits, reservation arithmetic, errors and unsupported explicit
association/supersession combinations are specified in the usage guide. Window
and reservation values are caller declarations, not authenticated provider/model
facts. Only the returned context is counted; unknown final prompts and JSON/tool
framing outside it are not. MCP exposes the same packing through explicit
`context_tokens` and recorded safe facts through independent `token_details`.
Tokenizer paths are trusted process-startup configuration only.

## Caller-supplied exact vector retrieval

The [native workflow and synthetic example](USAGE.md#caller-supplied-vector-workflow)
exports current normalized passage bytes, imports caller-produced embeddings and
queries one source in one explicitly named space. Lexical queries keep their
previous default, fields, packet identities and v1–v4 trail encodings. Mousa does
not run a model, call a provider, use a GPU or need another runtime/store.

`vector inputs` evaluates fresh CLI source authorization and lifecycle before
reading any passage bytes. An allow returns exact normalized `text`, `item`,
`source_id`, `segment_id`, `representation_id`, `representation_sha256`,
`content_sha256`, byte coordinates and segmentation policy. A deny or withdrawal
returns an empty passage array and the fresh decision metadata, without source
text or coverage. External producers run after this transaction closes.

`vector import` reads exactly one `mousa.vector_import.v1` JSON object from stdin;
it is not JSONL. The closed object has `schema`, `space` and `embeddings`. A space
has exactly `schema: "mousa.vector_space.v1"`, `provider`, `model`, `version`,
`dimensions` and `metric: "cosine"`. Provider/model/version are nonempty UTF-8
labels of at most 256 bytes each. They are caller declarations, not authenticated
facts or proof of which model ran. Change the version or model label when vectors
change; equal dimensions do not make declarations interchangeable.

Each embedding has exactly the five exported identity/content pins and `values`,
an array of finite float64 numbers with the declared dimension and nonzero norm.
Missing, null, unknown, case-aliased, duplicate or trailing members, duplicate
segments, invalid numbers and oversized input reject. Every pin must match an
existing current passage of the selected active source. Import is trusted local
administration, not a retrieval access grant. It changes no policy or current item.
The whole batch is atomic. Exact retries are safe; different values for an existing
source/segment/space reject as an immutable conflict.

Migration 15 adds `vector_spaces` and `embeddings` to canonical SQLite, plus
indexes for bounded current-item and passage enumeration. Space identities bind
the complete descriptor; embedding identities bind space, source, segment,
representation/content pins and supplied values. A unique source/segment/space
slot prevents replacement under a different content identity. Canonical reads and
startup scans verify bytes, identities, projections and immutable source ancestry
without repairing corruption. No old record or migration is rewritten.

`query --vector-space SPACE_ID` takes one `mousa.vector_query.v1` object on stdin,
with exactly `schema` and `values`, and takes no text argument. Explicit malformed,
unknown or dimension-mismatched spaces reject; there is no lexical fallback.
The query-vector identity binds the declared space and exact supplied values, not
an asserted natural-language meaning. Exact CPU cosine sorts descending, then by
canonical segment ID ascending. Per-operand scaling avoids norm overflow and
underflow; scores are clamped to [-1, 1]. There is no score threshold or top-k
truncation: all indexed current passages within the source bound are considered.
Zero and negative scores remain eligible for packing.

The vector response is `mousa.vector_result.v1`, not the lexical response shape.
It reports `mode: "vector"`, space/query-vector identities, fresh decision metadata,
packet/trail IDs, `eligible`, `indexed`, `missing`, packing budget/use and selected
evidence with an explicit `cosine` score. Missing current vectors are not silently
treated as lexical matches: partial coverage still searches the indexed subset;
zero coverage reports `no_indexed_passages`. An empty source reports `no_passages`.
Denial discloses no source coverage. Original and exact-v1 packing preserve the
existing whole-passage, skip-if-too-large and byte-equal retained-copy rules.

Without supersession enforcement, every vector query writes `mousa.source_trail.v5`, including denied and empty
queries. Its closed `vector` member binds the descriptor, space/query-vector
identities, query values, observed eligible/indexed counts and every ranked
embedding/source/segment/representation/content pin and cosine score. The usual
candidate rows bind rank, size, selected flags and packing omissions; v5 has no
lexical expression, associations or supersession member. Packet v1 identities
continue to bind the released selection rather than retrieval mode. Native `trail`
reauthorizes before releasing v5 metadata and rechecks scores against recorded
immutable embeddings, never today's current pointers. Resync/deletion/withdrawal
cannot make an old vector release retired text. History retains producing records,
not retired passage bytes; byte-equality verification remains limited to retained
indexed text, as for earlier trails. Authorization, current selection, packing and
trail persistence share one immediate writer transaction.

| Resource | Bound |
|---|---|
| Source per export/import/query | 100 active items, 100 current passages; larger sources reject, not truncate |
| Normalized source passage bytes | 262,144 total; item labels at most 4,096 bytes |
| Dimensions | 1–2,048 |
| Import | 1–100 embeddings; at most 8 MiB JSON |
| Query input | At most 128 KiB JSON |
| Vector released-text budget | 1–262,144 bytes; default 8,192 |
| Arithmetic work | At most 204,800 coordinate pairs per query |
| Native vector output | Below 8 MiB including escaped text, bounded labels and metadata; text budget excludes JSON overhead |

These bounds apply to selection, not the existing full-store startup integrity
scan. No ANN index, automatic embeddings or authenticated model-window guarantee is implemented.
Vector mode rejects explicit `--policy` and `--associations` flags.
Byte-only `--supersession` is described below; omission/false retains v5.
MCP `mousa_vector_query` uses the same native transaction for configured JSONL
sources. `mousa_trail` can release recorded vector facts with `retrieval_details`.
OpenAI mention/resource search stays lexical. No SDK or connector framework is introduced.

## Native hybrid lexical and vector retrieval

`query --hybrid-space SPACE_ID [--policy original|dedup] SOURCE TEXT` takes the
same strict `mousa.vector_query.v1` stdin object as vector-only mode. Use either
a directory argument or `--source ID`, not both. See the
[runnable example](USAGE.md#hybrid-lexical-and-vector-query). Text is an independent
caller input; Mousa does not verify that the supplied vector was generated from it.
Explicit hybrid mode rejects any `--vector-space` or `--associations` flag,
including empty values. Byte-only `--supersession` is described below;
omission/false retains v6. No embedding work or network call runs.

`EvaluateAndTraceHybrid` uses one immediate writer transaction for fresh policy
and lifecycle, current passage pins, both branches, fusion, packing and trail
persistence/readback. The vector source bounds above also bound hybrid selection:
100 active items/current passages, 256 KiB normalized bytes, 1–2048 dimensions,
128 KiB query JSON and 1–262144 released-text bytes. Larger sources reject rather
than truncate. Both branches share this verified current passage universe.

The lexical branch uses `PrepareLexicalTerms`: original preserves repeated terms,
dedup removes them before the established bounded quoted-OR preparation. Later
terms stop at the expression limit; an oversized first term or no preparable
terms rejects. Hybrid CLI preparation errors are usage errors (exit 2) before
opening the store; unknown spaces and dimension mismatches are store errors
(exit 1). A valid prepared expression with no matches still runs the vector branch.
Every matching current passage participates (at most 100), ranked by ascending
actual FTS5 BM25, then canonical segment ID. Every current indexed vector in the
named space participates (at most 100), ranked by descending exact cosine, then
canonical segment ID. Zero and negative cosine have no hidden threshold.
Ranks are consecutive from one within each complete branch, before packing.

The identity union cannot exceed current passages. Policy `rrf-equal-k60-v1`
adds `1/(60+lexical_rank)` and `1/(60+vector_rank)` where present, with equal
weights and zero for an absent branch. Fused order is descending `rrf`, then
ascending canonical segment ID. This is neither a probability nor BM25 plus
cosine. `lexical.bm25`, `vector.cosine` and their branch ranks retain separate
names; a vector-only hit has no lexical contribution.

The response `mousa.hybrid_result.v1` reports `mode: "hybrid"`, the producing
`hybrid` selection, decision/packet/trail IDs, missing coverage, packing and selected
source-linked evidence with fused `rank` and `rrf`. The selection records `eligible`
current passages and `indexed` current vectors. Partial coverage leaves unembedded
passages eligible lexically. A known space with zero current vectors still yields
an explicit hybrid result, never an ordinary lexical fallback. Unknown/mismatched
spaces or damaged records fail. Outcome is `evidence`, `no_passages`,
`no_candidates`, `budget_omitted` or `policy_excluded`. Denial/withdrawal records
an audit trail but returns no source-derived coverage/candidate facts (counts zero).

Both packers consume the fused order. Identity union happens before byte packing;
equal bytes under distinct segment IDs remain distinct candidates. `original`
packs each fitting passage; `exact-v1` omits copies of the true first retained
byte-equal passage, freeing budget for later candidates. Oversized/unselected
passages consume no released-text budget and never seed retained duplicates.
Packet v1 identity semantics are unchanged.

Without supersession enforcement, each hybrid query writes closed `mousa.source_trail.v6` history. It binds request
text, prepared terms/expression and term policy, fusion policy, space descriptor,
query vector/identity, observed coverage, candidate branch membership/ranks/scores,
canonical source/segment/representation/content pins, fused order and packing.
Native `trail` reauthorizes and verifies immutable producing pins/embeddings and
cosine, recorded ranks/RRF/order, content coordinates and packet accounting.
It does not search again or consult today's current pointers to reinterpret selection.
Recorded lexical BM25 depends on the producing FTS corpus and cannot be recomputed
from later index state. Past eligible completeness, lexical matching completeness,
missing-vector absence and activation timing are recorded observations, not
reconstructible proofs. Hashes bind those observations; they do not prove them.
Retired passage bytes are not archived; exact byte-duplicate verification is
limited to retained text, as in earlier versions. Startup verifies v6 without
relaxing v1–v5 readers or canonical bytes. MCP `mousa_hybrid_query` uses the same
native transaction; opted-in history releases recorded branch/fusion facts.

### Byte-only vector/hybrid supersession

Native `query --vector-space` and `query --hybrid-space` accept `--supersession`.
Configured MCP `mousa_vector_query` and `mousa_hybrid_query` accept the optional
strict boolean `supersession`. Omission/false preserves the existing canonical
version and result property set. True adds optional `supersession_excluded`,
including zero, and writes closed `mousa.source_trail.v9`.

The same writer transaction authorizes, admits the full current source, ranks,
consults verified active administration, packs survivors and persists/readbacks
the record. Denial consults no administration and reports zero source facts.
Only an accepted exact predecessor item/representation pin is withheld through
verified ancestry. Successor matching, embedding coverage, budget fit or current
availability never gates suppression; no successor is fetched as fallback.

Both byte packers retain original ranks, positive canonical sizes and producing
pins for withheld rows. Those rows release no text, spend no released budget and
cannot seed retained duplicates. Cosine, BM25, branch ranks, RRF(k60), identity
ties and eligible/indexed coverage do not change. The full 100-active-item,
100-current-passage, 256-KiB and 2,048-dimension bounds apply before masking.
Lexical v8 survivor token admission does not relax those source limits.

V9 requires exactly one existing `vector` or `hybrid` member plus the existing
`supersession` consultation. Its identity hashes the complete canonical record
with ID zeroed, including its schema. Packet v1 continues to bind the released
selection and budget, not the consultation or retrieval mode: equal selections
can share packet identity while their trails differ. Valid v1–v8 encodings and
identities are unchanged; v5/v6 cannot carry consultation and v8 stays lexical.

Existing canonical read/startup scans verify producing embeddings, pins,
coverage bounds, order, packing and recorded event/declaration membership.
History reads recorded administration under fresh authorization, never today's
activation or a rerun of today's index. BM25, past completeness and absence
cannot be reconstructed later; retired text is unarchived. An inspection may
commit fresh authorization audit evidence; historical rows are not rewritten.
MCP `mousa_trail` independently projects recorded consultation with `supersession`
and ranking facts with `retrieval_details`. Defaults retain legacy projections.

With `--context-tokens` or MCP `context_tokens`, true vector/hybrid supersession
writes closed `mousa.source_trail.v10`. It combines exactly one existing
`VectorSelection` or `HybridSelection`, `SupersessionSelection` and `TokenSelection`.
Both packers measure complete proposed survivor renderings with the official
offline backend, including provenance headers and separators. Withheld content
is still verified, but has null proposed count and never enters rendering,
released-byte accounting or retained-duplicate state. Source admission above
still precedes masking; lexical survivor accounting cannot admit a larger vector source.

Packet v3 binds the full producing selection, including schema, retrieval mode
and recorded consultation. Equal released text need not have equal packet identity.
Canonical validation projects ranking through suppression-aware v9, then verifies
recorded vectors, candidate content and administration through one queryer.
Normal history needs no Python; native explicit recount requires the original
selected bytes. MCP `supersession`, `retrieval_details` and `token_details` remain
independent history opt-ins under fresh authorization, with no recount.
Ordinary v7, lexical v8 and byte-only v9 construction and all valid v1–v9 bytes
and identities remain unchanged. Native explicit false vector/hybrid token flags,
previously rejected, now behave like omission and retain v7. Association
combinations remain unsupported.
See the [runnable synthetic example](USAGE.md#vector-and-hybrid-supersession).


## Supersession core boundary

The internal Go API stores source-local declarations pinned to exact predecessor
and successor item revisions. Separate immutable activation events select at most
one declaration per source, with an explicit expected predecessor and a verified
current-state projection. Deactivation retains its event in history. Actor, time
and reason are recorded labels, not authenticated authorship.

Administration does not move item pointers or filter default queries. Native directory
queries enable enforcement with `query --supersession DIRECTORY TEXT`; JSONL queries
use `query --source ID --supersession TEXT`. The fresh store method
`EvaluateAndTraceSupersessionLexical` evaluates authorization and lifecycle first,
retrieves verified source-scoped candidates, consults administration through the
same writer transaction and records v4 before canonical readback and commit.
Denied requests consult no administration; corrupt recorded state fails allowed
requests without committing their decision or trail.

Both `original` and `exact-v1` packing operate on survivors. Only the accepted
candidate's own exact predecessor representation matches; item labels, equal bytes,
ancestor pins and successor availability do not. The local stdio MCP query tool
implements the same explicit opt-in as an optional `supersession` boolean, and its trail
tool returns the consultation a `mousa.source_trail.v4` record recorded when the same
boolean is set ([MCP supersession opt-in](MCP_SUPERSESSION.md)), while MCP
administration and association-plus-supersession combinations are unsupported.
The [enforcement contract](SUPERSESSION_ENFORCEMENT.md) defines the exact counts,
outcomes, identities and failure boundaries; defaults and packet construction are unchanged.
Historical
revision pins remain readable after item updates or deletion; canonical existence
does not prove a revision was ever activated.

### Selection contract

The internal Go API also carries the pure selection half of that contract, in
`internal/mousa/supersession_selection.go`. `SupersessionSelection` is the
consultation member: a boolean, a nullable activation and declaration identity, and a
non-nullable disposition array. `SupersessionDisposition` is one typed row naming the
withheld candidate's segment identity and content digest, the applied declaration and
that declaration's exact predecessor and successor item and representation pins.
`BuildSupersessionSelection` derives the member from the decision source and outcome,
already verified lexical candidates and explicitly supplied verified activation and
declaration records; `Validate` and `ValidateAgainst` re-check a member against those
supplied inputs.

A non-allow outcome is unconsulted: no activation identity, no declaration identity and
no row, which asserts nothing about whether activation history exists. An allow outcome
is consulted, where a nil activation state means the caller verified no history, a state
naming no declaration is a deactivation, and an accepted candidate is withheld only when
its own segment representation equals the selected declaration's predecessor
representation and its verified ancestry names the decision source. Item labels, text
equality, segment identity, a later revision, an ancestor representation pin, another
source's candidates and lifecycle-rejected candidates never match, and rows follow the
original accepted `final_rank` order.

This API is structural and local: it reads no store, packs nothing, blanks no text,
releases no packet and changes no candidate rank, disposition or canonical size, and it
proves consistency with the supplied records only. Stored existence, pinned ancestry,
authorization, successor currency and current state remain transaction-bound store
responsibilities, so a hash never stands in for integrity or permission. The opted-in
store path supplies those verified inputs and records the selection in v4; default
and opted-out MCP query paths do not invoke it.

### Opt-in trail record and survivor packing

The second domain half lives in `internal/mousa/trail.go` and
`internal/mousa/packing.go`. `SourceTrailSchemaV4` is `mousa.source_trail.v4`, written
only by the separate constructor `NewSourceTrailWithSupersession`; `NewSourceTrail` and
every earlier public signature are unchanged, and v1, v2 and v3 bytes and identities are
unchanged. A v4 record requires an explicit `original` or `exact-v1` packing policy,
must carry exactly one closed `supersession` member and carries no associated passages
or association omissions. Earlier versions reject the member even when it is null.

The member is built by `BuildSupersessionSelection` from the request's source, the
decision's outcome, the verified candidates and the caller's verified activation state
and declaration, so the exact-pin match exists once rather than being reimplemented. An
allowed decision records `consulted: true`, where a nil activation state is verified
no-history, a state naming no declaration is a deactivation, and a selected declaration
contributes one row per withheld candidate; a denial records `consulted: false` with
null identities, an empty disposition array and no candidates. The consultation flag must
agree with the outcome. Both identities are explicitly present as null or values, and the
disposition array is required even when empty.

A suppression row is explicit membership: it must name exactly one accepted candidate of
that trail with a matching segment identity and content digest, a positive canonical
size, no lifecycle reasons and no packing omission or duplicate reference, and rows must
be unique and ordered by the withheld candidate's original `final_rank`. Before the
policy is applied, every candidate is checked against the invariants both policies share:
a segment is considered once, accepted candidates carry consecutive ranks from one with
no lifecycle reason, and a rejected candidate carries lifecycle reasons instead of a
packing omission, so `original` and `exact-v1` reject the same candidate set. Both packing
policies are then reproduced over the surviving candidates only, in original order and
with original ranks, and the result is mapped back onto the complete candidate list. A
withheld candidate stays present, accepted and unselected, keeps its canonical size, and
consumes no budget; a surviving candidate may still be an ordinary budget skip under
`original` without any omission field, and under `exact-v1` a withheld copy can never be
the retained duplicate. The v4 identity has its own version branch that binds the
consultation, both nullable identities and every row with length-delimited empty fields;
the packet identity keeps its existing construction and still binds only released bytes,
so an unchanged released selection keeps its packet identity and a v4 record never
shares the default trail's version or identity.

The domain checks are structural and local: they read no store, so they prove no stored
existence, canonical ancestry, authorization or transaction-local state. The opted-in
store path supplies verified inputs inside its writer transaction, writes v4,
verifies canonical readback and clears withheld text only in copied returned candidates.
Every legacy entry point retains its behavior.

### Stored v4 trail verification

The canonical store read path (`internal/sqlite/trail_content.go`, `internal/sqlite/trail.go`)
content-verifies a stored `mousa.source_trail.v4` record exactly where it already verifies v2 and v3,
on every read through `GetSourceTrail`, trail inspection and the enforced trace read-back, and in the
startup record scan. The verification has three parts, all read through the transaction snapshot the
caller already holds:

- Content. Every named segment still resolves to its canonical record, still carries the recorded
  digest and the recorded byte size in its coordinates, still derives from the decision source, and
  its retained indexed text still hashes to that digest. Retired indexed text is not archived, so the
  immutable digest and coordinate range remain checkable there and the recorded bytes are not.
- Packing. The recorded policy is reproduced over the surviving candidates only; a candidate that the
  record names as withheld remains content-verified but is neither selected nor allowed to seed the
  retained byte-equal set, so a withheld copy is never mistaken for a duplicate omission. Digests stay
  a comparison shortlist: exact-`v1` equality is decided by the retained bytes actually available.
- Administration and membership. The recorded activation event and declaration are read from the same
  snapshot, not from today's activation projection: the event must verify as canonical, belong to the
  decision source, carry the whole predecessor chain behind it and select exactly the recorded
  declaration (or a null one). The recorded rows are then re-derived from the verified declaration's
  exact predecessor pin through the domain `ValidateAgainst` rule, so a missing, extra, wrong-pin or
  unrelated-candidate row is an integrity failure. A denial and a verified no-history record keep
  their structural identity and empty-member checks and read no administration.

Later activation changes, deactivation and item revisions therefore do not reinterpret a stored
record: a replacement activation, a deactivation and a successor item revision leave an existing v4
trail readable and identical. A record that fails any check is reported as an integrity error and is
never repaired, rewritten or partially accepted, and the startup scan refuses to open the store. No
new object set, schema version or migration is involved.

A missing named dependency is part of that classification. A v4 record is written only after the
store verified the records it names, so a named segment or representation the store no longer holds
is existing corruption: the read reports an integrity error for it rather than `not_found`. An
identity that was never stored, including an unknown trail identity, still reports `not_found`, and
the v1–v3 readers keep the codes they already reported for the same damage. Every other storage error
keeps its own code; a v4 read never relabels a busy, unreadable or malformed store as corruption.

Source-scoped inspection first establishes ownership through the typed decision parent.
If that parent is missing in an already-open inspector, own-source and foreign-source
orphans remain indistinguishable from unknown IDs: all return `not_found`. The
unscoped canonical reader and fresh-open startup verifier reject that damage as
`integrity`. No path repairs damage or releases partial history.

### Transaction-local consultation

The store side of that contract is implemented in
`internal/sqlite/supersession_activation.go`. `verifySupersessionActivationState` is the one
verification of a source's current activation state: it reads the state projection, the event it
names, the event's canonical bytes and projections, its declaration and predecessor references and
the whole predecessor chain through a supplied query handle, and reports whether the source has
activation history. `GetSupersessionActivationState` is that verification inside its own read
transaction, with unchanged signature, reported code, message, snapshot and projection JSON.

`readSupersessionConsultation` adds the declaration read, so one call inside a transaction the
caller already holds returns the verified current state and, when the state selects one, that
declaration's verified canonical record, in the shape `BuildSupersessionSelection` consumes. It
opens no transaction and calls no public store method: a caller that holds the writer transaction
reads every provenance fact from that transaction's snapshot, so a transition another connection
commits meanwhile cannot mix into the consultation, and a verified absence of history is a nil
state rather than an error code. Recorded state that exists but cannot be verified stays an
integrity failure and is never repaired: a projection whose named event or selected declaration is
missing, a broken chain, a cross-source pointer or a projection disagreeing with its event. A
`not_found` code never leaves this reader as a successful absence, and a direct historical event read
keeps its own `not_found` for an identity that does not exist.

The fresh opted-in retrieval entry point invokes this consultation after authorization
and candidate verification. Historical reads verify recorded immutable administration
separately, never today's projection.

### Declaration administration

`supersession declaration put` reads exactly one
`mousa.supersession_declaration.v1` object from stdin. Input above 64 KiB is rejected
before decoding, and the strict codec refuses unknown, duplicate or missing fields,
trailing values, malformed identities and an identity that disagrees with its
content. The command opens the store named by `-store` writable, appends the
declaration through the immutable store path, and prints the canonical stored
declaration JSON. The declared source and both pinned revisions must already exist; the command
does not create or retarget sources, items or revisions, or repair records. An exact
retry is idempotent, and a reused identity with different content is a conflict.
A rejected declaration transaction appends no declaration and changes no canonical
records. Writable open may nevertheless create an absent store or upgrade an older
supported store before target validation; rejection does not undo that creation or
migration. Malformed or oversized input is rejected before the store opens.

`supersession declaration get <declaration-id>` opens the same store read-only and
prints the same canonical JSON. A missing record or absent store exits 1 with a
storage error, an unparsable identity exits 2 as an invalid invocation, and a corrupt
stored record fails integrity verification without repair. Reads never create or
migrate a store, and a read changes no stored byte.

Both commands are trusted local store administration over the store the operator
names with `-store`. They grant no retrieval permission, change no grant, policy or
authentication state, expose no document text, and establish nothing about who
authored a declaration or whether its labels are true.

### Activation administration

`supersession activation put` reads exactly one `mousa.supersession_activation.v1`
object from stdin. Input above 64 KiB is rejected before decoding, with the same
strict codec rules and the same refusal of malformed, duplicate, unknown, missing or
trailing content. Every nullable field must be present as either null or a value, so
an absent key is never read as an explicit null. The command opens the store
writable, applies the transition through the immutable store path, and prints the
canonical stored event JSON.

The caller states the transition rather than the command inferring it: the event
identity, the nullable expected predecessor event and the nullable selected
declaration. A null predecessor with a declaration is a root activation, a named
predecessor with a declaration is a replacement or reactivation, and a named
predecessor with a null declaration is a deactivation. The event identity covers the
source, expected predecessor and selected declaration only, so actor, time and reason
are recorded evidence: replaying an identity with different metadata is a conflict
rather than an exact retry, and recording them authenticates nothing.

The store enforces the semantics inside one writer transaction. A root must select a
declaration; a replacement or deactivation must name the exact current event; a
transition that would not change the active declaration is refused; an exact retry
succeeds only while its event is still current; a stale expectation, a competing
branch, a foreign-source declaration, a missing declaration and a missing source are
rejected with a storage classification. A rejected transition transaction appends
no event and changes no activation, declaration or canonical record. Writable open
may still create an absent store or upgrade an older supported store before target
validation; rejection does not undo that creation or migration. Malformed or oversized
input is rejected before the store opens.

`supersession activation get <activation-id>` opens the store read-only, prints the
same canonical event JSON, and keeps historical reads: an event stays readable after
later transitions advance the current state, while applying that same event again
after the state advanced is a conflict. A missing event or absent store exits 1 with
a storage error, an unparsable identity exits 2 as an invalid invocation, and a
corrupt event or current projection fails integrity verification without repair.
Reads never create or migrate a store, and a read changes no stored byte.

The activation commands are trusted local store administration over the store the
operator names with `-store`. They record which declaration is active for one source
and grant no retrieval permission, move no item pointer, mutate no declaration and
expose no document text. They change no default query; only native opted-in queries
apply the recorded active declaration.

### Current activation state

`supersession activation state <source-id>` opens the store read-only and prints one
JSON object with exactly `source_id`, `current_activation_id` and
`active_declaration_id`. The identity strings are the canonical lowercase hexadecimal
values the event and declaration records carry, and a deactivated source prints an
explicit JSON null declaration rather than omitting the field. The object is a
projection view over verified stored records, not a canonical record: it has no
identity, schema, hash or event of its own and is never stored.

The command parses the source identity before opening the store, so an unparsable
identity, a missing argument and an extra argument all exit 2 as invalid invocations,
while a store failure exits 1 with empty stdout. A source with no activation history
reports the store's `not_found` error; recorded state that exists but cannot be verified
reports an integrity failure instead and is not rebuilt, repaired or migrated by the read.
That covers history whose current projection is missing, a projection naming a missing
event or disagreeing with the event it names, a broken chain, and a missing declaration or
pinned provenance, so `not_found` from this command means only that the source has no
activation history. The read never creates a store, and an older supported store stays at
its own version because
read-only open refuses to migrate it. The reported state is a verified snapshot rather
than a reservation: `supersession activation put` still requires the caller's explicit
expected predecessor and can still conflict if another transition wins first. The
verification itself is `verifySupersessionActivationState`, shared with the internal
transaction-local consultation reader, so the command and a writer-transaction caller check the same
records; the command keeps its signature and projection JSON, and its `not_found` code and message
for a source with no history are unchanged.

The `state` command inspects the current projection; `get <activation-id>` reads one
historical event. There is no activation listing or per-source history command, no
source alias or name lookup, and no supersession MCP administration tool; the MCP query
opt-in reads the same records through the retrieval transaction.

Declaration storage starts at schema 12; activation history/current state at
schema 13. Schema 14 adds a source index for history-existence checks. Writable
open upgrades older supported stores with verified per-migration backups;
read-only open never upgrades. Older migration bytes and canonical record
identities are unchanged. The source index avoids scanning unrelated activation
history for that lookup; it is not a measured latency or startup-cost claim.

Missing, stale or inconsistent current state with retained history is an integrity
failure for the state read and for new-transition writes, and neither reconstructs it.
An exact retry succeeds only while its event is the verified
latest tip; a projection naming an event with a successor is corrupt, not a valid
retry. Rejected operations neither append events nor reconstruct damaged state.
Ordinary stale expectations and historical retries against valid current state
remain conflicts.

[Published contract checks](https://github.com/graydeon/mousa-benchmarks/tree/1e8cb7f1b32d1098cd3271a80363c519006ae4ea/results/2026-10-06-supersession-core)
cover the pinned historical implementation, not arbitrary later revisions. Current
product regression tests cover the later integrity and source-index corrections
plus native declaration and activation put/get CLI administration and the read-only
current-state command.
A [native administration capture](https://github.com/graydeon/mousa-benchmarks/tree/247928f86ed3f82468563eaec624b0fc4382da84/results/2026-10-07-native-supersession-administration)
pins the administration commands' observed behavior against a synthetic store at that
baseline; it is an administration-only record and does not exercise, accept or measure
the opt-in domain records or survivor packing described on this page.
Current product tests exercise native opt-in enforcement separately from that historical
administration capture. Semantic evaluation, performance and full-window or actual-host
agent acceptance remain unrun.

## Local stdio MCP

`mousa -store STORE mcp --caller cli --source inspection-notes
--ingest-source inspection-notes` serves one local client over stdin/stdout.
Repeat `--source` for each permitted JSONL source, and repeat `--ingest-source`
only for sources that may accept items or embedding imports. Writes are disabled by default. Source
IDs are exact, case-sensitive opaque identities in namespace `mousa-jsonl`,
not paths to open. This interface cannot import directory sources.

The store path, source allowlist and trusted caller ID come only from process
arguments. Unknown sources, paths used as unconfigured source IDs, and tool
fields such as `store`, `root` or `caller` are rejected before store mutation.
An operator may configure an opaque source ID that looks like a path; it still
does not grant filesystem access. Protect the executable invocation, store,
WAL, backups and released output with local filesystem controls.

`--caller cli` installs the same deployment-level allow used by CLI sync. It
does not override a source-scoped deny or resume a withdrawn source. Other
caller IDs use namespace `mousa-local.caller` and purpose
`mousa-local.purpose` / `retrieval`, and require an applicable policy provisioned
through the core separately. MCP does not expose policy administration or
authenticate the startup identity. CLI `access --source ID allow|deny` and
`withdraw --source ID` remain operator controls for their existing caller.

### Protocol and tools

The supported protocol is [MCP `2025-11-25`](https://modelcontextprotocol.io/specification/2025-11-25/basic/lifecycle),
implemented with the official [Go SDK v1.8.0](https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.8.0).
Initialization advertises the tools capability. If the client requests another
version, the server offers `2025-11-25`; a client that cannot use it should
disconnect. `tools/list` exposes the input and output JSON Schemas. The default
stdio mode exposes no prompts, resources, roots, sampling, tasks or remote transport.

| Tool | Required arguments | Optional arguments | Success `result` |
|---|---|---|---|
| `mousa_sync` | `source`, `items` | `segment_policy`: `fixed-v1` (default) or `passage-v1` | Existing sync report: action lists, `total_items`, segment policy, store-size sample and elapsed seconds |
| `mousa_status` | `source` | None | Existing status report: collection state, active items, observations and recovery state |
| `mousa_query` | `source`, `query`, `policy`: `original` or `dedup`, `budget_bytes` | `packing_policy`: `original` (default) or `exact-v1`; `supersession`: boolean (default `false`); inline `associations` document for byte-only queries without supersession | Existing evidence response, including authorization, selected text/ranges/digests, packet and trail IDs; supersession adds `supersession_excluded`, associations append independently attributed evidence and any omissions |
| `mousa_trail` | `source`, `trail_id` | Independent `supersession`, `retrieval_details` and `token_details` booleans (default `false`) | Filtered history under fresh authorization; no text. Opt-ins add only the recorded consultation, vector/hybrid facts or safe v7/v8/v10 token view that exists |
| `mousa_vector_query` | `source`, `space_id`, `values`, `budget_bytes` | `packing_policy`: `original` (default) or `exact-v1`; `supersession`: boolean; token budget fields below | Selected evidence, cosine/ranks, space/query-vector IDs, coverage, packing and producing IDs; opted-in withheld count |
| `mousa_hybrid_query` | Same as vector plus lexical `query` and `policy`: `original` or `dedup` | Same packing, supersession and token options | Selected evidence and actual lexical/vector branch ranks, BM25/cosine and equal-weight RRF(k60), with coverage and producing IDs |
| `mousa_vector_inputs` | `source` | None | Native `mousa.embedding_inputs.v1`: fresh decision ID/outcome/reasons and exact normalized current passages/pins; denied/withdrawn has only an empty passage list |
| `mousa_vector_import` | `source`, `import` | None | Native `mousa.vector_import_result.v1`: canonical space ID and accepted submitted count `imported`, including exact retries, not newly inserted rows |

Export calls `ExportEmbeddingInputs` with the trusted caller's retrieval request.
Fresh policy/lifecycle and current passage reads share its existing writer snapshot.
Configured read sources need no ingestion permission. Import calls the strict
bounded `DecodeVectorImport` before the existing atomic `ImportEmbeddings`.
It requires the alias's `--ingest-source`; HTTP additionally enforces `mousa:write`
after normal `mousa:read` admission. Discovery scope declarations are not the gate.
Import is administration, not retrieval authorization: retrieval denial still
blocks content release but does not add a retrieval-policy requirement to import.
Withdrawal and stale/foreign/mismatched pins reject inside native import without
partial embedding or space writes. Exact immutable retries preserve rows; changed
values conflict. No transaction or snapshot lease spans external computation.
The native item/passage/text/dimension/input bounds remain; MCP's 2 MiB input-frame
bound can refuse a request before the 8 MiB codec bound. See the
[end-to-end client workflow](USAGE.md#mcp-vector-and-hybrid-example).


All three query tools accept optional `context_tokens` (integer 1..524288),
`window_tokens`, `prompt_reserve_tokens` and `output_reserve_tokens` (integers
0..1000000000). Window/reservation fields require explicit `context_tokens`,
even when zero. Nonzero reservations require a positive window; their sum must
not exceed it. The effective limit is the lesser of the cap and window minus
reservations, or just the cap without a window. Zero remaining allowance is valid.
All three token modes accept supersession: true writes lexical v8 or vector/hybrid
v10; omission/false retains v7. Association combinations reject.

### MCP measured context and recorded token details

The operator must configure both `mcp --tokenizer-python PATH --tokenizer-cache DIR`.
These values never come from tool arguments, aliases or resources. Startup and
byte-only tools do not execute Python. Missing configuration or unusable prepared
backend gives bounded `tokenizer_unavailable`, never a byte fallback.
No packages or vocabulary are downloaded during a query.

Token execution calls `EvaluateAndTraceTokenLexical`, `EvaluateAndTraceTokenVector`
or `EvaluateAndTraceTokenHybrid` with the configured source and trusted caller.
The native transaction owns authorization, ranking, packing and v7/v8/v10 recording.
The result adds exact `context` bytes and the accepted-only `TokenSelectionView`
as `token`; the existing byte constraint still applies. Selected evidence and
packing counts describe token-constrained survivors. The count covers headers
and separators in `mousa.context_text.v1`, not tool/JSON-RPC envelopes or unknown
client/model framing. Reservations are declarations, not a complete-window guarantee.
Without `context_tokens`, the released property set, byte selection and Python
independence are unchanged. Refresh closed schema caches with `tools/list`.

`mousa_trail` with `token_details: true` releases the verified recorded safe
`token` view for v7/v8/v10, including native-created history. Omitted/false hides it;
legacy trails have no member. This is independent of `retrieval_details` and
`supersession`. It does not start a tokenizer, remeasure an old count or rerank.
Fresh denial withholds history and existence. Retired text remains unarchived;
retained digest verification and producing-count limits are unchanged.

Failed counting, failed worker completion and cancellation during counting do
not commit a successful producing decision/trail/packet. Invalid token arguments
reject before query mutation. This does not undo server startup/store migration,
policy deployment or legitimate fresh inspection authorization audit writes.
See the [runnable token example](USAGE.md#mcp-token-budget-example).

`mousa_query` accepts the optional `supersession` boolean, which withholds an accepted
candidate whose own representation is the selected declaration's exact predecessor pin
for that configured source; an opted-in response reports the declared count
`supersession_excluded`, including zero. `mousa_trail` accepts the same boolean and
returns the consultation member a `mousa.source_trail.v4` record carries: whether
supersession was consulted, the nullable activation and declaration identities and the
withholding rows. Omitted or `false` keeps the default retrieval and response property
set, and a legacy trail has no member, so the opt-in adds none. Other values are
`invalid_arguments`, and `mousa_sync` and `mousa_status` reject the field. The records are
prepared by native administration; MCP exposes no administration tool. The client
contract, the declared optional members and the reasons for keeping the envelope at
`mousa.mcp_result.v1` are specified in
[MCP supersession opt-in](MCP_SUPERSESSION.md).

### MCP supplied-vector retrieval and recorded details

The two explicit tools use `EvaluateAndTraceVector` / `EvaluateAndTraceHybrid`,
not subprocess retrieval or a second ranking implementation. An operator first
exports authorized normalized passages and imports externally produced embeddings
through the native workflow. MCP accepts a canonical nonzero lowercase hex
`space_id` and 1–2048 finite float64 query `values` with nonzero norm; it never
accepts an embedding executable, endpoint or filesystem path. Query values pass
through the native strict query-vector decoder, limited to 128 KiB including its
schema wrapper. Budgets are integers from 1 through 262144 bytes for these tools.
The same 100-item/passage, current-revision, dimension and space bounds apply.
Partial coverage searches the indexed subset; no indexed vectors gives
`no_indexed_passages` in vector mode. Hybrid may still select lexical-only hits.
There is no silent lexical fallback for wrong spaces or dimensions.

Both use the exact configured JSONL source alias and trusted startup caller,
fresh policy/lifecycle authorization and one native writer transaction. Missing
configured source records retain the native `integrity` error; empty existing
sources report `no_passages`. Unconfigured aliases reject before store use.
Denial/withdrawal releases no evidence, source coverage or candidate facts.
Vector/hybrid requests accept the optional `supersession` boolean: true withholds
exact declared predecessors and records byte-only v9 or token-budget v10, while
omission/false keeps the existing producing version and response properties.
Associations reject even when empty. See
[vector/hybrid supersession](#byte-only-vectorhybrid-supersession).
Vector-only queries also reject lexical policy/query fields.
Unknown, duplicate, missing, null and wrong-type input members reject before
mutation. Original packing remains the default; exact-v1 uses native byte equality
and retained-copy semantics after ranking. Oversized passages are skipped.

Within `mousa.mcp_result.v1`, closed discovered schemas describe the selected
source/segment/representation pins, normalized coordinates, text/digests, rank,
cosine or branch/fusion facts, producing identities and packing budget/use/
omission counts. Identities are lowercase hex strings, never byte arrays.
The `vector` / `hybrid` projection includes the declared space, query-vector
identity, observed coverage and ordered candidate pins/ranking facts, not
embedding values or the supplied query values.

`mousa_trail` with `retrieval_details: true` obtains these same facts from the
verified recorded producing view, including native-created v5/v6 histories.
It does not rerun ranking or reinterpret coverage under today's space/index.
Omission or false preserves the exact default released property set; a trail
without those facts has no added member. Supersession remains independent.
Fresh authorization precedes historical lookup; denied inspection discloses no
history or existence. Restart and revision replacement preserve recorded facts,
while `indexed_now` can change. Retired passage bytes remain unarchived, and
historical completeness/BM25 are producing observations, not reconstructed proof.
For native v7, retrieval details release the corresponding verified vector/hybrid
fields; the independent `token_details` opt-in releases the safe token view.
Canonical TokenSelection and rejected token metadata are never exposed.
See the [runnable synthetic MCP example](USAGE.md#mcp-vector-and-hybrid-example).


For example, call `mousa_sync` with:

```json
{
  "source": "inspection-notes",
  "items": [
    {"id": "notes/review@draft", "text": "The harbor inspection is scheduled for Friday."},
    {"id": "notes/withdrawn", "deleted": true}
  ]
}
```

Then call `mousa_query` with:

```json
{"source":"inspection-notes","query":"harbor inspection","policy":"original","budget_bytes":4096}
```

Add `"supersession":true` to that call to withhold an accepted candidate whose own
representation is the selected declaration's exact predecessor pin for the same source;
the result then reports `supersession_excluded` (including `0`). The records come from
the native `supersession declaration put` and `supersession activation put` commands.

Use the returned `result.trail_id` with `mousa_trail`. Items use the same strict
record validation, normalization, immutable revision identities and atomic
current-item activation as [JSONL input](#jsonl-input). There are no inferred
associations or new packing/token-window rules. Evidence coordinates refer to
normalized UTF-8 bytes; use the [same digest and range checks](#verify-a-selected-passages-location)
as for CLI evidence. Treat every source text as untrusted data, not protocol,
shell commands or instructions to the client.

### Structured results and failure state

Every completed known-tool call returns `structuredContent` with schema
`mousa.mcp_result.v1`. A text content block contains the same serialized JSON
for clients that do not consume structured content. Its object has:

| Field | Meaning |
|---|---|
| `schema` | Always `mousa.mcp_result.v1`. |
| `operation` | The invoked tool name. |
| `source` | Configured source label, or an empty string when arguments failed decoding. |
| `result` | Success payload described above; absent on a tool error. Its full schema is discoverable in that tool's `outputSchema`. |
| `error` | Tool error object; absent on success. |

`error` has `code`, a diagnostic `message` of at most 512 UTF-8 bytes,
`completed_items`, and an optional one-based `failed_item` index. `completed_items`
counts successfully applied item records, including no-op retries; their prefix
remains committed. It is zero for errors outside item application. No partial
success report is emitted on an ingestion error. Correct and replay the call:
unchanged content remains a no-op and repeated tombstones report `absent`.

Argument-envelope errors, unknown/unconfigured sources, disabled ingestion,
and excessive item counts are checked before applying items. Item validation
and size limits are checked in order, so a later invalid item does not undo
earlier activations. Failure during one item's preparation may retain immutable
history but does not activate that failed revision. This is not whole-batch
atomicity. Omitted items are unchanged; deletion must be explicit.

Tool errors set MCP `isError: true`. Boundary codes include
`invalid_arguments`, `source_not_permitted`, `ingestion_not_permitted`,
`invalid_item`, `resource_limit`, `busy` and `cancelled`; canonical storage errors
retain their existing codes, with `operation_failed` for other failures.
Authorization exclusions are successful query/inspection results, not tool errors.
Denied inspection commits its fresh authorization decision and necessary input rows,
returns that decision's ID and reasons, and performs no historical lookup. It releases
no historical payload. Unknown or foreign-source inspection errors roll back their
attempted authorization and return `not_found`; historical and administrative rows
remain unchanged. MCP inspection can read native-created same-source v3 association
history even though MCP query does not produce it.

Unknown tools/methods and malformed protocol parameters use SDK JSON-RPC errors.
Invalid JSON or an oversized inbound frame terminates the stdio session with a
bounded diagnostic on stderr, without a success message. Earlier completed
requests/items remain durable; inspect status after reconnecting.

### Limits, concurrency and shutdown

| Bound | Maximum |
|---|---|
| Inbound JSON-RPC frame | 2 MiB |
| Configured sources | 128 |
| Item records per sync call | 128 |
| Serialized JSON bytes per item | 1 MiB |
| Decoded UTF-8 text per item | 256 KiB |
| Item ID | 4,096 UTF-8 bytes; nonempty, no NUL |
| Query text | 4,096 UTF-8 bytes; nonempty |
| Released-text budget | Lexical: integer 1–65,536 bytes; vector/hybrid: integer 1–262,144 bytes |
| Lexical candidates | 100, preserving the existing query contract |
| Concurrent tool calls admitted | 8; excess calls return `busy` |
| Tool-call deadline, including time waiting for the store | 30 seconds |

JSON Schema string lengths count characters; byte bounds above are additionally
enforced at runtime. Frame overhead and JSON escaping also consume the framing
and per-item bounds. A released-text budget is not a cap on protocol output,
metadata, model tokens, or a complete model context window.

The process owns one canonical store connection and serializes complete
application calls, including each whole multi-item sync. Separate processes
still do not form a supported concurrent source-wide synchronization protocol.
Coordinate external ingestion and administrative changes; SQLite conflicts are
errors, not an implicit retry mechanism.

Cancellation is propagated into the current operation, including queued calls.
Already completed item activations are not rolled back by cancellation.
SQLite's existing one-second busy timeout remains unchanged; cancellation is
not a promise of instantaneous interruption of its busy wait.
[Stdio shutdown](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports#stdio)
uses stdin closure/disconnect or SIGINT/SIGTERM, not a shutdown tool. The server
cancels pending work, drains handlers and closes its owned store. Stdout contains
only protocol messages; diagnostics go to stderr.

The deterministic actual-client regression command is
`CGO_ENABLED=0 go test ./cmd/mousa -run TestMCP -count=1 -v`. It launches the built
executable with the official SDK client and temporary SQLite stores, validates
discovered output schemas and logs client/server receipts. It is not a
model-driven agent run or a comparative measurement.

## OpenAI MCP Extensions

The MVP follows the [OpenAI MCP Extensions specification](https://github.com/openai/mcp-extensions/blob/e314720a0daac326217d1f123fcf51647868fa9f/docs/spec.md)
and its Bits & Bolts registration/onboarding example. Enable it explicitly with
`--openai-extensions`. The six base tools retain their contracts; one additional
`mousa_mentions` tool and an evidence resource template are advertised. Mention
and resource search remain lexical.

`mousa_mentions` accepts only `{ "query": "cedar" }`. The tool advertises
`_meta["openai/extensions"]["mentions/search"]: {}` and app visibility through
`_meta["ui"]["visibility"]: ["app"]`. It returns `structuredContent.items` as
resource links. Empty/whitespace queries return an empty list. Nonempty queries
use the existing source-authorized retrieval and trails, original query policy
and a 65536-byte evidence budget per source. Results contain at most 30 links;
configured sources are searched in sorted label order, not globally ranked.
Argument strings are limited to 4096 UTF-8 bytes and reject null, duplicates,
unknown fields, invalid Unicode and embedded NUL.

Resource URIs use `mousa://evidence/<source-id>/<segment-id>`. Both identities
are canonical lowercase SHA-256 IDs, not filesystem paths. `resources/read`
performs a fresh policy evaluation before looking up text, then verifies current
indexed bytes, canonical ancestry and restrictive lifecycle in one writer
snapshot. A previous link grants no authority. Denied, retired, withdrawn,
unscoped and absent evidence is unavailable. The JSON resource uses schema
`mousa.evidence_resource.v1`, a fresh `decision_id`, and `evidence` containing
`segment`, `text` and `paths`. Paths contain representation, artifact, observation
and source IDs projected only for the authorized source. Text is limited to
65536 bytes and the complete resource to 1048576 bytes. Treat text as untrusted data.

No custom viewer, global/thread UI entrypoint, file handler, filesystem access,
settings mutation or extended form is implemented. These are optional extensions,
not required to make mention search useful. The specification lists desktop
composer mentions; its Web column means ChatGPT Work and excludes classic
ChatGPT. It does not establish CLI rendering support. Codex CLI uses the
core tools and evidence skill when desktop features are absent.

### Consent, packaging and privacy

`mousa -store STORE plugin --out NEW_DIRECTORY --source ID --consent-to-share`
creates a local marketplace only after explicit consent. Repeat `--source`.
The portable plugin has root `plugin.json`, `mcp.json`, `skills/` and `bin/`;
OpenAI presentation and `onboardingSkill` are under `extensions.com.openai`.
Portable stdio commands must be bare names or contained `./` paths, so the
package uses `./bin/mousa`, not an absolute executable path. The store path is
user-specific startup configuration. The generator does not open/ingest the
store, install credentials, alter host configuration or publish anything.
It refuses an existing destination. If generation fails before all package
files are written, it removes its newly created partial directory and reports
any cleanup error. A complete package remains if only emitting the final CLI
result fails. Protect the package/config like the store.

The setup skill explains sharing and reuses explicit user authorization for the
configured source labels and sharing with the configured host/provider. It asks
for confirmation before retrieval when that authorization is missing or the
source scope changes. Installation or tool discovery alone is not authorization.
The evidence skill explains querying, quotations and Source Trails.
Data sent to the host/provider includes query/tool arguments, selected evidence
text, source/item labels, canonical IDs, byte coordinates, hashes and provenance/
audit metadata. Local audit records remain in the canonical store. Host/provider
retention, account controls and deletion are governed by that host/provider.
Mousa does not detect secrets or control retention outside its process.

Default access does not mutate source content. Query, mention and trail/resource
inspection append authorization/audit records, so their tool annotations do not
claim `readOnlyHint: true`. Status is read-only/idempotent; sync can update/delete
explicit items and is marked destructive. All tools are closed-world. To enable
ingestion, separately configure an already permitted `--ingest-source`; consent
to retrieval is not consent to ingestion.

### Authenticated HTTP and deployment

HTTP is a separate transport, not an extension. `mcp --http-config FILE` serves
stateless JSON Streamable HTTP at `/mcp`. The same startup caller/source/store
bindings and source-deny precedence apply. Each process accepts one configured
OAuth subject; deploy separate processes, OS users and stores for separate
accounts. There is no shared multi-tenant writable store or client-selected path.
This is a dedicated-account, operator-hosted deployment, not a public pairing relay.

The JSON configuration requires:

| Field | Contract |
| --- | --- |
| `listen` | Numeric loopback address and port 1–65535; no LAN/public bind |
| `resource` | Stable absolute HTTPS URL ending in `/mcp`; no credentials/query/fragment |
| `issuer` | Exact HTTPS OAuth authorization-server issuer |
| `subject` | Exact expected user subject; all other subjects are rejected |
| `introspection_url` | Operator-configured HTTPS RFC 7662 token-introspection endpoint |
| `client_id` | Confidential resource-server introspection client |
| `client_secret_env` | Environment variable containing its secret; no secret in package/config |

Start it with the same `-store`, `--caller`, `--source`, optional ingestion and
extension flags as stdio. Supply an established OAuth 2.1 authorization server
with authorization-code/PKCE S256 and client registration appropriate for the
host, plus an operator-managed HTTPS reverse proxy. Mousa is the resource server,
not an OAuth authorization server. The proxy must preserve the configured public
Host or exact loopback authority. Forwarded identity/host headers confer no trust.
Untrusted Origins are rejected for MCP; public protected-resource metadata supports
cross-origin authentication discovery. No unauthenticated tool listener is started.

Every HTTP request introspects the bearer token without caching. The response
must contain active, exact `iss`/`sub`, matching `aud` (string or array), future
`exp`, acceptable `nbf` if present, and space-separated `scope`. Missing/invalid
claims fail closed. `mousa:read` is required for all MCP requests; item ingestion
and embedding import also require `mousa:write` and startup source permission. This is not an API-key login
fallback. Introspection uses HTTP Basic service authentication, a five-second
timeout, bounded response, TLS verification and no redirects. Provider errors
release no token/secret details. The configured IdP must support these claims.

The unauthenticated metadata endpoint is
`/.well-known/oauth-protected-resource/mcp`; 401 responses advertise it through
`WWW-Authenticate`. Request bodies are limited to 2 MiB, headers to 16 KiB and
admission to eight concurrent requests including token verification. Stateless
requests retain no session identity/replay buffer. Shutdown drains handlers before
closing the canonical store.

### Local connectivity and public directory gates

Same-host desktop/Codex stdio needs neither HTTPS nor a tunnel. Hosted private
testing can use an operator-configured [Secure MCP Tunnel](https://developers.openai.com/api/docs/guides/secure-mcp-tunnels);
workspace permissions, runtime credentials and activation are separate prerequisites.
A development tunnel is not public distribution.

The generated package reports `directory_submission_ready: false`. It is usable
for local/private testing, not a completed shared-directory submission. Public
submission needs stable authenticated HTTPS, verified publisher/domain, product,
support, privacy and terms URLs, icons, account-isolation/deployment review,
dedicated reviewer access, an actual walkthrough recording and executed positive/
negative review cases. Do not place reviewer credentials in the ZIP. No URLs,
account access or demo results are invented. Local stores cannot become generally
accessible through a public listing without an authorized deployed connectivity
solution. This implementation does not deploy that solution.

[Submission requirements](https://developers.openai.com/plugins/deploy/submission)
remain separate from extension support. The maintained
[integration matrix](../eval/local/openai-matrix.json) supplies five positive and
three negative review specifications and a synthetic fixture. It names ChatGPT
desktop and Codex CLI as primary, OMP/Hermes as secondary; Goose is not a current
benchmark. Protocol checks, actual native discovery/resource reads, desktop UI
checks and model-driven review cases must have separate PASS/FAIL/NOT RUN receipts.


## Item identity and lifecycle

A directory source uses namespace `mousa-local` and its absolute root path as its
external identity. An item is identified by its relative POSIX path. Moving a root
creates a different source. A renamed file is a deletion plus an addition.

A JSONL source uses namespace `mousa-jsonl` and the exact `--source` value as its
external identity. That namespace is distinct from directory sources, even if the
external value equals a directory path. JSONL item IDs are opaque, case-sensitive
strings; `@` and Unicode are supported.

Raw content identifies an immutable revision. Normalization may change the bytes
used for retrieval, so a raw digest is not a normalized representation ID. Replaying
a revision reuses its accepted delivery receipt, including the original capture time.
Timestamps and hash ordering do not select the current revision.

The current-item relationship is separate from revision history. Its activation and
lexical index rows change in one transaction:

| Prior state | Input | Reported action | Searchable state after success |
|---|---|---|---|
| Unknown item | Text, including empty text | `added` | Input revision |
| Active item | Identical raw content and segment policy | `unchanged` | Unchanged |
| Active item | Different content or segment policy, including an older revision | `updated` | Input representation only |
| Deleted item | Text | `restored` | Input revision only |
| Active item | Deletion | `deleted` | No evidence for that item |
| Unknown or already deleted item | Deletion | `absent` | No evidence for that item |

Empty text has no segments and produces no search result. Removing an index entry
is not secure erasure: canonical records remain as history, and old content may
remain in SQLite pages, WAL files, backups, or previously released output.

Canonical preparation can commit before activation. A preparation failure may
therefore leave unactivated historical records, but it does not replace the previous
searchable revision. Failure or abrupt exit during activation rolls back both index
replacement and the current-item pointer. Retrying the same input is supported.
A multi-item sync is not one transaction: earlier successful items remain committed
when a later item fails. Concurrent syncs of the same source are not a supported
source-wide snapshot protocol; serialize them.

Canonical history retains identities, digests, and provenance, not a complete text
archive. After deindexing, Mousa cannot reconstruct an old revision's text from its
digest. Retain original source material separately if historical text is required.

`status` distinguishes `active_items` from historical `observations` and reports
`needs_recovery`. Source collection state is separate: an active source can contain
zero active items.

For sync reports, `total_items` counts selected directory files or processed JSONL
records, not the source's active item count. `store_bytes` samples the main database
file before close and excludes WAL and other files; it is not total disk usage.
Use the separately reported post-exit file sizes in the lifecycle experiment when
comparing those measured stores.

## Ingestion segment policies

Both input forms accept `--segment-policy fixed-v1|passage-v1`. The default is
`fixed-v1`; query-term `--policy` is a separate option. For example:

```sh
./mousa -store local.sqlite sync --segment-policy passage-v1 ./documents
./mousa -store local.sqlite query --budget-bytes 1024 ./documents 'restart procedure'
./mousa -store notes.sqlite sync --source notes --segment-policy passage-v1 < items.jsonl
```

Repeat the selected policy on every sync. It is not saved as a source default.
Omitting the flag selects `fixed-v1`, including when the current items use passages.
Changing policy on identical raw bytes reports `updated`, reuses the original
observation, artifact and receipt, and atomically replaces the item's active
representation and index. It does not leave both segment sets searchable.
JSONL omission still leaves an item unchanged, so a partially resynced source can
contain items with different policies. Sync JSON includes `segment_policy`.

The policy is bound into the normalized representation's existing
`parameters_sha256` identity field. `fixed-v1` retains SHA-256 of empty bytes;
`passage-v1` uses SHA-256 of the exact UTF-8 bytes
`{"segmentation":"passage-v1"}`. Normalizer processor ID and version stay unchanged.
Segment IDs still bind representation, byte-range selector and content digest.
No schema migration or historical ID rewrite is needed. Returning to an earlier
policy and content reuses those historical identities. Use a passage-aware binary
for future syncs; older binaries do not understand the selected policy.

`fixed-v1` emits up to 4,096 bytes per segment at UTF-8 code-point boundaries.
`passage-v1` emits up to 1,024 bytes per segment using these deterministic rules:

- Split normalized text into LF-delimited lines. Whitespace-only lines delimit
  paragraphs and stay with the preceding block; initial whitespace stays with
  the first block.
- ATX headings (one to six `#` characters, followed by whitespace or end of line,
  after at most three spaces) start a new passage group. Following blocks join
  the heading when they fit.
- List markers are `-`, `*`, `+`, or one to nine digits followed by `.` or `)`,
  then a space or tab. Consecutive list lines and nonblank continuation lines
  form a block. Blank lines end that block, including in loose lists.
- Consecutive nonblank lines containing a literal `|` form a table-like block.
  Escaped pipes and column syntax are not interpreted.
- Backtick or tilde runs of at least three characters after at most three spaces
  open a fenced block. A run of the same character at least as long closes it
  only when followed by spaces or tabs, or the line ending. Internal blank lines
  and headings do not end the fence. An unclosed fence extends to end of input.
- Greedily group complete consecutive blocks while they fit. For a block larger
  than 1,024 bytes, emit fragments ending at the last LF within the limit,
  otherwise the last ASCII space or tab, otherwise a UTF-8 code-point boundary.
  Emit the final remainder separately before grouping subsequent blocks.

These are line-level rules, not a Markdown parser. Nested container semantics,
setext headings and HTML blocks are not recognized. A fragment can be an
incomplete command, list, table or code block; code-point safety does not imply
grapheme safety. No heading, fence delimiter or other text is reconstructed.
Every segment is an exact contiguous normalized source slice, with no overlap,
duplication or omitted bytes. Normalization removes an initial UTF-8 BOM and
converts CRLF and CR to LF, so selectors refer to normalized bytes, not raw-file
offsets. Empty text emits no segments. A representation exceeding 65,536 passages
is rejected before activation, leaving its previous searchable revision intact.

Passages make smaller evidence units eligible for packing, not necessarily more
relevant. They change lexical document frequencies and ranking. A complete
structure larger than the budget cannot fit; smaller budgets can still omit all
matching segments. The [research record](RESEARCH.md) reports the bounded
development comparison and its costs, not general superiority.

## Upgrading an existing directory store

Schema version 9 and earlier did not record a reliable current-item relationship.
Migration 10 preserves canonical history, removes the ambiguous directory search
projection, and marks each existing directory source as requiring recovery. The CLI
refuses queries for that source until a complete directory sync succeeds:

```sh
./mousa -store local.sqlite sync ./documents
./mousa -store local.sqlite status ./documents
```

Use the same absolute root identity and restore the desired source files before
syncing. Recovery reconstructs the current item set from those files; it does not
guess chronology from stored hashes or modification times. If original source input
is unavailable, current activation cannot be reconstructed automatically. Keep a
backup before upgrading. A failed recovery sync can be retried; the recovery flag
remains set until a full sync finishes.

Preview selection before recovery. The current defaults are narrower than older
CLI versions that attempted every UTF-8 file. Use explicit include patterns or
`--all-text` when the intended recovery set needs other file types or hidden files;
review the selection before committing it.

## Startup verification and schema 11

Migration 11 adds a nonunique partial index on active local items by source and
representation. It changes the access path for startup's orphan-index check,
not activation, canonical identities, evidence bytes or retrieval policy. An
upgrade from version 10 creates a verified
`<store>.pre-migrate-v10-to-v11.sqlite` backup before changing the schema.
Read-only opens refuse an older schema without migrating it. Older binaries
refuse a version-11 store; use the backup when an older binary is required.

Writable startup retains both read-only preflight and writable verification,
including the writable FTS structural integrity check. Canonical records,
ingestion records and local-item activation checks each use a read snapshot.
Observation, artifact and segment records are verified directly from ordered
scans with the same canonical decoding and projection checks as individual
reads. Each source's ingestion state is verified once per ingestion snapshot;
all receipts, withdrawals and state rows remain checked. No verification result
is reused across snapshots, opening passes or operations.

These snapshots can retain WAL pages while a concurrent writer commits. They
end when their verification phase finishes or fails. Startup still reads and
verifies retained history; the index does not make initialization independent
of corpus size or establish larger-store capacity.

## JSONL input

Each nonblank line contains one object with `id` and exactly one of `text` or
`deleted`. For deletion, `deleted` must be `true`.

```jsonl
{"id":"notes/review@draft","text":"The harbor inspection is scheduled for Friday."}
{"id":"notes/withdrawn","deleted":true}
```

```sh
./mousa -store local.sqlite sync --source inspection-notes < items.jsonl
./mousa -store local.sqlite query --source inspection-notes 'harbor inspection'
```

Limits per invocation:

- 1 MiB per input line, excluding its line ending.
- 64 MiB total input, including whitespace and line endings.
- 10,000 nonblank records. Split larger inputs across invocations.
- 4,096 UTF-8 bytes per item ID. IDs must be nonempty and cannot contain NUL.

Unknown fields, duplicate JSON keys, null values, invalid UTF-8, unpaired Unicode
surrogates, trailing JSON values, and repeated item IDs within one invocation are
errors. Blank lines are ignored. Record order is not item identity or delivery
sequence evidence. Omission from a JSONL stream never deletes an item; send an
explicit tombstone.

On malformed input or an exceeded limit, the command exits 1, identifies the failure
on stderr, and emits no success JSON on stdout. The successfully applied prefix
remains committed. Correct the input and replay it: unchanged current content is a
no-op, and repeated tombstones are reported as `absent`.

## Directory selection and input limits

```sh
./mousa -store local.sqlite sync --preview ./documents
./mousa -store local.sqlite sync --preview --include '*.md' --exclude 'drafts' ./documents
./mousa -store local.sqlite sync --include '*.md' --exclude 'drafts' ./documents
```

Preview uses the same selection and content validation as sync, but does not open,
create, or mutate the store. It reports relative item names, byte lengths, skipped
entries with reasons, and total selected bytes, never file contents. Preview does
not reserve a filesystem snapshot for a later sync.

Defaults select `.md`, `.markdown`, `.txt`, and `.rst` files, case-insensitively.
Hidden entries and directories named `node_modules`, `vendor`, `build`, `dist`, or
`target` are excluded. Observed symlinks, non-regular files, and `.git` entries
beneath the root are skipped even with `--all-text`.

- Repeatable `--include` patterns replace the default extension selection.
- Repeatable `--exclude` patterns apply to files and directories and always win.
- Patterns use Go's `path.Match` syntax, not recursive `**` globbing. A pattern
  without `/` matches a basename at any depth; a pattern with `/` matches the
  relative POSIX path. Excluding a directory skips its descendants.
- `--all-text` overrides hidden/generated and extension defaults. Include/exclude
  patterns still apply. For example, `--all-text --include '.notes'` deliberately
  selects that hidden filename rather than every file.

Selection flags are invocation-local, not saved as source configuration. Use the
same flags on subsequent syncs to retain the same scope. A successful sync
deactivates previously indexed items outside its selected set, including items
excluded by a changed filter. `skipped` now contains `{item, reason}` objects with
relative paths rather than the older list of absolute path strings.

| Limit | Default | Override |
|---|---|---|
| Selected bytes per file | 1 MiB | `--max-file-bytes` |
| Total selected file bytes | 64 MiB | `--max-bytes` |
| Visited entries, including directories and skipped entries | 10,000 | `--max-entries` |

Limits must be positive; byte limits must be below the maximum signed 64-bit value.
These bound accepted input, not process RSS or the memory needed to enumerate a
directory. An excluded directory counts as one visited entry. All selected content
is read once and validated within the aggregate byte bound before the store is
opened. A read error, invalid UTF-8 in a selected file, or exceeded limit fails the
command without changing prior activation. This differs from older directory
sync, which silently skipped invalid UTF-8. The explicit `--all-text` override does
not make binary files valid input.

The root must be an existing directory, not a symlink. Reads use `os.Root` to
constrain resolution beneath the opened root; the tested native Linux path rejects
escaping replacement links as well as skipping links found during enumeration.
This is not a concurrent filesystem snapshot, mount boundary, or hard-link
isolation mechanism. Use a stable, dedicated input tree. Root confinement does not
make arbitrary mounted filesystems or device content safe.

UTF-8 validity and filename defaults are not secret filtering. An ordinary
Markdown file can contain credentials or unrelated private text. Preview and retain
only material intended for this store; protect the store, WAL, backups, and released
output separately. Directory selection flags are not accepted with JSONL `--source`
input, which has its own explicit record bounds.

## Query policy, packing, and tracing

```sh
./mousa -store local.sqlite query --policy original --budget-bytes 4096 ./documents 'harbor inspection'
./mousa -store local.sqlite query --policy dedup --source inspection-notes 'harbor harbor inspection'
```

Put flags before positional arguments. `original` is the default and preserves
repeated terms, as in the published BEIR protocol. `dedup` folds repeated prepared
terms before expression capping. It can change both ranking and which terms fit;
it is a retrieval-policy choice, not an equivalent optimization. Earlier CLI
versions implicitly used deduplication.

The CLI and evaluation harness share preparation: split at characters other than
ASCII alphanumerics or non-ASCII runes, lowercase and trim terms, quote them as
literal FTS5 phrases, then OR-join them. This is not a complete implementation of
the index's `unicode61` tokenizer. Keep the leading terms whose expression fits
4,096 bytes. An oversized first term is retained and rejected by the store rather
than silently removed.
Opted-in `--supersession` queries instead reject unpreparable or oversized
expressions as usage errors before opening the store. Without that opt-in,
including `--supersession=false`, the existing post-open error boundary remains.

The candidate limit is 100, not an exhaustive match count. Default packing
(`--packing-policy original`) walks verified rank order, skips accepted segments
that exceed the remaining budget, and continues. It releases whole segments and
retains repeated text. `--budget-bytes` must be positive and defaults to 8,192.
The budget counts normalized UTF-8 evidence text only, not model tokens or JSON,
identifier, or provenance overhead.

Opt in to `--packing-policy exact-v1` to omit exact copies of already selected
passages. Authorization and lifecycle filtering happen first. The first fitting
passage in verified rank order is retained; a later candidate with the same digest
is omitted only after byte equality is checked. Unselected or oversized candidates
reserve nothing. Duplicate omission takes precedence over budget omission when a
retained equal passage exists, including when the remaining budget is zero.

This changes packing, not query preparation or ranking. It works with either
segmentation policy and with either query-term policy. There is no semantic
similarity, overlap removal, truncation, source merging or inferred corroboration.
Selected segment identities, ranks, source ancestry and normalized byte ranges
remain the original ones. Deduplication cannot recover candidates outside the
100-candidate limit or make an oversized passage fit.

With the default `fixed-v1` policy, text is segmented into at most 4,096 UTF-8 bytes,
ending at a code-point boundary, not a sentence, paragraph, or Markdown boundary.
A small budget can therefore return `budget_omitted` even when the relevant sentence is short: its entire
segment must fit. Inspect the trail's candidate `text_bytes` and `selected` fields
to distinguish this from no matches. Increase `--budget-bytes` to admit the
segment; 4,096 bytes can admit one full-sized segment but does not guarantee that
the selected text answers the question. Related context may be in another segment,
and broad lexical matches can consume the budget before the needed passage.
The opt-in ingestion passage policy reduces the maximum to 1,024 bytes using the
boundary rules above. Query packing does not split either kind of segment further.

Each invocation uses a fresh request identity. Current policy evaluation, verified
retrieval, canonical packing, and decision/trail storage share one writer
transaction. Failure while writing the trail rolls back the decision too. The core's
historical evaluation and tracing APIs retain their exact-retry semantics; the new
current-query path rejects an already-used request identity.

Responses include full `request_id`, `decision_id`, `trail_id`, `packet_id`, and
selected `segment_id` values. Each evidence hit includes its text digest.
`matched_candidates` counts considered candidates before packing, bounded by 100;
use the length of `evidence` for the selected count. The old misleading
`total_matches` field has been removed.

For directory and JSONL item sources, `--supersession` explicitly enables exact pinned-predecessor
withholding after authorization and lifecycle verification and before packing.
The opted-in store transaction reads the verified activation and declaration and
always writes v4. Withheld text is not released, its recorded rank and canonical size
stay unchanged, and it consumes no budget or retained duplicate slot. Successor
availability does not gate withholding or cause replacement text to be inserted.
`--associations` combinations are rejected before writable open. Directory source and
revision pin preparation is shown in the [native example](USAGE.md#directory-supersession-example).
MCP `mousa_query` exposes the same query opt-in through its optional `supersession`
boolean, and `mousa_trail` returns the recorded consultation member of a v4 trail under
the same optional boolean. MCP administration remains unavailable.

Valid queries open the store writable and may create or migrate it before a later
operation fails. Retrieval rollback leaves no partial decision or trail; it does not
undo that open boundary. Invalid invocation options are checked before open where
possible, including zero budget, invalid policy and invalid boolean values.

### Verify a selected passage's location

Each selected evidence hit also includes these additive fields:

| Field | Meaning |
|---|---|
| `representation_id` | Canonical identity of the immutable normalized representation that owns the segment. |
| `representation_sha256` | SHA-256 of the entire normalized representation, encoded as lowercase hexadecimal. |
| `byte_start` | Zero-based inclusive offset in normalized UTF-8 bytes. |
| `byte_end` | Exclusive end offset in normalized UTF-8 bytes. |
| `segment_policy` | `fixed-v1` or `passage-v1`, read from that representation's canonical parameters. |

Existing evidence fields retain their meanings. Consumers that reject unknown
JSON fields must accept these additions before upgrading. The fields are released
only with selected, authorized text, not for denied, rejected or budget-omitted
candidates. Historical trail inspection does not gain these fields.

To locate a passage from original source bytes, first validate UTF-8, remove one
initial UTF-8 BOM (`EF BB BF`), then replace CRLF with LF and remaining CR with LF.
No Unicode normalization or whitespace trimming is performed. For JSONL, the
source bytes are the UTF-8 encoding of the decoded `text` value, not the JSONL
record's serialization. Verify the entire normalized byte sequence against
`representation_sha256` **before** applying `[byte_start:byte_end]`. The slice
must equal the UTF-8 encoding of `text`, its length must equal `byte_length`,
and its SHA-256 must equal `content_sha256`.

These are not raw-file offsets, character positions, line numbers or editor
columns. The digest checks revision bytes, not factual truth, current filesystem
state or complete Markdown structure. Representation identity also binds
provenance and processing parameters: equal content digests do not make two
representations or sources interchangeable.

The maintained [`verify_evidence` client example](../eval/local/workflow.py)
retains original bytes, checks the digest and range, and rejects modified source
bytes for a saved response. Saved responses are historical releases. They cannot
revoke copies already delivered, reconstruct unavailable historical text, or
prove that a source is still current. Metadata and JSON serialization add output
bytes separately from the unchanged evidence-text budget.

| `outcome` | Meaning |
|---|---|
| `evidence` | At least one accepted segment fits. Other candidates may be omitted. |
| `no_matches` | Authorized lexical search found no candidates. |
| `policy_excluded` | Source policy denied the request; no lexical search was performed. |
| `lifecycle_excluded` | Source lifecycle blocked retrieval, or every considered candidate was lifecycle-rejected. |
| `budget_omitted` | Accepted candidates existed, but none fit the text budget. |
| `supersession_excluded` | Opt-in only: no evidence or surviving budget skip, and at least one accepted candidate was withheld by the exact pin. |

`budget_omitted` and `lifecycle_excluded` count disjoint considered candidates.
Exact packing returns `duplicate_omitted`, including zero. Opted-in supersession
returns `supersession_excluded`, including zero, and records its explicit packing
policy even under `original`; default original responses omit all three optional
fields. Without association expansion, selected, duplicate, budget, lifecycle and
supersession counts sum to `matched_candidates`, treating absent optional counts as zero.

Outcome precedence is denial (with the existing lifecycle override), selected evidence,
surviving budget omission, supersession exclusion, lifecycle exclusion, then no matches.
Suppression plus a surviving oversized candidate therefore reports `budget_omitted`
and both counts. A withheld candidate is neither a budget nor duplicate omission.
Duplicates require an earlier selected survivor, so there is no duplicate-only empty
outcome. A source-level gate runs before search, so candidate counters are zero;
that does not assert the corpus has no matches.
`decision_reasons` records source-level evaluation reasons without candidate metadata.

The decision authorizes the query's transaction snapshot, not all future access.
The trail records a packet plan, not proof that stdout reached its recipient.
`latency_micros` includes store opening and query work but excludes JSON output and
store close; external process timing is needed for full cold-CLI cost. Existing
published measurements remain bound to their recorded source manifests, not to
later implementations.

## Authorized trail inspection and source controls

Replace `TRAIL_ID` with the identifier returned by a query.

```sh
./mousa -store local.sqlite trail ./documents TRAIL_ID
./mousa -store local.sqlite trail --source inspection-notes TRAIL_ID
./mousa -store local.sqlite access --source inspection-notes deny
./mousa -store local.sqlite access --source inspection-notes allow
./mousa -store local.sqlite withdraw --source inspection-notes
```

Use the `trail_id` returned by a query. Inspection evaluates fresh source access
before looking up history. A deny response contains authorization IDs/reasons but
no historical payload, even for a nonexistent trail. An allowed request cannot
inspect another source's trail.

The `historical` payload is a filtered view, not a complete canonical Source Trail
record from which its ID can be recomputed. It contains the original decision
outcome/reasons, effective expression, byte accounting, packet ID, and accepted
candidate metadata. Rejected candidates are counted but their identities, hashes,
and other metadata are omitted. The trail stores the effective expression, not
the raw query or the name of its preparation policy; inspection cannot reconstruct
those missing fields.

`selected` describes the historical packet. `indexed_now` describes index
membership in the inspection snapshot. Retired revisions can have historical
metadata without current index membership; neither property authorizes a new text
release. Inspection never returns historical text.

### Versioned exact-packing explanations

Original packing writes canonical `mousa.source_trail.v1` records without rewriting
historical bytes or IDs. Exact packing writes `mousa.source_trail.v2` with
`packing_policy: "exact-v1"`. Its inspected view adds `schema` and `packing_policy`.
Accepted unselected candidates have `omission: "budget"` or `"duplicate"`;
duplicates also have `duplicate_of`, the segment ID of an earlier selected
candidate in that packet. Selected and rejected candidates have no packing
omission. Historical rejected metadata remains excluded from the inspected view.

The v2 trail ID binds the policy and relationships. The packet ID still binds the
budget and ordered selected segment IDs, digests, ranks and byte sizes; equal
selections under different policies can have the same packet ID. Unknown versions
and invalid version/policy combinations fail. V1 decoders reject v2-only fields,
even empty ones. Older executables that understand only v1 cannot reopen stores
containing v2 trails; upgrade readers before opting in.

Text-free validation checks ranks, sizes, accounting and a prior selected target
with the same digest and size. It rejects dangling, forward, cyclic and conflicting
relationships. Recomputing an ID does not prove the asserted bytes are equal.
Creation compares verified source text. Store reads and reopening additionally
check canonical segment metadata and source ancestry, and compare still-indexed
verified text. After update or deletion, retired text is not retained: its
historical equality cannot be re-proved from this text-free record alone.
Neither a trail ID nor a content digest is a signature against an attacker who
can rewrite the entire store. Inspection still requires fresh source authorization.

`access` manages a source-scoped allow/deny binding for the fixed CLI caller and
retrieval purpose. Concurrent changes use the core compare-and-swap activation
contract; conflicts are errors, not silent overwrites. This is an operator control,
not caller authentication or protection against someone who can directly read or
modify the store.

`withdraw` changes source lifecycle state without erasing index or canonical
records. Policy allow does not resume a withdrawn source. The CLI has no resume
command, and sync does not implicitly resume it. Item deletion/restoration is a
different operation from source withdrawal.

Directory identities remain usable by `query`, `status`, `trail`, `access`, and
`withdraw` after the physical directory disappears. `sync` still requires an
existing directory. Stored activation is not a claim about current filesystem
contents. Integrity checks, authorization, current activation, and factual
correctness are separate properties; none substitutes for the others.

### Declared associated context

`query --associations <file>` opts a caller into declared associated context.
The file is one strict JSON document, schema
`mousa.association_declarations.v1`, listing up to 32 declarations. Each
declaration names a `from_item` and `to_item` (item paths of the queried
source), a non-empty `basis`, and a non-empty `author`. Duplicate
`(from_item, to_item)` pairs and self-references are rejected. Mousa never
infers relationships and never treats a declaration as discovery; the author
and basis remain attached to every passage the declaration releases.

MCP `mousa_query` accepts the complete document as optional inline `associations`,
decoded from its original nested JSON bytes by the same native codec. This
source change is pending PR review, not a release; after merge, a source build
containing it supports the input. Refresh cached closed tool schemas.
Omission preserves exact default response properties. Supplied null, wrong
shape/schema, empty lists, unknown/repeated keys at any depth, invalid or repeated
declarations and more than 32 declarations return `invalid_arguments` before
producing query/trail/packet state. Item names are bounded to 1024 UTF-8 bytes,
basis to 512 and author to 128; schema character bounds do not replace byte checks.
The existing transport frame bound still applies. No path, URL or additional
source selector is accepted. Both term policies and byte packers work; omitted
or false `supersession` works. True supersession, any token budget/reservation
and vector/hybrid association requests reject during argument validation.
The other seven tools and the `mousa.mcp_result.v1` envelope are unchanged.
Source text and declaration strings are untrusted data; `author` is not an
authenticated identity. No ingestion permission is needed for retrieval.
`mousa_trail` already exposes verified v3 metadata under fresh source
authorization, including its ordinary authorization audit write. An unfired
declaration preserves native unassociated version/identity rules, not fresh
request/decision/trail IDs across separate calls.

A declaration is honored only when the declaring item contributed selected
primary evidence to that packet, so an unrelated query releases nothing.
Depth is one: the target's passages are never followed by further
declarations. At most four distinct target items are read per query; repeated
targets and declarations beyond the cap are recorded as omissions
(`duplicate_target`, `fan_out`), as are unknown targets (`target_unknown`) and
inactive or deleted ones (`target_inactive`). A relationship is not an access
grant: resolution stays inside the one allowed source decision and only reads
the current active revision of the target. Equal text does not transfer
permissions between items.

A query considers at most 256 target passages, counted across the targets its
declarations name, in declaration order and then in the target's document order.
A target with more passages is read only up to that bound, and its remaining
passages are recorded as `target_truncated`; the passages that were considered
keep their own rows, released or omitted. The bound is derived from the largest
row a declaration can produce: 1024 bytes of item identity for each side, 512
bytes of basis and 128 bytes of author, whose JSON encoding escapes control
characters at six bytes each, so 256 rows stay under a quarter of the canonical
record limit. Without it, a legal large target could exceed that limit and fail
the whole request. The target's passages are read before packing, so added cost
follows the number of passages considered rather than the bytes that fit, and a
query whose remaining budget cannot hold a passage still reads the target and
records the omissions.

`target_truncated` extends the v3 omission-reason vocabulary. Consumers that
enumerate the earlier v3 reasons must accept this value before reading a
truncated trail. The omitted target passages have no rows in that trail.

Associated passages pack into the remaining byte budget after primary
evidence, in declaration order and selector order. Primary evidence keeps its
existing selection and priority; an associated passage never displaces it.
Each associated passage keeps its own item, segment identity, representation
digest, byte coordinates, content digest and text, and is returned with
`origin: "association"` plus the declaration; lexical hits carry
`origin: "lexical"` and no declaration. Associated passages have no lexical
rank or BM25 score. A passage that does not fit is omitted with reason
`budget`; byte-equal duplicates are omitted with `duplicate` and a reference to
the released passage under exact-v1 packing. Small budgets therefore expose
omission without claiming completeness.

A trail that records an association stage is a `mousa.source_trail.v3` record
with `associated` rows and `association_omissions`, and its packet identity
uses a v2 domain that binds every released byte. Its inspected view adds
`associated` and `association_omissions`. V1 and v2 encoding, decoding and
validation are unchanged; a v3 record appears only when the association stage
has something to record, so an unfired declaration reproduces the unassociated
trail exactly. Older executables that understand only v1/v2 reject v3 records
rather than misreading them. As with all trail records, identities are
text-free explanations, not signatures, and inspection still requires fresh
source authorization.

Reading a stored trail re-verifies both stages against the store. Every released
passage must still decode from its canonical record with the recorded digest and
size, still belong to its representation and to the decision source, and still
hash its retained indexed text. An associated row is checked the same way,
including that the row names the item its segment belongs to and that its
declaring item contributed selected primary evidence. Membership comes from
immutable representation ancestry, so a trail stays readable after the target
item is revised, deactivated or removed. A v3 record validates its candidates
under the packing policy it records: an original-policy trail keeps repeated
byte-equal passages valid, while an exact-v1 trail still has to name the
duplicate it omitted.

## Maintained agent skills

The generated plugin includes `mousa-setup` and `mousa-evidence` for consent, configured-source retrieval and authorized provenance. They distinguish retrieval policy from packing policy and native administration from the MCP tool surface. Administration alone leaves default queries unchanged; an explicitly opted-in native or MCP query applies exact pinned-predecessor withholding.

The repository-owned [mousa-development skill](../skills/mousa-development/SKILL.md) guides implementation and review of canonical records, transaction snapshots, compatibility and reproducible evidence. Development hosts can register the `skills/` directory with their skill loader and read this skill when working on Mousa. Client skills remain bundled with the plugin; they are not development startup instructions.
