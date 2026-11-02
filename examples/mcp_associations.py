#!/usr/bin/env python3
"""Retrieve a primary instruction and declared qualification; no model calls."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile

from memory.memory import Client


FIXTURE = {
    "backup": "Run the backup inside the connection context manager.",
    "closure": "The context manager does not close the connection; close it explicitly.",
}
DECLARATION = {"from_item": "backup", "to_item": "closure",
               "basis": "connection cleanup qualification", "author": "example.maintainer"}
DOCUMENT = {"schema": "mousa.association_declarations.v1", "associations": [DECLARATION]}


def reference(result, python, cache):
    counted = subprocess.run(
        [str(python), "-I", "-c",
         "import sys,tiktoken,tiktoken.load\n"
         "from importlib.metadata import version\n"
         "assert version('tiktoken') == '0.12.0'\n"
         "def offline(*args, **kwargs): raise RuntimeError('prepare the offline cache first')\n"
         "tiktoken.load.read_file=offline\n"
         "print(len(tiktoken.get_encoding('o200k_base').encode_ordinary(sys.stdin.read())))"],
        input=result["context"], text=True, capture_output=True, timeout=30, check=True,
        env=dict(os.environ, TIKTOKEN_CACHE_DIR=str(cache)))
    count = int(counted.stdout)
    token = result["token"]
    assert token["measured_tokens"] == count <= token["limit"]
    assert token["rendered_bytes"] == len(result["context"].encode())
    assert token["rendered_sha256"] == hashlib.sha256(result["context"].encode()).hexdigest()
    expected = "\n\n".join(
        f"[source={token['source_id']} segment={p['segment_id']} sha256={p['content_sha256']}]\n{p['text']}"
        for p in result["evidence"])
    assert result["context"] == expected
    return count


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mousa", required=True, type=Path)
    parser.add_argument("--tokenizer-python", type=Path)
    parser.add_argument("--tokenizer-cache", type=Path)
    args = parser.parse_args()
    if (args.tokenizer_python is None) != (args.tokenizer_cache is None):
        parser.error("configure both tokenizer interpreter and cache")
    binary = args.mousa.resolve()
    python = args.tokenizer_python.absolute() if args.tokenizer_python else None
    cache = args.tokenizer_cache.resolve() if args.tokenizer_cache else None
    with tempfile.TemporaryDirectory(prefix="mousa-associations-") as temporary:
        root = Path(temporary)
        source = "notes"
        store = root / "evidence.sqlite"
        with (root / "stdio.jsonl").open("w", encoding="utf-8") as receipts:
            client = Client(binary, store, receipts, True, 30, source, python, cache)
            try:
                client.initialize()
                client.call("mousa_sync", {"source": source, "items": [
                    {"id": key, "text": value} for key, value in FIXTURE.items()]})
                query = {"source": source, "query": "backup", "policy": "original", "budget_bytes": 8192}
                ordinary, _ = client.call("mousa_query", query)
                assert [p["item"] for p in ordinary["evidence"]] == ["backup"]
                declared, _ = client.call("mousa_query", dict(query, associations=DOCUMENT))
                assert [p["item"] for p in declared["evidence"]] == ["backup", "closure"]
                assert declared["evidence"][0] == ordinary["evidence"][0]
                supporting = declared["evidence"][1]
                assert supporting["origin"] == "association" and supporting["association"] == DECLARATION
                assert "rank" not in supporting and "bm25_score" not in supporting
                for passage in declared["evidence"]:
                    text = FIXTURE[passage["item"]].encode()
                    assert passage["text"].encode() == text
                    assert (passage["byte_start"], passage["byte_end"], passage["byte_length"]) == (0, len(text), len(text))
                    assert passage["content_sha256"] == hashlib.sha256(text).hexdigest()
                    assert passage["representation_sha256"] == passage["content_sha256"]
                    assert all(len(passage[k]) == 64 for k in ("segment_id", "representation_id"))
                trail, _ = client.call("mousa_trail", {"source": source, "trail_id": declared["trail_id"]})
                history = trail["historical"]
                assert trail["authorization_outcome"] == "allow"
                assert trail["authorization_decision_id"] != declared["decision_id"]
                assert history["schema"] == "mousa.source_trail.v3"
                assert history["id"] == declared["trail_id"]
                assert all(history[k] == declared[k] for k in ("request_id", "decision_id", "packet_id"))
                row = history["associated"][0]
                assert row["segment_id"] == supporting["segment_id"] and row["selected"] is True
                assert all(row[k] == value for k, value in DECLARATION.items())
                print(json.dumps({"surface": "mcp-bytes", "result": declared, "history": trail}))
                if python:
                    for policy in ("original", "dedup"):
                        for packing in ("original", "exact-v1"):
                            request = dict(query, policy=policy, packing_policy=packing,
                                           associations=DOCUMENT, context_tokens=4096)
                            full, _ = client.call("mousa_query", request)
                            count = reference(full, python, cache)
                            exact, _ = client.call("mousa_query", dict(request, context_tokens=count))
                            short, _ = client.call("mousa_query", dict(request, context_tokens=count - 1))
                            assert exact["context"] == full["context"]
                            assert [p["item"] for p in short["evidence"]] == ["backup"]
                            assert short["evidence"][0] == exact["evidence"][0]
                            assert short["association_omissions"][0]["reason"] == "tokens"
                            reference(short, python, cache)
                            history, _ = client.call("mousa_trail", {
                                "source": source, "trail_id": exact["trail_id"], "token_details": True})
                            h = history["historical"]
                            assert h["schema"] == "mousa.source_trail.v11" and h["token"] == exact["token"]
                            assert all(h[k] == exact[k] for k in ("request_id", "decision_id", "packet_id"))
                            print(json.dumps({"surface": "mcp-tokens", "policy": policy, "packing": packing,
                                              "exact": exact, "short": short, "history": history}))
            finally:
                cleanup = client.close()
                if cleanup["exit_code"] != 0 or cleanup["forced"] or cleanup["cleanup_errors"]:
                    raise RuntimeError(cleanup)
        if python:
            notes = root / "directory"
            notes.mkdir()
            for key, text in FIXTURE.items():
                (notes / (key + ".txt")).write_text(text, encoding="utf-8")
            document = dict(DOCUMENT, associations=[dict(DECLARATION, from_item="backup.txt", to_item="closure.txt")])
            declarations = root / "associations.json"
            declarations.write_text(json.dumps(document), encoding="utf-8")

            def native(*argv):
                result = subprocess.run([str(binary), "-store", str(store), *map(str, argv)],
                                        text=True, capture_output=True, check=True, timeout=30)
                return json.loads(result.stdout)

            native("sync", notes)
            for policy in ("original", "dedup"):
                for packing in ("original", "exact-v1"):
                    options = ["query", "--associations", declarations, "--policy", policy,
                               "--packing-policy", packing, "--tokenizer-python", python, "--tokenizer-cache", cache]
                    full = native(*options, "--context-tokens", 4096, notes, "backup")
                    count = reference(full, python, cache)
                    exact = native(*options, "--context-tokens", count, notes, "backup")
                    short = native(*options, "--context-tokens", count - 1, notes, "backup")
                    assert exact["context"] == full["context"]
                    assert [p["item"] for p in short["evidence"]] == ["backup.txt"]
                    reference(short, python, cache)
                    print(json.dumps({"surface": "native-directory", "policy": policy, "packing": packing,
                                      "exact": exact, "short": short}))


if __name__ == "__main__":
    main()
