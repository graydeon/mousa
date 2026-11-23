#!/usr/bin/env python3
"""Scale probe: open, ingest and query timings of the real mousa binary on a synthetic corpus.

No private data: documents are generated from a seeded Zipf vocabulary. Per size N it
ingests N documents of about --doc-bytes bytes (one passage each when below the 4096-byte
segment limit) through `sync --source` in batches of 10000 (the CLI record limit), then
measures:
  ingest_docs_per_s  N / total wall time of all sync calls (each call reopens the store,
                     so any open cost that grows with the store shows up here)
  open_s             median wall time of `status --source` (opens the store, reads state)
  query_p50_ms/p95   wall time of `query --source`, 3-term queries, includes the open
  query_net_p50_ms   query wall minus open_s: an estimate of the query itself

Sizes run in increasing order and stop at the first size whose ingest exceeds
--max-ingest-seconds; the report says where it stopped. Sources are denied by default, so
every sync passes --allow.

--equality N: sync source A (N documents, seed), run --queries queries, sync an unrelated
source B (N documents, seed+1, same vocabulary), run the same queries on A again, and compare
the stored result digests. Each stored trail must match its query response.

--emit-docs N PATH and --emit-queries K PATH write a corpus (JSONL records for `sync
--source`) and query lines from the same generator, for contention_probe.

Usage: scale_probe.py --bin ./mousa --sizes 1000,10000,100000 --out results.json
       scale_probe.py --bin ./mousa --equality 2000 --out equality.json
       scale_probe.py --emit-docs 10000 base.jsonl --emit-queries 200 queries.txt --seed 1
"""
import argparse
import itertools
import json
import os
import random
import statistics
import subprocess
import tempfile
import time

BATCH = 10000
SOURCE = "probe"


def vocab(rng, n=20000):
    letters = "abcdefghijklmnopqrstuvwxyz"
    return ["".join(rng.choice(letters) for _ in range(rng.randint(3, 10))) for _ in range(n)]


def doc(rng, words, cum, nbytes):
    out, size = [], 0
    while size < nbytes:
        w = rng.choices(words, cum_weights=cum)[0]
        out.append(w)
        size += len(w) + 1
    return " ".join(out)


def run(binary, store, *args, stdin=None):
    t = time.perf_counter()
    p = subprocess.run([binary, "-store", store, *args], input=stdin, capture_output=True, text=True)
    dt = time.perf_counter() - t
    if p.returncode:
        raise RuntimeError(f"{args[0]} failed: {p.stderr[:300]}")
    return dt, p.stdout


def probe(binary, n, doc_bytes, nq, seed, workdir=None):
    rng = random.Random(seed)
    words = vocab(rng)
    cum = list(itertools.accumulate(1 / (i + 1) for i in range(len(words))))
    with tempfile.TemporaryDirectory(dir=workdir) as d:
        store = os.path.join(d, "s.db")
        ingest, done, sample = 0.0, 0, []
        while done < n:
            k = min(BATCH, n - done)
            lines = []
            for i in range(done, done + k):
                text = doc(rng, words, cum, doc_bytes)
                if len(sample) < 200:
                    sample.append(text.split())
                lines.append(json.dumps({"id": f"d{i}", "text": text}))
            dt, _ = run(binary, store, "sync", "--source", SOURCE, "--allow", stdin="\n".join(lines) + "\n")
            ingest += dt
            done += k
            print(f"  ingested {done}/{n} ({done / ingest:.0f} docs/s so far)", flush=True)
        opens = [run(binary, store, "status", "--source", SOURCE)[0] for _ in range(3)]
        qs = []
        for _ in range(nq):
            terms = rng.sample(rng.choice(sample), 3)
            qs.append(run(binary, store, "query", "--source", SOURCE, " ".join(terms))[0])
        size = os.path.getsize(store)
    qs.sort()
    open_s = statistics.median(opens)
    p50 = statistics.median(qs)
    return {
        "documents": n, "doc_bytes": doc_bytes, "store_bytes": size,
        "ingest_seconds": round(ingest, 2), "ingest_docs_per_s": round(n / ingest, 1),
        "open_s": round(open_s, 3),
        "query_p50_ms": round(p50 * 1000, 1), "query_p95_ms": round(qs[int(len(qs) * 0.95) - 1] * 1000, 1),
        "query_net_p50_ms": round(max(0.0, p50 - open_s) * 1000, 1),
    }


