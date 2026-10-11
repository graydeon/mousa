# MCP supersession opt-in

**Status: proposed, not implemented.** This document specifies a reviewable
compatibility contract for one explicit optional `supersession` boolean on the existing
`mousa_query` tool, and a matching opt-in on `mousa_trail` for inspecting what a query
recorded. Nothing in the current build accepts these inputs: a `supersession` argument
is rejected as `invalid_arguments`, and MCP responses omit every native-only
supersession field. Every behavior case in the acceptance matrices is **NOT RUN**, and
each JSON block below is a proposed shape, not an observation.

The implemented native contract remains
[Opt-in supersession enforcement](SUPERSESSION_ENFORCEMENT.md). This proposal adds a
client boundary for that contract. It changes no native behavior, storage object,
canonical record, schema version, migration, dependency or benchmark file.

## Recommendation

Extend the two existing tools with **declared optional inputs and declared optional
result properties**, and keep the envelope identifier `mousa.mcp_result.v1`:

| Surface | Change | Present only when |
|---|---|---|
| `mousa_query` input | optional `supersession`: boolean | supplied `true` |
| `mousa_query` result | optional `supersession_excluded`: integer | the caller opted in, including value `0` |
| `mousa_trail` input | optional `supersession`: boolean | supplied `true` |
| `mousa_trail` result `historical` | optional `supersession`: object | the caller opted in and the recorded trail is `mousa.source_trail.v4` |

Reasons, in detail below: a tool publishes one static output schema per session, that
schema is closed, and a validating client rejects a response field the schema does not
declare, so the opt-in result must be declared; declaring an optional property is
additive for every caller that does not use it; and the alternative of a second
envelope version or a second tool widens the discovered surface without adding a
compatibility guarantee.

## Current boundary

The client boundary today, from the current source:

- `decodeMCPArguments` in `cmd/mousa/mcp_tools.go` accepts exactly the field names listed
  per operation. Any other name, a duplicate name, a `null` value and trailing data are
  rejected before the operation switch, and the call returns
  `mcpFailure(operation, "", "invalid_arguments", ...)` with no `result`.
- `mcpOutputSchema` in the same file deletes `supersession_excluded` from the
  `mousa_query` result schema and `supersession` from the `mousa_trail`
  `historical` schema, so neither field is declared to any client.
- `(*mcpApplication).call` in `cmd/mousa/mcp.go` runs `mousa_query` through
  `queryItemsForRequest`, which is `queryItemsForRequestWithSupersession(..., false)`,
  and explicitly nils `inspection.Historical.Supersession` for `mousa_trail`.
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
| `true` | `queryItemsForRequestWithSupersession(..., nil, true)` | The existing CLI entry point already implements this behavior |
| `null` | Rejected: `invalid_arguments` | The decoder already refuses any null field value |
| `"true"`, `"false"` | Rejected: `invalid_arguments` | A string is not the declared boolean type; no truthy coercion |
| `1`, `0` | Rejected: `invalid_arguments` | A number is not the declared boolean type |
| Repeated `supersession` key | Rejected: `invalid_arguments` | Existing duplicate-field rule |
| Unknown field name | Rejected: `invalid_arguments` | Existing unknown-field rule |
| `supersession` on `mousa_sync` or `mousa_status` | Rejected: `invalid_arguments` | Each operation declares its own accepted field list |
| `supersession` on `mousa_trail` | Rejected until the inspection opt-in in section 5 exists | The field is meaningful only on a tool that declares it |
| Arguments after the closing brace | Rejected: `invalid_arguments` | Existing trailing-data rule |

**Reasons.** Strict typing keeps one representation per concept: the decoder's existing
rules already reject null, duplicate and unknown fields, and a boolean input that
silently accepted `"true"` or `1` would make the argument's meaning depend on the
decoder rather than on the declared schema. Omission and `false` must be
indistinguishable because the repository treats a default query as the published
compatibility boundary, not as a spelling to be detected.

**Discovery.** The `mousa_query` input schema gains one optional property,
`"supersession": {"type": "boolean"}`, and its description states that the flag enables
exact pinned-predecessor withholding for one configured JSONL source and that the
default remains uncurated. The query-only first slice leaves the other three tools unchanged; the completed contract also extends `mousa_trail` as section 5 describes. `mousa_sync` and `mousa_status` keep their current input and output schemas.

**Unchanged boundaries for opted-out callers.** Source selection, the required fields,
the error envelope with its `code`, `message`, `completed_items` and `failed_item`
fields, the `source` value in a failure envelope, and the per-operation accepted-field
list otherwise stay as they are. The store is opened once at process start, so no input
value affects store creation, migration or writability; unlike the CLI, MCP has no
pre-open validation stage to preserve.

**Acceptance requirement.** A future test must prove this boundary rather than assume it:
the declared input property appears on each tool when its implementation slice lands;
each rejected value above returns `invalid_arguments` with no `result`; and omitted or
`false` calls preserve the pre-change property set and stable semantic values, including
the absence of `supersession_excluded`.

