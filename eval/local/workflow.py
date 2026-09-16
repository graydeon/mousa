#!/usr/bin/env python3
"""Exercise the local workflow and compare bounded, native lexical CLI routes."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import sqlite3
import statistics
import subprocess
import sys
import tempfile
import time
from urllib.parse import urlsplit

from lifecycle import run_process

TOPICS = ("cedar", "orchid", "quartz", "nebula")
GENERATIONS = ("epochzero", "epochone", "epochtwo")


def document(index, generation):
    return f"{TOPICS[index % len(TOPICS)]} marker{index:03d} {generation} maintenance evidence." + " detail" * index + "\n"


def pack(candidates, budget):
    selected, used = [], 0
    for candidate in candidates:
        size = len(candidate["text"].encode())
        if size <= budget - used:
            selected.append(candidate)
            used += size
    return selected, used


def minimal_sqlite(args):
    store, operation, root, *query = args
    connection = sqlite3.connect(store)
    connection.execute("PRAGMA journal_mode=WAL")
    connection.execute("PRAGMA synchronous=FULL")
    connection.execute("CREATE TABLE IF NOT EXISTS documents(id INTEGER PRIMARY KEY, path TEXT UNIQUE, digest TEXT NOT NULL)")
    connection.execute("CREATE VIRTUAL TABLE IF NOT EXISTS search USING fts5(body, tokenize='unicode61 remove_diacritics 2')")
    if operation == "sync":
        current = {row[1]: (row[0], row[2]) for row in connection.execute("SELECT id,path,digest FROM documents")}
        selected = set()
        actions = {"added": 0, "updated": 0, "unchanged": 0, "deleted": 0}
        with connection:
            for path in sorted(Path(root).rglob("*.md")):
                item = path.relative_to(root).as_posix()
                raw = path.read_bytes()
                text = raw.decode("utf-8")
                digest = hashlib.sha256(raw).hexdigest()
                selected.add(item)
                prior = current.get(item)
                if prior and prior[1] == digest:
                    actions["unchanged"] += 1
                    continue
                if prior:
                    identifier = prior[0]
                    connection.execute("UPDATE documents SET digest=? WHERE id=?", (digest, identifier))
                    connection.execute("UPDATE search SET body=? WHERE rowid=?", (text, identifier))
                    actions["updated"] += 1
                else:
                    identifier = connection.execute("INSERT INTO documents(path,digest) VALUES(?,?)", (item, digest)).lastrowid
                    connection.execute("INSERT INTO search(rowid,body) VALUES(?,?)", (identifier, text))
                    actions["added"] += 1
            for item in current.keys() - selected:
                connection.execute("DELETE FROM search WHERE rowid=?", (current[item][0],))
                connection.execute("DELETE FROM documents WHERE id=?", (current[item][0],))
                actions["deleted"] += 1
        result = actions
    elif operation == "query":
        # This comparison deliberately uses single ASCII terms, not a general query parser.
        term = query[0]
        if not term.isascii() or not term.isalnum():
            raise ValueError("minimal comparison query must be one ASCII alphanumeric term")
        rows = connection.execute('SELECT d.path,s.body,bm25(search) FROM search s JOIN documents d ON d.id=s.rowid WHERE search MATCH ? ORDER BY bm25(search),d.path LIMIT 100', ('"' + term + '"',))
        result = [{"item": row[0], "text": row[1], "score": row[2]} for row in rows]
    else:
        raise ValueError("unknown minimal SQLite operation")
    connection.close()
    print(json.dumps(result))


class Commands:
    def __init__(self, work, timeout):
        self.work, self.timeout, self.rows = work, timeout, []
        self.time_binary = shutil.which("time")

    def run(self, engine, stage, repeat, args, *, environment=None, records=None, expected_code=0):
        rss = self.work / "rss.txt"
        rss.unlink(missing_ok=True)
        command = list(map(str, args))
        executed = [self.time_binary, "-f", "%M", "-o", str(rss), *command] if self.time_binary else command
        started = time.monotonic()
        code, stdout, stderr = run_process(executed, self.timeout, stdout=subprocess.PIPE, stderr=subprocess.PIPE, stdin=subprocess.PIPE, text=True, env=environment) if records is None else self.with_input(executed, records, environment)
        elapsed = time.monotonic() - started
        peak = None
        if rss.exists():
            lines = rss.read_text().splitlines()
            if lines and lines[-1].isdigit():
                peak = int(lines[-1])
        decode_started = time.monotonic()
        try:
            response = json.loads(stdout)
        except json.JSONDecodeError:
            response = None
        decode_seconds = time.monotonic() - decode_started
        row = {"engine": engine, "stage": stage, "repeat": repeat, "args": command,
               "wall_seconds": elapsed, "peak_rss_kib": peak, "exit_code": code,
               "stdout_bytes": len(stdout.encode()), "json_decode_seconds": decode_seconds,
               "stderr": stderr, "correct": code == expected_code}
        row["response" if response is not None else "stdout"] = response if response is not None else stdout
        self.rows.append(row)
        if code != expected_code:
            raise RuntimeError(f"{engine} {stage} exited {code}: {stderr}")
        if code and stdout:
            raise RuntimeError("failed Mousa command emitted success output")
        if engine.startswith("mousa") and code == 0 and not isinstance(response, dict):
            row["correct"] = False
            raise RuntimeError(f"{engine} {stage} returned invalid JSON object")
        return response, row

    def with_input(self, command, records, environment):
        # Use a file-backed stdin so the shared process-group timeout helper remains unchanged.
        path = self.work / "input.jsonl"
        path.write_text("\n".join(json.dumps(record) for record in records) + "\n")
        with path.open() as stream:
            return run_process(command, self.timeout, stdin=stream, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=environment)


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def verify_evidence(hit, original):
    """Verify normalized revision bytes before using a passage's coordinates."""
    original.decode("utf-8", errors="strict")
    normalized = original.removeprefix(b"\xef\xbb\xbf").replace(b"\r\n", b"\n").replace(b"\r", b"\n")
    if hashlib.sha256(normalized).hexdigest() != hit["representation_sha256"]:
        raise ValueError("source bytes do not match the evidence representation")
    start, end = hit["byte_start"], hit["byte_end"]
    require(type(start) is int and type(end) is int and 0 <= start < end <= len(normalized),
            "invalid normalized UTF-8 byte range")
    selected = normalized[start:end]
    require(selected == hit["text"].encode("utf-8") and len(selected) == hit["byte_length"],
            "evidence text disagrees with normalized source range")
    require(hashlib.sha256(selected).hexdigest() == hit["content_sha256"],
            "evidence content digest mismatch")
    require(len(hit["representation_id"]) == 64 and hit["segment_policy"] in ("fixed-v1", "passage-v1"),
            "unsupported representation metadata")
    return selected

