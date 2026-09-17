#!/usr/bin/env python3
"""Retrieve inspectable documentation passages through the Mousa CLI."""

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import subprocess
import sys
import tarfile
import time

# Reuse the maintained normalized-coordinate verifier and process timeout helper.
sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "eval" / "local"))
from lifecycle import run_process
from workflow import verify_evidence


def relative_path(name):
    path = PurePosixPath(name)
    if not name or path.is_absolute() or ".." in path.parts or str(path) != name:
        raise ValueError("corpus paths must be canonical relative paths")
    return path


def load_corpus(directory):
    manifest_bytes = (directory / "corpus.json").read_bytes()
    manifest = json.loads(manifest_bytes)
    if len(set(manifest["documents"])) != len(manifest["documents"]):
        raise ValueError("corpus needs distinct documents")
    documents = {}
    for name, metadata in manifest["files"].items():
        path = directory / relative_path(name)
        if any(parent.is_symlink() for parent in (path, *path.parents)):
            raise ValueError("corpus links are not supported")
        content = path.read_bytes()
        if hashlib.sha256(content).hexdigest() != metadata["sha256"]:
            raise ValueError("corpus hash mismatch: " + name)
        if name in manifest["documents"]:
            content.decode("utf-8", errors="strict")
            documents[name] = content
    if set(documents) != set(manifest["documents"]):
        raise ValueError("document missing from corpus manifest")
    return manifest, documents, hashlib.sha256(manifest_bytes).hexdigest()


def prepare(directory, archive_path=None):
    # Only unpack the bundled, bounded regular-file archive into a new directory.
    with tarfile.open(archive_path or Path(__file__).with_name("git-docs.tar.xz"), "r:xz") as archive:
        members = archive.getmembers()
        if len({m.name for m in members}) != len(members) or sum(m.size for m in members) > 1_000_000:
            raise ValueError("invalid bundled corpus size or duplicate member")
        for member in members:
            relative_path(member.name)
            if not member.isfile():
                raise ValueError("corpus archive must contain only regular files")
        directory.mkdir(parents=True, exist_ok=False)
        for member in members:
            target = directory / member.name
            target.parent.mkdir(parents=True, exist_ok=True)
            with archive.extractfile(member) as stream:
                target.write_bytes(stream.read())
    manifest, _, digest = load_corpus(directory)
    return {"outcome": "prepared", "version": manifest["version"], "manifest_sha256": digest}


def invoke(binary, store, arguments, timeout):
    code, stdout, stderr = run_process(
        [str(binary), "-store", str(store), *arguments], timeout,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    if code != 0:
        raise RuntimeError(f"Mousa exited {code}: {stderr.strip()}")
    response = json.loads(stdout)
    if not isinstance(response, dict):
        raise ValueError("Mousa response must be a JSON object")
    return response


def sync(binary, store, directory, timeout):
    manifest, documents, digest = load_corpus(directory)
    arguments = ["sync", "--segment-policy", "passage-v1"]
    for name in documents:
        # Slash-free CLI includes also match nested basenames; preview checks membership.
        if any(c in name for c in "*?[]\\"):
            raise ValueError("corpus document names cannot contain glob characters")
        arguments.extend(["--include", name])
    if not documents:
        arguments.extend(["--exclude", "*"])
    preview = invoke(binary, store, [*arguments, "--preview", str(directory)], timeout)
    selected = {item["item"] for item in preview["selected"]}
    if selected != set(documents):
        raise ValueError("directory selection differs from corpus documents: "
                         + json.dumps({"extra": sorted(selected - documents.keys()),
                                       "missing": sorted(documents.keys() - selected)}))
    response = invoke(binary, store, [*arguments, str(directory)], timeout)
    return {"outcome": "synced", "version": manifest["version"],
            "manifest_sha256": digest, "response": response}


def ask(binary, store, directory, question, budget, timeout, arguments=()):
    manifest, documents, digest = load_corpus(directory)
    response = invoke(binary, store, ["query", "--budget-bytes", str(budget), *arguments,
                      str(directory), question], timeout)
    if response["source"] != str(directory) or response["query"] != question:
        raise ValueError("query source or question mismatch")
    if response["budget_bytes"] != budget:
        raise ValueError("query budget mismatch")
    evidence = response["evidence"] or []
    if evidence and (response["decision_outcome"] != "allow" or response["outcome"] != "evidence"):
        raise ValueError("evidence without an allowed decision")
    used = 0
    for hit in evidence:
        name = hit["item"]
        if name not in documents:
            raise ValueError("evidence is outside the declared corpus: " + name)
        selected = verify_evidence(hit, documents[name])
        used += len(selected)
        normalized = documents[name].removeprefix(b"\xef\xbb\xbf").replace(b"\r\n", b"\n").replace(b"\r", b"\n")
        first = normalized[:hit["byte_start"]].count(b"\n") + 1
        last = normalized[:hit["byte_end"] - 1].count(b"\n") + 1
        hit["location"] = {"path": name, "line_start": first, "line_end": last,
                           "url": manifest["files"][name]["url"]}
    if used != response["used_bytes"] or used > budget:
        raise ValueError("released evidence exceeds declared accounting")
    if not evidence and response["outcome"] == "evidence":
        raise ValueError("empty evidence result")
    return {"outcome": "evidence_available" if evidence else "insufficient_evidence",
            "support": "not_assessed", "answer": None,
            "snapshot": "Authorized at query time; not proof of current access or current source state.",
            "corpus": {"name": manifest["name"], "version": manifest["version"],
                       "revision": manifest["revision"], "manifest_sha256": digest,
                       "attribution": manifest["attribution"]},
            "response": response}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mousa", type=Path, default=Path("./mousa"))
    parser.add_argument("--store", type=Path, default=Path("docs.sqlite"))
    parser.add_argument("--directory", type=Path, required=True)
    parser.add_argument("--timeout", type=float, default=30)
    commands = parser.add_subparsers(dest="operation", required=True)
    commands.add_parser("prepare", help="unpack the pinned corpus into a new directory")
    commands.add_parser("sync", help="replace current corpus evidence from verified files")
    query = commands.add_parser("ask", help="retrieve passages; does not generate an answer")
    query.add_argument("--budget-bytes", type=int, default=4096)
    query.add_argument("question")
    args = parser.parse_args()
    if args.timeout <= 0 or (args.operation == "ask" and args.budget_bytes <= 0):
        parser.error("timeout and byte budget must be positive")
    started = time.perf_counter_ns()
    try:
        directory = Path(os.path.abspath(args.directory))
        if args.operation == "prepare":
            result = prepare(directory)
        elif args.operation == "sync":
            result = sync(args.mousa.resolve(strict=True), args.store, directory, args.timeout)
        else:
            result = ask(args.mousa.resolve(strict=True), args.store, directory,
                         args.question, args.budget_bytes, args.timeout)
        result["elapsed_ms"] = (time.perf_counter_ns() - started) / 1e6
        print(json.dumps(result, ensure_ascii=False, indent=2))
        return 0
    except (OSError, ValueError, RuntimeError, KeyError, TypeError, tarfile.TarError) as error:
        print(json.dumps({"outcome": "error", "error": str(error)}), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
