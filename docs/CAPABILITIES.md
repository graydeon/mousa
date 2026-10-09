# Supported local workflow

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
| Source lifecycle and retrieval policy decisions | Yes | Fresh query evaluation; `access` and `withdraw` | CLI deny/allow, source withdrawal, and cross-source isolation | Evolving CLI example and warm current-query costs; not an isolated policy ablation |
| Byte-budget evidence selection | Yes | `query --budget-bytes <positive>`; opt-in `--packing-policy exact-v1` | Byte boundaries, skip-oversized selection, duplicate displacement, versioned explanations and historical compatibility | Bounded exact-content packing measurements; no general retrieval-quality claim |
| Declared associated context | Yes | Opt-in `query --associations <file>` | Same-source authorization, selected-primary trigger, bounded fan-out/target passages, omission reasons and v3 trail readback | [Caller-reviewed backup example](../examples/backup/README.md) and its bounded development cases; no general retrieval-quality claim |
| Explicit query-term policy | Yes | Default `original`; explicit `--policy dedup` | Repetition changes ranking; shared expression capping | Published BEIR original/dedup results, not a new CLI quality claim |
| Durable Source Trails and context packet IDs | Yes | Every query; `trail` inspection | Actual-CLI ID round-trip, current authorization, retired revisions; transaction rollback and rejected metadata filtering | Cold CLI and separate warm traced/current-query observations |
| Classification records | Yes | No administration command | Canonical storage and validation | None; not automatic classification or classification-based authorization |
| Revision-pinned supersession declarations | Yes | `supersession declaration put`, `supersession declaration get <id>` | Strict identity/codec, immutable SQLite storage, same-source item/revision provenance, retry, restart and integrity failures; actual-CLI input bound, malformed/duplicate/unknown/trailing/oversized input, unpinned targets, missing identity, absent store, read-only read, rejected writes leaving stored bytes unchanged | None; declarations do not filter retrieval or establish factual truth |
| Supersession activation administration | Yes | `supersession activation put`, `supersession activation get <id>`, `supersession activation state <source-id>` | Strict identity/codec, immutable SQLite events and verified current projection, initial selection, replacement, deactivation, reactivation, exact retry, stale/competing/redundant rejection, metadata replay under an existing identity, historical event reads, current-state projection with canonical identity strings, explicit null declaration and per-source isolation, corrupt history/projection rejection without repair | None; recorded activation does not move item pointers, filter retrieval or establish factual truth |
| Transaction-local supersession consultation | Yes | No command; internal Go API | Verified current state and the declaration it selects read through one supplied query handle: no-history, initial selection, replacement, deactivation and reactivation, the caller's snapshot kept while another connection commits a transition, and missing projection, projection naming a missing current event or a missing selected declaration classified as an integrity failure (with the historical event reader still not-found for an unknown identity), all rejected with no repair or write | None; no caller consults it yet, retrieval still releases every candidate, and v4 content and startup verification are absent |
| Supersession selection contract | Yes | No command; internal Go API | Consultation/denial/deactivation shapes, exact predecessor-pin matching, source isolation, rejected and non-matching candidates, row order and copying, structural validation failures | None; the API is not called by retrieval and withholds nothing |
| Opt-in supersession trail and survivor packing | Yes | No command; internal Go API | `mousa.source_trail.v4` version boundary, required closed consultation member, strict member codec and identity binding, no-history/deactivation/active/denial records, survivor-aware `original` and `exact-v1` packing including an ordinary budget skip and a withheld duplicate copy, row membership/order/pins, canonical size and input ownership, shared candidate invariants under both policies, unchanged v1–v3 bytes and identities | None; no retrieval path builds one, no store writes or content-verifies one, and nothing withholds evidence |
| Semantic/hybrid retrieval, model inference, answer generation | No | No | Not implemented | None |
| Local stdio MCP | Yes | `mcp --caller <id> --source <id>` | Real SDK client/executable round trips, persistence, configured boundaries, committed prefix, framing, cancellation and shutdown | None; acceptance is not a performance or model-driven evaluation |
| OpenAI MCP Extensions | Yes | Opt-in `mcp --openai-extensions`; `plugin` packaging | Real executable mention/resource authorization and reconnect; native Codex install, component recognition, tool discovery and resource read; actual desktop rendering not verified | None; protocol/client checks are not model-driven evaluation |
| OAuth-protected Streamable HTTP | Yes | Opt-in `mcp --http-config FILE` | Local TLS introspection fixture covers subject, audience, issuer, expiry, scopes, revocation and host/origin boundaries | None; live identity-provider/deployment interoperability not verified |
| Stable SDK, general connectors, public pairing relay | No supported interface | No | Not implemented as supported interfaces | None |