def evolving_example(runner, binary, work, policy="fixed-v1"):
    store = work / (policy + "-example.sqlite")
    prefix = [binary, "-store", store]
    def call(stage, *args, records=None, expected_code=0):
        if args[0] == "sync" and "--segment-policy" not in args:
            args = (args[0], "--segment-policy", policy, *args[1:])
        response, row = runner.run("mousa_example", stage, 0, [*prefix, *args],
                                   records=records, expected_code=expected_code)
        row["segment_policy"] = policy
        return response
    original = "\ufeffCedar launch starts Tuesday.\r\nCafé 東京.\r".encode("utf-8")
    call("initial", "sync", "--source", "bulletins", records=[
        {"id": "schedule@draft", "text": original.decode("utf-8")},
        {"id": "inventory", "text": "Orchid inventory is separate."},
    ])
    call("peer", "sync", "--source", "peer", records=[{"id": "peer", "text": "Cedar peer-only material."}])
    first = call("query", "query", "--budget-bytes", "64", "--source", "bulletins", "cedar")
    require([hit["item"] for hit in first["evidence"]] == ["schedule@draft"], "source isolation failed")
    verify_evidence(first["evidence"][0], original)
    require(first["evidence"][0]["segment_policy"] == policy, "sync policy was not retained")
    inspected = call("trail", "trail", "--source", "bulletins", first["trail_id"])
    require(inspected["historical"]["packet_id"] == first["packet_id"], "packet trail did not round-trip")
    call("cross_source_trail", "trail", "--source", "peer", first["trail_id"], expected_code=1)
    other = "passage-v1" if policy == "fixed-v1" else "fixed-v1"
    call("jsonl_policy_resync", "sync", "--segment-policy", other, "--source", "bulletins",
         records=[{"id": "schedule@draft", "text": original.decode("utf-8")}])
    changed_policy = call("jsonl_policy_query", "query", "--source", "bulletins", "cedar")
    verify_evidence(changed_policy["evidence"][0], original)
    require(changed_policy["evidence"][0]["representation_id"] != first["evidence"][0]["representation_id"]
            and changed_policy["evidence"][0]["segment_policy"] == other,
            "JSONL policy change did not replace the representation")
    call("jsonl_policy_restore", "sync", "--source", "bulletins",
         records=[{"id": "schedule@draft", "text": original.decode("utf-8")}])
    policy_restored = call("jsonl_policy_restored_query", "query", "--source", "bulletins", "cedar")
    require(policy_restored["evidence"][0]["segment_id"] == first["evidence"][0]["segment_id"],
            "JSONL policy restoration changed canonical identity")
    call("update", "sync", "--source", "bulletins", records=[{"id": "schedule@draft", "text": "Cedar launch moved to Friday."}])
    current = call("updated_query", "query", "--source", "bulletins", "cedar")
    require(current["evidence"][0]["text"] == "Cedar launch moved to Friday.", "stale revision released")
    revised = b"Cedar launch moved to Friday."
    verify_evidence(current["evidence"][0], revised)
    try:
        verify_evidence(first["evidence"][0], revised)
    except ValueError:
        pass
    else:
        raise RuntimeError("old response accepted a different source revision")
    verify_evidence(first["evidence"][0], original)
    retired = call("retired_trail", "trail", "--source", "bulletins", first["trail_id"])
    require(retired["historical"]["candidates"][0]["indexed_now"] is False, "retired revision reported indexed")
    retained = call("omission_retains_item", "query", "--source", "bulletins", "orchid")
    require([hit["item"] for hit in retained["evidence"]] == ["inventory"], "JSONL omission deleted an item")
    call("delete", "sync", "--source", "bulletins", records=[{"id": "schedule@draft", "deleted": True}])
    deleted = call("deleted_query", "query", "--source", "bulletins", "cedar")
    require(deleted["outcome"] == "no_matches" and not deleted["evidence"], "tombstone left current text indexed")
    call("restore", "sync", "--source", "bulletins", records=[{"id": "schedule@draft", "text": "Cedar launch moved to Friday."}])
    restored = call("restored_query", "query", "--source", "bulletins", "cedar")
    require(restored["evidence"][0]["segment_id"] == current["evidence"][0]["segment_id"], "identical restore changed canonical segment identity")
    omitted = call("budget_omitted", "query", "--budget-bytes", "1", "--source", "bulletins", "cedar")
    require(omitted["outcome"] == "budget_omitted" and not omitted["evidence"], "budget omission not distinguished")
    call("deny", "access", "--source", "bulletins", "deny")
    denied = call("denied_query", "query", "--source", "bulletins", "cedar")
    denied_trail = call("denied_trail", "trail", "--source", "bulletins", first["trail_id"])
    require(denied["outcome"] == "policy_excluded" and not denied["evidence"], "denied text released")
    require("historical" not in denied_trail, "denied historical metadata released")
    for value in (first["evidence"][0]["segment_id"], first["evidence"][0]["content_sha256"],
                  first["evidence"][0]["representation_id"], first["evidence"][0]["representation_sha256"],
                  first["evidence"][0]["text"]):
        require(value not in json.dumps(denied) and value not in json.dumps(denied_trail), "denied candidate metadata leaked")
    call("allow", "access", "--source", "bulletins", "allow")
    call("withdraw", "withdraw", "--source", "bulletins")
    call("allow_after_withdrawal", "access", "--source", "bulletins", "allow")
    withdrawn = call("withdrawn_query", "query", "--source", "bulletins", "cedar")
    require(withdrawn["outcome"] == "lifecycle_excluded" and not withdrawn["evidence"], "allow overrode withdrawal")
    peer = call("unaffected_peer", "query", "--source", "peer", "cedar")
    require([hit["item"] for hit in peer["evidence"]] == ["peer"], "withdrawal affected peer source")

    root = work / (policy + "-directory")
    root.mkdir()
    raw = ("\ufeff" + "Amber café 東京.\r\n\r\n" * 300 + "Amber end\r").encode("utf-8")
    path = root / "locations.md"
    path.write_bytes(raw)
    call("directory_sync", "sync", root)
    located = call("directory_query", "query", "--budget-bytes", "16384", root, "amber")
    require(len(located["evidence"]) > 1, "fixture did not return multiple passages")
    for hit in located["evidence"]:
        verify_evidence(hit, raw)
        require(hit["segment_policy"] == policy, "directory policy mismatch")
    require(len({hit["representation_id"] for hit in located["evidence"]}) == 1,
            "one revision has inconsistent representation identities")
    other = "passage-v1" if policy == "fixed-v1" else "fixed-v1"
    call("policy_resync", "sync", "--segment-policy", other, root)
    changed = call("policy_query", "query", "--budget-bytes", "16384", root, "amber")
    require(changed["evidence"][0]["representation_id"] != located["evidence"][0]["representation_id"],
            "policy change reused the old representation")
    for hit in changed["evidence"]:
        verify_evidence(hit, raw)
        require(hit["segment_policy"] == other, "policy resync did not replace the active segments")
    retired = call("policy_trail", "trail", root, located["trail_id"])
    require(all(not row["indexed_now"] for row in retired["historical"]["candidates"]),
            "policy-retired segments remain indexed")
    call("policy_restore", "sync", root)
    restored = call("policy_restored_query", "query", "--budget-bytes", "16384", root, "amber")
    require([h["segment_id"] for h in restored["evidence"]] == [h["segment_id"] for h in located["evidence"]],
            "policy restoration changed canonical identities")
    path.unlink()
    call("directory_delete", "sync", root)
    deleted = call("directory_deleted_query", "query", root, "amber")
    require(not deleted["evidence"] and deleted["outcome"] == "no_matches", "directory deletion released text")
    path.write_bytes(raw)
    call("directory_restore", "sync", root)
    restored = call("directory_restored_query", "query", "--budget-bytes", "16384", root, "amber")
    require([h["segment_id"] for h in restored["evidence"]] == [h["segment_id"] for h in located["evidence"]],
            "directory restoration changed canonical identities")


