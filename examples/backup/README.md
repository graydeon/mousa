# Caller-reviewed SQLite backup checklist

Prepare a Python 3.13.7 SQLite backup checklist from two pinned documentation
excerpts. The required facts cover concurrent access, pages per backup step,
progress callback arguments, and explicit connection closure. Missing a page-limit
qualification or mistaking a transaction context manager for connection cleanup
can produce an incorrect checklist.

This example needs Python 3.9 or later and a built Mousa CLI. It uses the existing
documentation consumer for exact manifest selection, passage retrieval and byte
verification. It adds no engine API, provider, database or answer generator.

## Run

From the repository root:

```sh
CGO_ENABLED=0 go build -o mousa ./cmd/mousa
python3 examples/backup/backup.py --directory /tmp/backup-docs prepare
python3 examples/backup/backup.py --directory /tmp/backup-docs sync
python3 examples/backup/backup.py --directory /tmp/backup-docs retrieve --case complete > packet.json
python3 examples/backup/backup.py --directory /tmp/backup-docs template --packet packet.json --caller 'Checklist reviewer' > assessment.json
```

Read `packet.json` and edit `assessment.json`. Every fact starts `unassessed`.
For each required fact, choose `supported`, `partial`, `unsupported`,
`contradictory`, or `unassessed`; explain your judgment in `reason`. References
are objects with a returned `packet_id` and `segment_id`. Copy the IDs from the
saved packet, not another retrieval. Supported, partial and contradictory
judgments require at least one reference. A valid reference does not establish
that its text supports your judgment.

```sh
python3 examples/backup/backup.py --directory /tmp/backup-docs assess --packet packet.json --assessment assessment.json
```

The output separates citation consistency, per-fact caller judgments, unresolved
facts and the decision. `covered` means all required facts are supported **according
to the named caller**; it is not an engine-generated answer or permission to run a
backup. Missing entries stay unassessed. Unsupported, partial and contradictory
judgments remain visible. No majority vote establishes completeness.

## Declared associated context

The corpus maintainer declares one association between the two excerpts
(`associations.json`): the backup examples use the connection as a transaction
context manager, and the connection-context section documents that this
context manager does not close the connection. The declaration
names its author and basis; Mousa does not discover relationships, and a
declaration is not an access grant. Retrieval without `--associations` is
unchanged.

```sh
python3 examples/backup/backup.py --directory /tmp/backup-docs retrieve --case followup \
  --associations examples/backup/associations.json > packet-assoc.json
```

The returned packet keeps every lexical passage unchanged and appends the
declared target's passages with `origin: "association"`, each with its own
segment identity, byte coordinates, content digest and the declaration that
included it. Associated bytes share the declared budget after primary evidence
and are recorded in the stored trail. Assessment verifies that an associated
passage matches a declaration bound to the saved packet
(`associations_sha256`); without that binding, assessment rejects the packet.
This lets the demonstrated closure fact be supported from the initial packet
without the caller already knowing which note to seek. The judgment remains a
caller judgment, not an engine claim.

## One explicit follow-up

When a fact remains unresolved, the caller may put this object in the assessment:

```json
{
  "next_retrieval": {
    "fact": "closure",
    "question": "context manager neither closes connection",
    "budget_bytes": 4096
  }
}
```

Use a required fact ID that is not supported. This requests one retrieval, not an
automatic retry. The consumer reports `retrieve` only while that attempt remains.

```sh
python3 examples/backup/backup.py --directory /tmp/backup-docs followup --packet packet.json --assessment assessment.json > followup.json
python3 examples/backup/backup.py --directory /tmp/backup-docs template --packet followup.json --caller 'Checklist reviewer' > followup-assessment.json
python3 examples/backup/backup.py --directory /tmp/backup-docs assess --packet followup.json --assessment followup-assessment.json
```

The follow-up file retains the exact original saved JSON, its query and the
requesting assessment. The new assessment can reference either packet. It must
review completeness again; old judgments are not silently carried forward.
After one follow-up, incomplete coverage is `unresolved`, even if another query
is proposed. Corpus-absence claims come from the caller's review, not empty
retrieval. Request a separate task or change the information source outside this
bounded workflow when the available documentation cannot settle the question.

## Verification and trust

Mousa authorizes each new query and returns source passages. Retrieval continues
to use `answer: null` and `support: not_assessed`. Offline assessment verifies the
exact saved file SHA-256, corpus manifest/revision, full normalized representation
digests, UTF-8 byte ranges, selected text, content digests and released-byte
accounting. References must belong to the assessed packet. Existing packet and
segment IDs retain their meanings; the saved-file checksum binds additional
consumer metadata and all retained packets to the caller's assessment.

Saved-file consistency does not prove current access or current source state.
Offline assessment makes no store query. Keep the matching corpus snapshot to
verify historical output after an update. A new retrieval uses current policy
and source state; denial or withdrawal does not delete previously saved output.
A saved judgment cannot grant access. Follow-up combines only the same source and
corpus revision; start a new task rather than combine changed revisions silently.

Trust the local CLI, caller identity declaration, saved files, and pinned corpus
manifest. Checksums are not signatures. Rewriting both data and metadata,
including their checksums, is outside this consistency check's authenticity
claims. Structural checks cannot detect every semantic error, dishonest caller
identity, omitted caveat or incorrect interpretation.

## Corpus and development cases

`python-docs.tar.xz` contains unmodified `sqlite3` backup and connection-context
sections from CPython 3.13.7, revision
`bcee1c322115c581da27600f2ae55e5439c027eb`, with its LICENSE and a hash manifest.
The manifest records upstream file hash, excerpt byte offsets, starting lines
and immutable source links. Displayed passage coordinates are relative to the
excerpt; add its upstream starting line minus one for original line numbers.
These are bounded excerpts, not the complete Python or SQLite manuals. Referenced
encoding and contextlib documentation is not included. Python documentation
contributors retain attribution under the bundled Python license.

`cases.json` fixes six development cases and expectations before retrieval:
complete checklist, partial qualification, missing retrieval with one follow-up,
unsupported completion-time guarantee, unsuccessful absence follow-up, and a
one-byte budget. These expectations are hypotheses, not manufactured successes.
Curated demonstration judgments are caller fixtures, not an automatic assessor.
Deterministic boundary tests use separately labeled original modifications.
No held-out evaluation or semantic-quality accuracy is claimed.

The [fixed six-case run](https://github.com/graydeon/mousa-benchmarks/tree/9d86f3cd1893b1f3dcee505ff0215426e47e4f59/results/2026-09-17-backup-decision)
demonstrated the complete checklist across multiple passages. A backup-only
packet covered three facts but lacked the context-manager qualification; its
single declared follow-up recovered that note. The completion-time task remained
unsupported even after a follow-up. The 180-byte partial-query hypothesis instead
returned no passage because of its byte budget; that miss is retained.

All eight measured queries and citation checks passed, but only two of six final
caller-reviewed tasks were covered. Whole-consumer median time was 338.62 ms,
excluding setup and human review, on a shared host. These are development
observations, not general quality or performance claims. The report preserves
packets, curated judgments, costs and separate pilot/measured source identities.
