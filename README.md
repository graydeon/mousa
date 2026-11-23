![Mousa — an experimental memory instrument](assets/mousa_readme_banner_v2_1600x480.png)

# Useful context. A trail back to its source.

**Mousa turns local documents into compact, source-linked context for agents and applications.**
Search your material, set a byte budget,
and receive passages with an inspectable record of how they were selected.

[Quick start](#quick-start) · [Usage guide](docs/USAGE.md) · [Capabilities](docs/CAPABILITIES.md) · [Public benchmarks](https://github.com/graydeon/mousa-benchmarks)

## Why Mousa?

*Mousa* is the Ancient Greek singular of *Muse*. In Greek mythology,
Mnemosyne—the Titan goddess of memory—is the mother of the nine Muses.
The name gives Mousa a practical organizing idea: durable memory is not one act.
Sources move through nine explicit stages before they become usable context.

## Nine stages, one inspectable path

![Mousa's nine stages, read left to right and top to bottom: ingest, normalize, segment; index, match, rank; verify, trace, pack.](assets/mousa-nine-stages.svg)

Mousa prepares your material, finds matching evidence, and assembles passages with one durable query trail. Each stage has one responsibility; the diagram shows the logical backbone rather than the exact order of database operations.

[Explore each stage and its implementation boundary →](docs/OVERVIEW.md#the-nine-stage-retrieval-backbone)

> **Pre-alpha.** CLI and local stdio MCP are available. Build from source with
> Go 1.25+. Packages are internal; there is no stable SDK.
> Source-checkout capabilities and pending changes are distinguished in the
> [development status](docs/OVERVIEW.md#source-and-release-status).

## Why use it?

An agent needs relevant context without carrying every document into every request.
Mousa makes that context smaller and easier to inspect:

| Your need | What Mousa gives you |
|---|---|
| Find useful passages | Source-scoped lexical retrieval. |
| Control context size | A released-text byte budget, with exact-content deduplication. |
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
./mousa -store local.sqlite sync --allow ./documents
./mousa -store local.sqlite query --budget-bytes 4096 ./documents 'harbor inspection'
```

Commands return JSON. Query returns `passages` and their `trail`. Read the saved trail with:

```sh
./mousa -store local.sqlite trail TRAIL_ID
```

Replace `TRAIL_ID` with `trail.id`. The 4,096-byte budget covers
released passage text, not JSON overhead or model tokens. Preview shows what would
be imported without opening the store. A source with no access event is denied to
every caller; `--allow` records allow after the sync. Without it, the query above
would return `trail.sources[0].outcome: deny` and no passages. See
[access](docs/USAGE.md#access).

**Next:** [directory selection and resync](docs/USAGE.md#local-usage-cmdmousa),
[JSONL input and administration](docs/USAGE.md#local-usage-cmdmousa), or
[MCP setup](docs/USAGE.md#local-mcp-usage).

## What works today?

Local directory and JSONL ingestion, lexical retrieval, source lifecycle checks, and byte-budget packing are implemented. One query writes one canonical `mousa.trail.v1` record as a log leaf; its `result_sha256` is stable for the same request and store state. Agents can save, export, import, and recall durable notes with authorship and citation status. The same executable serves local stdio MCP.

Access defaults to deny. The owner reads full trails through the store file; MCP checks current access and omits denied-source details. Vector and hybrid retrieval are not supported by this schema.

Scale at 100k documents and 1M retrieval units is a development target, not a
property of today's binary. It remains a target until the scale probe passes at
both sizes. See [scale targets](docs/OVERVIEW.md#scale-targets).

The [capability matrix](docs/CAPABILITIES.md) is the detailed supported-behavior reference.

The source-built BEIR evaluator (`cmd/beir`) accepts
`-segment-policy fixed-v1|passage-v1`, default `fixed-v1`.

## Explore

| Start here | For |
|---|---|
| [Usage guide](docs/USAGE.md) | CLI commands, input limits, administration and consumer examples. |
| [MCP integration](docs/USAGE.md#local-mcp-usage) | Local stdio tool setup and the project-history consumer example. |
| [How Mousa works](docs/OVERVIEW.md) | The nine-stage pipeline, Source Trails and design principles. |
| [Capability matrix](docs/CAPABILITIES.md) | Supported interfaces, exact contracts and limitations. |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for issue and pull request templates,
required author-review attestations, testing evidence, and issue linking.

## Credits

README banner portrait source: Dante Gabriel Rossetti, *Mnemosyne* (c. 1876–1881), Delaware Art Museum, accession 1935-22. [Public-domain reproduction via Wikimedia Commons](https://commons.wikimedia.org/wiki/File:Mnemosyne_DAM.jpg). Materially transformed for Mousa.

## License

Mousa is licensed under the GNU Affero General Public License v3.0. See [`LICENSE`](LICENSE).
