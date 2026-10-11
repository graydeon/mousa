# Using Mousa

[← Back to Mousa](../README.md) · [Capability matrix](CAPABILITIES.md) · [How Mousa works](OVERVIEW.md)

Full CLI, MCP and consumer examples. Run commands from the repository root;
build the executable from source before using the examples.

## Local usage (cmd/mousa)

The local CLI synchronizes text items into a canonical store and returns
source-linked evidence under a byte budget. Build with Go 1.25 or newer:

```sh
CGO_ENABLED=0 go build -o mousa ./cmd/mousa
./mousa -store local.sqlite sync --preview ./documents
./mousa -store local.sqlite sync ./documents
./mousa -store local.sqlite status ./documents
./mousa -store local.sqlite query --budget-bytes 4096 ./documents 'zebra habitat'
```

Commands print JSON on stdout. Directory item identity is the
relative POSIX path. Updates and reverts atomically replace current search evidence for the same item ID;
deletion removes current evidence without deleting canonical history. Identical
content restored after deletion becomes searchable again. `status` reports active
items separately from historical observations.

Directory defaults select Markdown/plain-text extensions and skip hidden and
generated entries. Preview shows selected names and byte lengths without opening
the store. Use repeatable `--include`/`--exclude` globs to constrain scope and
`--all-text` only as a deliberate override. Selection flags are not saved between
invocations; successful scope changes remove old items outside the selected set.
See [directory selection and limits](CAPABILITIES.md#directory-selection-and-input-limits).

The default ingestion policy, `fixed-v1`, uses segments of at most 4,096 UTF-8 bytes.
To opt into bounded passage boundaries, use
`./mousa -store local.sqlite sync --segment-policy passage-v1 ./documents`.
This policy groups document blocks into at most 1,024-byte source slices, with
explicit fallback for oversized blocks. Repeat the flag on resync; a policy change
replaces current evidence even when source bytes are unchanged. Smaller passages
can fit smaller evidence budgets but change lexical ranking and increase segment
count. See [policy identity, boundaries and limitations](CAPABILITIES.md#ingestion-segment-policies).

For an explicit item stream:

```sh
printf '%s\n' '{"id":"note@draft","text":"The harbor inspection is on Friday."}' |
  ./mousa -store local.sqlite sync --source inspection-notes
./mousa -store local.sqlite query --source inspection-notes 'harbor inspection'
```

JSONL input is bounded to 1 MiB per line, 64 MiB per invocation, and 10,000 records.
Deletion is explicit; omission does not delete. Failed input retains its committed
prefix and emits no success result. See the [input and recovery contract](CAPABILITIES.md)
before upgrading an existing directory store or retaining sensitive material.

Each query stores a canonical Source Trail and returns full request, decision,
trail, packet, and selected segment IDs. Inspect the returned `trail_id` with
`./mousa -store local.sqlite trail ./documents TRAIL_ID`, or use
`trail --source inspection-notes TRAIL_ID` for JSONL input. Replace `TRAIL_ID` with
the returned identifier. Inspection evaluates
current source access before releasing historical metadata; it never returns old
text or rejected candidate identities.

The default query-term policy is `original`, which preserves repetition.
`--policy dedup` explicitly folds repeated terms and can change ranking. Earlier
CLI versions implicitly used deduplication. `--budget-bytes` sets a positive
released-text budget, defaulting to 8,192 bytes; JSON overhead and model tokens are
not covered. Queries distinguish no matches, policy/lifecycle exclusion, and budget
omission. See the [query and inspection contract](CAPABILITIES.md#query-policy-packing-and-tracing).

`--packing-policy exact-v1` separately enables exact-content deduplication:

```sh
./mousa -store local.sqlite query --packing-policy exact-v1 --budget-bytes 4096 ./documents 'harbor inspection'
```

It omits byte-equal copies of already selected passages without changing ranking.
Authorization and lifecycle filtering run first; text that does not fit reserves
nothing. Each duplicate keeps its own provenance and references the retained
segment in the trail. Equal text is not independent corroboration or shared
authorization. Default `--packing-policy original` retains repeated passages.

`--associations <file>` opts a query into declared associated context:

```sh
./mousa -store local.sqlite query --associations associations.json --budget-bytes 4096 ./documents 'harbor inspection'
```

The file declares author-attributed relationships between items of the queried
source, for example that a procedure item is incomplete for its declared use
without the qualification documented in another item. Mousa never infers these
relationships; the author and basis stay attached to every passage the
declaration releases. A declaration is honored only when the declaring item
contributed selected primary evidence, so an unrelated query releases nothing.
Depth is one and at most four distinct targets are read per query, up to 256
target passages in total; a larger target is read only up to that bound and its
remaining passages are reported as an omission. Associated
passages pack into the remaining budget after primary evidence, keep their own
item, segment identity, byte coordinates and content digest, and carry the
declaration that included them. A missing, inactive or unfired declaration is
recorded as a visible omission, never a silent gap or a completeness claim. A
relationship is not an access grant: resolution stays inside the one allowed
source decision and only reaches the current active revision. The stored trail
records the complete association stage; decoding stays strict across trail
versions, and reading a stored trail re-verifies every released passage against
canonical store content. See the
[associated context contract](CAPABILITIES.md#declared-associated-context).

### CLI client example

For a persistent documentation lookup, see the
[versioned Git documentation example](../examples/docs/README.md). It imports
pinned public manuals and their direct include fragments, then returns passages
with verified byte ranges, normalized line locations and upstream links. Its
optional `--context-tokens` mode renders cited passages within a
`tiktoken==0.12.0`/`o200k_base` prompt-content cap; the raw packet still
contains the full byte-packed response. The example does not generate answers.

For an explicit caller decision over those retrieval contracts, see the
[SQLite backup checklist](../examples/backup/README.md). It verifies saved passages
from pinned Python documentation, accepts caller-authored fact judgments, and
reports coverage, unresolved facts, or one requested follow-up retrieval.
Citation consistency is separate from the caller's assessment of semantic support.

Run the maintained Python standard-library example against the built CLI:

```sh
CGO_ENABLED=0 go build -o mousa ./cmd/mousa
python3 eval/local/workflow.py --example-only --mousa ./mousa \
  --output client-results.json --timeout 5
```

Prerequisites: Go 1.25 or later to build Mousa, and Python 3.9 or later
with its standard-library `sqlite3` module on Linux. Process-group timeout
handling and executable test fixtures are Linux-supported; other operating
systems are not validated. GNU `time` is optional for peak-RSS measurements;
if `time` is on `PATH`, it must support GNU `-f` and `-o` options.
No QMD, Node, models, network access, or third-party Python packages are needed
to run the client example or acceptance suite. Building requires the Go module
dependencies, downloaded beforehand for an offline build.
The example creates an isolated temporary store and removes it after the run;
`client-results.json` retains the responses and command results.

The client passes argument arrays and JSONL stdin without shell interpolation.
It runs the same lifecycle checks with `fixed-v1` and `passage-v1`, repeating the
chosen policy on resync. It ingests two bulletins, a separate peer source and a
multibyte directory fixture, then checks update, deletion, restoration, denial,
withdrawal and cross-source trail rejection. It distinguishes `no_matches`,
`budget_omitted`, `policy_excluded`, and `lifecycle_excluded`. Denied trail
inspection releases no historical metadata.
It also verifies exact-content packing and the retained-segment explanation under
both policies, including the small-budget duplicate-displacement regression.

The example retains original source bytes and uses `verify_evidence` to check
the normalized representation digest before slicing the returned byte range.
It verifies the selected text and its content digest, rejects changed bytes for
an old saved response, and checks policy changes on identical source bytes.
The directory fixture exercises multiple passages, UTF-8, BOM, CRLF and CR.
See [normalized passage coordinates](CAPABILITIES.md#verify-a-selected-passages-location)
for the additive output fields, normalization rules and limits of saved evidence.

Success exits zero and writes `"result": "PASS"`. An unexpected child exit,
invalid JSON object, or failed behavioral check stops the workflow with exit
one and a partial `"result": "FAIL"` report. Each child has the `--timeout`
deadline; expiration kills and reaps its process group and records exit 124.
Missing prerequisites or invalid arguments can fail before a report is created.
Inspect the report's `error`, `rows`, and `summary`; timings are single-invocation
diagnostics, not a speedup or retrieval-quality claim.

This is a supported example of the CLI, not a stable SDK or a general client
library. It does not implement retrieval or packing. The same
[evolving workflow](RESEARCH.md#evolving-evidence-and-native-lexical-cli-comparison)
can separately run native lexical comparisons when `--example-only` is omitted.
Run the required client acceptance suite from the repository root:

```sh
CGO_ENABLED=0 go build -o mousa ./cmd/mousa
python3 eval/local/workflow_test.py --mousa ./mousa
```

The command requires an explicit readable executable and runs the actual CLI
workflow plus nonzero-exit, malformed-JSON, wrong-shaped-output, and timeout
consumer tests. It also runs the versioned documentation and caller-reviewed
backup consumer suites. It exits nonzero on a failed top-level check, missing
prerequisite, or skipped top-level test. The optional tokenizer test inside the
documentation suite skips if `tiktoken` is absent; that nested skip does not
fail this command. Run `python3 examples/docs/docs_test.py --mousa ./mousa`
with `tiktoken==0.12.0` installed to exercise the opt-in projection. Failures
include captured workflow output for diagnosis. Each run uses temporary
stores; the original workflow's CLI calls have five-second deadlines (0.2
seconds for the intentional timeout), documentation and backup calls have
30-second deadlines, and each workflow subprocess has a 120-second deadline.
The suite does not run comparisons or benchmarks. Ad hoc `unittest`
discovery may still skip the real-CLI test when `MOUSA_EXECUTABLE` is unset;
it is not a substitute for this required command.

`access ./documents deny` blocks this CLI caller's retrieval and trail inspection;
`access ./documents allow` restores that policy permission. Both also accept
`--source <id>`. `withdraw ./documents` changes source lifecycle state: policy allow
does not undo withdrawal, and the CLI has no resume command. These controls are not
secure erasure or a replacement for filesystem access controls.

`supersession declaration put` appends one strict revision-pinned declaration read
from stdin, and `supersession declaration get <declaration-id>` reads one stored
declaration back. Both print the canonical stored declaration JSON:

```sh
./mousa -store local.sqlite supersession declaration put < declaration.json
./mousa -store local.sqlite supersession declaration get DECLARATION_ID
```

A declaration names its source plus the exact predecessor and successor item and
representation revisions, so item labels alone cannot pin a revision. It is
immutable history, not an activation pointer and not an access grant: storing one
does not filter queries, suppress the predecessor or authorize anything. The input
is one `mousa.supersession_declaration.v1` object limited to 64 KiB before decoding;
the codec rejects unknown or duplicate fields, trailing values, malformed identities
and an identity that disagrees with its content. The declared source and both pinned revisions must already exist; the command
does not create or retarget sources, items or revisions, or repair records.
Writable open may create an absent store or upgrade an older supported store
before target validation rejects a declaration. Such a rejection appends no
declaration; it does not undo store creation or migration. An exact retry is idempotent; a reused identity with different
content is a conflict. `get` opens the store read-only, so an invalid identity, a
missing record, an absent store and a corrupt stored record all fail without
creating, migrating or repairing anything.

`supersession activation put` applies one strict activation transition read from
stdin, and `supersession activation get <activation-id>` reads one stored event
back. Both print the canonical stored event JSON, and
`supersession activation state <source-id>` reports the verified current state for
one source as a projection object:

```sh
./mousa -store local.sqlite supersession activation put < activation.json
./mousa -store local.sqlite supersession activation get ACTIVATION_ID
./mousa -store local.sqlite supersession activation state SOURCE_ID
```

The input is one `mousa.supersession_activation.v1` object limited to 64 KiB before
decoding, with the same strict codec rules. The caller states the transition: the
event identity, the nullable expected predecessor event and the nullable selected
declaration. A null predecessor with a declaration is a first activation, a named
predecessor with a declaration is a replacement or reactivation, and a named
predecessor with a null declaration is a deactivation. Nothing infers the current
predecessor or invents a transition. The transition identity covers only the source,
expected predecessor and selected declaration, so actor, time and reason are
recorded labels: replaying an identity with different metadata is a conflict, not an
exact retry, and recording them authenticates nothing.

The store enforces the transition semantics inside one writer transaction: a first
activation must be a root, a replacement or deactivation must name the exact current
event, a transition that would not change the active declaration is refused, an exact
retry succeeds only while its event is still current, and a stale or competing
expectation is a conflict. A rejected transition transaction appends no event and
changes no activation, declaration or canonical record. Writable open may still
create an absent store or upgrade an older supported store before target validation
rejects the transition; rejection does not undo that creation or migration. Malformed
or oversized input is rejected before the store opens.
`get` opens the store read-only and keeps historical reads: an event stays readable
after later transitions, while a retry of that event is rejected. Activation is
recorded local administration only; it filters no query, grants no access, moves no
item pointer and mutates no declaration.

`state` opens the store read-only and prints one JSON object with exactly
`source_id`, `current_activation_id` and `active_declaration_id`. The identity
strings are the same canonical lowercase hexadecimal values the event and declaration
records carry, and a deactivated source reports an explicit JSON null declaration.
The object is a current-state projection: it carries no identity, schema or hash of
its own and is not stored. A source with no activation history reports the store's
`not_found` error, while history whose current projection is missing or inconsistent
with the event it names fails integrity verification and is never rebuilt or repaired
by the read. The state is a verified snapshot rather than a reservation: a caller that
transitions next still states its own expected predecessor and can still conflict if
another caller wins first. Use `state` for the current projection and `get` for one historical event. There is
no activation listing or per-source history command and no supersession MCP surface.

The declaration and activation command groups are trusted local store administration,
and none of them claims to authenticate a recorded actor or author. See the
[supersession boundary](CAPABILITIES.md#supersession-core-boundary).

Integrity checks, authorization, current activation, and factual truth are separate
properties. Classification records are not automatic categorization or
classification-based authorization.

## Local MCP usage

The same executable serves MCP `2025-11-25` over stdio, backed directly by the
canonical Go/SQLite operations. Configure an MCP client's executable and
arguments, for example:

```json
{
  "mcpServers": {
    "mousa": {
      "command": "/absolute/path/mousa",
      "args": [
        "-store", "/absolute/path/evidence.sqlite",
        "mcp", "--caller", "cli",
        "--source", "inspection-notes",
        "--ingest-source", "inspection-notes"
      ]
    }
  }
}
```

The client launches the process and performs initialization and tool discovery.
The four tools are `mousa_sync`, `mousa_status`, `mousa_query` and `mousa_trail`.
Store, permitted sources and trusted caller are fixed at startup. Ingestion is
disabled unless that source also has `--ingest-source`. The default stdio mode
has no listener, directory import, shell, policy administration or withdrawal tool.

`--caller cli` uses the existing CLI retrieval policy, including source-scoped
denials and withdrawal. This is trusted local configuration, not caller
authentication or protection from someone who can modify the store. Other
caller IDs require matching policy provisioned separately; they do not inherit
CLI access.

Tool calls return structured, versioned results. Queries require an explicit
query-term policy and released-text byte budget, and return evidence bytes,
normalized coordinates, digests, packet IDs and Source Trail IDs. Retrieved text
is data, not instructions. See [tool schemas, limits, errors and lifecycle](CAPABILITIES.md#local-stdio-mcp).
The actual-executable MCP client regressions run with:

```sh
CGO_ENABLED=0 go test ./cmd/mousa -run TestMCP -count=1 -v
```

MCP acceptance is deterministic client/server verification, not a model-driven
agent or retrieval-quality measurement. A stable public SDK and general
connectors remain unsupported.

### Bounded project-history consumer

[`examples/memory/memory.py`](../examples/memory/memory.py) uses native stdio MCP
with an explicit history source. The default is an independently authored,
28-document synthetic fixture. It releases attributed passages, not answers.
Python 3 on Linux is optional; no package, model, service or second store is required.

Each command starts a fresh consumer and Mousa process, negotiates MCP, captures
decoded JSON request/response receipts, then closes and reaps the process.
Receipts are not original wire framing. Use an isolated
store and a distinct receipt file for each command:

```sh
python3 examples/memory/memory.py --mousa ./mousa --store memory.sqlite --receipts ingest.jsonl ingest
python3 examples/memory/memory.py --mousa ./mousa --store memory.sqlite --receipts before.jsonl ask 'How does saving an edit survive the computer losing power?' --budget-bytes 2048
python3 examples/memory/memory.py --mousa ./mousa --store memory.sqlite --receipts change.jsonl change
python3 examples/memory/memory.py --mousa ./mousa --store memory.sqlite --receipts after.jsonl ask 'How long may delivery attempts continue after the first failure?' --budget-bytes 2048
```

`ingest` applies explicit item identities; `change` applies the fixture's same-item
correction and explicit tombstone. Omission never deletes an item. The tombstone
deactivates that item, not its source. `inspect TRAIL_ID` inspects an original
query's historical selection under fresh authorization without releasing text.
Do not run `ingest` again after `change` unless restoring the initial fixture is
intentional. Receipt files must not already exist.

Custom histories are validated before receipt creation or server startup.
Originals and corrections require nonempty UTF-8 author, date and URI strings,
string text and its raw UTF-8 SHA-256 digest. Item IDs contain 1–4096 UTF-8 bytes
without NUL. Text, serialized items and complete sync requests must fit the
native MCP limits (262144, 1048576 and 2097152 bytes respectively).
The declared source must be nonempty lossless UTF-8 without NUL (the process argv
boundary). Startup permits only that source; ingestion permission is added only
for `ingest`, `change` or `apply`. The v1 fixture retains `documents` and distinct
existing-item `changes`, containing corrected text or `deleted: true`, never both.
Empty change commands are rejected before startup.

For multiple lifecycle epochs, use `mousa-project-history-v2`:

```json
{
  "schema": "mousa-project-history-v2",
  "source": "project-alpha",
  "revisions": [
    {
      "revision": "initial",
      "id": "note",
      "text": "",
      "sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      "author": "Synthetic author",
      "date": "epoch-1",
      "uri": "synthetic://project/note"
    }
  ],
  "epochs": [
    {"epoch": "create", "operations": [{"id": "note", "revision": "initial"}]},
    {"epoch": "delete", "operations": [{"id": "note", "deleted": true}]},
    {"epoch": "restore", "operations": [{"id": "note", "revision": "initial"}]}
  ]
}
```

Save this as `history.json`, then run with a fresh store and distinct receipts:

```sh
python3 examples/memory/memory.py --mousa ./mousa --history history.json --store chronology.sqlite --receipts create.jsonl apply create
python3 examples/memory/memory.py --mousa ./mousa --history history.json --store chronology.sqlite --receipts delete.jsonl apply delete
python3 examples/memory/memory.py --mousa ./mousa --history history.json --store chronology.sqlite --receipts restore.jsonl apply restore
python3 examples/memory/memory.py --mousa ./mousa --history history.json --store chronology.sqlite --receipts status.jsonl status
python3 examples/memory/memory.py --mousa ./mousa --history history.json --store chronology.sqlite --receipts query.jsonl ask note --policy dedup --packing-policy exact-v1 --budget-bytes 2048
```

`apply EPOCH` sends only that epoch's ordered operations. It neither replays
predecessors nor checks a Python progress ledger. A revert explicitly references
old content; a restore also supplies the referenced content. Exact replay is
allowed. Each epoch contains 1–128 distinct item operations; histories contain
1–128 distinct epoch labels and 1–4096 distinct revision labels. Array order is
fixture chronology, not authenticated time. The store may already be ahead of
the requested epoch. `requested_epoch` describes the invocation, while native
actions and `source_status` describe observed state.

Native sync commits each item separately. A later failure retains its committed
prefix, reported by `native_error.error.completed_items` and `failed_item`.
There is no automatic retry, batch splitting or rollback. Repair requires a new
explicit invocation. Known input and frame bounds are preflighted before launch.

`ask` accepts native query policies `original` (default) and `dedup`, and packing
policies `original` (default) and `exact-v1`. Dedup folds repeated query terms;
exact packing omits byte-identical passages after the first fitting selection.
Neither mode supplies semantic support. Requested and returned identities must
agree. Evidence budgets are 1–65536 bytes; queries contain 1–4096 UTF-8 bytes.

JSON reports distinguish `operation_status`, overall `status`, `capture_complete`
and process teardown. Consumer `PASS` means completed operation/capture/cleanup,
not scenario acceptance. `assertion_status` and `agent_acceptance` remain `NOT RUN`
in consumer output. Preflight failures emit a report without creating receipts.

`--timeout` must be finite and positive. It bounds each request's pipe writes
and response wait, including unrelated notifications; history and executable
hashing, receipt-file I/O, JSON parsing and process teardown are separate
boundaries. Partial writes are completed before a request receipt is recorded.
Each response frame is limited to 32 MiB, excluding its newline.
Responses already buffered before a request cannot fulfill it, even if their
IDs predict that request.
Native text and structured tool results must agree, including their source.
Transport, tool and receipt errors fail the command rather than produce
successful empty evidence. Reports distinguish incomplete receipt capture and
process cleanup diagnostics from the causal error.

The consumer checks released text, digests and normalized byte coordinates
against the attributed fixture revision. Revision matching removes a leading
UTF-8 BOM and normalizes CRLF or bare CR to LF, as ingestion does. Revisions
with equal normalized bytes must have identical author/date/URI attribution;
conflicts fail preflight and rendering. Equivalent revisions with identical
attribution share the evidence identity and expose all matching `revision_labels`,
not an arbitrarily chosen epoch. Corrected, restored and reverted selections use
their own revision's metadata.
These synthetic attribution labels are not signatures or
authenticated authorship. The rendered context retains the exact passages and
identities; its byte count and digest are separate from the evidence-text budget.
No tokenizer or whole-model context limit is supplied.

Twenty frozen development cases retrieved all 21 required initial passages,
including separately attributed multi-document passages. Correction, tombstone,
restart and original-trail checks passed. All three unsupported questions also
released unrelated passages; `evidence_available` does not establish semantic
support. Across 25 initial/post-change observations, 309 of 334 released passages
did not support the frozen requirements. Literal OR-term retrieval can release
misleading near matches. This is deterministic retrieval/lifecycle evaluation,
not held-out relevance, semantic memory or successful model-driven agent use.

The [frozen protocol, measured source and exact receipts](https://github.com/graydeon/mousa-benchmarks/tree/b1be28ac9f12134a7513c1e28fe884e7f657c988/results/2026-09-30-agent-memory)
retain the measured consumer's original identity. Later transport and validation
fixes are not relabeled as that run or as native-agent acceptance.

### ChatGPT desktop and Codex CLI

The integration follows [OpenAI MCP Extensions](https://github.com/openai/mcp-extensions):
composer evidence mentions, authorized resource reads and plugin onboarding.
Extensions do not establish transport, authentication or directory publication.
The four core tools remain the fallback for hosts without desktop extensions.

Build Mousa, ingest only data you intend to share, then create a new local
marketplace. This example uses the maintained synthetic fixture:

```sh
CGO_ENABLED=0 go build -o ./mousa ./cmd/mousa
./mousa -store ./evidence.sqlite sync --source fixture < eval/local/openai-fixture.jsonl
./mousa -store ./evidence.sqlite plugin --out ./mousa-marketplace \
  --source fixture --consent-to-share
codex plugin marketplace add ./mousa-marketplace
codex plugin add mousa@mousa-local
codex plugin list --marketplace mousa-local --json
```

The package includes root `plugin.json`, `mcp.json`, setup/evidence skills and
a contained executable. Its store path and source permissions are bound to
this user. Regenerate configuration for another user; do not distribute private
store paths, stores or credentials. The executable targets the platform used
to build it. Existing output directories are refused. Ingestion remains disabled
unless separately requested with `--ingest-source`.

In ChatGPT desktop, install the local marketplace plugin and run its onboarding.
The setup skill reuses explicit user authorization for the configured sources
and sharing with the configured host/provider; otherwise it requests confirmation
before retrieval. Installation alone does not establish that authorization.
Then search for Cedar using the composer mention picker. Codex CLI can use the
evidence skill and core tools without a mention picker. The directory is local, not a public listing.
Actual desktop interaction and model-driven CLI use require the corresponding
supported app/account and remain separate from credential-free acceptance.

The host/provider receives selected text, labels, identifiers, coordinates,
digests and provenance metadata. Mousa does not filter secrets. See
[extension, privacy, HTTP and publication boundaries](CAPABILITIES.md#openai-mcp-extensions).
Local stdio requires no tunnel. Hosted/private connectivity may require a
separately configured Secure MCP Tunnel; it is not public directory deployment.
