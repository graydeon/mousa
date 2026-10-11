# How Mousa works

[← Back to Mousa](../README.md) · [Usage guide](USAGE.md) · [Capability matrix](CAPABILITIES.md)

Implementation detail and design targets for the pre-alpha core. Supported interfaces
and measured results are distinguished below and in the capability matrix.

## Why Mousa?

*Mousa* is the Ancient Greek singular of *Muse*. In Greek mythology, Mnemosyne—the titan Goddess of Memory—is the mother of the nine Muses. The name gives Mousa a practical organizing idea: durable memory is not one act. Sources move through nine explicit stages before they become usable context.

## The problem

Long-lived knowledge does not only grow. It becomes stale, duplicated, contradictory, and detached from its sources. Search can return matching text without showing why it was selected, whether it remains current, or what it replaced. Agent systems also pay a direct cost when retrieval sends more context than the task requires.

Mousa currently offers source-linked lexical retrieval, source-lifecycle and
policy checks, inspectable ranking, and byte-budget evidence packing. A Git
documentation example can optionally limit its rendered prompt content using
a pinned tokenizer; this is not a token limit on CLI packets or a model's
complete context window.

## Current development boundary

The internal Go core now stores source-local supersession declarations pinned to
exact predecessor and successor item revisions, plus immutable activation history
and a verified current-state projection. Declaration and activation-event
administration are reachable natively through `supersession declaration put|get`
and `supersession activation put|get`, and the verified current state for one source
is inspectable read-only through `supersession activation state <source-id>`.

