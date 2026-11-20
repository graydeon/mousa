#!/usr/bin/env python3
"""Milestone eval harness: does a retrieval system's context cover the answer passages?

Reads a private question set (path given at run time) and a retrieval adapter, runs
every question at each token budget, and reports per question and per corpus whether
every answer group is covered by the returned context, and the rank of the first
covering result.

Eval tooling only: nothing here is a runtime dependency of the Go core.

Adapter contract (the `cmd` adapter and the built-ins all produce this):
    results = [{"source": str, "text": str, "byte_start": int?, "byte_end": int?}, ...]
ranked best first. The harness, not the adapter, enforces the token budget.

Token counting (same counter for every system): count_tokens() counts regex pieces
`\\w+|[^\\w\\s]` in the result text. It is deterministic, has no dependencies and
tracks BPE counts only roughly; what matters is that it is identical for all
systems. Results are taken in rank order while the running total stays within the
budget; the first result that would overflow, and everything after it, is dropped.
Adapters receive the budget so they can size their request, but their own token
accounting is not trusted.

Coverage of a group (a group is covered if any alternative passage is covered):
  * same source, and when both the result and the passage carry a byte range, the
    ranges overlap by at least one byte; otherwise
  * same source, and the passage anchor occurs in the result text after HTML-entity
    unescaping and whitespace normalisation.
A question is covered when every group is covered.
"""
import argparse
import html
import json
import re
import statistics
import subprocess
import sys

BUDGETS = (1000, 4000, 16000)
_PIECE = re.compile(r"\w+|[^\w\s]")


def count_tokens(text):
    return len(_PIECE.findall(text))


def norm(text):
    return re.sub(r"\s+", " ", html.unescape(text)).strip()


def passage_covered(passage, result):
    """True if `result` (adapter dict) covers answer `passage` (question-set dict)."""
    if result["source"] != passage["source"]:
        return False
    rs, re_ = result.get("byte_start"), result.get("byte_end")
    ps, pe = passage.get("byte_start"), passage.get("byte_end")
    if None not in (rs, re_, ps, pe):
        return rs < pe and ps < re_
    return norm(passage["anchor"]) in norm(result["text"])


def apply_budget(results, budget):
    kept, used = [], 0
    for r in results:
        n = count_tokens(r["text"])
        if used + n > budget:
            break
        kept.append(r)
        used += n
    return kept, used


def score_question(q, results, budget):
    """Per-question record. `results` is the adapter's ranked list, uncapped."""
    kept, used = apply_budget(results, budget)
    groups = {}
    for p in q["answer_passages"]:
        groups.setdefault(p["group"], []).append(p)
    group_rank = {}
    for g, passages in sorted(groups.items()):
        ranks = [i for i, r in enumerate(kept, 1) if any(passage_covered(p, r) for p in passages)]
        group_rank[g] = ranks[0] if ranks else None
    covered = all(v is not None for v in group_rank.values())
    distractor_first = None
    for i, r in enumerate(kept, 1):
        if any(passage_covered(d, r) for d in q.get("distractor_passages", [])):
            distractor_first = i
            break
    return {
        "id": q["id"],
        "corpus": q["corpus"],
        "covered": covered,
        "groups": {str(g): v for g, v in group_rank.items()},
        "rank": max(group_rank.values()) if covered else None,
        "groups_covered": sum(v is not None for v in group_rank.values()),
        "groups_total": len(group_rank),
        "distractor_rank": distractor_first,
        "returned": len(kept),
        "tokens_used": used,
    }


