![Mousa — an experimental memory instrument](assets/mousa-readme-banner.png)

> **Status:** Repository initialization / pre-alpha. Mousa does not yet provide a working engine, supported installation path, or stable API.

Mousa is a local-first retrieval and memory backbone for agents and other software that need useful context over long periods. It is intended to recover relevant source material, preserve what changed, and assemble compact context packets that can be inspected before use. The result is memory with a source trail rather than an opaque answer.

## Why Mousa?

*Mousa* is the Ancient Greek singular of *Muse*. In Greek mythology, Mnemosyne—Memory—is the mother of the nine Muses. The name gives Mousa a practical organizing idea: durable memory is not one act. Sources move through nine explicit stages before they become usable context.

## The problem

Long-lived knowledge does not only grow. It becomes stale, duplicated, contradictory, and detached from its sources. Search can return matching text without showing why it was selected, whether it remains current, or what it replaced. Agent systems also pay a direct cost when retrieval sends more context than the task requires.

Mousa is being designed to address those problems with source-linked retrieval, explicit freshness and supersession metadata, inspectable ranking, and token-budget-aware context assembly.

## The nine-stage retrieval backbone

Mousa's planned retrieval backbone deliberately echoes the nine Muses:

| Stage | Responsibility |
|---|---|
| **1. Ingest** | Register source material with its origin, identity, and policy metadata. |
| **2. Normalize** | Convert input into a stable representation while preserving the original source. |
| **3. Segment** | Produce deterministic, addressable chunks. |
| **4. Index** | Build portable lexical and optional semantic search structures. |
| **5. Match** | Retrieve candidate evidence for a request. |
| **6. Rank** | Order candidates using inspectable relevance and policy signals. |
| **7. Verify** | Check authority, freshness, sensitivity, conflicts, and supersession. |
| **8. Trace** | Record how evidence moved through retrieval in a durable Source Trail. |
| **9. Pack** | Assemble the selected evidence within an explicit context budget. |

Each stage has one responsibility and a visible boundary. The pipeline can be tested stage by stage, and no single model, provider, or harness owns the result.

## Source Trails

A Source Trail is the planned durable record of how a context packet was produced. It will identify the request and actor, candidate sources, filters, ranking stages, selected chunks, source hashes, transforms, supersession decisions, and final packet hash.

The goal is not to present a score as an explanation. The goal is to retain enough evidence to inspect what was selected, what was rejected, and why.

## Intended callers

Mousa is intended to expose one portable core through narrow interfaces for:

- agent harnesses and model-independent automation;
- command-line tools;
- application and SDK integrations;
- MCP clients;
- documented HTTP clients where a service boundary is justified;
- ordinary JSON and JSONL import and export.

These interfaces are planned, not shipped. A future human-facing application would provide observability, policy control, provenance inspection, correction, and export. It would not be the canonical store or a generic chat shell.

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

## Planned foundation

The initial architecture is expected to evaluate:

- SQLite-first local storage;
- lexical retrieval through SQLite FTS5 or an equally portable built-in mechanism;
- optional semantic retrieval behind a narrow provider interface;
- inspectable hybrid ranking;
- deterministic ingestion, chunk identity, source hashing, and manifests;
- authority, freshness, sensitivity, status, and supersession metadata;
- secret filtering and non-indexable sensitivity classes;
- token-budget-aware selection, deterministic truncation, deduplication, and redundancy removal;
- source-linked context packets with durable Source Trail identifiers;
- documented export formats that other tools can read without Mousa.

This list is a design target. It is not a release checklist or a statement of current functionality.

## Credits

README banner portrait source: Dante Gabriel Rossetti, *Mnemosyne* (c. 1876–1881), Delaware Art Museum, accession 1935-22. [Public-domain reproduction via Wikimedia Commons](https://commons.wikimedia.org/wiki/File:Mnemosyne_DAM.jpg). Materially transformed for Mousa.

## License

Mousa is licensed under the GNU Affero General Public License v3.0. See [`LICENSE`](LICENSE).