def corpus(n, doc_bytes, seed, prefix="d"):
    """(JSONL lines, sample word lists) from the seeded generator; vocabulary depends only on 1."""
    words = vocab(random.Random(1))
    cum = list(itertools.accumulate(1 / (i + 1) for i in range(len(words))))
    rng = random.Random(seed)
    lines, sample = [], []
    for i in range(n):
        text = doc(rng, words, cum, doc_bytes)
        if len(sample) < 200:
            sample.append(text.split())
        lines.append(json.dumps({"id": f"{prefix}{i}", "text": text}))
    return lines, sample


def queries(sample, k, seed):
    rng = random.Random(seed)
    return [" ".join(rng.sample(rng.choice(sample), 3)) for _ in range(k)]


def equality(binary, n, doc_bytes, nq, seed):
    a, sample = corpus(n, doc_bytes, seed, "a")
    b, _ = corpus(n, doc_bytes, seed + 1, "b")
    qs = queries(sample, nq, seed)
    with tempfile.TemporaryDirectory() as d:
        store = os.path.join(d, "s.db")
        run(binary, store, "sync", "--source", "A", "--allow", stdin="\n".join(a) + "\n")

        def trails():
            out = []
            for q in qs:
                trail = json.loads(run(binary, store, "query", "--source", "A", q)[1])["trail"]
                checked = json.loads(run(binary, store, "trail", trail["id"])[1])
                if checked != trail:
                    raise RuntimeError("stored trail disagrees with query response")
                out.append(checked)
            return out
        before = trails()
        run(binary, store, "sync", "--source", "B", "--allow", stdin="\n".join(b) + "\n")
        after = trails()
    rows = []
    for q, x, y in zip(qs, before, after):
        cx, cy = x["candidates"], y["candidates"]
        seg = lambda c: [r.get("segment_id") for r in c]
        rows.append({"query": q, "equal": x["result_sha256"] == y["result_sha256"],
                     "same_candidate_order": seg(cx) == seg(cy),
                     "same_candidate_set": sorted(seg(cx)) == sorted(seg(cy)),
                     "candidates": [len(cx), len(cy)]})
    return {"documents_per_source": n, "doc_bytes": doc_bytes, "queries": nq,
            "equal": sum(r["equal"] for r in rows), "same_order": sum(r["same_candidate_order"] for r in rows),
            "same_set": sum(r["same_candidate_set"] for r in rows), "rows": rows,
            "first_pair": {"before": before[0], "after": after[0]}}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--bin")
    ap.add_argument("--sizes", default="1000,10000,100000")
    ap.add_argument("--doc-bytes", type=int, default=1200)
    ap.add_argument("--queries", type=int, default=30)
    ap.add_argument("--max-ingest-seconds", type=float, default=1800)
    ap.add_argument("--seed", type=int, default=1)
    ap.add_argument("--out")
    ap.add_argument("--workdir", help="directory for the probe stores (default: system temp; /tmp may be tmpfs)")
    ap.add_argument("--equality", type=int, metavar="N")
    ap.add_argument("--emit-docs", nargs=2, metavar=("N", "PATH"))
    ap.add_argument("--emit-queries", nargs=2, metavar=("K", "PATH"))
    a = ap.parse_args()
    if a.emit_docs or a.emit_queries:
        lines, sample = corpus(int(a.emit_docs[0]) if a.emit_docs else 200, a.doc_bytes, a.seed)
        if a.emit_docs:
            with open(a.emit_docs[1], "w") as f:
                f.write("\n".join(lines) + "\n")
        if a.emit_queries:
            with open(a.emit_queries[1], "w") as f:
                f.write("\n".join(queries(sample, int(a.emit_queries[0]), a.seed)) + "\n")
        return
    if a.equality:
        report = equality(a.bin, a.equality, a.doc_bytes, a.queries, a.seed)
        print(json.dumps({k: v for k, v in report.items() if k not in ("rows", "first_pair")}))
        if a.out:
            with open(a.out, "w") as f:
                json.dump(report, f, indent=1)
        return
    results, stopped = [], None
    for n in map(int, a.sizes.split(",")):
        print(f"size {n}", flush=True)
        r = probe(a.bin, n, a.doc_bytes, a.queries, a.seed, a.workdir)
        print(json.dumps(r), flush=True)
        results.append(r)
        if r["ingest_seconds"] > a.max_ingest_seconds:
            stopped = f"stopped after {n}: ingest took {r['ingest_seconds']}s, over {a.max_ingest_seconds}s"
            break
    report = {"results": results, "stopped": stopped}
    if a.out:
        with open(a.out, "w") as f:
            json.dump(report, f, indent=1)


if __name__ == "__main__":
    main()