def comparison(runner, args, work):
    root = work / "corpus"
    root.mkdir()
    engines = ["mousa", "sqlite"] + (["qmd"] if args.qmd else [])
    warm, stores = [], []
    for repeat in range(args.repeats):
        order = engines[repeat % len(engines):] + engines[:repeat % len(engines)]
        for engine in order:
            for path in root.iterdir():
                path.unlink()
            expected = {f"d{index:03d}.md": document(index, GENERATIONS[0]) for index in range(args.documents)}
            for name, text in expected.items():
                (root / name).write_text(text)
            store = work / f"{engine}-{repeat}.sqlite"
            home = work / f"qmd-{repeat}"
            environment = dict(os.environ, HOME=str(home / "home"), QMD_CONFIG_DIR=str(home / "config"), XDG_CONFIG_HOME=str(home / "config"), XDG_CACHE_HOME=str(home / "cache"), NO_COLOR="1")
            if args.node:
                environment["PATH"] = str(args.node.parent) + os.pathsep + environment["PATH"]
            qmd = [str(args.node or "node"), str(args.qmd), "--index", "comparison"]
            def sync(stage, initial=False):
                if engine == "mousa":
                    command = [args.mousa, "-store", store, "sync", root]
                elif engine == "sqlite":
                    command = [sys.executable, Path(__file__).resolve(), "sqlite", store, "sync", root]
                else:
                    command = [*qmd, "collection", "add", root, "--name", "notes", "--mask", "**/*.md"] if initial else [*qmd, "update"]
                response, row = runner.run(engine, stage, repeat, command, environment=environment)
                if engine in ("mousa", "sqlite") and stage.startswith("noop"):
                    count = len(response.get("unchanged") or []) if engine == "mousa" else response["unchanged"]
                    row["correct"] = count == len(expected)
                    require(row["correct"], f"{engine} no-op did not preserve all current documents")
            def query(stage, term, budget):
                if engine == "mousa":
                    command = [args.mousa, "-store", store, "query", "--budget-bytes", str(budget), root, term]
                elif engine == "sqlite":
                    command = [sys.executable, Path(__file__).resolve(), "sqlite", store, "query", root, term]
                else:
                    command = [*qmd, "search", term, "-c", "notes", "--json", "--full", "-n", "100"]
                response, row = runner.run(engine, stage, repeat, command, environment=environment)
                packing_started = time.monotonic()
                if engine == "mousa":
                    selected, used = response["evidence"], response["used_bytes"]
                    considered = response["matched_candidates"]
                else:
                    candidates = response if engine == "sqlite" else [{"item": urlsplit(hit["file"]).path.lstrip("/"), "text": hit["body"]} for hit in response]
                    considered = len(candidates)
                    selected, used = pack(candidates, budget)
                packing_seconds = time.monotonic() - packing_started
                relevant = {name for name, text in expected.items() if term in text.split()}
                returned = [hit["item"] for hit in selected]
                correct = (len(returned) == len(set(returned)) and set(returned) <= relevant
                           and all(hit["text"] == expected.get(hit["item"]) for hit in selected)
                           and sum(len(hit["text"].encode()) for hit in selected) == used <= budget
                           and considered == len(relevant))
                row.update(query=term, budget_bytes=budget, used_bytes=used, returned_items=returned,
                           relevant_items=sorted(relevant), considered=considered, correct=correct,
                           relevant_selected=len(set(returned) & relevant),
                           recall_at_budget=len(set(returned) & relevant) / len(relevant) if relevant else None,
                           packing_seconds=packing_seconds, consumer_seconds=row["wall_seconds"] + row["json_decode_seconds"] + packing_seconds)
                require(correct, f"{engine} {stage} returned stale, out-of-scope, or incorrectly budgeted evidence")
                return response
            if engine == "mousa":
                preview, row = runner.run(engine, "preview", repeat, [args.mousa, "-store", store, "sync", "--preview", root])
                require(not store.exists() and {item["item"] for item in preview["selected"]} == set(expected), "preview changed the store or selected the wrong files")
            sync("initial", initial=True)
            sync("noop-1")
            for budget in args.budgets:
                for topic in TOPICS:
                    query("initial_query", topic, budget)
            for revision, generation in enumerate(GENERATIONS[1:], start=2):
                for index in range(args.documents):
                    name = f"d{index:03d}.md"
                    expected[name] = document(index, generation)
                    (root / name).write_text(expected[name])
                sync(f"update-{revision}")
                sync(f"noop-{revision}")
                for topic in TOPICS:
                    query(f"current-{revision}", topic, args.budgets[0])
                query(f"stale-{revision}", GENERATIONS[revision-2], args.budgets[0])
            removed = expected.pop("d000.md")
            (root / "d000.md").unlink()
            sync("delete")
            query("deleted_query", "marker000", args.budgets[0])
            expected["d000.md"] = removed
            (root / "d000.md").write_text(removed)
            sync("restore")
            query("restored_query", "marker000", args.budgets[0])
            reference = query("warm_reference", "cedar", args.budgets[0])
            if engine == "mousa" and args.warm:
                response, row = runner.run(engine, "warm_probe", repeat, [args.warm, "-store", store, "-root", root, "-query", "cedar", "-budget", str(args.budgets[0]), "-iterations", str(args.warm_iterations)])
                count = sum("cedar" in text.split() for text in expected.values())
                require(all(stage["candidates"] == count for stage in response["stages"] if stage["stage"] != "preparation"), "warm probe did not exercise the same fixture")
                require(all(stage["used_bytes"] == reference["used_bytes"] for stage in response["stages"] if stage["stage"] in ("new_historical_trace", "fresh_current_query")), "warm packing differs from the cold CLI fixture")
                warm.append(response)
            files = [path for path in (home.rglob("*") if engine == "qmd" else work.glob(store.name + "*")) if path.is_file()]
            stores.append({"engine": engine, "repeat": repeat, "post_exit_bytes": sum(path.stat().st_size for path in files), "files": {str(path.relative_to(work)): path.stat().st_size for path in files}})
    return warm, stores


