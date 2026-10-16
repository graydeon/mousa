# MCP supersession opt-in

**Status: the `mousa_query` and `mousa_trail` opt-ins are implemented; their
independent client captures are separate.** This document specifies one explicit optional
`supersession` boolean on the existing `mousa_query` tool, and a matching opt-in on
`mousa_trail` for inspecting what a query recorded. The current build accepts the
argument on both tools, declares the count the query reports and the recorded member the
inspection returns, and reuses the native transaction and the verified inspection read;
the query cases of the acceptance matrix are covered by product regressions in
`cmd/mousa/mcp_supersession_test.go` and the inspection cases by
`cmd/mousa/mcp_trail_supersession_test.go`. Independent query-only and
2026-10-13 inspection observations are published separately in the benchmark archive
linked in section 6. Both milestones are accepted; their observations retain their
own source pins. The JSON blocks below are documentation examples with placeholder
identities, not captured responses.

The implemented native contract remains
[Opt-in supersession enforcement](SUPERSESSION_ENFORCEMENT.md). This interface adds a
client boundary for that contract. It changes no native behavior, storage object,
canonical record, schema version, migration, dependency or benchmark file.

## Recommendation

Extend the two existing tools with **declared optional inputs and declared optional
result properties**, and keep the envelope identifier `mousa.mcp_result.v1`:

| Surface | Change | Present only when |
|---|---|---|
| `mousa_query` input | optional `supersession`: boolean (implemented) | supplied `true` |
| `mousa_query` result | optional `supersession_excluded`: integer (implemented) | the caller opted in, including value `0` |
| `mousa_trail` input | optional `supersession`: boolean (implemented) | supplied `true` |
| `mousa_trail` result `historical` | optional `supersession`: object (implemented) | the caller opted in and the recorded trail is `mousa.source_trail.v4` |

Reasons, in detail below: a tool publishes one static output schema per session, that
schema is closed, and a validating client rejects a response field the schema does not
declare, so the opt-in result must be declared; declaring an optional property is
additive for every caller that does not use it; and the alternative of a second
envelope version or a second tool widens the discovered surface without adding a
compatibility guarantee.

## Current boundary

The client boundary of the implemented build, from the current source:

- `decodeMCPArguments` in `cmd/mousa/mcp_tools.go` accepts exactly the field names listed
  per operation, which include `supersession` for `mousa_query` and `mousa_trail`. Any
  other name, a duplicate name, a `null` value and a value of the wrong type are rejected
  before the operation switch, and the call returns
  `mcpFailure(operation, "", "invalid_arguments", ...)` with no `result`.
- `mcpOutputSchema` in the same file declares `supersession_excluded` for `mousa_query`
  and the recorded `supersession` member inside `mousa_trail`'s `historical` schema, and
  maps the activation, declaration and representation identity types that member contains
  to the existing lowercase hex string declaration. The other three output schemas are
  unchanged from the query-only build.
- `(*mcpApplication).call` in `cmd/mousa/mcp.go` runs `mousa_query` through
  `queryItemsForRequestWithSupersession` with the caller's boolean and a nil association
  list, and inspects the trail through `Store.InspectSourceTrail` in both cases, clearing
  `inspection.Historical.Supersession` only when the caller did not opt in.
- `mcpCommand` opens the canonical store once, before the server accepts requests. No
  tool call opens, creates or migrates a store, so an MCP argument has no store-open
  boundary to change.

The native side already implements everything the opt-in needs:

- `Store.EvaluateAndTraceSupersessionLexical` (`internal/sqlite/trail.go`) and its
  `traceLexical` body evaluate authorization, verify candidates, consult administration
  for an allow, build `mousa.source_trail.v4`, insert it, read it back canonically and
  clear withheld text in a copied candidate slice, all inside one writer transaction.
- `queryItemsForRequestWithSupersession` (`cmd/mousa/query.go`) is the existing
  CLI reuse point for that path, including the rejection of a supersession-plus-association
  combination.
- `readSupersessionConsultation` (`internal/sqlite/supersession_activation.go`) reads the
  verified current state and selected declaration through a caller-supplied handle, so no
  second transaction is opened.
- `Store.InspectSourceTrail` (`internal/sqlite/trail_inspection.go`) already returns the
  recorded `supersession` member for a v4 record after a fresh authorization decision;
  the MCP handler is what discards it.

Three schema facts decide the output contract:

1. Each tool publishes one static output schema, discovered through `tools/list`, that
   cannot depend on the arguments of a later call.
2. `Server.AddTool` in the official Go SDK documents output validation as the caller's
   responsibility, and Mousa's own acceptance client resolves each discovered
   `outputSchema` and validates every envelope against it (`cmd/mousa/mcp_test.go`).
3. The discovered schemas are closed, so a response field the schema does not declare is
   a violation for any validating client.

## Terms