def summarize(records):
    def one(rs):
        covered = [r for r in rs if r["covered"]]
        ranks = [r["rank"] for r in covered]
        return {
            "questions": len(rs),
            "covered": len(covered),
            "coverage": len(covered) / len(rs) if rs else 0.0,
            "group_coverage": sum(r["groups_covered"] for r in rs) / max(1, sum(r["groups_total"] for r in rs)),
            "median_rank": statistics.median(ranks) if ranks else None,
            "mean_tokens_used": statistics.mean(r["tokens_used"] for r in rs) if rs else 0,
            "distractor_before_answer": sum(
                1 for r in rs if r["distractor_rank"] is not None
                and (r["rank"] is None or r["distractor_rank"] < r["rank"])),
        }
    out = {"overall": one(records)}
    for c in sorted({r["corpus"] for r in records}):
        out[c] = one([r for r in records if r["corpus"] == c])
    return out


# ---- adapters -------------------------------------------------------------

def mousa_adapter(binary, store, directory, prefix):
    """Query a directory-synced store through the real CLI. Item paths get `prefix`
    so they match question-set sources. The CLI budget is in bytes; ask generously and
    let the harness cap by tokens."""
    def run(q, budget):
        out = subprocess.run([binary, "-store", store, "query", "--budget-bytes", str(budget * 8),
                              directory, q["question"]], capture_output=True, text=True, check=True).stdout
        return [{"source": prefix + e["item"], "text": e["text"],
                 "byte_start": e["byte_start"], "byte_end": e["byte_end"]}
                for e in json.loads(out).get("evidence", [])]
    return run


def cmd_adapter(command):
    """Run `command` (shell) with {"question","corpus","budget_tokens"} on stdin; it
    prints {"results": [...]} on stdout."""
    def run(q, budget):
        req = json.dumps({"question": q["question"], "corpus": q["corpus"], "budget_tokens": budget})
        out = subprocess.run(command, shell=True, input=req, capture_output=True, text=True, check=True).stdout
        return json.loads(out)["results"]
    return run


# ---- driver ---------------------------------------------------------------

def load_questions(path):
    with open(path) as f:
        return [json.loads(l) for l in f if l.strip()]


def run(questions, adapter, budgets):
    report = {}
    for b in budgets:
        records = [score_question(q, adapter(q, b), b) for q in questions]
        report[str(b)] = {"summary": summarize(records), "questions": records}
    return report


def print_table(report):
    print(f"{'budget':>7} {'corpus':<9} {'covered':>9} {'coverage':>9} {'groups':>7} {'med.rank':>8} {'distr>ans':>9}")
    for b, data in report.items():
        for c, s in data["summary"].items():
            mr = "-" if s["median_rank"] is None else f"{s['median_rank']:g}"
            print(f"{b:>7} {c:<9} {s['covered']:>4}/{s['questions']:<4} {s['coverage']:>9.3f} "
                  f"{s['group_coverage']:>7.3f} {mr:>8} {s['distractor_before_answer']:>9}")


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("questions", help="path to questions.jsonl (private; never committed)")
    ap.add_argument("--out", help="write full JSON report here (use the private results directory)")
    ap.add_argument("--budgets", default=",".join(map(str, BUDGETS)))
    g = ap.add_mutually_exclusive_group(required=True)
    g.add_argument("--cmd", help="shell command implementing the adapter contract on stdin/stdout")
    g.add_argument("--mousa-bin", help="mousa binary; needs --mousa-store, --mousa-dir")
    ap.add_argument("--mousa-store")
    ap.add_argument("--mousa-dir")
    ap.add_argument("--source-prefix", default="", help="prefix mapping Mousa item paths to question sources")
    a = ap.parse_args(argv)
    if a.cmd:
        adapter = cmd_adapter(a.cmd)
    else:
        if not (a.mousa_store and a.mousa_dir):
            ap.error("--mousa-bin needs --mousa-store and --mousa-dir")
        adapter = mousa_adapter(a.mousa_bin, a.mousa_store, a.mousa_dir, a.source_prefix)
    report = run(load_questions(a.questions), adapter, [int(x) for x in a.budgets.split(",")])
    print_table(report)
    if a.out:
        with open(a.out, "w") as f:
            json.dump(report, f, indent=1)


if __name__ == "__main__":
    main()