def source_manifest(root):
    files = list(root.rglob("*.go")) + list((root / "eval/local").glob("*.py")) + [root / "go.mod", root / "go.sum"]
    files += list((root / "eval/local/qmd").glob("*.json"))
    return {str(path.relative_to(root)): hashlib.sha256(path.read_bytes()).hexdigest() for path in sorted(files)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mousa", type=Path, required=True)
    parser.add_argument("--example-only", action="store_true", help="run only the synthetic CLI client workflow, without comparisons")
    parser.add_argument("--warm", type=Path)
    parser.add_argument("--qmd", type=Path, help="published package's bin/qmd entry; lexical search only")
    parser.add_argument("--node", type=Path)
    parser.add_argument("--dependency-lock", type=Path)
    parser.add_argument("--environment", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--documents", type=int, default=24)
    parser.add_argument("--repeats", type=int, default=3)
    parser.add_argument("--budgets", type=int, nargs="+", default=[128, 256, 512])
    parser.add_argument("--timeout", type=float, default=30)
    parser.add_argument("--warm-iterations", type=int, default=30)
    args = parser.parse_args()
    if not 4 <= args.documents <= 96 or args.repeats < 1 or args.repeats > 10 or any(b < 64 or b > 8192 for b in args.budgets) or args.timeout <= 0:
        parser.error("require 4..96 documents, 1..10 repeats, 64..8192 byte budgets, and positive timeout")
    if args.example_only and any((args.warm, args.qmd, args.node, args.dependency_lock)):
        parser.error("--example-only cannot be combined with comparison executables or dependency lock")
    for name in ("mousa", "warm", "qmd", "node", "dependency_lock"):
        path = getattr(args, name)
        if path:
            setattr(args, name, path.resolve(strict=True))
    report = {"protocol": "mousa-evolving-lexical-workflow-v1", "result": "FAIL",
              "documents": args.documents, "repeats": args.repeats, "budgets": args.budgets,
              "generations": list(GENERATIONS), "python": platform.python_version(), "sqlite_baseline_version": sqlite3.sqlite_version,
              "source_manifest": source_manifest(Path(__file__).resolve().parents[2]),
              "binary_sha256": hashlib.sha256(args.mousa.read_bytes()).hexdigest(),
              "dependency_lock_sha256": hashlib.sha256(args.dependency_lock.read_bytes()).hexdigest() if args.dependency_lock else None,
              "qmd": "native lexical search" if args.qmd else "NOT RUN: no QMD entry supplied",
              "warm_status": "requested" if args.warm else "NOT RUN: no warm probe supplied",
              "model_backed": "NOT RUN: no equivalent implemented Mousa route; no model inference or downloads",
              "measurement": "Fresh CLI process per command; full startup/output/close included; builds excluded; no cache dropping; rotating engine order. GNU time RSS is not summed simultaneous process-tree RSS. Baseline packing happens after native output, so native baseline text release is not budget-bounded.",
              "fixture_limits": "Small generated ASCII/LF Markdown, single terms without stemming/prefix collisions, no semantic relevance claim. All topic matches are relevant by construction; recall measures fixture coverage under budget, not general retrieval quality. Minimal SQLite has one-transaction sync and no history, policy, trails, or ingestion safety controls.",
              "path_redaction": "Ephemeral work root becomes <work>; executable paths become named placeholders. Canonical identifiers are unchanged."}
    report["mode"] = "client-example" if args.example_only else "comparison"
    if args.example_only:
        report["measurement"] = "One invocation per client stage; full CLI startup/output/close included; builds excluded. Diagnostic timings, not a performance comparison."
        report["fixture_limits"] = "Synthetic bulletins, an isolated peer and a multibyte directory fixture under both segmentation policies. Uses only the supported CLI; no retrieval or packing implementation in the client path."
    if args.environment:
        report["environment"] = json.loads(args.environment.read_text())
    with tempfile.TemporaryDirectory(prefix="mousa-workflow-") as temporary:
        work = Path(temporary)
        runner = Commands(work, args.timeout)
        try:
            if args.qmd:
                environment = dict(os.environ, HOME=str(work / "version-home"), QMD_CONFIG_DIR=str(work / "version-config"), XDG_CACHE_HOME=str(work / "version-cache"), NO_COLOR="1")
                _, row = runner.run("qmd", "version", 0, [args.node or "node", args.qmd, "--version"], environment=environment)
                report["qmd_version"] = row.get("stdout")
                _, row = runner.run("node", "version", 0, [args.node or "node", "--version"], environment=environment)
                report["node_version"] = row.get("stdout")
            for policy in (("fixed-v1", "passage-v1") if args.example_only else ("fixed-v1",)):
                evolving_example(runner, args.mousa, work, policy)
            if not args.example_only:
                report["warm"], report["stores"] = comparison(runner, args, work)
            if args.warm:
                report["warm_status"] = "PASS"
            report["result"] = "PASS"
        except Exception as error:
            report["error"] = str(error)
            if runner.rows:
                runner.rows[-1]["correct"] = False
        report["rows"] = runner.rows
        report["rss_method"] = "GNU time maximum resident set size, KiB" if runner.time_binary else "NOT RUN: GNU time unavailable"
        groups = {}
        for row in runner.rows:
            if row["stage"] == "warm_probe":
                continue
            key = (row["engine"], row["stage"], row.get("budget_bytes"))
            groups.setdefault(key, []).append(row)
        report["summary"] = [{"engine": key[0], "stage": key[1], "budget_bytes": key[2], "samples": len(rows),
                              "correct_samples": sum(row["correct"] for row in rows),
                              "median_wall_seconds": statistics.median(row["wall_seconds"] for row in rows),
                              "min_wall_seconds": min(row["wall_seconds"] for row in rows),
                              "max_wall_seconds": max(row["wall_seconds"] for row in rows)} for key, rows in groups.items()]
        serialized = json.dumps(report, indent=2).replace(str(work), "<work>")
        for path, label in ((args.mousa, "<mousa>"), (args.warm, "<warm-probe>"), (args.qmd, "<qmd>"), (args.node, "<node>"), (Path(__file__).resolve(), "<workflow.py>"), (sys.executable, "<python>")):
            if path:
                serialized = serialized.replace(str(path), label)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(serialized + "\n")
    print("WORKFLOW_RESULTS=" + json.dumps(json.loads(serialized), separators=(",", ":")), flush=True)
    return int(report["result"] != "PASS")


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "sqlite":
        minimal_sqlite(sys.argv[2:])
    else:
        raise SystemExit(main())