Existing terms keep their meaning from the
[enforcement contract](SUPERSESSION_ENFORCEMENT.md#terms) and the
[capability matrix](CAPABILITIES.md#supersession-core-boundary): **released candidate**,
**pin**, **active declaration**, **suppressed candidate** and **selection disposition**.

| Term | Meaning here |
|---|---|
| Tool input schema | The JSON Schema a client discovers for a tool's arguments. It is closed, so an undeclared input field is invalid. |
| Tool output schema | The JSON Schema a client discovers for a tool's `structuredContent`, including the `mousa.mcp_result.v1` envelope fields. |
| Envelope | The `schema` / `operation` / `source` / `result` / `error` object every completed call returns. |
| Opted-in call | A call that supplies `supersession: true` on a tool that declares the input. |
| Opted-out call | A call that omits the input or supplies `supersession: false`. |

## 1. Input compatibility

**Decision.** `supersession` is an optional boolean on `mousa_query` (and, separately,
on `mousa_trail` in section 5). Omission and explicit `false` are the same request: both
take the existing default path. Only explicit `true` opts in.

| Input | Result | Why |
|---|---|---|
| Field omitted | Legacy path; response property set unchanged | The field is optional and defaults to `false` |
| `false` | Same path, property set and stable semantic values as omission | The opt-in is one boolean value, not a presence marker |
| `true` | `queryItemsForRequestWithSupersession(..., nil, true)` on `mousa_query`; the recorded member is returned by `mousa_trail` | The existing CLI entry point already implements the query behavior, and the inspection read already verifies the member |
| `null` | Rejected: `invalid_arguments` | The decoder refuses any null field value |
| `"true"`, `"false"` | Rejected: `invalid_arguments` | A string is not the declared boolean type; no truthy coercion |
| `1`, `0` | Rejected: `invalid_arguments` | A number is not the declared boolean type |
| Repeated `supersession` key | Rejected: `invalid_arguments` | Existing duplicate-field rule |
| Unknown field name alongside the opt-in | Rejected: `invalid_arguments` | Existing unknown-field rule |
| `supersession` on `mousa_sync` or `mousa_status` | Rejected: `invalid_arguments` | Each operation declares its own accepted field list |
| Arguments that are not an object, or that carry data after the object | Rejected: `invalid_arguments` | Existing arguments-object and trailing-data rules; a valid JSON-RPC frame cannot carry the second form, so it is asserted at the decoder boundary |

**Reasons.** Strict typing keeps one representation per concept: the decoder's existing
rules already reject null, duplicate and unknown fields, and a boolean input that
silently accepted `"true"` or `1` would make the argument's meaning depend on the
decoder rather than on the declared schema. Omission and `false` must be
indistinguishable because the repository treats a default query as the published
compatibility boundary, not as a spelling to be detected.

**Discovery.** Each opted-in tool's input schema declares one optional property,
`"supersession": {"type": "boolean"}`, whose description states the withholding rule for
`mousa_query` and the recorded member for `mousa_trail`. Both declared tools carry it and
neither makes it required; `mousa_sync` and `mousa_status` keep their accepted field
lists, input schemas and output schemas unchanged, and the query-only build's three other
output schemas stay byte-identical as section 5 records.

**Unchanged boundaries for opted-out callers.** Source selection, the required fields,
the error envelope with its `code`, `message`, `completed_items` and `failed_item`
fields, the `source` value in a failure envelope, and the per-operation accepted-field
list otherwise stay as they are. The store is opened once at process start, so no input
value affects store creation, migration or writability; unlike the CLI, MCP has no
pre-open validation stage to preserve.

**Acceptance requirement.** The boundary is proven, not assumed: the declared input
properties, every rejected value in the table, the omitted/`false` property-set and
stable-value equality, and the store-open boundary are covered for `mousa_query` by
`cmd/mousa/mcp_supersession_test.go`, and the inspection cases in section 5 by
`cmd/mousa/mcp_trail_supersession_test.go`.

**Comparison boundary.** Independent query calls produce fresh `request_id`,
`decision_id`, `trail_id`, `evaluated_at_usec` and `latency_micros`. Exclude exactly those
fields when comparing otherwise equivalent responses; validate and retain their raw
values separately. Compare every remaining field, including packet identity, evidence
identities/text/coordinates, reasons, counts and property presence. Do not normalize away
new or unknown fields. For inspection, fresh `authorization_decision_id` belongs to the
authorization wrapper, while the historical request/decision/trail identities remain
recorded facts: the inspection regressions compare every other property, compare the
returned member against the stored record's own bytes, and bind those recorded identities
to the query that wrote them rather than to the inspection's own decision. Compare
`indexed_now` only under unchanged index membership; after an item revision, assert its
current meaning separately from the immutable recorded member.

## 2. Source identity and administration

**Decision.** The configured MCP source label is the only source identity the opt-in
uses. `mcpCommand` resolves each `--source` label through `streamSource`, which derives
`mousa.NewSourceID("mousa-jsonl", label)`; the query request carries that identity, and
the consultation reads administration for exactly that identity. No new mapping, alias
or configuration is introduced.

| Situation | Behavior of an opted-in call |
|---|---|
| Records prepared by native administration for the same JSONL source | Consultation applies: the active declaration's exact predecessor representation is withheld |
| No activation history for the configured source | Verified absence: the query consults, withholds nothing, and records a consulted member with null activation and declaration identities |
| Source deactivated (an activation event selecting no declaration) | Consultation applies with no selected declaration: nothing is withheld, and the recorded event is named |
| Declaration or activation recorded for another source | No effect: administration is source-local, and a declaration for another source is not selected |
| Selected declaration or projection missing, cross-source, or disagreeing with its event | The call fails closed with the storage `integrity` code; no packet, decision or trail is committed, and nothing is repaired |
| Store holds a damaged `mousa.source_trail.v4` record | The process refuses to open the store at startup, so the MCP server does not start |

**Reasons.** Native administration already owns preparation: `supersession declaration
put` and `supersession activation put` write the immutable records against the source and
revisions the operator names, and `mousa_query` therefore reuses them unchanged. MCP
administration tools and new storage objects are outside this interface. A source label
already resolves to one canonical source identity, so adding an MCP-side source mapping
would create a second way to name the same source.

**Corruption classification.** A recorded state that exists cannot be reported as the
absence of history. The consulted-no-history case is a returned fact (`hasHistory`
false), not a `not_found` code, so the opt-in can distinguish "this source was never
curated" from "this store's recorded state is damaged" and fail closed on the latter.

## 3. One transaction, authorization and denial

**Decision.** The committed MCP handler reuses `queryItemsForRequestWithSupersession`
(`cmd/mousa/query.go`) with a nil association list and the caller's boolean. That call
reaches `Store.EvaluateAndTraceSupersessionLexical` (`internal/sqlite/trail.go`), which
delegates to the shared `traceLexical` body and performs every stage inside one
`writeImmediate` writer transaction:

1. evaluate and store the current authorization decision;
2. read the decision snapshot and verify the source-scoped lexical candidates;
3. for an allow outcome only, read the verified current administration through
   `readSupersessionConsultation` on that transaction's handle;
4. derive dispositions and build the trail through
   `NewSourceTrailWithSupersession` and `BuildSupersessionSelection`;
5. pack the surviving candidates under the requested policy;
6. insert the trail with its ordered candidate projection;
7. read the record back and require byte-exact agreement;
8. commit, and only then release text, with withheld candidates cleared.

The handler must not call `GetSupersessionActivationState` or any other public store read
for this feature: those open their own transaction and could observe a different state
than the candidates being filtered.

**Denial.** A non-allow decision skips step 3 entirely, so a denied request consults no
administration and records `consulted: false` with null identities and no dispositions.
The MCP handler must not add an administrative read of its own before or after that call.

**Unchanged semantics.** Exact predecessor-representation matching, successor currency
that does not gate withholding, canonical candidate rank and size, both packing policies
(`original` and `exact-v1`), the rejection of a supersession-plus-association
combination, and fail-closed corruption handling are already implemented and are reused
as they are. This interface adds no ranking rule, no storage object and no new
disposition value.

## 4. Output compatibility

**Decision.** Keep the `mousa.mcp_result.v1` envelope and declare the opt-in result
field in the existing `mousa_query` output schema. The query result schema declares
`supersession_excluded` unconditionally, exactly as it already declares the sibling
optional counts `duplicate_omitted` and `association_omissions`; the field's presence in
a response stays conditional on the caller opting in.

**Reasons.** The envelope's own field set is unchanged, so its identifier still
describes the same shape. A tool's output schema is static and discovered per session
from the same server build that answers the call, so declaring an optional property is
the mechanism by which a validating client can accept the field at all. Two alternatives
were considered and rejected:

| Alternative | Consequence |
|---|---|
| A second envelope identifier (`mousa.mcp_result.v2`) for opted-in responses | One static schema would have to describe both shapes through a composition, so the discovered surface grows more than the single optional property, and the identifier would then vary with the caller's input rather than the result shape |
| A separate tool for opted-in retrieval | Discovery advertises a fifth tool that duplicates an existing one, and every client must choose between two tools that differ by one boolean |

Omitting the field for opted-out calls is not cosmetic: the field has `omitempty`, so an
opted-out response carries exactly today's property set. Discovery still advertises the optional
capability to every client. Clients must refresh schemas from the current server;
a cached older closed schema cannot validate newly opted-in fields.

**Query input and result** (documentation examples: the field set is what the committed
build returns, and the identities are placeholders rather than captured records):

```json
{"source": "inspection-notes", "query": "harbor inspection", "policy": "original", "budget_bytes": 4096, "supersession": true}
```

Consulted with no activation history, and deactivated, both withhold nothing, so the
result is the ordinary evidence response plus one declared field:

```json
{
  "schema": "mousa.mcp_result.v1",
  "operation": "mousa_query",
  "source": "inspection-notes",
  "result": {
    "query": "harbor inspection",
    "source": "inspection-notes",
    "query_policy": "original",
    "expression": "\"harbor\" OR \"inspection\"",
    "request_id": "1111111111111111111111111111111111111111111111111111111111111111",
    "decision_id": "2222222222222222222222222222222222222222222222222222222222222222",
    "decision_outcome": "allow",
    "decision_reasons": [
      "allow"
    ],
    "evaluated_at_usec": 1700000000000000,
    "trail_id": "3333333333333333333333333333333333333333333333333333333333333333",
    "packet_id": "4444444444444444444444444444444444444444444444444444444444444444",
    "outcome": "evidence",
    "matched_candidates": 1,
    "candidate_limit": 100,
    "lifecycle_excluded": 0,
    "budget_omitted": 0,
    "supersession_excluded": 0,
    "packing_policy": "original",
    "used_bytes": 46,
    "budget_bytes": 4096,
    "evidence": [
      {
        "item": "notes/harbor@1f3c",
        "segment_id": "5555555555555555555555555555555555555555555555555555555555555555",
        "content_sha256": "40f048dad1531ef91af8701b549f1748f5f3e27947ec313699487ad360356b1c",
        "representation_id": "6666666666666666666666666666666666666666666666666666666666666666",
        "representation_sha256": "40f048dad1531ef91af8701b549f1748f5f3e27947ec313699487ad360356b1c",
        "byte_start": 0,
        "byte_end": 46,
        "segment_policy": "fixed-v1",
        "rank": 1,
        "bm25_score": -1.0,
        "byte_length": 46,
        "text": "The harbor inspection is scheduled for Friday."
      }
    ],
    "latency_micros": 100
  }
}
```

An active declaration that withholds the only matching candidate produces:

```json
{
  "schema": "mousa.mcp_result.v1",
  "operation": "mousa_query",
  "source": "inspection-notes",
  "result": {
    "query": "harbor inspection",
    "source": "inspection-notes",
    "query_policy": "original",
    "expression": "\"harbor\" OR \"inspection\"",
    "request_id": "7777777777777777777777777777777777777777777777777777777777777777",
    "decision_id": "8888888888888888888888888888888888888888888888888888888888888888",
    "decision_outcome": "allow",
    "decision_reasons": [
      "allow"
    ],
    "evaluated_at_usec": 1700000000000000,
    "trail_id": "9999999999999999999999999999999999999999999999999999999999999999",
    "packet_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "outcome": "supersession_excluded",
    "matched_candidates": 1,
    "candidate_limit": 100,
    "lifecycle_excluded": 0,
    "budget_omitted": 0,
    "supersession_excluded": 1,
    "packing_policy": "original",
    "used_bytes": 0,
    "budget_bytes": 4096,
    "evidence": [],
    "latency_micros": 100
  }
}
```

These examples carry every required existing field. The repeated hex digits stand for
schema-compatible identities, not actual records or recomputed canonical hashes; the
passage digest and its 46-byte UTF-8 size are concrete. The committed build returns this
field set, and the product regressions assert the same properties against real calls;
the identities here are placeholders. Four readings:

- An opted-in response also reports the `packing_policy` the v4 record carries, which a
  legacy record leaves empty for the default `original` policy. That is existing native
  behavior of the recorded shape, and `mcp_supersession_test.go` compares every other
  property unchanged between an omitted call and an opted-in call.
- The consulted-no-history and deactivated cases are **not** distinguishable from the
  response counts alone: both withhold nothing. The recorded `mousa.source_trail.v4`
  member distinguishes them (`activation_id` null means verified no history; an event
  with a null `declaration_id` means deactivation), and section 5 defines how a client
  reads it.
- A withheld candidate keeps its accepted disposition, rank and canonical size; its
  text is cleared in the copied returned candidate slice, and it consumes no budget. The
  response reports no text for it.
- The envelope `error` shape, the `source_not_permitted` check, the per-source
  authorization outcome, and the `mousa:read` scope requirement under the HTTP transport
  are unchanged; the opt-in is not authorization.

**Declared property type.** Because `evidenceResult.SupersessionExcluded` is a pointer
to an integer, the derived declaration is `{"type":["null","integer"]}`, matching the
sibling `duplicate_omitted` declaration. The payload never carries a null for it. A
stricter `{"type":"integer"}` override is possible but would make two optional counts in
one schema use different declaration conventions.

## 5. Historical inspection

**Decision (implemented).** The same optional `supersession` boolean is declared on the
`mousa_trail` input, and the optional `historical.supersession` object property is
declared in that tool's output schema. When the caller opts in, the handler returns the member
`InspectSourceTrail` already produces for a v4 record instead of discarding it; the store
read verifies that member either way.

| Input and record | Response |
|---|---|
| `supersession` omitted or `false` | Today's filtered inspection; the `historical` property set is unchanged |
| `supersession: true`, v4 record | `historical.supersession` carries the recorded member unchanged |
| `supersession: true`, v1–v3 record | No member: those versions have none, so the response keeps today's property set |
| `supersession: true`, authorization declined | No `historical` at all, no member, and the call is a successful result with the current decision reasons — denial must not become a tool error or reveal whether a trail exists |
| `supersession: true`, unknown or other-source trail | `not_found`, unchanged |
| `supersession: true`, damaged v4 record whose source ownership is established | `integrity` failure; the record is never repaired or partially returned |
| Already-open inspection, missing decision parent needed to establish ownership | `not_found`, like an unknown or foreign identity; the unscoped canonical reader and fresh-open startup verifier reject the damage as `integrity` |

**Reasons.** A separately opted-in inspection is the smallest change that keeps ordinary
inspection compatible in shape and stable semantics: every existing caller of `mousa_trail` keeps exactly the
property set it has today, and a client that cannot render the member never receives it.
Returning the member unconditionally for v4 records was rejected because it silently
changes ordinary inspection, and a new inspection tool was rejected for the same reason
as a new query tool in section 4.

**Recorded, not reinterpreted.** The member is the record's own evidence: historical
selection is verified from the stored event and declaration it names plus a fresh
authorization decision, never from today's activation projection. A replacement
activation or deactivation does not reinterpret that member. Later item revisions may
change a candidate's current `indexed_now` value; fresh authorization may change the
wrapper or decline disclosure. The recorded member, when authorization permits it,
remains the one recorded at query time.

**Authorization writes and refusal.** Successful inspection, including denial, commits
one fresh authorization decision and its necessary input rows. The returned
`authorization_decision_id` identifies that decision; denial returns its reasons but
does not look up history or reveal existence. An unknown, foreign or damaged-history
error instead rolls back the in-flight transaction, including its attempted decision.
Neither path changes historical or administrative rows.

Source-scoped inspection establishes ownership by joining the trail's typed decision
parent. A trail has no independent source column. If that parent disappears through
out-of-band damage after the inspector opens, neither an own-source nor a foreign-source
orphan can establish ownership, so both remain `not_found`. Reading unverified record
bytes or reporting integrity merely because a trail row exists would disclose foreign
existence. This live-open refusal differs from fresh-open verification, which rejects
the damaged store before serving requests, and from successful denial, which commits
authorization without looking up even damaged history.

**Output schema implication.** The declared member needs the identity types it contains
(`mousa.SupersessionActivationID`, `mousa.SupersessionDeclarationID` and
`mousa.RepresentationID`) mapped to the existing lowercase-hex string declaration
(`{"type":"string","pattern":"^[0-9a-f]{64}$"}`) in `mcpOutputSchema`, exactly as
`mousa.SourceTrailID`, `mousa.SegmentID` and `mousa.SHA256` already are. Without that
mapping the derived schema would describe those identities as byte arrays, which is not
what the payload emits. The two nullable identities derive `["null","string"]` with the
same pattern, which is what a consulted-no-history or deactivated member emits. No shipped
schema names those three types, so `mousa_sync` and `mousa_status` keep identical schemas.
The implemented slice leaves the already-extended query schema unchanged from the
query-only build and extends only the trail schema; the inspection regressions pin the
accepted digests of the sync, status and query output schemas, so a shared declaration
cannot silently change them.

A default JSONL query records v1 for `original` packing and v2 for `exact-v1`,
both without a member. MCP query does not produce associations, but native
`query --source alpha --associations FILE` can create v3 history for the same
JSONL source, and MCP inspection can read it under either packing policy.
Only a v4 record carries the member; the opt-in fabricates no member for v1–v3.

**Inspection input and result** (documentation example; the field set is what the
committed build returns and the identities are placeholders rather than captured
records):

```json
{"source": "inspection-notes", "trail_id": "9999999999999999999999999999999999999999999999999999999999999999", "supersession": true}
```

```json
{
  "schema": "mousa.mcp_result.v1",
  "operation": "mousa_trail",
  "source": "inspection-notes",
  "result": {
    "source": "inspection-notes",
    "trail_id": "9999999999999999999999999999999999999999999999999999999999999999",
    "authorization_decision_id": "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
    "authorization_outcome": "allow",
    "authorization_reasons": [
      "allow"
    ],
    "historical": {
      "id": "9999999999999999999999999999999999999999999999999999999999999999",
      "request_id": "7777777777777777777777777777777777777777777777777777777777777777",
      "decision_id": "8888888888888888888888888888888888888888888888888888888888888888",
      "decision_outcome": "allow",
      "decision_reasons": [
        "allow"
      ],
      "expression": "\"harbor\" OR \"inspection\"",
      "budget_bytes": 4096,
      "used_bytes": 0,
      "packet_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      "lifecycle_excluded": 0,
      "candidates": [
        {
          "segment_id": "5555555555555555555555555555555555555555555555555555555555555555",
          "content_sha256": "40f048dad1531ef91af8701b549f1748f5f3e27947ec313699487ad360356b1c",
          "rank": 1,
          "text_bytes": 46,
          "selected": false,
          "indexed_now": true
        }
      ],
      "schema": "mousa.source_trail.v4",
      "packing_policy": "original",
      "supersession": {
        "consulted": true,
        "activation_id": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
        "declaration_id": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
        "dispositions": [
          {
            "selection": "superseded",
            "segment_id": "5555555555555555555555555555555555555555555555555555555555555555",
            "content_sha256": "40f048dad1531ef91af8701b549f1748f5f3e27947ec313699487ad360356b1c",
            "declaration_id": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
            "predecessor_item_id": "notes/harbor@1f3c",
            "predecessor_representation_id": "6666666666666666666666666666666666666666666666666666666666666666",
            "successor_item_id": "notes/harbor@9ab0",
            "successor_representation_id": "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
          }
        ]
      }
    }
  }
}
```

The member is text-free: it names identities, digests and pins, and the candidate rows
beside it carry canonical sizes, so the tool's existing "never evidence text" property
holds. It contains no disposition for a denied or unconsulted record.

## 6. Acceptance and evidence

The query cases below are covered by the committed product regressions in
`cmd/mousa/mcp_supersession_test.go` and the inspection cases by
`cmd/mousa/mcp_trail_supersession_test.go`, which run the built executable over stdio
against fresh temporary SQLite stores. Independent query-only and inspection observations
are published separately below. The store-level transaction and damage semantics are
already covered by the existing product
regressions in `internal/sqlite/trail_supersession_test.go` and
`internal/sqlite/supersession_activation_test.go`; the cases here prove the client
boundary and do not duplicate those tests.

### Product-local regression cases

| Case | What it proves | Status |
|---|---|---|
| Discovered schema | The optional boolean is declared on `mousa_query`, the query count is declared in its output schema, and the envelope enum is unchanged. The trail declares the same input and the optional recorded member, with its identity types mapped to lowercase hex; sync and status keep their definitions, and the accepted sync, status and query output schemas stay byte-identical | Covered by `TestMCPSupersessionQueryDeclaresOptInSchema` and `TestMCPSupersessionTrailDeclaresOptInSchema` |
| Omitted and explicit false | Both preserve the property set and stable semantic values under an active declaration, with no `supersession_excluded`; apply only the enumerated per-call exclusions from section 1 and retain all raw values | Covered by `TestMCPSupersessionQueryOptInPreservesDefaultPropertySet` |
| Strict input errors | `null`, `"true"`, `1`, `0`, a duplicate key, an unknown key, non-object arguments and trailing data are rejected with `invalid_arguments`, `isError: true` and no `result` | Covered by `TestMCPSupersessionQueryOptInInputBoundaries` |
| Store-open boundary | Starting the server against an absent store path creates it before any call; a rejected-input call writes no decision or trail, and no tool call opens, creates or migrates a store of its own | Covered by `TestMCPSupersessionQueryOptInInputBoundaries` |
| Consulted, no history | `supersession_excluded` is `0`, evidence is released, the packet identity equals the default query's for the same selection, and the recorded trail is v4 with null activation and declaration identities | Covered by `TestMCPSupersessionQueryOptInConsultedNoHistoryAndDeactivation` |
| Deactivated | `supersession_excluded` is `0`, evidence is released, and the recorded member names the consulted event with a null declaration | Covered by `TestMCPSupersessionQueryOptInConsultedNoHistoryAndDeactivation` |
| Active suppression, both policies | The withheld candidate is not released, keeps its rank and canonical size, and frees budget for a later rank under `original` and under `exact-v1` | Covered by `TestMCPSupersessionQueryOptInFreesBudgetUnderBothPolicies` |
| No successor match | Suppression still applies when the pinned successor revision went historical, and no replacement passage appears | Covered by `TestMCPSupersessionQueryOptInFreesBudgetUnderBothPolicies` |
| Denial | No administrative lookup and no identity revealed; the recorded trail is v4 with `consulted: false`; the response carries the declared count as `0` and otherwise reports the same outcome, reasons and empty evidence as the default query | Covered by `TestMCPSupersessionQueryOptInDenialConsultsNothing` |
| Corrupt recorded state | An activation projection naming a missing event fails the opted-in call with the storage `integrity` code, commits no decision or trail, repairs nothing, and leaves the default query path behaving as today | Covered by `TestMCPSupersessionQueryOptInCorruptAdministrationFailsClosed` |
| Restart | Under unchanged index membership and allowing authorization, the recorded historical fields and supersession member are unchanged; the inspection authorization ID and fresh query request/decision/trail IDs vary and are validated separately | Covered by `TestMCPSupersessionQueryOptInRestartKeepsRecordedMember` |
| Inspection opt-in property set | Omitted and explicit `false` keep the filtered inspection under both packing policies; `supersession: true` adds exactly the recorded `historical.supersession` member and no other property, and the returned member equals the producing record's own member as canonical JSON | Covered by `TestMCPSupersessionTrailOptInPreservesDefaultPropertySet` |
| Recorded members | The recorded member is returned for an active declaration, a verified absence of history, a deactivation and an unconsulted denial, and the recorded identities, consultation flag, disposition rows and pins are asserted against the fixture | Covered by `TestMCPSupersessionTrailOptInRecordedMembers` |
| Legacy record versions | v1 (`original` packing), v2 (`exact-v1`) and native-created same-source v3 association history keep their historical properties and producing identities under omitted/false/true; none gains a member | Covered by `TestMCPSupersessionTrailOptInLegacyVersions` and `TestMCPSupersessionTrailNativeAssociationHistory` |
| Strict input errors | `null`, `"true"`, `1`, `0`, a duplicate key, an unknown key, non-object arguments and trailing data are rejected with `invalid_arguments` and no `result`, and each rejected call is bracketed against the stored trail, candidate, declaration, activation and decision rows; `mousa_sync` and `mousa_status` still reject the field | Covered by `TestMCPSupersessionTrailOptInInputBoundaries` |
| Declined authorization and unknown trails | Denial succeeds with current reasons and no history, committing exactly its response-bound decision and necessary input children while existing rows remain unchanged; unknown/foreign errors roll back their attempted decision | Covered by `TestMCPSupersessionTrailOptInAuthorizationBoundaries` and `TestMCPSupersessionTrailDeniedAudit` |
| Inspection after an activation change | A restart, a replacement activation and a deactivation after the trail was recorded do not change the returned member, and the fresh wrapper authorization identity is validated separately | Covered by `TestMCPSupersessionTrailOptInRestartAndActivationChange` |
| Damaged v4 records | A removed candidate row, a removed candidate segment, a removed recorded activation event, a removed recorded declaration and a damaged record all fail with `integrity` under the default and opted-in call, with no partial member, no repair and no committed audit row | Covered by `TestMCPSupersessionTrailOptInRefusesDamagedRecords` |
| Missing decision parent | Already-open own/foreign orphan and unknown inspections return the same `not_found` without writes; denied inspection commits only authorization without reading damaged history; unscoped canonical reads and fresh-open verification reject the orphan as `integrity` | Covered by `TestMCPSupersessionTrailOrphanSourceMasking` |

Ordinary inspection keeps its filter unless the caller opts in:
`TestMCPSupersessionQueryOptInKeepsFilteredV4History` asserts that the default
`mousa_trail` call returns no `supersession` member for a v4 record and treats its recorded
selection as recorded.

### Independent stdio MCP client observations

The [independent query-only capture](https://github.com/graydeon/mousa-benchmarks/tree/2f419c47be796f6e647997e120fe5a364351e9ce/results/2026-10-12-mcp-query-supersession-schedule-binding) pins product commit
`7ae0d8768e7cd203c48478412b493e1255c97b11`, tree
`2b8b14f76e0907f3ca3fd5dc8c33934567e61d14`. Two fresh-store captures each record
129 steps, 2,847 passing assertions and 39 stable selections; the pilot has 2,846
assertions. All 21 negative controls reject, including request-binding, strict JSON
and schema controls. A focused regression also preserves JSON types when comparing
recorded metadata with the frozen schedule. The real forbidden-write control fails
capture and replay. Earlier archives retain their own runners and historical limits.

### Accepted inspection observations — 2026-10-13

The [independent inspection capture](https://github.com/graydeon/mousa-benchmarks/tree/7b29e226ea1d8362262f99dfb95bf2ee06c5fa7d/results/2026-10-13-mcp-trail-supersession)
is accepted with [product PR75](https://github.com/graydeon/mousa/pull/75) and
[companion PR30](https://github.com/graydeon/mousa-benchmarks/pull/30).
It targets public product commit `5ffa0286b35ecf84b36abe53d029a78075c02b86`, tree
`c73fb12a9ccdc6df65ad998a3039255a43dc6ad1`, not the later product merge
`899449f8b000c7b35a4f98266efdfdf1119c1971`. The capture-time companion HEAD is
`2f419c47be796f6e647997e120fe5a364351e9ce`, tree
`422fe7106535c6646dfea2eb69db3adea9b52fbd`; runner and fixture hashes pin the bytes
used. The evidence head `9a5a4433f0bdce707288c7e7b97fb986d5ce38ff` and later
companion merge `7b29e226ea1d8362262f99dfb95bf2ee06c5fa7d` share tree
`5c0708b0902c71eb79c42ca45d241dc53885d544`. Those are publication identities,
not renamed capture-time identities.

Two independent final captures each record 268 steps, 5,314 passing assertions and
60 stable historical selections; their offline replays pass. All 60 selections match
between stores after the protocol's declared fresh identity exclusions, with producing
identities validated against each capture's own native query and stored trail.
A supplemental comparison finds all 18 denied wrappers per store equal after excluding
only the fresh authorization ID and echoed requested trail ID. It is separate from the
per-capture assertion count, which does not represent independent relevance cases.

The [runner, frozen protocol and reproduction guide](https://github.com/graydeon/mousa-benchmarks/tree/7b29e226ea1d8362262f99dfb95bf2ee06c5fa7d/mcp-trail-supersession)
record the following client-boundary evidence without importing product internals:

| Boundary | Recorded evidence |
|---|---|
| Build and discovery | Clean anonymous public product checkout; Go 1.27.1, `GOWORK=off`, `CGO_ENABLED=0`, `go build -trimpath`, binary SHA-256 and build receipt; MCP `2025-11-25` discovery with only the trail schemas extended from the query-only baseline |
| Real requests and responses | Exact stdio frames, native commands, exits and notifications; generated frames bound to the frozen schedule with JSON types and recursive duplicate-key checks preserved |
| Historical inspection | Both packing policies; v4 active, no-history, deactivated and recorded unconsulted-denial members; omitted/false filtering; later activation and content changes, restart; native-created v1–v3 including same-source v3 associations, without fabricating a member |
| State and refusals | Full logical SQLite snapshots including committed WAL state; successful denial commits only response-bound authorization and necessary input children; erroneous reads commit nothing; already-open own/foreign orphan refusals stay masked `not_found`, distinct from fresh-open startup integrity refusal |
| Controls | 17 rehashed negative mutations reject at named detectors; two ordinary controls pass; a separate actual extra-table-and-row write makes capture and replay fail at `orphan-deny-unknown-true:only-bound-authorization-write` |

These are synthetic native/stdio observations, not semantic truth, retrieval quality,
latency/throughput, authentication, native-host acceptance or general physical database
integrity. The named controls do not establish general fault coverage. Startup refusal
does not prove live transaction rollback, and a matching rebuilt binary in the tested
environment is not universal reproducible-build proof. Current `indexed_now` values
remain live properties, not immutable historical selection fields.

The existing native capture at product `e0837750bc8972d6fc7ebffc86a8fba05a0ee1f4`
under benchmark `f252d7aadba35330abcd0c387053a017f08b1b8d` remains native CLI
evidence. It and the query-only archive above keep their original source pins and raw
bytes; neither is relabelled as inspection acceptance.

## 7. Accepted client contract decisions

Both implemented and accepted slices use these decisions:

1. **Schema evolution or a new envelope version.** This document treats a declared
   optional property in a tool's static output schema as an additive change and keeps
   `mousa.mcp_result.v1`. The alternative is a second envelope identifier for opted-in
   responses, which would require one schema to describe two shapes or a separate tool,
   and would make the identifier depend on the caller's input. The committed query slice
   exercises the first mechanism, and the inspection member uses it too.
2. **A shared input name.** This document reuses `supersession` on both tools and no
   other name. The alternative is a distinct name on `mousa_trail` such as
   `include_supersession`, which would make one concept have two names in one tool
   surface; the implemented inspection slice shares the name.
3. **No query-level consultation mode.** The opted-in query response carries only the
   count, so a client cannot tell consulted-no-history from deactivation without reading
   the trail. The alternative is a consultation object on the query result, which would
   expose native-only identities to every opted-in query and duplicate what the recorded
   member already says. The implemented division keeps the count on the query result and
   the full consultation on the recorded member.

## 8. Implementation slices

Each slice is independently reviewable and changes no native code path.

1. **Query opt-in and its declared result field — implemented.** `cmd/mousa/mcp_tools.go`
   (`mcpArguments`, `decodeMCPArguments`, `mcpOutputSchema`, `mcpToolDefinitions`),
   `cmd/mousa/mcp.go` (the `mousa_query` case), tests in
   `cmd/mousa/mcp_supersession_test.go` with the existing `TestMCP...` regressions in
   `cmd/mousa/mcp_test.go` kept green. Covers every product-local query case above.
2. **Inspection opt-in — implemented.** `cmd/mousa/mcp.go` (the `mousa_trail` case),
   `cmd/mousa/mcp_tools.go` (accepted field, output-schema member and identity-type
   mapping, description), and the inspection cases in
   `cmd/mousa/mcp_trail_supersession_test.go`.
3. **Documentation status and companion capture — accepted.**
   The independent inspection observations above complete the paired milestone.
   Product-local regressions remain separate evidence from the standalone client archive.

## Non-goals

- No MCP administration tool for declarations or activations, and no new storage object;
  native administration remains the preparation path.
- No opt-in on the OpenAI extension surface: the `mousa_mentions` tool and the evidence
  resource template call `queryItemsForRequest` with fixed options
  (`cmd/mousa/mcp_openai.go`) and stay unchanged.
- No directory enforcement, no association-plus-supersession combination, no transitive
  or automatic supersession, and no change to ranking, authorization or item pointers.
- No new envelope version, no new tool, and no new output field on any existing opt-out
  response.
- No semantic-truth, retrieval-quality, performance or authentication claim. The
  consultation is linear in a source's activation history, and no latency measurement
  accompanies these correctness cases.
