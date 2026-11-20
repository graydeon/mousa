# Supported local workflow

For source-build versus tagged-release availability,
see [development status](OVERVIEW.md#source-and-release-status).

Mousa is pre-alpha. The supported executable is `cmd/mousa`, built from source with
Go 1.25 or newer. The core packages are internal, not a stable SDK. Its CLI and
MCP interfaces store and retrieve evidence; none generates answers or establishes
factual truth.

## Capability matrix

“Core” means an implementation exists. “CLI” means an operator can use it through
`cmd/mousa`. Passing behavior tests do not establish retrieval quality or performance
on other workloads. Measurements are published separately in
[mousa-benchmarks](https://github.com/graydeon/mousa-benchmarks); the pre-rework
research record is archived at [archive/research-pre-rework.md](archive/research-pre-rework.md)
and does not describe the current code.

| Capability | Core | CLI | Behavioral coverage |
|---|---|---|---|
| UTF-8 directory import and resync | Yes | `sync <dir>` | Import, repeated no-op, update, revert, delete, restore, restart |
| Directory preview and selection controls | Yes | `sync --preview`, include/exclude globs, explicit overrides and limits | Actual-CLI scope changes, preview without store creation, failed validation preserving activation, rooted link rejection |
| JSONL item input | Yes | `sync --source <id>` | Retry, replacement, tombstone, malformed input, committed prefix, input limits |
| Raw-content identity and normalized text | Yes | Both input forms | CRLF, BOM, empty text, exact retries |
| Versioned passage boundaries | Yes | `sync --segment-policy passage-v1`; source-built BEIR `-segment-policy fixed-v1|passage-v1` | Exact source slices, policy replacement, retries, interrupted activation, historical identities and source exclusion; evaluator identity/reuse/resume and explicit comparison axis |
| Atomic current-item activation | Yes | Both input forms | Transaction failure, reader snapshots, abrupt exit, retry |
| Source-scoped lexical retrieval | Yes | `query` | Cross-source isolation and current-only evidence |
| Caller-supplied exact vector retrieval | Yes | Native `vector inputs`/`vector import` and `query --vector-space`; MCP `mousa_vector_inputs`/`mousa_vector_import`/`mousa_vector_query` for configured JSONL | Synthetic cosine/tie arithmetic, strict imports/arguments, current-only source pins, explicit write gates, partial coverage, restart, both packing policies, v5 history |
| Hybrid lexical/vector retrieval | Yes | Native `query --hybrid-space`; MCP `mousa_hybrid_query` for configured JSONL | Equal-weight RRF(k=60), current snapshot, partial/zero coverage, both packers, v6 branch history |
| Source lifecycle (withdrawal) | Yes | `withdraw` | Withdrawn sources release no evidence; cross-source isolation |
| Per-source access decisions | Yes | `access <dir>|--source <id> allow|deny`; `sync --allow`; default deny with no event | Append-only events, default deny, no-op repeats, MCP-synced sources denied until native allow; see [Access](USAGE.md#access) |
| Byte-budget evidence selection | Yes | `query --budget-bytes <positive>`; opt-in `--packing-policy exact-v1` | Byte boundaries, skip-oversized selection, duplicate displacement, versioned explanations and historical compatibility |
| Explicit query-term policy | Yes | Default `original`; explicit `--policy dedup` | Repetition changes ranking; shared expression capping |
| Durable Source Trails and context packet IDs | Yes | Every query; `trail` inspection | Actual-CLI ID round-trip, current authorization, retired revisions; transaction rollback and rejected metadata filtering |
| Embedding inference, answer generation | No | No | Not implemented |
| Local stdio MCP | Yes | `mcp --caller <id> --source <id>` | Real SDK client/executable round trips, persistence, configured boundaries, committed prefix, framing, cancellation and shutdown |
| OAuth-protected Streamable HTTP | Yes | Opt-in `mcp --http-config FILE` | Local TLS introspection fixture covers subject, audience, issuer, expiry, scopes, revocation and host/origin boundaries |
| Stable SDK, general connectors, public pairing relay | No supported interface | No | Not implemented as supported interfaces |

The [Git documentation example](../examples/docs/README.md) is a separate Python CLI
example. It returns source passages and does not generate an answer.

## Caller-supplied exact vector retrieval

The [native workflow and synthetic example](USAGE.md#caller-supplied-vector-workflow)
exports current normalized passage bytes, imports caller-produced embeddings and
queries one source in one explicitly named space. Lexical queries keep their
previous default, fields, packet identities and v1/v2 trail encodings. Mousa does
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

The schema has `vector_spaces` and `embeddings` tables, plus
indexes for bounded current-item and passage enumeration. Space identities bind
the complete descriptor; embedding identities bind space, source, segment,
representation/content pins and supplied values. A unique source/segment/space
slot prevents replacement under a different content identity. Canonical reads and
startup scans verify bytes, identities, projections and immutable source ancestry
without repairing corruption. No old record is rewritten.

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

Every vector query writes `mousa.source_trail.v5`, including denied and empty
queries. Its closed `vector` member binds the descriptor, space/query-vector
identities, query values, observed eligible/indexed counts and every ranked
embedding/source/segment/representation/content pin and cosine score. The usual
candidate rows bind rank, size, selected flags and packing omissions; v5 has no
lexical expression. Packet v1 identities
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
Vector mode rejects explicit `--policy`; declared support is described below.
MCP `mousa_vector_query` uses the same native transaction for configured JSONL
sources. `mousa_trail` can release recorded vector facts with `retrieval_details`.
No SDK or connector framework is introduced.

## Native hybrid lexical and vector retrieval

`query --hybrid-space SPACE_ID [--policy original|dedup] SOURCE TEXT` takes the
same strict `mousa.vector_query.v1` stdin object as vector-only mode. Use either
a directory argument or `--source ID`, not both. See the
[runnable example](USAGE.md#hybrid-lexical-and-vector-query). Text is an independent
caller input; Mousa does not verify that the supplied vector was generated from it.
Explicit hybrid mode rejects any `--vector-space` flag, including empty values.
It accepts declared support. No embedding work or network call runs.

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

Each hybrid query writes closed `mousa.source_trail.v6` history. It binds request
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
relaxing v1, v2 and v5 readers or canonical bytes. MCP `mousa_hybrid_query` uses the same
native transaction; opted-in history releases recorded branch/fusion facts.

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

Access is per source and applies to every caller, including MCP. MCP exposes no
access tool and `mousa_sync` has no allow option, so an MCP-synced source stays
denied until the operator runs `access ... allow` natively; see
[Access](USAGE.md#access). MCP does not authenticate the startup identity.

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
| `mousa_status` | `source` | None | Existing status report: collection state, active items and observations |
| `mousa_query` | `source`, `query`, `policy`: `original` or `dedup`, `budget_bytes` | `packing_policy`: `original` (default) or `exact-v1` | Existing evidence response, including authorization, selected text/ranges/digests, packet and trail IDs |
| `mousa_trail` | `source`, `trail_id` | `retrieval_details` boolean (default `false`) | Filtered history under fresh authorization; no text. The opt-in adds only the recorded vector/hybrid facts that exist |
| `mousa_vector_query` | `source`, `space_id`, `values`, `budget_bytes` | `packing_policy`: `original` (default) or `exact-v1` | Primary evidence, cosine/ranks, coverage and producing IDs |
| `mousa_hybrid_query` | Same as vector plus lexical `query` and `policy`: `original` or `dedup` | Same packing option | Primary evidence and actual branch ranks, BM25/cosine and RRF(k60) |
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
without those facts has no added member.
Fresh authorization precedes historical lookup; denied inspection discloses no
history or existence. Restart and revision replacement preserve recorded facts,
while `indexed_now` can change. Retired passage bytes remain unarchived, and
historical completeness/BM25 are producing observations, not reconstructed proof.
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

`mousa_sync` records no access event, so the source is denied until the operator
runs `mousa -store STORE access --source inspection-notes allow` natively. Then
call `mousa_query` with:

```json
{"source":"inspection-notes","query":"harbor inspection","policy":"original","budget_bytes":4096}
```

Use the returned `result.trail_id` with `mousa_trail`. Items use the same strict
record validation, normalization, immutable revision identities and atomic
current-item activation as [JSONL input](#jsonl-input). There are no new
packing rules. Evidence coordinates refer to
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
remain unchanged. 
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

`status` distinguishes `active_items` from historical `observations`. Source collection state is separate: an active source can contain
zero active items.

For sync reports, `total_items` counts selected directory files or processed JSONL
records, not the source's active item count. `store_bytes` samples the main database
file before close and excludes WAL and other files; it is not total disk usage.

## Ingestion segment policies

Both input forms accept `--segment-policy fixed-v1|passage-v1`. The default is
`fixed-v1`; query-term `--policy` is a separate option. For example:

```sh
./mousa -store local.sqlite sync --segment-policy passage-v1 --allow ./documents
./mousa -store local.sqlite query --budget-bytes 1024 ./documents 'restart procedure'
./mousa -store notes.sqlite sync --source notes --segment-policy passage-v1 --allow < items.jsonl
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
No schema change or historical ID rewrite is needed. Returning to an earlier
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
matching segments.

## Store schema and older stores

A store has one schema. Mousa marks it in the SQLite header (`application_id`
and `user_version`) and checks that header before it reads or verifies any record.
There are no migrations. A store written before the schema was collapsed, or by
an earlier schema version, is refused with an `incompatible_schema` error that
says to create a new store and re-sync it from its sources. A SQLite file that is
not a Mousa store and a store from a newer binary are refused the same way. A
refused store is not modified.

Startup verification opens a writable store once, including the writable FTS
structural integrity check. Canonical records, ingestion records and local-item
activation checks each use a read snapshot. Observation, artifact and segment
records are verified directly from ordered scans with the same canonical decoding
and projection checks as individual reads. Each source's ingestion state is
verified once per ingestion snapshot; all receipts, withdrawals and state rows
remain checked. No verification result is reused across snapshots, opening passes
or operations.

These snapshots can retain WAL pages while a concurrent writer commits. They
end when their verification phase finishes or fails. Startup reads and verifies
retained history, so initialization still depends on corpus size.

## JSONL input

Each nonblank line contains one object with `id` and exactly one of `text` or
`deleted`. For deletion, `deleted` must be `true`.

```jsonl
{"id":"notes/review@draft","text":"The harbor inspection is scheduled for Friday."}
{"id":"notes/withdrawn","deleted":true}
```

```sh
./mousa -store local.sqlite sync --source inspection-notes --allow < items.jsonl
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
./mousa -store local.sqlite sync --include '*.md' --exclude 'drafts' --allow ./documents
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

Valid queries open the store writable and may create it before a later
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

`budget_omitted` and `lifecycle_excluded` count disjoint considered candidates.
Exact packing returns `duplicate_omitted`, including zero; default original
responses omit the optional fields. Selected, duplicate, budget and lifecycle
counts sum to `matched_candidates`, treating absent optional counts as zero.

Outcome precedence is denial (with the existing lifecycle override), selected evidence,
budget omission, lifecycle exclusion, then no matches.
Duplicates require an earlier selected survivor, so there is no duplicate-only empty
outcome. A source-level gate runs before search, so candidate counters are zero;
that does not assert the corpus has no matches.
`decision_reasons` records source-level evaluation reasons without candidate metadata.

The decision authorizes the query's transaction snapshot, not all future access.
The trail records a packet plan, not proof that stdout reached its recipient.
`latency_micros` includes store opening and query work but excludes JSON output and
store close; external process timing is needed for full cold-CLI cost.

## Authorized trail inspection and withdrawal

Replace `TRAIL_ID` with the identifier returned by a query.

```sh
./mousa -store local.sqlite trail ./documents TRAIL_ID
./mousa -store local.sqlite trail --source inspection-notes TRAIL_ID
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

Source-scoped inspection first establishes ownership through the typed decision parent.
If that parent is missing in an already-open inspector, own-source and foreign-source
orphans remain indistinguishable from unknown IDs: all return `not_found`. The
unscoped canonical reader and fresh-open startup verifier reject that damage as
`integrity`. No path repairs damage or releases partial history.

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

Per-source access is an append-only event log with default deny; see
[Access](USAGE.md#access). It is an operator
control, not caller authentication, and it does not protect against someone who can read
or modify the store directly.

`withdraw` changes source lifecycle state without erasing index or canonical
records. The CLI has no resume command, and sync does not implicitly resume a withdrawn
source. Item deletion/restoration is a different operation from source withdrawal.

Directory identities remain usable by `query`, `status`, `trail`, and
`withdraw` after the physical directory disappears. `sync` still requires an
existing directory. Stored activation is not a claim about current filesystem
contents. Integrity checks, authorization, current activation, and factual
correctness are separate properties; none substitutes for the others.

## Maintained agent skills

The repository-owned [mousa-development skill](../skills/mousa-development/SKILL.md) guides implementation and review of canonical records, transaction snapshots, compatibility and reproducible evidence. Development hosts can register the `skills/` directory with their skill loader and read this skill when working on Mousa.