**Comparison boundary.** Independent query calls produce fresh `request_id`,
`decision_id`, `trail_id`, `evaluated_at_usec` and `latency_micros`. Exclude exactly those
fields when comparing otherwise equivalent responses; validate and retain their raw
values separately. Compare every remaining field, including packet identity, evidence
identities/text/coordinates, reasons, counts and property presence. Do not normalize away
new or unknown fields. For inspection, fresh `authorization_decision_id` belongs to the
authorization wrapper, while the historical request/decision/trail identities remain
recorded facts. Compare `indexed_now` only under unchanged index membership; after an item
revision, assert its current meaning separately from the immutable recorded member.

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
administration tools and new storage objects are outside this proposal. A source label
already resolves to one canonical source identity, so adding an MCP-side source mapping
would create a second way to name the same source.

**Corruption classification.** A recorded state that exists cannot be reported as the
absence of history. The consulted-no-history case is a returned fact (`hasHistory`
false), not a `not_found` code, so the opt-in can distinguish "this source was never
curated" from "this store's recorded state is damaged" and fail closed on the latter.

## 3. One transaction, authorization and denial

**Decision.** The MCP handler reuses `queryItemsForRequestWithSupersession`
(`cmd/mousa/query.go`) with a nil association list and the caller's boolean. That call
reaches `Store.EvaluateAndTraceSupersessionLexical` (`internal/sqlite/trail.go`), whose
`traceLexical` body performs every stage inside one `writeImmediate` writer
transaction:

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
as they are. This proposal adds no ranking rule, no storage object and no new
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

**Proposed query input and result** (not implemented; not produced by the current build):

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

These are complete hypothetical envelopes with the required existing fields. Repeated
hex digits stand for schema-compatible identities, not actual records or recomputed
canonical hashes; the passage digest and its 46-byte UTF-8 size are concrete. No current
build produced these payloads. Three readings:

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

**Decision.** Add the same optional `supersession` boolean to the `mousa_trail` input,
and declare the optional `historical.supersession` object property in that tool's output
schema. When the caller opts in, the handler returns the member
`InspectSourceTrail` already produces for a v4 record instead of discarding it.

| Input and record | Response |
|---|---|
| `supersession` omitted or `false` | Today's filtered inspection; the `historical` property set is unchanged |
| `supersession: true`, v4 record | `historical.supersession` carries the recorded member unchanged |
| `supersession: true`, v1–v3 record | No member: those versions have none, so the response keeps today's property set |
| `supersession: true`, authorization declined | No `historical` at all, no member, and the call is a successful result with the current decision reasons — denial must not become a tool error or reveal whether a trail exists |
| `supersession: true`, unknown or other-source trail | `not_found`, unchanged |
| `supersession: true`, damaged v4 record | `integrity` failure; the record is never repaired or partially returned |

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

**Output schema implication.** The declared member needs the identity types it contains
(`mousa.SupersessionActivationID`, `mousa.SupersessionDeclarationID` and
`mousa.RepresentationID`) mapped to the existing lowercase-hex string declaration
(`{"type":"string","pattern":"^[0-9a-f]{64}$"}`) in `mcpOutputSchema`, exactly as
`mousa.SourceTrailID`, `mousa.SegmentID` and `mousa.SHA256` already are. Without that
mapping the derived schema would describe those identities as byte arrays, which is not
what the payload emits. No shipped schema currently names those three types, so the
completed implementation must leave `mousa_sync` and `mousa_status` schemas identical.
The inspection slice must also leave the already-extended query schema unchanged from
the query-only slice, while extending only the trail schema.

**Proposed inspection input and result** (not implemented; not produced by the current
build):

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

Every case below is **NOT RUN**. The store-level transaction and damage semantics are
already covered by the existing product regressions in
`internal/sqlite/trail_supersession_test.go` and
`internal/sqlite/supersession_activation_test.go`; the cases here prove the client
boundary and must not duplicate those tests.

### Product-local regression cases

