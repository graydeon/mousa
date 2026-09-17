"""Run the frozen, small documentation-usefulness study without query tuning."""

import argparse
import hashlib
import json
from pathlib import Path
import statistics
import subprocess
import sys
import tempfile
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mousa", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    here = Path(__file__).resolve().parent
    questions = json.loads((here / "questions.json").read_text())
    rows = []
    report = {"scope": questions["scope"], "result": "FAIL", "rows": rows,
              "binary_sha256": hashlib.sha256(args.mousa.read_bytes()).hexdigest(),
              "inputs": {name: hashlib.sha256((here / name).read_bytes()).hexdigest()
                         for name in ("docs.py", "questions.json", "git-docs.tar.xz", "evaluate.py")}}
    try:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            store = root / "docs.sqlite"
            command = [sys.executable, str(here / "docs.py"), "--mousa", str(args.mousa.resolve()),
                       "--store", str(store), "--directory", str(root / "corpus")]
            def call(stage, arguments, **metadata):
                started = time.perf_counter_ns()
                process = subprocess.run([*command, *arguments], capture_output=True, text=True, timeout=60)
                row = {"stage": stage, **metadata, "wall_ms": (time.perf_counter_ns() - started) / 1e6,
                       "exit": process.returncode, "stderr": process.stderr, "stdout": process.stdout,
                       "store_bytes": store.stat().st_size if store.exists() else 0}
                rows.append(row)
                if process.returncode:
                    raise RuntimeError("consumer failed: " + stage)
                row["packet"] = json.loads(row.pop("stdout"))
                return row
            call("prepare", ["prepare"])
            call("sync", ["sync"])
            for repeat in range(5):
                for index, question in enumerate(questions["development"]):
                    row = call("ask", ["ask", "--budget-bytes", str(questions["budget_bytes"]), question["question"]],
                               repeat=repeat, question_id=question["id"], prior_queries=repeat * 5 + index)
                    packet = row["packet"]
                    text = "\n".join(hit["text"] for hit in packet["response"]["evidence"] or []
                                     if hit["item"] == question.get("item"))
                    row["support_covered"] = all(passage in text for passage in question["support"])
                    if "expected_outcome" in question:
                        row["support_covered"] = packet["outcome"] == question["expected_outcome"]
                print(f"Completed documentation pass {repeat + 1}/5", flush=True)
            report["passes"] = [{"repeat": repeat,
                                  "median_wall_ms": statistics.median(r["wall_ms"] for r in rows if r.get("repeat") == repeat),
                                  "max_wall_ms": max(r["wall_ms"] for r in rows if r.get("repeat") == repeat),
                                  "support_covered": sum(r["support_covered"] for r in rows if r.get("repeat") == repeat)}
                                 for repeat in range(5)]
            report["result"] = "PASS"
    except (OSError, ValueError, RuntimeError, subprocess.TimeoutExpired) as error:
        report["error"] = str(error)
    finally:
        args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    return int(report["result"] != "PASS")


if __name__ == "__main__":
    raise SystemExit(main())
