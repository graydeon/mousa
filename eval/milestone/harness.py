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

Answer check (stricter, guards against wins on fragments): a passage is answered when the
packet holds the whole block that contains its anchor and the nearest heading above that
block (blocks.py), both from the passage's source. A span counts as held when the byte
ranges of the kept results from that source cover it, or when its word sequence occurs in
one kept result from that source or in all of them joined in rank order. Needs the corpus
files (`--corpus`); a passage with no file falls back to the coverage rule.

No-answer items have no answer passages. They are scored apart: abstained means the packet
is empty.

Split: halves() fixes a tuning and a held-out half over the whole question set: within each
kind, ids are ordered by a salted sha256 and alternate. Corpus filters apply after it.

Adapter packing: the Mousa and chunk adapters pack skip-and-continue by default (a result that
does not fit is skipped and later ones still tried), as Mousa does; `--pack stop` hands the
uncapped list to the harness rule instead. `--cmd` adapters pack on their side.
"""
import argparse
import hashlib
import html
import json
import re
import statistics
import subprocess
import sys

import blocks as B

BUDGETS = (1000, 4000, 16000)
_PIECE = re.compile(r"\w+|[^\w\s]")


def count_tokens(text):
    return len(_PIECE.findall(text))


def norm(text):
    return re.sub(r"\s+", " ", html.unescape(text)).strip()


_TAGS = re.compile(r"<(/?)([a-zA-Z0-9]+)[^>]*>|<!--.*?-->", re.S)
_WORD = re.compile(r"\w+")


def words(text):
    return " " + " ".join(_WORD.findall(_TAGS.sub(" ", html.unescape(text)).lower())) + " "


def passage_covered(passage, result):
    """True if `result` (adapter dict) covers answer `passage` (question-set dict)."""
    if result["source"] != passage["source"]:
        return False
    rs, re_ = result.get("byte_start"), result.get("byte_end")
    ps, pe = passage.get("byte_start"), passage.get("byte_end")
    if None not in (rs, re_, ps, pe):
        return rs < pe and ps < re_
    return norm(passage["anchor"]) in norm(result["text"])


def span_held(span, results):
    """True if `span` (Block) is in `results` (kept, same source) by byte ranges or by words."""
    ranges = sorted((r["byte_start"], r["byte_end"]) for r in results
                    if r.get("byte_start") is not None and r.get("byte_end") is not None)
    at = span.start
    for s, e in ranges:
        if s <= at < e:
            at = e
        if at >= span.end:
            return True
    want = words(span.text)
    if want.strip() == "":
        return True
    return any(want in words(r["text"]) for r in results) or want in words(" ".join(r["text"] for r in results))


def passage_answered(passage, kept, corpus):
    """Answer check for one passage against the kept packet; see the module docstring."""
    blocks = corpus.blocks(passage["source"]) if corpus else None
    if blocks is None or passage.get("byte_start") is None:
        return any(passage_covered(passage, r) for r in kept)
    ps, pe = passage["byte_start"], passage["byte_end"]
    need = [b for b in blocks if b.start < pe and ps < b.end]
    if not need:
        return any(passage_covered(passage, r) for r in kept)
    if need[0].heading is not None and need[0].heading not in need:
        need.append(need[0].heading)
    same = [r for r in kept if r["source"] == passage["source"]]
    return all(span_held(b, same) for b in need)


def pack_skip(results, budget):
    """Skip-and-continue packing by the harness token counter."""
    kept, used = [], 0
    for r in results:
        n = count_tokens(r["text"])
        if used + n <= budget:
            kept.append(r)
            used += n
    return kept


def apply_budget(results, budget):
    kept, used = [], 0
    for r in results:
        n = count_tokens(r["text"])
        if used + n > budget:
            break
        kept.append(r)
        used += n
    return kept, used


def score_question(q, results, budget, corpus=None):
    """Per-question record. `results` is the adapter's ranked list, uncapped."""
    kept, used = apply_budget(results, budget)
    if not q["answer_passages"]:
        return {"id": q["id"], "corpus": q["corpus"], "no_answer": True, "abstained": not kept,
                "returned": len(kept), "tokens_used": used}
    groups = {}
    for p in q["answer_passages"]:
        groups.setdefault(p["group"], []).append(p)
    group_rank = {}
    for g, passages in sorted(groups.items()):
        ranks = [i for i, r in enumerate(kept, 1) if any(passage_covered(p, r) for p in passages)]
        group_rank[g] = ranks[0] if ranks else None
    covered = all(v is not None for v in group_rank.values())
    answered = all(any(passage_answered(p, kept, corpus) for p in ps) for ps in groups.values())
    distractor_first = None
    for i, r in enumerate(kept, 1):
        if any(passage_covered(d, r) for d in q.get("distractor_passages", [])):
            distractor_first = i
            break
    return {
        "id": q["id"],
        "corpus": q["corpus"],
        "covered": covered,
        "answered": answered,
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
            "answered": sum(r["answered"] for r in rs),
            "coverage": len(covered) / len(rs) if rs else 0.0,
            "group_coverage": sum(r["groups_covered"] for r in rs) / max(1, sum(r["groups_total"] for r in rs)),
            "median_rank": statistics.median(ranks) if ranks else None,
            "mean_tokens_used": statistics.mean(r["tokens_used"] for r in rs) if rs else 0,
            "distractor_before_answer": sum(
                1 for r in rs if r["distractor_rank"] is not None
                and (r["rank"] is None or r["distractor_rank"] < r["rank"])),
        }
    answerable = [r for r in records if not r.get("no_answer")]
    none = [r for r in records if r.get("no_answer")]
    out = {"overall": one(answerable)}
    for c in sorted({r["corpus"] for r in answerable}):
        out[c] = one([r for r in answerable if r["corpus"] == c])
    out["no_answer"] = {"questions": len(none), "abstained": sum(r["abstained"] for r in none),
                        "mean_tokens_used": statistics.mean(r["tokens_used"] for r in none) if none else 0}
    return out