| Case | What it must prove |
|---|---|
| Discovered schema | The completed milestone declares the optional boolean on query and trail, the query count and historical member in their respective output schemas, and the unchanged envelope enum. Sync/status schemas are byte-identical; during the query-only intermediate slice, trail is unchanged too |
| Omitted and explicit false | Both preserve the pre-change property set and stable semantic values under an active declaration, with no `supersession_excluded`; apply only the enumerated per-call exclusions from section 1 and retain all raw values |
| Strict input errors | `null`, `"true"`, `1`, `0`, a duplicate key, an unknown key and trailing data each return `invalid_arguments` with `isError: true` and no `result` |
| Store-open boundary | Starting the server against an absent store path creates it before any call; a rejected-input call writes nothing, a denied call writes only the decision and trail the operation defines, and no tool call opens, creates or migrates a store of its own |
| Consulted, no history | `supersession_excluded` is `0`, evidence is released, the packet identity equals the default query's for the same selection, and the recorded trail is v4 with null activation and declaration identities |
| Deactivated | `supersession_excluded` is `0`, evidence is released, and the recorded member names the consulted event with a null declaration |
| Active suppression, both policies | The withheld candidate is not released, keeps its rank and canonical size, and frees budget for a later rank under `original` and under `exact-v1` |
| No successor match | Suppression still applies when the pinned successor is historical, inactive, unmatched or over budget, and no replacement passage appears |
| Denial | No administrative lookup and no identity revealed; the recorded trail is v4 with `consulted: false`; the response carries the declared count as `0` and otherwise reports the same outcome, reasons and empty evidence as the default query |
| Corrupt recorded state | An activation projection naming a missing event fails the opted-in call with the storage `integrity` code, commits no decision or trail, and leaves the default query path behaving as today |
| Restart | Under unchanged index membership and allowing authorization, the recorded historical fields and supersession member are unchanged; the inspection authorization ID and fresh query request/decision/trail IDs vary and are validated separately |
| Inspection opt-in | `supersession: true` returns the recorded member; omitted, `false` and v1–v3 records keep today's property set; a declined authorization returns no `historical` |
| Inspection after an activation change | A replacement activation or deactivation after the trail was recorded does not change the member |

### Independent stdio MCP client observations

A companion benchmark capture supplies the independently reproduced client behavior; a
product test is not that evidence. The planned paired gate is: the product implementation
PR requires the companion capture in the same milestone, and the capture is published
before the product PR links to it.

The smallest capture that proves the new client behavior is one standalone
standard-library runner in the benchmark repository that speaks MCP `2025-11-25` over
the pinned executable's stdio in separate processes. It mirrors the existing native
runner's conventions: a `pilot` that freezes only observed values, a `capture` and a
`verify` that replay it, and a `negative-controls` mode.

| Requirement | Form |
|---|---|
| Pins | Product commit and tree, benchmark head and tree, `GOWORK=off`, Go version, and the built binary's SHA-256 with a build receipt (command, exit, output digest) |
| Real requests and responses | Exact request bytes and exact response bytes per call, from `initialize`, `notifications/initialized`, `tools/list` and `tools/call` |
| Fresh stores | Each case starts from a new store prepared by the runner, so no case depends on another's residue |
| State brackets | A logical SQLite fingerprint (table names, row counts and content digests, including WAL-visible state) before and after every command, with reads required to change only their own recorded audit rows |
| Cases | Discovery; omitted versus explicit false stable-value/property-set equality with raw per-call values retained; active suppression under both packing policies; consulted no-history; deactivation; the strict input rejections; inspection opt-in true versus false; corruption failure; restart determinism |
| Negative controls | An intentionally inverted assertion, an undeclared response field injected into a captured response, a damaged activation row the call must refuse, and a read whose bracket is made to look like a write |

The existing native capture at product `e0837750bc8972d6fc7ebffc86a8fba05a0ee1f4`
under benchmark `f252d7aadba35330abcd0c387053a017f08b1b8d` remains native CLI
evidence. It is not relabelled as MCP acceptance, and it is not a substitute for the
capture above.

## 7. Open questions for review

1. **Schema evolution or a new envelope version.** This proposal treats a declared
   optional property in a tool's static output schema as an additive change and keeps
   `mousa.mcp_result.v1`. The alternative is a second envelope identifier for opted-in
   responses, which would require one schema to describe two shapes or a separate tool,
   and would make the identifier depend on the caller's input. Question: does the
   project accept per-tool schema growth with a stable envelope identifier as the
   compatibility mechanism for future optional result fields?
2. **A shared input name.** This proposal reuses `supersession` on both tools and no
   other name. The alternative is a distinct name on `mousa_trail` such as
   `include_supersession`, which would make one concept have two names in one tool
   surface. Question: confirm the shared name.
3. **No query-level consultation mode.** The opted-in query response carries only the
   count, so a client cannot tell consulted-no-history from deactivation without reading
   the trail. The alternative is a consultation object on the query result, which would
   expose native-only identities to every opted-in query and duplicate what the recorded
   member already says. Question: confirm that the count plus the recorded member is the
   intended division of surfaces.

## 8. Implementation slices

Each slice is independently reviewable and changes no native code path.

1. **Query opt-in and its declared result field.** `cmd/mousa/mcp_tools.go`
   (`mcpArguments`, `decodeMCPArguments`, `mcpOutputSchema`, `mcpToolDefinitions`),
   `cmd/mousa/mcp.go` (the `mousa_query` case), tests in
   `cmd/mousa/mcp_supersession_test.go` with the existing `TestMCP...` regressions in
   `cmd/mousa/mcp_test.go` kept green. Covers every product-local case above except the
   inspection cases.
2. **Inspection opt-in.** `cmd/mousa/mcp.go` (the `mousa_trail` case),
   `cmd/mousa/mcp_tools.go` (accepted field, output-schema member and identity-type
   mapping, description), tests for the inspection cases.
3. **Documentation status and companion capture.** Flip this document to implemented
   status, update the four status notes added by the proposing PR, and publish the
   companion benchmark capture.

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
