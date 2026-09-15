![Mousa — an experimental memory instrument](assets/mousa_readme_banner_v2_1600x480.png)

> **Status:** Pre-alpha. Build `cmd/mousa` from source for local UTF-8 directory sync, bounded JSONL input, and source-scoped lexical queries. The Go core also implements policy decisions, Source Trails, and context packets; not every core capability is exposed by the CLI. Packages remain internal, with no stable SDK. See the [capability matrix](docs/CAPABILITIES.md) for supported behavior and the [research record](docs/RESEARCH.md) for measured results and limitations.

Mousa is a local-first retrieval and memory backbone for agents and other software that need useful context over long periods. It is intended to recover relevant source material, preserve what changed, and assemble compact context packets that can be inspected before use. The result is memory with a source trail rather than an opaque answer.

## Why Mousa?

*Mousa* is the Ancient Greek singular of *Muse*. In Greek mythology, Mnemosyne—the titan Goddess of Memory—is the mother of the nine Muses. The name gives Mousa a practical organizing idea: durable memory is not one act. Sources move through nine explicit stages before they become usable context.

## The problem

Long-lived knowledge does not only grow. It becomes stale, duplicated, contradictory, and detached from its sources. Search can return matching text without showing why it was selected, whether it remains current, or what it replaced. Agent systems also pay a direct cost when retrieval sends more context than the task requires.

Mousa is being designed to address those problems with source-linked retrieval, explicit lifecycle and policy metadata, inspectable ranking, and byte-budget context assembly; token-aware budgeting is planned.

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
| **7. Verify** | Check authority, freshness, sensitivity, conflicts, and supersession. | Partially implemented: source lifecycle and deployment policy decisions; freshness, supersession, and conflict checks are deferred. |
| **8. Trace** | Record how evidence moved through retrieval in a durable Source Trail. | Implemented for enforced lexical retrieval; see the narrower field list below. |
| **9. Pack** | Assemble the selected evidence within an explicit context budget. | Implemented as an explicit byte budget; token-aware selection is planned. |

Each stage has one responsibility and a visible boundary. The pipeline can be tested stage by stage, and no single model, provider, or harness owns the result.

## Source Trails

A Source Trail is the durable record of how a context packet was produced. Today's `mousa.source_trail.v1` record binds the policy request and decision, the decision outcome, the search expression, the byte budget, the context packet identity, and the ordered candidates with segment identity, content hash, final rank, released byte count, disposition, lifecycle reasons, and selection flag. Binding an actor, filters, ranking stages, transforms, supersession decisions, and an explicit packet hash remains planned.

The goal is not to present a score as an explanation. The goal is to retain enough evidence to inspect what was selected, what was rejected, and why.

## Intended callers

Mousa is intended to expose one portable core through narrow interfaces for:

- agent harnesses and model-independent automation;
- command-line tools;
- application and SDK integrations;
- MCP clients;
- documented HTTP clients where a service boundary is justified;
- ordinary JSON and JSONL import and export.

The first shipped interface is the local vertical slice, `cmd/mousa`.

## Local usage (cmd/mousa)

The local CLI synchronizes text items into a canonical store and returns
source-linked evidence under a byte budget. Build with Go 1.25 or newer:

```sh
CGO_ENABLED=0 go build -o mousa ./cmd/mousa
./mousa -store local.sqlite sync ./documents
./mousa -store local.sqlite status ./documents
./mousa -store local.sqlite query ./documents 'zebra habitat'
```

`sync`, `status`, and `query` print JSON on stdout. Directory item identity is the
relative POSIX path. Updates and reverts atomically replace current search evidence;
deletion removes current evidence without deleting canonical history. Identical
content restored after deletion becomes searchable again. `status` reports active
items separately from historical observations.

For an explicit item stream:

```sh
printf '%s\n' '{"id":"note@draft","text":"The harbor inspection is on Friday."}' |
  ./mousa -store local.sqlite sync --source inspection-notes
./mousa -store local.sqlite query --source inspection-notes 'harbor inspection'
```

JSONL input is bounded to 1 MiB per line, 64 MiB per invocation, and 10,000 records.
Deletion is explicit; omission does not delete. Failed input retains its committed
prefix and emits no success result. See the [input and recovery contract](docs/CAPABILITIES.md)
before upgrading an existing directory store or retaining sensitive material.

The CLI currently uses a fixed 8,192-byte released-text budget and does not expose
durable Source Trail inspection. A byte budget is not a token budget. Verified
provenance does not establish factual truth, and classification records are not
automatic categorization or classification-based authorization.

SDK, MCP, HTTP, and human-facing application interfaces remain planned. They are not
part of the supported CLI workflow.

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
- deterministic ingestion, chunk identity, source hashing, and manifests (ingestion and identity implemented; manifests planned);
- authority, freshness, sensitivity, status, and supersession metadata (lifecycle status and deployment policy implemented; freshness, supersession, and conflicts deferred);
- secret filtering and non-indexable sensitivity classes (planned);
- byte-budget-aware selection today, with deterministic truncation, deduplication, redundancy removal, and token-aware budgeting planned;
- source-linked context packets with durable Source Trail identifiers (implemented in the core; not yet exposed by `cmd/mousa`);
- documented export formats that other tools can read without Mousa (planned).

Items marked planned are design targets. They are not a release checklist or a statement of current functionality, and nothing in this document is a measured claim.

## Credits

README banner portrait source: Dante Gabriel Rossetti, *Mnemosyne* (c. 1876–1881), Delaware Art Museum, accession 1935-22. [Public-domain reproduction via Wikimedia Commons](https://commons.wikimedia.org/wiki/File:Mnemosyne_DAM.jpg). Materially transformed for Mousa.

## License

Mousa is licensed under the GNU Affero General Public License v3.0. See [`LICENSE`](LICENSE).
