![Mousa — an experimental memory instrument](assets/mousa_readme_banner_v2_1600x480.png)

# Useful context. A trail back to its source.

**Mousa turns local documents into compact, source-linked context for agents and applications.**
Search your material, set a byte budget or opt into a rendered-context token cap,
and receive passages with an inspectable record of how they were selected.

[Quick start](#quick-start) · [Usage guide](docs/USAGE.md) · [Capabilities](docs/CAPABILITIES.md) · [Public benchmarks](https://github.com/graydeon/mousa-benchmarks)

## Why Mousa?

*Mousa* is the Ancient Greek singular of *Muse*. In Greek mythology,
Mnemosyne—the Titan goddess of memory—is the mother of the nine Muses.
The name gives Mousa a practical organizing idea: durable memory is not one act.
Sources move through nine explicit stages before they become usable context.

## Nine stages, one inspectable path

![Mousa's nine stages, read left to right and top to bottom: ingest, normalize, segment; index, match, rank; verify, trace, pack.](assets/mousa-nine-stages.svg)

Mousa prepares your material, finds matching evidence, and assembles a context packet
with a Source Trail. Each stage has one responsibility; the diagram shows the
logical backbone rather than the exact order of database operations.

Search defaults to lexical retrieval. Native callers can also export authorized
passages, import externally produced vectors and query one declared space by exact
cosine similarity or fuse lexical and vector matches with reciprocal rank fusion.
Automatic embeddings, factual freshness and semantic conflict checks remain planned.
[Explore each stage and its implementation boundary →](docs/OVERVIEW.md#the-nine-stage-retrieval-backbone)

> **Pre-alpha.** CLI and local stdio MCP are available. Build from source with
> Go 1.25+. Packages are internal; there is no stable SDK.

## Why use it?

An agent needs relevant context without carrying every document into every request.
Mousa makes that context smaller and easier to inspect:

| Your need | What Mousa gives you |
|---|---|
| Find useful passages | Source-scoped lexical, exact cosine, or hybrid RRF retrieval with caller-supplied vectors. |
| Control context size | A released-text byte budget, optional exact-content deduplication, and native whole-context token packing with a prepared local tokenizer. |
| Follow the evidence | Source identities, passage coordinates, digests and a durable Source Trail. |
| Keep up with edits | Resync current items while preserving canonical history. |
| Use your own tools | One Go executable, SQLite storage, JSON output and local stdio MCP. |

Mousa retrieves evidence; your agent or application decides how to use it.
It does not generate answers or determine whether a passage is factually true.

## Quick start

From the repository root, build Mousa and index a directory of Markdown or text files:

```sh
CGO_ENABLED=0 go build -o mousa ./cmd/mousa
./mousa -store local.sqlite sync --preview ./documents
./mousa -store local.sqlite sync ./documents
./mousa -store local.sqlite query --budget-bytes 4096 ./documents 'harbor inspection'
```

Commands return JSON. A query reports selected evidence, a context packet ID and a
`trail_id`. Inspect that trail with:

```sh
./mousa -store local.sqlite trail ./documents TRAIL_ID
```

Replace `TRAIL_ID` with the query's returned identifier. The 4,096-byte budget covers
released passage text, not JSON overhead or model tokens. Preview shows what would
be imported without opening the store.

**Next:** [directory selection and resync](docs/USAGE.md#local-usage-cmdmousa),
[JSONL input and administration](docs/USAGE.md#local-usage-cmdmousa), or
[MCP setup](docs/USAGE.md#local-mcp-usage).

For externally generated embeddings, see the [native vector workflow](docs/USAGE.md#caller-supplied-vector-workflow).
Mousa performs no model inference or network calls for this path.
Use [native hybrid retrieval](docs/USAGE.md#hybrid-lexical-and-vector-query) to combine
query text and an explicitly supplied vector in one ranked, packed result.

For an exact returned context string under a token cap, use
[`query --context-tokens`](docs/USAGE.md#native-token-aware-context-packing).
This opt-in counts the complete versioned rendering with official
`tiktoken==0.12.0`, `o200k_base`, ordinary-text encoding. It requires a prepared
local Python environment/cache; queries never download vocabulary assets.
Ordinary queries and the CGO-disabled Go build do not require Python.

## What works today?

Local directory and JSONL ingestion, lexical retrieval, source lifecycle and access
checks, byte-budget packing, exact-content deduplication, declared associations and
Source Trail inspection are implemented. The same executable serves local stdio MCP.

Revision-pinned supersession declarations and activation administration are available,
together with explicit native directory (`query --supersession DIRECTORY TEXT`) and
JSONL (`query --source ID --supersession TEXT`) query opt-ins, and the same opt-in on
the local stdio MCP `mousa_query` tool; an opted-in `mousa_trail` call returns the consultation a
`mousa.source_trail.v4` record recorded. Default and opted-out queries remain
uncurated; inspections preserve recorded selection and omit the member unless requested.
Inspection reauthorizes access, including a committed decision on denial without
historical disclosure. Native-created v1–v3 history remains inspectable without a
supersession member. Opt-in usage and source-masked refusal boundaries are in the
[usage guide](docs/USAGE.md#local-usage-cmdmousa). Native caller-supplied vector retrieval
uses a separate v5 trail; hybrid selection records branch contributions in v6.
Native token-aware lexical/vector/hybrid selection records measured rendering
and reservation facts in v7. MCP token packing, automatic embedding generation
and secret filtering remain pending. Caller-declared reservations do not prove
full model-window safety.

The [directory supersession example](docs/USAGE.md#directory-supersession-example)
shows how to discover revision pins, prepare declaration and activation records, and
inspect the recorded reason for withholding a predecessor.

The [2026-10-13 independent MCP inspection observations](https://github.com/graydeon/mousa-benchmarks/tree/7b29e226ea1d8362262f99dfb95bf2ee06c5fa7d/results/2026-10-13-mcp-trail-supersession)
record synthetic native/stdio behavior, not retrieval quality or performance.

The [capability matrix](docs/CAPABILITIES.md) is the detailed supported-behavior reference.
The [research record](docs/RESEARCH.md) separates regression coverage from measured
retrieval quality and performance.

## Explore

| Start here | For |
|---|---|
| [Usage guide](docs/USAGE.md) | CLI commands, input limits, administration and consumer examples. |
| [MCP and host integration](docs/USAGE.md#local-mcp-usage) | Local tool setup, project-history consumer, ChatGPT desktop and Codex CLI. |
| [How Mousa works](docs/OVERVIEW.md) | The nine-stage pipeline, Source Trails and design principles. |
| [Capability matrix](docs/CAPABILITIES.md) | Supported interfaces, exact contracts and limitations. |
| [Research and public benchmarks](docs/RESEARCH.md) | Reproducible observations, pinned revisions and their evidence boundaries. |
| [Supersession contract](docs/SUPERSESSION_ENFORCEMENT.md) | Revision-pinned replacement and the implemented native opt-in enforcement boundary. |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for issue and pull request templates,
required author-review attestations, testing evidence, and issue linking.

## Credits

README banner portrait source: Dante Gabriel Rossetti, *Mnemosyne* (c. 1876–1881), Delaware Art Museum, accession 1935-22. [Public-domain reproduction via Wikimedia Commons](https://commons.wikimedia.org/wiki/File:Mnemosyne_DAM.jpg). Materially transformed for Mousa.

## License

Mousa is licensed under the GNU Affero General Public License v3.0. See [`LICENSE`](LICENSE).
