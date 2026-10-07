# Opt-in supersession enforcement

**Status: proposed contract, not implemented.** No production code implements anything on
this page: there is no query opt-in, no CLI flag, no MCP surface, no trail version and no
packet version for supersession enforcement. This document settles the selection and trail
contract *before* retrieval changes, so a later bounded implementation does not improvise
semantics the current records cannot support. Until that implementation lands, a recorded
activation still filters no query, and separately identified current items remain
independent: a newer correcting item does not suppress an older item.

Declarations, activation history and the verified current-state projection are implemented;
see the [supersession boundary](CAPABILITIES.md#supersession-core-boundary). This page only
proposes what an *opt-in* query would do with them.

## What exists today

The contract below is grounded in the current source:

- A declaration is immutable, source-local history pinned to an exact predecessor and
  successor item **and** representation (`mousa.supersession_declaration.v1`). Storing one
  filters no query and grants nothing.
- An activation event selects at most one declaration per source, or deactivates it, and the
  store keeps a verified current-state projection (`mousa.supersession_activation.v1`).
- A query evaluates source authorization and lifecycle, retrieves source-scoped lexical
  candidates, verifies each candidate's canonical segment, digest and ancestry, assigns
  authorization/lifecycle dispositions and accepted ranks, packs the accepted passages into
  the byte budget, and writes the decision and one immutable Source Trail in a single writer
  transaction (`internal/sqlite/trail.go`, `internal/mousa/retrieval.go`).
- Only the current revision of an active item carries lexical rows: activation removes the
  previous revision's rows and deactivation removes the current ones, and startup verification
  requires an item's active flag to agree exactly with its index rows
  (`internal/sqlite/local_items.go`). A candidate that derives from an item observation is
  therefore the current revision of exactly one item, an item revision is exactly the text
  representation derived from that observation's artifact, and a candidate without item
  ancestry can never match a declaration pin.
- Packing and association stages stay inside that transaction; the trail records candidate
  dispositions, packing omissions and association rows as v1, v2 or v3, and its content is
  re-verified against canonical store bytes on every read.

## Terms

- **Released candidate**: a candidate that passed authorization and lifecycle verification
  and can be packed. Ranking-stage rejection is already recorded per candidate.
- **Pin**: the exact `(item, representation)` pair a declaration names.
- **Active declaration**: the declaration named by the source's verified current activation
  state; a deactivated source has none.
- **Suppressed candidate**: a released candidate removed from the selection because an
  active declaration pins its exact revision as superseded.
- **Selection disposition**: why a released candidate is not in the selection. It is not an
  authorization outcome and not a byte-budget or duplicate decision.

## Contract at a glance

1. Explicit opt-in. Default queries, their identities and stored history are unchanged; the
   enforcement stage runs only when the caller opts in.
2. The disposition happens after authorization and lifecycle verification and before packing,
   inside the retrieval transaction. It never grants access and never converts an allow into a
   policy deny.
3. Suppression matches the declaration's exact pinned predecessor representation. It is not
   item-label matching and never extends to later revisions of the same item.
4. A valid active declaration suppresses its pinned predecessor whether or not the pinned
   successor revision is still current, indexed, matched, inside the candidate limit or inside
   the budget. No replacement text is ever manufactured.
5. The record is versioned: an opt-in query writes a new trail version whose identity binds
   the consulted activation, declaration and every suppression. Older trail and packet
   versions keep their exact bytes, identities and readers.
6. Stored state that is corrupt, unreachable or inconsistent fails the whole request closed:
   no packet, no committed decision, no trail, no partial omission.

The enforcement stage never mutates declarations, activation history, current item pointers,
canonical records or retrieval ranking, and it never authenticates an author or decides
factual truth.

## 1. Successor availability does not gate suppression

**Decision.** An active declaration suppresses its pinned predecessor revision regardless of
whether the pinned successor revision is still the successor item's current revision, still
indexed, still active, or still present. The successor item's availability does not change the
selection outcome. Canonical pin records must still exist and pass provenance verification;
a missing or corrupt canonical record is an integrity failure, not ordinary unavailability.
Availability is recorded as identity evidence (below) and reported by read-time
diagnostics, never by inventing a passage.

| State of the successor pin | Selection effect | Why |
|---|---|---|
| Current active revision, matched by the query | Predecessor suppressed; successor released by ordinary lexical selection | The declaration explains the omission; the query still supplies its own evidence |
| Current active revision, not a lexical candidate, outside the candidate limit, or over budget | Predecessor suppressed; the successor contributes nothing | Availability must not silently re-enable obsolete text |
| Historical: the successor item has since been updated to another revision | Predecessor suppressed | The declaration is mutable only through explicit activation, not through an unrelated item edit |
| Successor item deactivated or no longer present | Predecessor suppressed; the packet may contain no evidence | Deleting another item is not an implicit revocation of a recorded declaration |
| Declaration deactivated, or the source has no activation history | No suppression | Recorded state, not absence of text, decides |

**Reasons.** The two mutable inputs an operator controls are the declaration's activation and
the item revisions themselves. Gating on the successor item's *current pointer* would add a
second, undocumented revocation channel driven by state the declaration never pinned: a
routine edit of the successor would silently reinstate obsolete predecessor text, which the
packet cannot show. The opposite failure — withholding in force while the successor is gone —
is visible in the trail and reversible by one explicit activation.

**Consequences.** The query may legitimately release no evidence while the source still holds
the predecessor text; that is an explained outcome, not an error, and it is reported (see
section 3). The recorded omission names the pinned pair. A read-time diagnostic may report
whether the pinned successor revision is still its item's current revision, exactly as trail
inspection already reports `indexed_now` for a historical candidate; this is a fact about
today's store, not part of the immutable selection record.

## 2. Exact pin matching over segment ancestry

**Decision.** A released candidate is suppressed exactly when its segment's representation
equals the declaration's pinned predecessor representation, the declaration is the source's
active declaration, and the candidate belongs to the decision's source. The pinned
`(item, representation)` provenance is re-verified when the declaration is read inside the
retrieval transaction, so representation equality carries item equality without a second
ambiguity.

**Reasons.** Candidate identity is the **segment**: one representation and one byte range. A
segment can carry several ancestry paths, and a candidate is the same segment whatever path
reached it, so no path, item label or segment listing decides membership. Representation
identity already determines the source, item and revision: an artifact identity includes its
observation, and a representation identity includes its input artifacts, while an item
revision resolves to exactly one representation with exactly one artifact input. Two items
with byte-equal text therefore have different representations, and a candidate that is
unrelated ancestry or belongs to another source can never match a pin.

| Case | Match? | Why |
|---|---|---|
| Segment of the pinned predecessor representation | Yes | Exact pinned revision |
| Later revision of the same predecessor item | No | Item labels do not pin revisions; the pin names a representation |
| Different item, byte-equal text, same source | No | Different artifact, observation and representation |
| Segment of another source reached through shared ancestry | No | Source scoping and pinned provenance are exact |
| Segment ID equal to some representation ID | Irrelevant | Segment and representation identities are different identities |

Matching does not depend on the query text, the ranking position, the byte budget or the
candidate limit, so a suppressed candidate is suppressed for the same reason in every query
that considers it.

## 3. Selection disposition, packing, ranks and identities

**Decision.** Suppression is a **selection disposition** recorded in the trail, not a change
to the authorization/lifecycle disposition and not a packing omission.

- The candidate row keeps `disposition: accepted` (its authorization and lifecycle outcome is
  unchanged), keeps its accepted `final_rank`, and keeps its text size. It is not selected and
  contributes no bytes.
- Ranks are not renumbered: `final_rank` is assigned over accepted candidates before
  selection, so remaining rank order is exactly the order a default query would report.
- Packing sees only surviving accepted candidates: the byte budget, greedy first-fit
  skipping and exact-content duplicate displacement all operate on the surviving set. A
  suppressed passage frees its bytes, so a later candidate that a default query omits for
  budget may be released. Selected counts, `used_bytes` and the packet identity follow from
  the surviving selection.
- The withheld text is not returned to the caller or packed: a suppressed candidate's text is
  blanked only in the returned payload after selection/trail construction. Its recorded
  positive text size remains the verified canonical size; never derive it from a blanked
  payload. The trail records identities, sizes and reasons, never text.

**Reasons.** The authorization path must not be rewritten by an editorial skip, and
`source_trail_candidates` constrains `disposition` to `accepted`/`rejected` — a new
authorization value would need a table migration for a fact that is not an authorization
fact. Packing omissions explain byte and duplicate decisions *inside* a candidate set;
enforcement removes a candidate from that set before packing, so reporting it as a packing
omission would misplace the decision.

**Identity bindings.**

| Artifact | Binds | Enforcement effect |
|---|---|---|
| Policy evaluation request | Caller, purpose, source, time | Unchanged: the opt-in is not part of authorization, so request schema and identity stay as they are |
| Policy decision | That request and the current authorization outcome | Unchanged: enforcement never alters allow/deny, and the decision is written in the same transaction |
| Context packet | Budget and the ordered selected segment identities, digests, ranks and byte lengths | Unchanged construction: two requests that release the same selection keep the same packet identity even if only one opted in, because the packet identifies released bytes, not the rule that produced them |
| Source trail | Request, decision, outcome, expression, budget, used bytes, packet identity, packing policy and every candidate decision, in a versioned record | New: an opt-in query writes the next trail version, whose identity additionally binds the consulted activation, declaration and every suppression |

An opt-in request with no active declaration therefore cannot collide with a default request:
their selections match, but their trails differ by version and identity. A deactivation is
recorded as a consulted activation event with a null declaration.

## 4. Associations and packing modes

**Decision for first enforcement.** Both packing policies are supported. An opt-in
enforcement request **must not** carry association declarations: the combination is rejected
as an invalid query before the retrieval transaction begins, so no part of the request is
written. The rejection is documented product behavior, not a silent bypass.

**Reasons.** Associated passages are read from a target item's current revision, so they are
the one path that could return a passage the suppression rule should have withheld; leaving
that path unconsidered would make enforcement incomplete while looking complete. Supporting
it needs associated-passage suppression plus its own omission reason, which is a second
bounded slice, not a first one. Rejecting the combination is explicit, testable and
reversible.

| Mode | First enforcement |
|---|---|
| `original` packing | Supported; the budget applies to surviving candidates |
| `exact-v1` packing | Supported; duplicate displacement applies to surviving candidates, so a suppressed copy cannot be named as the retained duplicate |
| `--associations` | Rejected with an invalid-query error |

## 5. Historical trail reads and compatibility

**Decision.** A trail read keeps its current shape: a fresh request is evaluated for current
authorization, and only then is the stored record returned. The recorded selection is
returned as recorded and is never reinterpreted under today's declaration, activation state
or item pointers.

- A v4 record is content-verified in the same way as v2/v3: every named segment still exists
  with the recorded digest, every suppression names an accepted, unselected candidate of that
  trail, and every named declaration and activation event still exists and still pins exactly
  the recorded revisions. Deactivation, later revisions and deleted items do not invalidate
  the record, because none of those facts is re-read for the decision.
- Read-time flags stay read-time facts. `indexed_now` continues to describe today's index, and
  a successor-currency diagnostic, if added, is computed when the trail is read.
- v1, v2 and v3 readers, identity derivations and bytes are unchanged, as are packet v1 and
  v2. No lifecycle reason is repurposed and no field is silently appended to an existing
  canonical schema; the new encoding is a new version with its own identity construction.
- CLI and MCP interfaces are unchanged by this proposal. `mousa_trail` keeps returning the
  same filtered, text-free structure, gaining at most one additive optional member when a v4
  record is read. The MCP query tool exposes no association opt-in today, so it should not
  expose enforcement either without a separate decision.
- A store that contains v4 trails requires a binary that knows v4, exactly as v2 and v3
  records already do; no store schema version changes, because no object set changes.

## Recorded selection

An opt-in query writes `mousa.source_trail.v4` instead of v1/v2/v3. Its
`packing_policy` is required and is either `original` or `exact-v1`, including a deny.
It has no associated passages or association omissions. Legacy field presence is unchanged.
It adds one closed member, present in every v4 record and rejected in earlier versions:

```json
"supersession": {
  "consulted": true,
  "activation_id": "…64 hex…",
  "declaration_id": "…64 hex…",
  "dispositions": [
    {
      "selection": "superseded",
      "segment_id": "…64 hex…",
      "content_sha256": "…64 hex…",
      "declaration_id": "…64 hex…",
      "predecessor_item_id": "…",
      "predecessor_representation_id": "…64 hex…",
      "successor_item_id": "…",
      "successor_representation_id": "…64 hex…"
    }
  ]
}
```

- `consulted` is a required boolean, never inferred from missing fields. An allow
  outcome requires `true`; a deny requires `false`, null activation and declaration IDs,
  an empty dispositions array, no candidates and zero used bytes. The unconsulted denial
  asserts nothing about whether activation history exists and reads no such history.
- When consulted, `activation_id` is the verified current event, or `null` only after
  verifying no activation history. `declaration_id` is the selected declaration, or `null`
  for no history or a deactivation. Both IDs must be explicitly present as null or values;
  the dispositions array is required even when empty, and null is not an empty array.
- A disposition row is self-contained evidence: which candidate was withheld, which
  declaration was applied, and which exact item and representation pins it names. Rows are
  ordered by the withheld candidate's `final_rank`. The row's `selection` value is the closed
  selection outcome; the candidate row's own `disposition` field keeps its
  authorization/lifecycle meaning.
- Validation requires: a non-null `declaration_id` implies a non-null `activation_id`;
  each unique disposition names one accepted, unselected candidate of the same trail with
  positive canonical text size, no packing omission, no duplicate reference and no lifecycle
  reasons, and uses the member's declaration. Dispositions are impossible without a selected
  declaration. Full canonical-content verification checks exact source and representation
  matching and completeness; a text-free codec alone cannot prove stored ancestry.
- Packing validation treats only the explicitly named disposition rows as superseded.
  It reproduces the recorded policy over surviving candidates in their original order and
  with their original ranks, mapping the result back to the complete candidate list. Under
  `original`, a survivor may be unselected for budget and still have no omission field:
  that is not supersession. Under `exact-v1`, surviving budget/duplicate omissions keep
  their usual meanings, and a duplicate must reference a prior selected survivor. Suppressed
  candidates remain unselected and consume no bytes; `used_bytes` counts selection only.
  For example, suppressing rank 1 of sizes [4, 8, 2] with budget 5 selects rank 3, while
  rank 2 is an ordinary budget skip without a supersession row.
- The trail identity is derived with a new version branch that binds the consultation boolean, the member's IDs and
  every disposition row in a fixed order, with a length-delimited empty field for each null,
  so absence and presence cannot collide. Earlier branches are untouched.
- The version is the marker: an opt-in request always writes v4, including a deny outcome and
  a source with no active declaration, so a v4 record never means "default query" and a
  default query never means "enforcement was considered".

### Query response counts and outcome precedence

Only opted-in query responses add `supersession_excluded`, an integer count of explicitly
withheld accepted candidates. It is present even when zero, including a deny or no-history
result; default responses omit it. `matched_candidates` still counts the considered primary
candidates before suppression. Lifecycle-rejected candidates count only as
`lifecycle_excluded`; suppressed candidates count only as `supersession_excluded`, never
as budget or duplicate omissions. Budget/duplicate counts cover surviving accepted candidates.

The new response `outcome` literal is `supersession_excluded`. This is a query-response
summary, not a new policy-decision outcome or a change to trail `outcome: allow|deny`.
Evaluate these branches in order:

| Condition | Response outcome |
|---|---|
| Policy decision is not allow | Existing `policy_excluded` behavior, with its existing `lifecycle_excluded` override for missing/inactive collection state; do not consult supersession |
| Any evidence was selected | `evidence` |
| No evidence and surviving candidates were omitted for budget | `budget_omitted` |
| No evidence, no budget omission, and supersession_excluded > 0 | `supersession_excluded` |
| No evidence, no budget or supersession exclusion, and lifecycle_excluded > 0 | `lifecycle_excluded` |
| Otherwise | `no_matches` |

For an empty result with both suppression and budget skips, budget takes precedence because
otherwise eligible surviving candidates failed to fit; both counts remain visible. Suppression
plus lifecycle rejection, without a surviving budget skip, yields `supersession_excluded`:
all otherwise eligible considered candidates were withheld. Lifecycle rejection alone keeps
its existing outcome. Exact duplicates cannot by themselves produce an empty packet: a
duplicate must refer to an earlier selected survivor. Default response values, field presence
and precedence remain unchanged.

## Transaction, verification reuse and failure handling

The enforcement stage runs inside the existing retrieval transaction, in this order:
evaluate and store the authorization decision; read the decision snapshot and verify the
source-scoped candidates; for an allow, verify the active declaration and derive dispositions,
or for a deny record unconsulted state without any administrative lookup; build the
trail from the surviving selection and pack it; insert the trail with its ordered candidate
projection; read it back and compare byte-exactly; commit. Any failure rolls the whole
request back, so a failed request leaves no decision, no trail and no omission row.

The active declaration must be read from **that same snapshot**. `supersession activation
state` starts its own read transaction, so it must not be called from inside the retrieval
transaction: a second transaction could observe a different current state than the candidates
being filtered. The smallest correct reuse is the store's existing verification helpers, all
of which already accept the current transaction handle: read the current-state row, read the
event it names, verify the projection against that event including the full predecessor chain
and the absence of a later successor, then read the declaration, which re-verifies both pins'
canonical provenance. The `state` command should then call the same extracted helper from its
own read transaction, so one verification exists with two callers.

Failure classes stay distinct:

| Recorded state | Behavior |
|---|---|
| Policy denial | No history lookup; recorded with consulted false, null IDs and no dispositions |
| Allowed source with no activation history | No suppression; recorded with consulted true and a null activation |
| Activation selects no declaration (deactivation) | No suppression; recorded with the consulted event and a null declaration |
| Successor pin historical, deactivated or no longer indexed | Suppression still applies while the declaration is active (section 1) |
| Missing current projection while history exists, broken chain, cycle, missing declaration, cross-source pointer, projection disagreeing with its tip | Integrity failure; the request fails closed with no packet and no committed decision or trail |

The chain verification is linear in the number of activation transitions for the source;
that cost is inside the writer transaction and must be measured by the implementation slice
rather than assumed away.

## Authorization and privacy

- Enforcement never runs for a non-allow decision: a deny releases no candidate, consults no
  declaration and records no other source's identities.
- A source's activation state is read by the source the decision authorized; the declaration
  named by that state must belong to the same source, and every disposition is written only
  into a trail whose decision belongs to that source; trail inspection still requires fresh
  authorization for that source.
- Error messages, omission rows and diagnostics name only identities of the authorized
  source. A denial must not reveal whether a declaration or activation exists, and a failed
  request must not leak another source's identifiers through an error string.

## Future acceptance matrix

None of these checks has been run; they describe what a later implementation must prove.

| Case | What it proves |
|---|---|
| Default baseline: two independent current items, no opt-in | Both items are released; trail and packet bytes and identities are exactly today's, so default behavior and IDs are unchanged |
| Opt-in, active declaration, pinned predecessor released | The predecessor is omitted with one disposition row binding activation, declaration and pins; the successor is not added |
| Deactivation restoration | After a deactivation event the predecessor is released again; the record shows the consulted event and a null declaration with no dispositions |
| Stale pin after the predecessor item is revised | The new revision is released because the pin names the previous representation; the item label alone never suppresses |
| Successor historical, inactive or absent | Suppression stays in force while the declaration is active; empty evidence is explained and no replacement passage is produced |
| Successor current but unmatched, over the limit or over budget | The predecessor is still suppressed and no successor text is inserted to fill the gap |
| Same content, multiple ancestry paths | Only the pinned representation's segments are withheld; byte-equal content and unrelated/source-shared ancestry are untouched |
| Unrelated-source isolation | A declaration and activation in one source suppress nothing in another; a denial reveals no declaration identity |
| Candidate limit and budget | A suppressed candidate frees budget for a later candidate; original budget skips stay distinct from suppression; recorded sizes and remaining ranks are preserved; an all-suppressed result is empty and labeled, not padded |
| Denied opt-in with retained activation history | No history is consulted or revealed; the v4 record says consulted false, not no-history |
| Empty mixed-cause response | Suppression plus a surviving budget skip yields budget_omitted; suppression plus lifecycle rejection without budget skips yields supersession_excluded; counts remain disjoint and defaults stay unchanged |
| Restart determinism | Reopening the store and re-running the same inputs produces the same dispositions, selection, packet identity and trail bytes |
| Historical trail readability | After later revisions, deletions and deactivation, the stored record still reads with current authorization and is not reinterpreted; v1–v3 reads are unchanged |
| Association combination | An opt-in enforcement request with associations is rejected before any write; both packing policies work with enforcement |
| Concurrent activation and query | A transition racing an opted-in query yields either the previous or the next state, never a mixture, and no partial decision or trail |
| Corrupt state rollback | Damaged projection, broken chain, missing declaration or cross-source pointer fails the request with nothing committed |

## Implementation sequence

Four bounded slices, each independently reviewable and testable:

1. **Domain contract in two bounded PRs.** First implement only the pure selection record,
   validation and exact-match function; no trail/packing integration. Then implement trail v4,
   its strict codec/identity and survivor-aware packing. Keep the existing `NewSourceTrail`
   and packing APIs and legacy bytes unchanged; use a separate opt-in constructor rather than
   changing the signature all current callers use. No store, CLI, migration or MCP change.
   *First implementable slice; exact scope below.*
2. **Transaction-local verified state.** One extracted verification helper reused by the
   `state` command and by retrieval; the opt-in trace path inside the existing writer
   transaction; v4 content and startup verification. Real SQLite fixtures.
3. **Native opt-in query surface.** An explicit boolean query opt-in, the added result count
   and outcome value, and an actual-CLI regression over a real store. MCP unchanged.
4. **Client or benchmark work** only where the changed surface justifies it independently.

**First slice: exact scope.** Only `internal/mousa/supersession_selection.go` (new)
and its test file: the selection member and disposition types, pure validation and the
exact-match function over verified candidates and explicitly supplied consulted state/declaration.
Bind consultation, event/declaration IDs and pins structurally, preserve input candidates and
canonical sizes, and return explicit suppression evidence without blanking text or packing.
Domain validation does not claim stored existence or canonical ancestry; transaction-local store
verification remains the later integration's responsibility. Preserve nonmatching/rejected
candidates and source isolation. No `trail.go`/`packing.go`, SQLite, CLI/MCP, migration,
dependency or identity-codec edit in this first slice. The following domain PR adds
`SourceTrailSchemaV4`, the versioned member/identity/codec, opt-in construction and packing
validation; each existing public function signature and legacy golden stays unchanged.

## Limitations and non-goals

This proposal does not implement, and does not claim: query enforcement; semantic
contradiction detection; authenticated authorship; factual truth or freshness; transitive
supersession chains or cycles; automatic or default enforcement; enforcement of non-item
sources; associated-passage enforcement; or any change to ranking, authorization, item
pointers, canonical declarations and activation history. It makes no performance claim: the
selection stage's cost, including the reused chain verification, is unmeasured until a later
slice measures it. No check in the acceptance matrix above has run.
