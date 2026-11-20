# Milestone eval and scale probe

Eval tooling in Python; nothing here is a runtime dependency of the Go core. The question set and corpora are private and are read from paths given at run time. Write reports to the private results directory, not the repository.

```
python3 -m unittest discover -s eval/milestone          # harness tests
python3 eval/milestone/harness.py Q.jsonl [NA.jsonl] --corpus ROOT --chunks ROOT --out R.json
mousa -store S sync --allow --include '*.html' --include '*.md' --include '*.txt' ROOT   # denied by default
python3 eval/milestone/harness.py Q.jsonl [NA.jsonl] --corpus ROOT --mousa-bin ./mousa --mousa-store S --mousa-dir ROOT
python3 eval/milestone/harness.py Q.jsonl --cmd '<adapter command>'
python3 eval/milestone/table.py NAME=R.json ... --questions Q.jsonl [NA.jsonl] [--corpora a,b]
python3 eval/milestone/scale_probe.py --bin ./mousa --sizes 1000,10000,100000 --out P.json
```

- `harness.py` documents the adapter contract, the token counter (one regex counter for every system), the coverage rule, the answer check, no-answer scoring, the fixed tuning and held-out split (`--split`), and adapter packing (`--pack`). `--corpus` is the root that holds each question source as `ROOT/<source>`; the answer check needs it.
- `blocks.py` cuts HTML, Markdown and text into blocks with byte ranges and heading pointers; the answer check and the chunk baseline both use it.
- `chunks.py` is the plain-FTS baseline: SQLite FTS5 BM25 over markup-stripped 450-token chunks.
- `table.py` prints covered and answered per budget for all, tuning and held-out from saved reports.
