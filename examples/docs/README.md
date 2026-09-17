# Ask about versioned Git documentation

This example is for a developer or local application looking up Git working-tree
operations. It returns a JSON packet of source passages, not a generated answer.
The bundled corpus contains the Git 2.51.0 `git-restore` and `git-switch` AsciiDoc
manual sources at commit `c44beea485f0f2feaf460e2ac87fdd5608d63cf0`.
Git contributors retain copyright; the unmodified upstream `COPYING` file is
included under GPL-2.0-only. This is a documentation aggregate, not Git code
incorporated into Mousa. Includes and linked manuals are not expanded, so this
corpus is not the complete rendered Git manual.

## Run

From the Mousa checkout, with Go 1.25+ and Python 3.9+ on Linux:

```sh
CGO_ENABLED=0 go build -o mousa ./cmd/mousa
python3 examples/docs/docs.py --directory ./git-docs prepare
python3 examples/docs/docs.py --directory ./git-docs sync
python3 examples/docs/docs.py --directory ./git-docs ask \
  --budget-bytes 4096 'What does restore overlay mode do?' > packet.json
```

Go dependencies must already be available for an offline build. The example uses
Python's standard library, the existing CLI and the maintained normalized-range
verifier in `eval/local/workflow.py`. Run it from a complete checkout; it is not a
standalone package or stable SDK. No network, model, service or extra framework is
needed at runtime. Use `--mousa PATH` and `--store PATH` before the operation to
select a binary and store. The default store is `docs.sqlite`.

`prepare` unpacks the bundled archive into a **new** directory and refuses to
overwrite an existing directory. `corpus.json` pins each source file's SHA-256,
upstream URL, version and revision. `sync` checks the manifest and imports only
its declared documents with `passage-v1`. The directory's absolute path identifies
the source; moving it creates a different source. Keep the directory and store at
stable locations. Use one writer for this example; corpus files and manifest are
trusted local inputs and must not change during a command.

## Read the packet

- `outcome: evidence_available` means lexical matches fit the budget. It does not
  mean the passages answer the question. `support: not_assessed` and `answer: null`
  are explicit: the caller must assess relevance and completeness.
- `outcome: insufficient_evidence` contains no passages or invented citations.
  Inspect `response.outcome` to distinguish `no_matches`, `budget_omitted`,
  `policy_excluded` and `lifecycle_excluded`. A semantically unrelated lexical
  match can still produce `evidence_available`; there is no answerability model.
- `response.evidence` contains the exact retrieved text, item, segment and
  representation identities, normalized representation SHA-256 and zero-based,
  end-exclusive UTF-8 byte coordinates. `location` adds the pinned source URL
  and one-based normalized line range. Line ranges locate the selected bytes;
  they do not promise a complete section, command or AsciiDoc include.
- The byte budget counts released text once, not JSON metadata, URLs or tokens.
  The example verifies the full normalized source digest before checking every
  selected range and segment digest. It refuses mismatched source files or
  evidence outside the manifest rather than attaching a misleading citation.
- `elapsed_ms` covers corpus loading, CLI process execution and packet construction;
  it excludes the Python interpreter's startup and final stdout serialization.
  The evaluation runner separately records outer end-to-end wall time.

Empty evidence is an expected result and exits zero. Invalid inputs, failed CLI
commands, timeouts, changed corpus hashes or inconsistent evidence exit one with
an error JSON object on stderr and no packet on stdout. Argument errors exit two.
Do not consume a redirected output file unless the process succeeded.

## Updates, deletion and access

For another locally reviewed corpus revision, replace the document bytes and
update `corpus.json` with the actual hashes, revision and source URLs, then rerun
`sync`. Do not assign an upstream URL to locally edited bytes. Remove deleted
paths from `documents` and `files`; the complete directory sync deactivates old
selected items, even if an excluded file still exists on disk. The manifest is
provenance supplied by the corpus maintainer, not an authenticity signature.
A matching hash alone does not establish authorship or authorization.

Use the existing CLI for access and lifecycle operations:

```sh
./mousa -store docs.sqlite access ./git-docs deny
python3 examples/docs/docs.py --directory ./git-docs ask 'restore overlay'
./mousa -store docs.sqlite access ./git-docs allow
```

Every `ask` makes a fresh authorized query. Saved `packet.json` bytes are a snapshot
of an earlier authorized response, not a fresh authorization or proof that the
source is still current. Denial and deletion do not erase saved files. Keep the
matching corpus revision if you need to verify a saved packet's coordinates;
text-free historical trails cannot reconstruct retired text. Equal text in another
source does not transfer permission or provenance. Filesystem permissions remain
necessary for the store, corpus and saved packets.

## Acceptance and limited evaluation

```sh
python3 examples/docs/docs_test.py --mousa ./mousa
python3 examples/docs/evaluate.py --mousa ./mousa --output docs-results.json
```

Acceptance uses synthetic lifecycle fixtures to check updates, deletion, denial,
withdrawal, source isolation, changed-byte refusal and saved/current distinctions.
It also exercises the real bundled corpus. `questions.json` fixes four supporting-
passage questions and one no-match case before retrieval. They are development
questions, not held-out evaluation. The evaluation makes five passes of those same
five questions without resetting history or tuning keywords. Its `result: PASS`
means execution completed; `support_covered` records usefulness separately and can
be false. Each packet and latency is retained, including misses.

This small study cannot establish general answer quality, semantic retrieval or
universal performance. The lexical CLI can miss a supporting passage that exists
in the corpus. Queries durably record trails, and opening the growing store checks
its history; repeated use can become more expensive. Do not treat a small byte
budget as a bound on startup cost.
