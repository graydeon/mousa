# How Mousa works

[← Back to Mousa](../README.md) · [Usage guide](USAGE.md) · [Capability matrix](CAPABILITIES.md)

Implementation detail and design targets for the pre-alpha core. Supported interfaces
and measured results are distinguished below and in the capability matrix.

## Source and release status

The supported installation is a source build with Go 1.25 or newer. At the
2026-10-24 documentation audit there were no Git tags or GitHub releases; merged
source is pre-alpha development, not a versioned release or stable SDK.

Accepted main as of 2026-10-30 is `12e959e1c4a0c2242a654e3e6fa8e7e3789c9ea6`.
It includes native/MCP external-vector import, vector/hybrid retrieval, token
budgets and lexical supersession with token budgets. [PR90](https://github.com/graydeon/mousa/pull/90)
and its independent client evidence are accepted; [issue91](https://github.com/graydeon/mousa/issues/91)
is closed after the survivor aggregate-admission correction.

This branch proposes explicit supersession for byte-packed vector/hybrid queries,
with closed v9 producing history. The implementation and runnable example below
do not establish independent acceptance. Vector/hybrid token-plus-supersession
remains unsupported. Synthetic vectors establish protocol and arithmetic, not
embedding-model quality.

## Why Mousa?

*Mousa* is the Ancient Greek singular of *Muse*. In Greek mythology, Mnemosyne—the titan Goddess of Memory—is the mother of the nine Muses. The name gives Mousa a practical organizing idea: durable memory is not one act. Sources move through nine explicit stages before they become usable context.

## The problem

Long-lived knowledge does not only grow. It becomes stale, duplicated, contradictory, and detached from its sources. Search can return matching text without showing why it was selected, whether it remains current, or what it replaced. Agent systems also pay a direct cost when retrieval sends more context than the task requires.

Mousa offers source-linked lexical/vector/hybrid retrieval, source-lifecycle and
policy checks, inspectable ranking, and whole-passage byte packing. Native
`--context-tokens` optionally selects against the exact returned rendered context
using a prepared official offline tokenizer. Caller-declared window reservations
do not prove the safety of an application's complete model prompt.

## Current development boundary

Native callers can export authorized current normalized passages, import embeddings
from an external producer and query one declared vector space by exact CPU cosine
similarity or combine lexical and vector branches with equal-weight RRF(k=60).
The native [vector](USAGE.md#caller-supplied-vector-workflow) and
[hybrid](USAGE.md#hybrid-lexical-and-vector-query) workflows work for directory and
JSONL sources. Generation stays external; synthetic examples do not establish
embedding-model quality, semantic equivalence or factual truth.

The internal Go core now stores source-local supersession declarations pinned to
exact predecessor and successor item revisions, plus immutable activation history
and a verified current-state projection. Declaration and activation-event
administration are reachable natively through `supersession declaration put|get`
and `supersession activation put|get`, and the verified current state for one source
is inspectable read-only through `supersession activation state <source-id>`.

Native directory queries (`query --supersession DIRECTORY TEXT`) and JSONL queries
(`query --source ID --supersession TEXT`) can opt into exact revision-pin suppression.
The local stdio MCP `mousa_query` tool takes the same opt-in as an optional boolean. The MCP `mousa_trail` tool takes that
boolean too and returns the recorded consultation member of a v4 trail. The fresh store
entry point
`EvaluateAndTraceSupersessionLexical` evaluates authorization, verifies candidates,
consults administration and records a v4 trail inside one writer transaction.
Only an accepted candidate whose own representation is the active declaration's
exact predecessor pin is withheld. Successor availability does not gate suppression,
and no successor passage is manufactured. Both packing policies apply to survivors.

Byte-packed vector and hybrid queries also accept explicit supersession, natively
and through configured MCP aliases. Their v9 history records the existing producing
ranking facts and consultation together. Source admission and coverage remain
unchanged before withholding. See the [runnable example](USAGE.md#vector-and-hybrid-supersession).

Defaults are unchanged: recording an activation alone does not filter a default
query, reinterpret a recorded member or move item pointers. MCP administration and
association-plus-supersession queries are unsupported. Actor, time and reason labels
do not authenticate authorship or establish factual truth.

A query prepares and size-checks its expression before opening the store only when it
opts in. Default and `--supersession=false` queries, including directory queries, keep
the established post-open failure and writable-open boundary.

Stored v4 trails verify their recorded event, declaration and canonical candidate
content rather than today's activation or item pointers. Later transitions and item
revisions do not reinterpret history. Damaged administration fails a fresh allowed
request without committing its decision or trail; a denied request consults no
administration. See the [enforcement contract](SUPERSESSION_ENFORCEMENT.md),
[capability boundary](CAPABILITIES.md#supersession-core-boundary) and
[evidence record](RESEARCH.md#supersession-contract-evidence).

## The nine-stage retrieval backbone

Mousa's retrieval backbone deliberately echoes the nine Muses. Stages 1–5 are implemented in the Go core; stages 6–9 are implemented in narrower form and the deferred parts are listed:

| Stage | Responsibility | Status |
|---|---|---|
| **1. Ingest** | Register source material with its origin, identity, and policy metadata. | Implemented. |
| **2. Normalize** | Convert input into a stable representation while preserving the original source. | Implemented for UTF-8 text. |
| **3. Segment** | Produce deterministic, addressable chunks. | Implemented for byte-range text segments. |
| **4. Index** | Build portable lexical and optional vector search structures. | SQLite FTS5 and canonical caller-supplied vectors implemented; no ANN index or embedding inference. |
| **5. Match** | Retrieve candidate evidence for a request. | Lexical, explicit vector, and combined hybrid queries over one current source snapshot. |
| **6. Rank** | Order candidates using inspectable relevance and policy signals. | BM25, exact cosine, or equal-weight RRF(k=60) with separate branch facts. |
| **7. Verify** | Check authority, freshness, sensitivity, conflicts, and supersession. | Partially implemented: source lifecycle and deployment policy decisions, revision-pinned supersession administration and explicit directory/JSONL and configured MCP lexical query enforcement. Defaults remain uncurated; factual freshness, authenticated authorship and semantic conflict checks are not implemented. |
| **8. Trace** | Record how evidence moved through retrieval in a durable Source Trail. | Lexical, vector and hybrid selection through CLI/MCP; versioned producing facts below. |
| **9. Pack** | Assemble selected evidence within an explicit context budget. | Released-text byte packing and optional CLI/MCP whole-rendering token selection, with declared reservations. Provider/model prompt framing remains outside the count. |

Each stage has one responsibility and a visible boundary. The pipeline can be tested stage by stage, and no single model, provider, or harness owns the result.

## Source Trails

A Source Trail records how a context packet was produced. Default packing writes
`mousa.source_trail.v1`, binding the policy request and decision, outcome,
search expression, byte budget, packet identity, and ordered candidate
dispositions. Opt-in exact-content packing writes v2, which also binds
duplicate omissions and retained-segment relationships. Queries that record
a declared-association stage write v3, binding associated passages and
omissions. Older trail bytes and identities remain unchanged.

A byte-only opted-in directory or JSONL query always writes
`mousa.source_trail.v4`, including denial, verified no-history and deactivation.
Its closed `supersession` member binds consultation, nullable activation and
declaration identities, and each withheld candidate's exact pins. Withheld candidates
keep their accepted disposition, original rank and canonical size, but release no text,
consume no budget and seed no retained duplicate set. The packet identity still binds
the released selection, so an unchanged selection has the same packet identity.

Native vector queries write `mousa.source_trail.v5` instead. Its closed `vector`
member records the declared space, exact query vector and identity, current eligible
and indexed counts, and ordered embedding/source/content pins and cosine scores.
It has no lexical expression, associations or supersession consultation. Original
and exact-v1 byte packing use the same packet v1 identity domain for the released
selection. Native history reauthorizes and checks recorded producing embeddings,
not today's vectors or current item pointers. Lexical defaults and earlier bytes
remain unchanged. MCP history exposes recorded vector facts only with
`retrieval_details: true` after fresh authorization.

Hybrid queries write `mousa.source_trail.v6`: prepared lexical request/policy,
space/query-vector facts, canonical pins, separate branch ranks/BM25/cosine and
the fixed `rrf-equal-k60-v1` order. Both byte packers consume the fused identity
union. History checks producing records without searching today's index; past
coverage completeness and lexical BM25 cannot be reconstructed from later state.
The [hybrid contract](CAPABILITIES.md#native-hybrid-lexical-and-vector-retrieval)
states those limits. Earlier canonical versions remain unchanged; MCP query and
opted-in history have their own declared hybrid schemas.

CLI and MCP token queries write `mousa.source_trail.v7` and packet v3, binding existing
retrieval facts, the versioned rendering/backend, declared allowances, selections,
precise omissions and measured digest/count. A prepared local Python process
counts proposed whole renderings inside the authorized writer snapshot and is
reaped before insertion. Default queries and v1–v6 remain unchanged.
See [token packing](CAPABILITIES.md#native-token-aware-packing).

On the pending composition branch, lexical token queries with supersession write
v8. It binds recorded consultation and token selection, verifies withheld canonical
content, and packs survivors. Omitted/false token supersession stays v7; byte-only
enforcement stays v4. Packet v3 binds the complete producing selection: equal
returned text does not establish equal v7/v8 packet identities. See the
[source status and aggregate-bound correction](#source-and-release-status).

Canonical read and startup verification check the recorded administration and candidate
content. A missing canonical parent named by v4 is an integrity failure; an unknown
requested identity remains `not_found`, and legacy readers retain their behavior.
Source-scoped inspection is narrower: if a missing decision parent prevents ownership
verification in an already-open inspector, it returns `not_found` for own and foreign
orphans alike. Fresh-open verification rejects the store instead. Successful denied
inspection commits only fresh authorization evidence and never looks up history.
Retired indexed text is not archived: historical byte comparisons can use only text
still retained. Normal token-aware history verifies retained rendering digests
without Python and reports the count as recorded producer evidence. Explicit
native recount requires every original selected byte and the official backend.
The separate documentation consumer's prompt projection is not a canonical packet.

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

## Evidence and temporal boundaries

A content digest identifies bytes, not factual authority. Keep source identity,
logical item identity and physical revision pins separate when importing versions;
a repeated upstream ID alone does not establish a replacement relationship.
Explicit supersession is a source-local declaration, not inferred truth.

Current item pointers and immutable trails do not provide arbitrary historical
text retrieval. A future historical adapter must declare knowledge cutoff K,
validity target V and current serving permission S, including when corrections
and authority assertions became known. Historical access still obeys current
permission. Filtering candidates by time alone would not establish prefix-only
ranking: shared FTS statistics can depend on other indexed rows. No such temporal
query contract is currently implemented.

Deletion removes current searchable evidence under the adapter's contract; it
does not promise secure erasure of canonical metadata, backups or copies held by
callers. A retrieval trail describes evidence selection, not execution or task
completion. Empty retrieval is a bounded observation, not proof of absence.
See the [evaluation priorities](RESEARCH.md#evaluation-priorities-and-promotion-gates).

## Foundation

Implemented and planned capabilities, marked per item:

- SQLite-first local storage (implemented);
- lexical retrieval through SQLite FTS5 or an equally portable built-in mechanism (FTS5 implemented);
- bounded exact vector retrieval with external embeddings (implemented through CLI/MCP); automatic embedding generation and provider integration (planned);
- inspectable native hybrid ranking with caller-supplied vectors (implemented);
- deterministic ingestion, chunk identity and source hashing (implemented); caller-owned pinned corpus manifests (documentation examples); general core manifest support (planned);
- authority, freshness, sensitivity, status, and supersession metadata (lifecycle status and deployment policy implemented; native revision-pinned declaration/activation administration and explicit directory/JSONL query supersession enforcement with v4 trails; factual freshness, authenticated authorship and semantic conflicts not implemented);
- secret filtering and non-indexable sensitivity classes (planned);
- byte-budget selection and opt-in exact-content deduplication; optional native token-aware selection and declared window reservations (implemented); truncation, approximate redundancy removal and authenticated provider/model framing (not implemented);
- source-scoped declared associations with attributed, bounded context (opt-in CLI query; not inferred relationships or access grants);
- source-linked context packets with durable Source Trail identifiers (implemented in the core and exposed by `cmd/mousa`);
- documented JSON/JSONL inputs and authorized normalized passage/vector interchange (implemented); portable full-store interchange (not implemented).

Items marked planned are design targets, not released functionality or a release checklist. The capability matrix distinguishes supported interfaces from internal APIs; the research record binds observations to their measured revisions and limitations.