SPLIT_SALT = b"mousa-milestone-split-v1:"


def halves(questions):
    """id -> "tuning" | "heldout", fixed by kind and a salted hash of the id."""
    by_kind, out = {}, {}
    for q in questions:
        by_kind.setdefault(q["kind"], []).append(q["id"])
    for ids in by_kind.values():
        ids.sort(key=lambda i: hashlib.sha256(SPLIT_SALT + i.encode()).digest())
        for n, i in enumerate(ids):
            out[i] = "tuning" if n % 2 == 0 else "heldout"
    return out


def select(questions, split="all", corpora=None):
    """Split over the whole set first, then filter by corpus."""
    if split != "all":
        half = halves(questions)
        questions = [q for q in questions if half[q["id"]] == split]
    if corpora:
        questions = [q for q in questions if q["corpus"] in corpora]
    return questions


# ---- adapters -------------------------------------------------------------

def packed(adapter, pack):
    if pack == "stop":
        return adapter
    return lambda q, budget: pack_skip(adapter(q, budget), budget)


def mousa_adapter(binary, store, directory, prefix):
    """Query a directory-synced store through the real CLI. Item paths get `prefix`
    so they match question-set sources. The CLI budget is in bytes; ask generously and
    let the harness cap by tokens."""
    def run(q, budget):
        out = subprocess.run([binary, "-store", store, "query", "--full", "--budget-bytes", str(budget * 8),
                              directory, q["question"]], capture_output=True, text=True, check=True).stdout
        return [{"source": prefix + e["item"], "text": e["text"],
                 "byte_start": e["range"]["start"], "byte_end": e["range"]["end"]}
                for e in json.loads(out)["passages"]]
    return run


def chunks_adapter(root):
    import chunks
    index = chunks.Index(root)
    return lambda q, budget: index.search(q["question"])


def cmd_adapter(command):
    """Run `command` (shell) with {"question","corpus","budget_tokens"} on stdin; it
    prints {"results": [...]} on stdout."""
    def run(q, budget):
        req = json.dumps({"question": q["question"], "corpus": q["corpus"], "budget_tokens": budget})
        out = subprocess.run(command, shell=True, input=req, capture_output=True, text=True, check=True).stdout
        return json.loads(out)["results"]
    return run


# ---- driver ---------------------------------------------------------------

def load_questions(*paths):
    out = []
    for path in paths:
        with open(path) as f:
            out += [json.loads(l) for l in f if l.strip()]
    return out


def run(questions, adapter, budgets, corpus=None):
    report = {}
    for b in budgets:
        records = [score_question(q, adapter(q, b), b, corpus) for q in questions]
        report[str(b)] = {"summary": summarize(records), "questions": records}
    return report


def print_table(report):
    print(f"{'budget':>7} {'corpus':<9} {'covered':>9} {'answered':>9} {'groups':>7} {'med.rank':>8} {'distr>ans':>9}")
    for b, data in report.items():
        for c, s in data["summary"].items():
            if c == "no_answer":
                print(f"{b:>7} {'no-answer':<9} abstained {s['abstained']}/{s['questions']}")
                continue
            mr = "-" if s["median_rank"] is None else f"{s['median_rank']:g}"
            print(f"{b:>7} {c:<9} {s['covered']:>4}/{s['questions']:<4} {s['answered']:>4}/{s['questions']:<4} "
                  f"{s['group_coverage']:>7.3f} {mr:>8} {s['distractor_before_answer']:>9}")


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("questions", nargs="+", help="question files (private; never committed)")
    ap.add_argument("--split", choices=("all", "tuning", "heldout"), default="all")
    ap.add_argument("--corpora", help="comma list of corpora to keep, applied after the split")
    ap.add_argument("--corpus", help="root holding <source> files, for the answer check")
    ap.add_argument("--pack", choices=("skip", "stop"), default="skip", help="Mousa and chunk adapter packing")
    ap.add_argument("--out", help="write full JSON report here (use the private results directory)")
    ap.add_argument("--budgets", default=",".join(map(str, BUDGETS)))
    g = ap.add_mutually_exclusive_group(required=True)
    g.add_argument("--cmd", help="shell command implementing the adapter contract on stdin/stdout")
    g.add_argument("--mousa-bin", help="mousa binary; needs --mousa-store, --mousa-dir")
    g.add_argument("--chunks", metavar="DIR", help="plain-FTS 450-token chunk baseline over DIR")
    ap.add_argument("--mousa-store")
    ap.add_argument("--mousa-dir")
    ap.add_argument("--source-prefix", default="", help="prefix mapping Mousa item paths to question sources")
    a = ap.parse_args(argv)
    if a.cmd:
        adapter = cmd_adapter(a.cmd)
    elif a.chunks:
        adapter = packed(chunks_adapter(a.chunks), a.pack)
    else:
        if not (a.mousa_store and a.mousa_dir):
            ap.error("--mousa-bin needs --mousa-store and --mousa-dir")
        adapter = packed(mousa_adapter(a.mousa_bin, a.mousa_store, a.mousa_dir, a.source_prefix), a.pack)
    questions = select(load_questions(*a.questions), a.split, a.corpora and set(a.corpora.split(",")))
    report = run(questions, adapter, [int(x) for x in a.budgets.split(",")], a.corpus and B.Corpus(a.corpus))
    print_table(report)
    if a.out:
        with open(a.out, "w") as f:
            json.dump(report, f, indent=1)


if __name__ == "__main__":
    main()
