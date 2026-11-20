# Milestone eval and scale probe

Eval tooling in Python; nothing here is a runtime dependency of the Go core. The question set is private and is read from a path given at run time.

```
python3 -m unittest discover -s eval/milestone          # coverage-matching tests
mousa -store S sync --allow D                                    # sources are denied by default
python3 eval/milestone/harness.py <questions.jsonl> --mousa-bin ./mousa --mousa-store S --mousa-dir D --source-prefix <repo>/
python3 eval/milestone/harness.py <questions.jsonl> --cmd '<adapter command>'
python3 eval/milestone/scale_probe.py --bin ./mousa --sizes 1000,10000,100000 --out <report.json>
```

`harness.py` documents the adapter contract, the token counter (one regex counter for every system) and the coverage rule. Write reports to the private results directory, not the repository.