The [Git documentation consumer](../examples/docs/README.md) is a separate
Python CLI example, not a core or `cmd/mousa` token-budget feature. Its opt-in
`ask --context-tokens` counts a rendered prompt with pinned `o200k_base`; the
returned byte-packed packet, message framing and reserved output are outside
that cap. The [pinned development case](https://github.com/graydeon/mousa-benchmarks/tree/4a9a477907b7f0eb7af708118e50c047ba9b2a5d/results/2026-09-29-context-512)
keeps the fragment and verified parent passage at 511 of 512 content tokens
by omitting a duplicate path from citation headers; an unrelated `git-switch`
passage is not selected. It does not establish model-window safety, answer
quality or general relevance.

## Supersession core boundary

The internal Go API stores source-local declarations pinned to exact predecessor
and successor item revisions. Separate immutable activation events select at most
one declaration per source, with an explicit expected predecessor and a verified
current-state projection. Deactivation retains its event in history. Actor, time
and reason are recorded labels, not authenticated authorship.

These APIs do not change item activation, lexical retrieval, ranking, packing or
existing Source Trails. Declaration and activation-event administration are
reachable as trusted local administration through `supersession declaration put`,
`supersession declaration get <declaration-id>`, `supersession activation put`,
`supersession activation get <activation-id>` and
`supersession activation state <source-id>` in `cmd/mousa`, where the state command
is a read-only projection of the verified current state. MCP declaration surfaces,
activation listing or history commands, and supersession query enforcement remain
unimplemented. A proposed opt-in enforcement contract is recorded in
[opt-in supersession enforcement](SUPERSESSION_ENFORCEMENT.md): both of its domain
halves and the transaction-local consultation of the verified current state are
implemented, they change no default behavior, no retrieval path reaches them, and no
packet version exists for them.
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
responsibilities, so a hash never stands in for integrity or permission. No retrieval
path calls it, no trail member or version records it, and nothing reachable from the CLI
or MCP withholds evidence.

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

Scope of the checks: they are structural and local. They read no store, so they prove no
stored existence, canonical ancestry, authorization or transaction-local current state.
No retrieval path calls the constructor, no store writes a v4 record, a stored v4 record
is not content-verified on read or startup, and no CLI or MCP surface can request one,
which is why no query withholds evidence. The trail's own decode is reachable through
`DecodeSourceTrail` for any caller that can already present those bytes.

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

These checks are store verification, not enforcement. No retrieval path consults the reader, no
command or MCP surface reaches it, a stored v4 record is still not content-verified on read or
startup, and no query withholds evidence.

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
and grant no retrieval permission, filter no query, move no item pointer, mutate no
declaration and expose no document text.

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
source alias or name lookup, and no supersession MCP tool.

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
Supersession query enforcement, semantic evaluation or full-window memory
acceptance has not been demonstrated.

## Local stdio MCP

`mousa -store STORE mcp --caller cli --source inspection-notes
--ingest-source inspection-notes` serves one local client over stdin/stdout.
Repeat `--source` for each permitted JSONL source, and repeat `--ingest-source`
only for sources that may accept items. Writes are disabled by default. Source
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
| `mousa_query` | `source`, `query`, `policy`: `original` or `dedup`, `budget_bytes` | `packing_policy`: `original` (default) or `exact-v1` | Existing evidence response, including authorization, selected text/ranges/digests, packet and trail IDs |
| `mousa_trail` | `source`, `trail_id` | None | Existing filtered trail inspection with fresh authorization; no evidence text |

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
Authorization exclusions are successful query/inspection results, not tool
errors: they carry current decision reasons but no selected evidence or
historical trail payload. An allowed request for another source's trail returns
`not_found`.

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
| Released-text budget | Integer from 1 through 65,536 bytes |
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
`--openai-extensions`. The base four tools are unchanged; one additional
`mousa_mentions` tool and an evidence resource template are advertised.

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

The setup skill explains sharing and asks the user to confirm sources before
retrieval. The evidence skill explains querying, quotations and Source Trails.
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
claims fail closed. `mousa:read` is required for all MCP requests; ingestion also
requires `mousa:write` and startup source permission. This is not an API-key login
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

```json
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

`budget_omitted` and `lifecycle_excluded` count considered candidates. Exact packing
also returns `packing_policy: "exact-v1"` and `duplicate_omitted`, including zero.
These added fields are absent with original packing, preserving default output.
Selected count is `len(evidence)`; selected, duplicate-omitted, budget-omitted and
lifecycle-rejected counts sum to `matched_candidates`. Duplicate omission is not
a lifecycle rejection and does not hide the candidate from its trail.

If accepted candidates exist but none fit, the outcome is `budget_omitted`;
duplicates require a selected retained passage, so there is no separate
duplicate-only outcome. A source-level gate runs before search, so its candidate
counters are zero; that does not assert the corpus has no matches.
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

The generated plugin includes `mousa-setup` and `mousa-evidence` for consent, configured-source retrieval and authorized provenance. They distinguish retrieval policy from packing policy and native administration from the MCP tool surface. Supersession administration does not currently withhold query evidence.

The repository-owned [mousa-development skill](../skills/mousa-development/SKILL.md) guides implementation and review of canonical records, transaction snapshots, compatibility and reproducible evidence. Development hosts can register the `skills/` directory with their skill loader and read this skill when working on Mousa. Client skills remain bundled with the plugin; they are not development startup instructions.
