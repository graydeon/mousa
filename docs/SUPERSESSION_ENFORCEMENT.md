# Opt-in supersession enforcement

For source-build versus release status and the pending v8 composition boundary,
see [development status](OVERVIEW.md#source-and-release-status).

**Status: implemented in the fresh store retrieval entry point, native directory and
JSONL query opt-ins and the local stdio MCP tools.** Use
`query --supersession DIRECTORY TEXT` or `query --source ID --supersession TEXT`
with flags before positional arguments, or MCP `mousa_query` with `supersession: true`.
Defaults, legacy trail versions and every opted-out MCP response are unchanged.
Association-plus-supersession combinations are rejected.
The MCP inspection opt-in, which returns the recorded member of a v4 trail with the same
boolean, is implemented and specified in
[MCP supersession opt-in](MCP_SUPERSESSION.md). The
[2026-10-13 independent inspection observations](https://github.com/graydeon/mousa-benchmarks/tree/7b29e226ea1d8362262f99dfb95bf2ee06c5fa7d/results/2026-10-13-mcp-trail-supersession)
record its synthetic native/stdio client boundary separately from the older native
and query-only captures; they do not measure retrieval quality or performance.

`Store.EvaluateAndTraceSupersessionLexical` evaluates current authorization, reads
source-scoped verified candidates, consults verified administration and builds
`mousa.source_trail.v4` inside the existing writer transaction. It reuses
`readSupersessionConsultation`, `BuildSupersessionSelection` and
`NewSourceTrailWithSupersession`; no public reader opens a nested transaction.
Every byte-only opted-in query records v4, even when denied or nothing is suppressed.
Canonical readback must agree before commit.

Lexical token-budget composition is also implemented: add `--context-tokens N`
and the prepared tokenizer flags, or MCP `context_tokens` with its trusted
startup backend. This path writes `mousa.source_trail.v8` through
`EvaluateAndTraceTokenLexical` in the same writer transaction. Both packers count
the complete survivor rendering only; withheld rows keep their original sizes
and ranks but consume neither budget nor duplicate retention.
Token admission counts all candidates toward its 100-candidate limit and
retains each accepted passage's 4,096-byte bound. Its 262,144-byte aggregate
text limit applies to accepted survivors before budget/duplicate omissions,
not to withheld bytes. Every withheld candidate still requires canonical
content and ancestry verification with its positive size and original rank.
Byte-only enforcement still writes v4; omitted/false token enforcement writes v7.
Vector/hybrid and association supersession combinations remain unsupported.
The [v8 identity and verification contract](CAPABILITIES.md#native-token-aware-packing)
preserves packet v3's full producing-selection binding, so equal returned text
does not imply equal v7/v8 packet IDs. The byte-only packet rule below is unchanged.
History verifies recorded administration after later transitions, never today's
activation projection. MCP independently opts into recorded supersession/token
members; only native explicit recount measures retained selected bytes again.

A valid active declaration withholds only its exact predecessor representation's
accepted candidates. Candidate ranks, digests, sizes and ancestry remain recorded;
withheld text is cleared in a copied returned candidate slice after trail construction.
Both packing policies process survivors, and a withheld copy cannot seed a duplicate.
No successor evidence is manufactured. Stored v4 history verifies the event and
declaration it recorded, not today's activation or item pointers.

Declarations, activation history and verified current-state projection are separate
administration; recording them never changes a default query or moves an item pointer.
See the [supersession boundary](CAPABILITIES.md#supersession-core-boundary).

For a complete native directory example, including exact source identity and revision
pin preparation, see [Directory supersession example](USAGE.md#directory-supersession-example).
Directory resolution retains the `mousa-local` namespace and absolute path; `--source`
retains the distinct `mousa-jsonl` namespace. Both call the same enforced transaction.
Resyncing a changed predecessor creates an unpinned revision; historical v4 inspection
continues to verify the original pins.

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

- A v4 record is content-verified in the same way as v2/v3, and additionally against the
  administration it recorded: every named segment still exists with the recorded digest and
  canonical size, every withheld candidate is re-derived from the verified declaration's exact
  predecessor pin, and the named declaration and activation event still exist, still belong to
  the decision source and still agree with each other. The event and declaration are read
  from the record's own snapshot, not today's activation projection. Deactivation and
  item revisions do not invalidate history. The opted-in store path writes v4 after
  verifying those inputs in its writer transaction; the text-free domain codec alone
  proves structural consistency, not stored existence or canonical ancestry.
- Read-time flags stay read-time facts. `indexed_now` continues to describe today's index, and
  a successor-currency diagnostic, if added, is computed when the trail is read.
- v1, v2 and v3 readers, identity derivations and bytes are unchanged, as are packet v1 and
  v2. No lifecycle reason is repurposed and no field is silently appended to an existing
  canonical schema; the new encoding has its own version and identity construction.
- Native directory and JSONL queries expose the boolean opt-in and native `trail` inspection includes
  the recorded v4 member. The local stdio MCP tools expose the same opt-in:
  `mousa_query` reports the withheld count, and `mousa_trail` returns the recorded member
  of a v4 record. Opted-out responses keep today's property set, so ordinary inspection
  stays filtered, text-free and unchanged for a legacy record.
  Association combinations remain unsupported.
- A store that contains v4 trails requires a binary that knows v4, exactly as v2 and v3
  records already do; no store schema version changes, because no object set changes.

## Recorded selection

An opt-in query writes `mousa.source_trail.v4` instead of v1/v2/v3. Its
`packing_policy` is required and is either `original` or `exact-v1`, including a deny.
It has no associated passages or association omissions. Legacy field presence is unchanged.
It adds one closed member, present in every v4 record and rejected in earlier versions:

```json
{
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
being filtered. The reuse is implemented in `internal/sqlite/supersession_activation.go`: the
state command's verification is the extracted `verifySupersessionActivationState`, which reads
the current-state row, the event it names, the projection against that event including the full
predecessor chain and the absence of a later successor, all through a supplied query handle, and
`readSupersessionConsultation` adds the declaration read, which re-verifies both pins' canonical
provenance. `GetSupersessionActivationState` supplies the read transaction and the commit for its
own callers and changes no reported code, message or projection; a caller inside the writer
transaction consults the same verification through the consultation reader instead of a second
transaction. A verified absence of activation history is reported as a nil state, so no error code
is read as one, and a missing projection with retained history stays an integrity failure.

Failure classes stay distinct:

| Recorded state | Behavior |
|---|---|
| Policy denial | No history lookup; recorded with consulted false, null IDs and no dispositions |
| Allowed source with no activation history | No suppression; recorded with consulted true and a null activation |
| Activation selects no declaration (deactivation) | No suppression; recorded with the consulted event and a null declaration |
| Successor pin historical, deactivated or no longer indexed | Suppression still applies while the declaration is active (section 1) |
| Missing current projection while history exists, projection naming a missing current event, broken chain, cycle, missing declaration, cross-source pointer, projection disagreeing with its tip | Integrity failure; the request fails closed with no packet and no committed decision or trail |

The chain verification is linear in the number of activation transitions for the source
and runs inside the writer transaction. No latency or capacity claim follows from the
correctness checks; sustained contention and long-chain performance remain unmeasured.

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

## Acceptance matrix

The product regression suite exercises the store and native CLI boundaries below.
Independent native observations belong in a separate companion capture, not in the
domain or SQLite test counts.

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
| Restart determinism | Reopening and re-running preserves dispositions, selection and packet identity; fresh request/decision/trail identities intentionally differ, while reading the original trail preserves its exact bytes |
| Historical trail readability | After later revisions, deletions and deactivation, the stored record still reads with current authorization and is not reinterpreted; v1–v3 reads are unchanged |
| Association combination | An opt-in enforcement request with associations is rejected before any write; both packing policies work with enforcement |
| Concurrent activation and query | A transition racing an opted-in query yields either the previous or the next state, never a mixture, and no partial decision or trail |
| Corrupt state rollback | Damaged projection, broken chain, missing declaration or cross-source pointer fails the request with nothing committed |

## Implemented interfaces

The domain selection and v4 trail constructors remain separate internal APIs; their
existing signatures and all legacy identities are unchanged. The store adds
`EvaluateAndTraceSupersessionLexical(ctx, request, expression, limit, budgetBytes,
packingPolicy)`, a fresh-evaluation entry point with no association argument. Existing
`EvaluateAndTraceLexical`, `EvaluateAndTraceAssociatedLexical` and
`TraceEnforcedLexical` retain their default and retry contracts.

The native flag is boolean and only available on the JSONL item-source query path:

```sh
mousa -store local.sqlite query --source notes --supersession 'amber'
mousa -store local.sqlite query --source notes --supersession --packing-policy exact-v1 'amber'
```

Invocation validation precedes writable open where possible. Invalid flags, directory
queries and association combinations do not create a store. A valid query uses the
existing writable open: creation or supported migrations can happen before later source,
authorization or integrity failures. Rollback guarantees apply to the retrieval
transaction's decision and trail writes, not to that creation/migration boundary.

**Implemented (selection half).** `internal/mousa/supersession_selection.go` and its test file
provide:

- `SupersessionSelection` — the consultation member: `Consulted`, a nullable activation and
  declaration identity, and a non-nullable, explicitly non-null disposition array.
- `SupersessionDisposition` — one typed row naming the withheld candidate's segment identity and
  content digest, the applied declaration and that declaration's exact predecessor/successor
  item and representation pins.
- `BuildSupersessionSelection(sourceID, outcome, candidates, activation, declaration)` — the pure
  builder and matcher. A non-allow outcome is unconsulted with no identity and no row; an allow
  outcome is consulted, where a nil activation state means the caller verified no history and a
  state naming no declaration is a deactivation. It withholds accepted candidates whose own
  segment representation equals the selected declaration's predecessor pin and whose verified
  ancestry names the decision source, emits one row per withheld candidate in original
  accepted `final_rank` order, copies identities instead of aliasing caller values, and reads no
  store, successor state, text or budget.
- `SupersessionSelection.Validate`, `SupersessionDisposition.Validate` and
  `SupersessionSelection.ValidateAgainst` — structural and consistency checks that reject
  contradictory consultation, invalid records, cross-source or mismatched identities, duplicate
  or out-of-order rows, wrong pins and rows naming rejected or otherwise non-matching candidates.
  They prove consistency with the supplied canonical and verified inputs only; they establish no
  stored existence, authorization, pinned ancestry, integrity or current state.

The function itself remains pure selection evidence. The opted-in store caller uses
the separate v4 constructor and clears withheld returned text only after preserving
canonical candidate sizes in the trail.

**Implemented (trail half).** `internal/mousa/trail.go` and `internal/mousa/packing.go` now
provide:

- `SourceTrailSchemaV4` (`mousa.source_trail.v4`) and the `SourceTrail.Supersession` member. A v4
  record requires exactly one closed member and an explicit `original` or `exact-v1` packing
  policy, carries no associated passages or association omissions, and every earlier version
  rejects the member even when it is null.
- `NewSourceTrailWithSupersession(request, decision, expression, candidates, budgetBytes,
  packingPolicy, activation, declaration)` — the separate explicit opt-in constructor. It derives
  the member through `BuildSupersessionSelection` (no second implementation of the exact-pin
  match), requires the consultation flag to agree with the decision outcome, keeps an unchanged
  released selection on the same packet identity, and adds the consulted activation, declaration
  and every disposition row to the trail identity in a new version branch with length-delimited
  empty fields. `NewSourceTrail`, every existing packing function signature and every legacy
  golden are unchanged.
- Survivor-aware packing: `packAcceptedCandidates` and `packExact` take an explicit suppression
  mask, so `original` reproduces its greedy first-fit and `exact-v1` its duplicate displacement
  over the surviving candidates only, mapped back onto the complete candidate list with original
  ranks. A withheld candidate stays accepted and unselected with its canonical size and consumes
  no budget; a suppressed copy can never be the retained exact-v1 duplicate.
- `Validate` and `DecodeSourceTrail` version-aware strictness: mime-free structural checks of the
  member, membership of each row in that trail's accepted unselected candidates with a matching
  digest, positive size, no lifecycle reasons and no packing omission, unique rows ordered by the
  withheld candidate's original `final_rank`, the recorded policy reproduced over survivors, and
  rejection of missing, null, unknown, duplicate, mistyped or contradictory members. For a v4 record
  the candidate invariants both policies share are checked before the packing policy is applied: a
  segment is considered once, accepted candidates carry consecutive ranks from one with no lifecycle
  reason, and a rejected candidate carries lifecycle reasons instead of a packing omission, so
  `original` and `exact-v1` reject the same candidate set.

The domain constructor reads no store and proves no stored existence, canonical ancestry
or authorization. The opted-in retrieval path supplies transaction-verified inputs,
stores the record and verifies canonical readback before commit. The same canonical
verifier serves historical reads and startup.

## Stored v4 trail verification

The read and startup half of the second slice is implemented in `internal/sqlite/trail_content.go`
and `internal/sqlite/trail.go`. The canonical trail read verifies a stored `mousa.source_trail.v4`
record where it already verified v2 and v3: every named segment must still resolve to its canonical
record with the recorded digest and byte size, still derive from the decision source and still hash
its retained indexed text to that digest; the recorded packing policy is reproduced over the
surviving candidates only, with a withheld candidate neither selected nor allowed to seed the
retained byte-equal set, so a digest stays a comparison shortlist and the retained bytes decide
exact-`v1` equality; and the recorded activation event and declaration are read through the same
snapshot, must verify as canonical, belong to the decision source and carry the whole predecessor
chain behind them, and the event must select exactly the recorded declaration or a null one. The
recorded rows are then re-derived from the verified declaration's exact predecessor pin, so a
missing, extra, wrong-pin or unrelated-candidate row is an integrity failure. A denial and a verified
no-history record keep their structural identity and empty-member checks and read no administration.
The startup record scan runs the same verification, so a store holding a damaged v4 record refuses to
open; no new object set, schema version or migration is involved. A failed check never repairs,
rewrites or partially accepts a record, and later activation changes, deactivation and item revisions
do not reinterpret a stored trail. A named canonical dependency that has disappeared is classified as
that damage rather than as ordinary unavailability: the read reports an integrity error for it, while
an identity that was never stored keeps its `not_found`, as do the v1–v3 reads.
Source-scoped inspection must first prove ownership through the typed decision parent.
If that parent has disappeared after the inspector opened, the source guard returns
`not_found` for both own and foreign orphans, as for an unknown ID. It does not infer
ownership from unverified record bytes. Unscoped canonical reads and fresh-open startup
verification still reject the damage as `integrity`; this is not an in-flight repair or
a successful read. Denial precedes that guard and commits only fresh authorization.

## Limitations and non-goals

Enforcement is explicit, source-local and exact-pin-only. It does not implement semantic
contradiction detection, authenticated authorship, factual truth or freshness, transitive
supersession, automatic/default enforcement or associated-passage enforcement;
MCP administration remains unavailable
([MCP supersession opt-in](MCP_SUPERSESSION.md)). Ranking, authorization, current item
pointers, declarations and activation history are not rewritten by a query, and an
inspection reinterprets no recorded selection.

Real-file SQLite regressions cover live-store corruption rollback and explicit
transaction ordering. Native subprocess regressions exercise the CLI surface; native
startup refusal is distinct from corruption injected after an already-open store.
Historical retired indexed text remains unarchived, so byte equality is checked only
where text is retained. No performance, semantic-quality, model-window or actual-host
agent acceptance claim is made.
