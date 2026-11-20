#!/usr/bin/env python3
"""Baseline table from harness reports: covered and answered per budget, by split.

Usage: table.py NAME=report.json ... --questions Q.jsonl [Q2.jsonl ...] [--corpora a,b]
The split comes from harness.halves() over all given question files, so it matches
`harness.py --split`; the corpus filter applies after it.
"""
import argparse
import json

import harness as h


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--questions", nargs="+", required=True)
    ap.add_argument("--corpora")
    ap.add_argument("reports", nargs="+", metavar="NAME=PATH")
    a = ap.parse_args()
    questions = h.load_questions(*a.questions)
    corpora = a.corpora and set(a.corpora.split(","))
    splits = {s: {q["id"] for q in h.select(questions, s, corpora)} for s in ("all", "tuning", "heldout")}
    print("| system | split | n | 1k cov / ans | 4k cov / ans | 16k cov / ans | no-answer abstained (1k/4k/16k) |")
    print("|---|---|---|---|---|---|---|")
    for spec in a.reports:
        name, path = spec.split("=", 1)
        with open(path) as f:
            report = json.load(f)
        for split, ids in splits.items():
            cells, abstain, n = [], [], 0
            for budget in ("1000", "4000", "16000"):
                recs = [r for r in report[budget]["questions"] if r["id"] in ids]
                ans = [r for r in recs if not r.get("no_answer")]
                none = [r for r in recs if r.get("no_answer")]
                n = len(ans)
                cells.append(f"{sum(r['covered'] for r in ans)} / {sum(r['answered'] for r in ans)}")
                abstain.append(f"{sum(r['abstained'] for r in none)}/{len(none)}")
            print(f"| {name} | {split} | {n} | " + " | ".join(cells) + f" | {' '.join(abstain)} |")


if __name__ == "__main__":
    main()
