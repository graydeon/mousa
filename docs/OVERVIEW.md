# How Mousa works

[← Back to Mousa](../README.md) · [Usage guide](USAGE.md) · [Capability matrix](CAPABILITIES.md)

Implementation detail and design targets for the pre-alpha core. Supported interfaces
and measured results are distinguished below and in the capability matrix.

## Source and release status

The supported installation is a source build with Go 1.25 or newer. No release has
been tagged; merged source is pre-alpha development, not a versioned release or stable
SDK. The `pre-rework` tag marks the last state before the 2026 rework.

## Scale targets

Opening a store in under 1 s at 100k documents, lexical ingest above 500 documents/s,
and lexical query p50 below 50 ms at 1M retrieval units are development targets,
not properties of today's binary. They are targets for verify-on-read instead of
full-store verification on open, batch ingest, and bounded query work. They remain
targets until the scale probe passes at 100k documents and 1M retrieval units.

## Why Mousa?

*Mousa* is the Ancient Greek singular of *Muse*. In Greek mythology, Mnemosyne—the titan Goddess of Memory—is the mother of the nine Muses. The name gives Mousa a practical organizing idea: durable memory is not one act. Sources move through nine explicit stages before they become usable context.

## The problem

Long-lived knowledge does not only grow. It becomes stale, duplicated, contradictory, and detached from its sources. Search can return matching text without showing why it was selected or whether it remains current. Agent systems also pay a direct cost when retrieval sends more context than the task requires.

Mousa offers source-linked lexical retrieval, source-lifecycle checks,
inspectable ranking, and whole-passage byte packing.

## Current development boundary

This schema supports lexical retrieval. Vector and hybrid modes are not supported.

Raw historical record-by-ID reads remain native-only.
Actor, time and reason labels do not authenticate authorship or establish factual truth.

## The nine-stage retrieval backbone

Mousa's retrieval backbone deliberately echoes the nine Muses. Stages 1–5 are implemented in the Go core; stages 6–9 are implemented in narrower form and the deferred parts are listed:

| Stage | Responsibility | Status |
|---|---|---|
| **1. Ingest** | Register source material with its origin and identity. | Implemented. |
| **2. Normalize** | Convert input into a stable representation while preserving the original source. | Implemented for UTF-8 text. |
| **3. Segment** | Produce deterministic, addressable chunks. | Implemented for byte-range text segments. |
| **4. Index** | Build portable lexical and optional vector search structures. | SQLite FTS5 for lexical candidates. |
| **5. Match** | Retrieve candidate evidence for a request. | Lexical candidates in an authorized source snapshot. |
| **6. Rank** | Order candidates using inspectable relevance and policy signals. | BM25 with recorded branch scores and ranks. |
| **7. Verify** | Check source state and, later, freshness and conflicts. | Partially implemented: source lifecycle checks and append-only per-source access events with default deny ([Access](USAGE.md#access)). Factual freshness, authenticated authorship and semantic conflict checks are not implemented. |
| **8. Trace** | Record how evidence moved through retrieval in a durable Source Trail. | One canonical query trail as a log leaf. |
| **9. Pack** | Assemble selected evidence within an explicit context budget. | Released-text byte packing. Provider/model prompt framing remains outside the count. |

Each stage has one responsibility and a visible boundary. The pipeline can be tested stage by stage, and no single model, provider, or harness owns the result.

## Source Trails

Each query stores one `mousa.trail.v1` record as a log leaf before returning
passages. The trail binds the request, decisions, snapshot, candidates and byte
packing. The `result_sha256` omits per-call metadata and denied-source details so
the same request and state can be compared. Trail reads are read-only; the file
owner can inspect the record, while a served reply carries a caller view. Retired text is not archived, so historical reads can verify immutable
digests and coordinates but not reconstruct removed bytes.

## Intended callers

Mousa is intended to expose one portable core through narrow interfaces for:

- agent harnesses and model-independent automation;
- command-line tools;
- application and SDK integrations;
- documented HTTP clients where a service boundary is justified;
- ordinary JSON and JSONL import and export.

The supported executable, `cmd/mousa`, provides the local CLI and an opt-in loopback HTTP service. Mousa ships no MCP server; an agent with a shell calls the CLI, and anything else calls the HTTP service.

## Design principles

- **Local-first:** the baseline should work without a hosted service.
- **Source-linked:** retrieved context should retain durable links to its evidence.
- **Lean:** use the smallest reliable components and avoid mandatory services.
- **Portable:** start with ordinary files, SQLite, documented protocols, and a tested CPU-only baseline.
- **Inspectable:** ranking, filtering, transformation, and packet assembly should be traceable.
- **Deterministic where possible:** stable identities, hashes, and truncation rules should make results reproducible.
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

## Foundation

Implemented and planned capabilities, marked per item:

- SQLite-first local storage (implemented);
- lexical retrieval through SQLite FTS5 or an equally portable built-in mechanism (FTS5 implemented);
- deterministic ingestion, chunk identity and source hashing (implemented); full-store interchange (not implemented);
- source status and lifecycle (implemented); per-source access events with default deny (implemented); factual freshness, authenticated authorship and semantic conflicts (not implemented);
- secret filtering (planned);
- byte-budget selection and exact-content deduplication; truncation, approximate redundancy removal and authenticated provider/model framing (not implemented);
- source-linked passages with durable query trail identifiers (implemented in the core and exposed by `cmd/mousa`);
- documented JSON/JSONL inputs and durable-note export/import (implemented); portable full-store interchange (not implemented).

Items marked planned are design targets, not released functionality or a release checklist. The capability matrix distinguishes supported interfaces from internal APIs.
