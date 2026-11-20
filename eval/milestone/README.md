# Milestone eval and scale probe

Eval tooling in Python, plus one Go probe; nothing here is a runtime dependency of the Go core. The question set and corpora are private and are read from paths given at run time. Write reports to the private results directory, not the repository.

```
python3 -m unittest discover -s eval/milestone          # harness tests
python3 eval/milestone/harness.py Q.jsonl [NA.jsonl] --corpus ROOT --chunks ROOT --out R.json
mousa -store S sync --allow --include '*.html' --include '*.md' --include '*.txt' ROOT   # denied by default
python3 eval/milestone/harness.py Q.jsonl [NA.jsonl] --corpus ROOT --mousa-bin ./mousa --mousa-store S --mousa-dir ROOT
python3 eval/milestone/harness.py Q.jsonl --cmd '<adapter command>'
python3 eval/milestone/table.py NAME=R.json ... --questions Q.jsonl [NA.jsonl] [--corpora a,b]
python3 eval/milestone/scale_probe.py --bin ./mousa --sizes 1000,10000,100000 --out P.json
python3 eval/milestone/scale_probe.py --bin ./mousa --equality 2000 --out E.json
go run ./eval/milestone/contention_probe -bin ./mousa -store S -source base -layout A|B -queries Q.txt -sync-source other -sync-file other.jsonl
```

- `harness.py` documents the adapter contract, the token counter (one regex counter for every system), the coverage rule, the answer check, no-answer scoring, the fixed tuning and held-out split (`--split`), and adapter packing (`--pack`). `--corpus` is the root that holds each question source as `ROOT/<source>`; the answer check needs it.
- `blocks.py` cuts HTML, Markdown and text into blocks with byte ranges and heading pointers; the answer check and the chunk baseline both use it.
- `chunks.py` is the plain-FTS baseline: SQLite FTS5 BM25 over markup-stripped 450-token chunks.
- `table.py` prints covered and answered per budget for all, tuning and held-out from saved reports.
- `scale_probe.py` times open, ingest and query of the real binary on a synthetic corpus, checks trail equality before and after an unrelated sync, and emits corpora and queries for `contention_probe`.
- `contention_probe` measures query latency with concurrent queriers during a real `mousa sync`, with the trail written inside the query's writer transaction (A) or in a short write next to a read transaction (B). Its header says how B stands in for the 2.1 design.
