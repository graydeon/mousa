# How Mousa works

[← Back to Mousa](../README.md) · [Usage guide](USAGE.md) · [Capability matrix](CAPABILITIES.md)

Implementation detail and design targets for the pre-alpha core. Supported interfaces
and measured results are distinguished below and in the capability matrix.

## Source and release status

The supported installation is a source build with Go 1.25 or newer. At the
2026-10-24 documentation audit there were no Git tags or GitHub releases; merged
source is pre-alpha development, not a versioned release or stable SDK.

Accepted source includes measured token budgets. These source capabilities are
not a claim about a tagged release; independent captures retain their own producing commits.

These accepted source additions remain distinct from tagged releases and
independently published observations.

Source builds containing `manifest` add bounded current-source inspection through
the native directory/JSONL selectors and MCP configured JSONL aliases. This is
the source-manifest addition, not a tagged-release or accepted measurement claim.
See [usage](USAGE.md#current-source-manifests) for caller refusal on mismatch or
denial and the indexed-snapshot boundary.

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

Raw historical record-by-ID reads and policy administration remain native-only.
Actor, time and reason labels do not authenticate authorship or establish factual truth.

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
| **7. Verify** | Check authority, freshness, sensitivity, and conflicts. | Partially implemented: source lifecycle and deployment policy decisions. Defaults remain uncurated; factual freshness, authenticated authorship and semantic conflict checks are not implemented. |
| **8. Trace** | Record how evidence moved through retrieval in a durable Source Trail. | Lexical, vector and hybrid selection through CLI/MCP; versioned producing facts below. |
| **9. Pack** | Assemble selected evidence within an explicit context budget. | Released-text byte packing and optional CLI/MCP whole-rendering token selection, with declared reservations. Provider/model prompt framing remains outside the count. |

Each stage has one responsibility and a visible boundary. The pipeline can be tested stage by stage, and no single model, provider, or harness owns the result.

## Source Trails

A Source Trail records how a context packet was produced. Default packing writes
`mousa.source_trail.v1`, binding the policy request and decision, outcome,
search expression, byte budget, packet identity, and ordered candidate
dispositions. Opt-in exact-content packing writes v2, which also binds
duplicate omissions and retained-segment relationships. Older trail bytes and identities remain unchanged.

Native vector queries write `mousa.source_trail.v5` instead. Its closed `vector`
member records the declared space, exact query vector and identity, current eligible
and indexed counts, and ordered embedding/source/content pins and cosine scores.
It has no lexical expression. Original
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
reaped before insertion. Default queries and v1, v2, v5 and v6 remain unchanged.
See [token packing](CAPABILITIES.md#native-token-aware-packing).

An unknown requested identity remains `not_found`, and legacy readers retain their behavior.
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
- deterministic ingestion, chunk identity and source hashing (implemented); caller-owned current indexed-source manifests with fresh-authorized native/MCP comparison (source builds containing `manifest`); full-store interchange and action-completion tracking (not implemented);
- authority, freshness, sensitivity and status metadata (lifecycle status and deployment policy implemented; factual freshness, authenticated authorship and semantic conflicts not implemented);
- secret filtering and non-indexable sensitivity classes (planned);
- byte-budget selection and opt-in exact-content deduplication; optional native token-aware selection and declared window reservations (implemented); truncation, approximate redundancy removal and authenticated provider/model framing (not implemented);
- source-linked context packets with durable Source Trail identifiers (implemented in the core and exposed by `cmd/mousa`);
- documented JSON/JSONL inputs and authorized normalized passage/vector interchange (implemented); portable full-store interchange (not implemented).

Items marked planned are design targets, not released functionality or a release checklist. The capability matrix distinguishes supported interfaces from internal APIs; the research record binds observations to their measured revisions and limitations.