MCP declaration surfaces and query enforcement remain absent: a recorded activation
does not filter queries, move item pointers or create supersession omissions in
Source Trails. Separately identified current items remain
independent: a newer correcting item does not automatically suppress an older item.
Actor, time and reason labels do not authenticate authorship or establish factual
truth. Both domain halves of the proposed opt-in enforcement contract are
implemented: an internal pure selection API that derives consultation and withholding
evidence from the decision source and outcome, already verified candidates and
explicitly supplied verified activation and declaration records, plus the
`mousa.source_trail.v4` record with its strict version-aware codec and identity, a
separate explicit opt-in constructor and survivor-aware packing for both policies.
Neither half is called by a retrieval path, no CLI or MCP surface exposes one, and
nothing withholds evidence yet. The store also carries the transaction-local
consultation the later retrieval change needs: one private reader verifies a source's
current activation state and returns the declaration it selects, reading the state
projection, the event it names, the predecessor chain and the declaration through the
query handle it is given, so a caller that already holds a transaction consults that
transaction's snapshot instead of opening a second one, and a source with no history is
reported as such rather than as damaged state. Recorded state that exists but cannot be verified
fails as an integrity failure rather than as `not_found`: a current projection naming a missing event
is classified as corruption, and only a source with no activation history reports `not_found` with an
unchanged message. That reader changes no query and its
`supersession activation state` callers keep their signature and projection JSON. The canonical store
read path now content-verifies a stored v4 trail on every read and during startup, against the
activation event and declaration that record itself names rather than against today's activation
projection: a later transition, deactivation or item revision cannot reinterpret a stored record, each
withheld row is re-derived from the verified declaration's exact predecessor pin, and a missing,
extra, wrong-pin or unrelated-candidate row, a missing event or declaration and a cross-source
reference all fail as integrity errors without repair. The retrieval opt-in, its CLI and MCP surfaces
and query enforcement remain recorded in
[opt-in supersession enforcement](SUPERSESSION_ENFORCEMENT.md) and are
not implemented: no query emits a v4 trail and nothing withholds evidence. See the
[supersession boundary](CAPABILITIES.md#supersession-core-boundary)
and [contract evidence](RESEARCH.md#supersession-contract-evidence).

## The nine-stage retrieval backbone

Mousa's retrieval backbone deliberately echoes the nine Muses. Stages 1–5 are implemented in the Go core; stages 6–9 are implemented in narrower form and the deferred parts are listed:

| Stage | Responsibility | Status |
|---|---|---|
| **1. Ingest** | Register source material with its origin, identity, and policy metadata. | Implemented. |
| **2. Normalize** | Convert input into a stable representation while preserving the original source. | Implemented for UTF-8 text. |
| **3. Segment** | Produce deterministic, addressable chunks. | Implemented for byte-range text segments. |
| **4. Index** | Build portable lexical and optional semantic search structures. | Lexical (SQLite FTS5) implemented; semantic retrieval is planned, not implemented. |
| **5. Match** | Retrieve candidate evidence for a request. | Implemented for lexical matching. |
| **6. Rank** | Order candidates using inspectable relevance and policy signals. | Implemented for BM25 order plus source-lifecycle policy; hybrid ranking is planned. |
| **7. Verify** | Check authority, freshness, sensitivity, conflicts, and supersession. | Partially implemented: source lifecycle and deployment policy decisions; revision-pinned declaration and activation-event administration plus read-only current-state inspection through native commands, and the internal supersession contract (a pure selection API, a v4 trail record with survivor-aware packing, a transaction-local reader that verifies the current activation state and the declaration it selects inside a caller's transaction, and canonical store content and startup verification of a stored v4 trail) that no retrieval path calls yet. Query supersession enforcement, factual freshness, and semantic conflict checks are deferred. |
| **8. Trace** | Record how evidence moved through retrieval in a durable Source Trail. | Implemented for enforced lexical retrieval; see the narrower field list below. |
| **9. Pack** | Assemble selected evidence within an explicit context budget. | Core and CLI use a released-text byte budget. The Git documentation consumer offers an optional prompt-content token projection; model-window accounting is not implemented. |

Each stage has one responsibility and a visible boundary. The pipeline can be tested stage by stage, and no single model, provider, or harness owns the result.

## Source Trails

A Source Trail records how a context packet was produced. Default packing writes
`mousa.source_trail.v1`, binding the policy request and decision, outcome,
search expression, byte budget, packet identity, and ordered candidate
dispositions. Opt-in exact-content packing writes v2, which also binds
duplicate omissions and retained-segment relationships. Queries that record
a declared-association stage write v3, binding associated passages and
omissions. Older trail bytes and identities remain unchanged.

The opt-in supersession record exists as a domain type without a caller. The separate
explicit constructor `NewSourceTrailWithSupersession` writes `mousa.source_trail.v4`
from an allow or deny decision, already verified candidates and explicitly supplied
verified activation and declaration records: it requires one closed `supersession`
member binding whether supersession was consulted, the nullable activation and
declaration identities and every suppression row, requires an explicit packing policy,
carries no associated passages, packs only the surviving candidates under that policy
and keeps a withheld candidate's canonical size, rank and accepted disposition. No
retrieval path, store write or CLI or MCP surface builds one, so no query emits a v4
trail. A stored v4 record is content-verified by the canonical read path and by startup
verification, against the recorded activation event and declaration and that
declaration's exact predecessor pin, with a withheld candidate excluded from the
retained byte-equal set so it is never mistaken for a duplicate omission. A canonical parent a v4
record names but the store no longer holds is reported as an integrity failure rather than
`not_found`; an identity that was never stored still reports `not_found`. Transforms,
ranking-stage explanations, the retrieval opt-in and query enforcement remain planned;
the transaction-local consultation reader that a retrieval caller would use is
implemented and changes no query. The selection and
trail-version
contract for opt-in supersession enforcement is recorded in
[docs/SUPERSESSION_ENFORCEMENT.md](SUPERSESSION_ENFORCEMENT.md). A
consumer's token-limited rendering is not a stored Source Trail or a new
canonical packet.

The goal is not to present a score as an explanation. The goal is to retain enough evidence to inspect what was selected, what was rejected, and why.

## Intended callers

Mousa is intended to expose one portable core through narrow interfaces for:

- agent harnesses and model-independent automation;
- command-line tools;
- application and SDK integrations;
- MCP clients;
- documented HTTP clients where a service boundary is justified;
- ordinary JSON and JSONL import and export.

The supported executable, `cmd/mousa`, provides the local CLI and a stdio MCP interface.

## Design principles

- **Local-first:** the baseline should work without a hosted service.
- **Source-linked:** retrieved context should retain durable links to its evidence.
- **Lean:** use the smallest reliable components and avoid mandatory services.
- **Portable:** start with ordinary files, SQLite, documented protocols, and a tested CPU-only baseline.
- **Inspectable:** ranking, filtering, transformation, and packet assembly should be traceable.
- **Deterministic where possible:** stable identities, hashes, manifests, and truncation rules should make results reproducible.
- **Token-aware:** retrieval quality and context cost should be measured together.
- **Provider-independent:** no model provider, accelerator, agent harness, or database service should own the canonical store.

Compatibility will be documented as a tested matrix. Mousa will not claim support for an operating system, architecture, accelerator, model backend, or agent harness until that combination has reproducible public verification.

## Foundation

Implemented and planned capabilities, marked per item:

- SQLite-first local storage (implemented);
- lexical retrieval through SQLite FTS5 or an equally portable built-in mechanism (FTS5 implemented);
- optional semantic retrieval behind a narrow provider interface (planned);
- inspectable hybrid ranking (planned);
- deterministic ingestion, chunk identity and source hashing (implemented); caller-owned pinned corpus manifests (documentation examples); general core manifest support (planned);
- authority, freshness, sensitivity, status, and supersession metadata (lifecycle status and deployment policy implemented; revision-pinned supersession declaration storage with native `put`/`get` CLI administration; immutable activation events and a verified current-state projection with native `put`/`get`/`state` CLI administration; an internal pure supersession selection API and a `mousa.source_trail.v4` record with survivor-aware packing that no retrieval path calls yet, plus canonical store content and startup verification of a stored v4 trail against the activation event and declaration it recorded; supersession query enforcement, factual freshness, and semantic conflicts deferred);
- secret filtering and non-indexable sensitivity classes (planned);
- byte-budget selection with opt-in exact-content deduplication (implemented in core/CLI); optional prompt-content token projection (Git documentation consumer only); truncation, approximate redundancy removal and model-window budgeting (planned);
- source-scoped declared associations with attributed, bounded context (opt-in CLI query; not inferred relationships or access grants);
- source-linked context packets with durable Source Trail identifiers (implemented in the core and exposed by `cmd/mousa`);
- documented export formats that other tools can read without Mousa (planned).

Items marked planned are design targets, not released functionality or a release checklist. The capability matrix distinguishes supported interfaces from internal APIs; the research record binds observations to their measured revisions and limitations.
