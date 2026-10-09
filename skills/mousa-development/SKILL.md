---
name: mousa-development
description: Implement or review Mousa changes against its canonical identity, authorization, SQLite, retrieval and public evidence contracts.
---

# Develop Mousa

Read the current task, repository documentation and operator-supplied workflow before work. Follow the authorized scope and execution environment. This skill supplies product-specific guidance; it does not authorize publication, model calls, source access or infrastructure changes.

## Find the affected contract

Read [CAPABILITIES](../../docs/CAPABILITIES.md) for supported interfaces and evidence limits, and the relevant implementation and callers before editing. For supersession, read [SUPERSESSION_ENFORCEMENT](../../docs/SUPERSESSION_ENFORCEMENT.md): implemented domain records and transaction-local consultation are distinct from proposed retrieval enforcement. Inspect current code and documentation rather than assuming a proposal is available to clients.

Trace changed symbols and record callers and compatibility impact using available local code intelligence. Keep policy authorization, lifecycle rejection, supersession selection and packing dispositions distinct. A source-scoped decision must not silently broaden to another source or caller.

## Preserve canonical behavior

Keep earlier schema versions, public signatures, canonical encodings and identity goldens unchanged unless the task explicitly authorizes a compatibility change. A hash binds fields; it does not prove stored existence, ancestry, authorization, byte equality or historical timing. Compare canonical bytes where exact-content packing requires equality; a digest is only a comparison shortlist.

Reuse the supplied SQLite transaction for candidate, administrative and provenance reads. Calling a public method that opens another transaction can mix snapshots. Distinguish verified no history from damaged recorded state: a missing event named by an existing projection is corruption; an unknown identity requested directly from a historical reader can be not-found. Fail without manufacturing, repairing or committing partial records.

Historical trail reads verify recorded immutable evidence and current authorization without reinterpreting selection under today's activation projection. Preserve canonical candidate sizes and ranks when withholding; suppressed candidates consume no packing budget and cannot become retained duplicates. Keep unavailable historical text verification limits explicit.

## Prove and deliver the change

Use focused regression checks that fail for the intended behavior before the fix and pass afterward. Missing symbols are compile-stage evidence, not behavioral proof. Use real temporary SQLite stores for storage and transaction claims, explicit ordering for snapshot tests, and recomputed identities for corruption tests when a stale ID would obscure the invariant. Retain independent golden expectations and negative observations.

Apply installed simplification, Go compatibility, documentation and quality-review guidance when relevant. Inspect supported Go versions and CI before choosing APIs. Run the exact checks required by the current publication workflow on the final tree; preserve logs and distinguish local tests, published-head CI and post-merge CI.

Update public capability and usage docs with the actual supported behavior. Use Conventional Commit subjects and PR titles. Submit a reviewable PR and stop when the operator's workflow assigns merge to an independent reviewer. Report final tested/public tree identities, evidence and unresolved findings; silence or a prior-head review is not final-head approval.

At each handoff assess [mousa-benchmarks](https://github.com/graydeon/mousa-benchmarks) separately. New native interfaces, retrieval behavior or empirical claims need independently reproducible public observations at the matching milestone. Internal contract tests do not establish native or semantic acceptance. Preserve historical archive pins; do not relabel administration-only observations as enforcement results. Publish companion evidence before linking product claims to it. Design-only changes need accurate status, not fabricated measurements.
