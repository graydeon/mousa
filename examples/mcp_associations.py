#!/usr/bin/env python3
"""Demonstrate caller-declared supporting context over stdio MCP; no model calls."""
import argparse
import hashlib
import json
from pathlib import Path
import tempfile

from memory.memory import Client


FIXTURE = {
    "backup": "Run the backup inside the connection context manager.",
    "closure": "The context manager does not close the connection; close it explicitly.",
}
DECLARATION = {"from_item": "backup", "to_item": "closure",
               "basis": "connection cleanup qualification", "author": "example.maintainer"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mousa", required=True, type=Path)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="mousa-associations-") as temporary:
        root = Path(temporary)
        source = "notes"
        fixture = root / "notes.jsonl"
        fixture.write_text("".join(json.dumps({"id": key, "text": value}) + "\n"
                                   for key, value in FIXTURE.items()), encoding="utf-8")
        with (root / "stdio.jsonl").open("w", encoding="utf-8") as receipts:
            client = Client(args.mousa.resolve(), root / "evidence.sqlite", receipts, True, 30, source)
            try:
                client.initialize()
                client.call("mousa_sync", {"source": source, "items": [
                    json.loads(line) for line in fixture.read_text(encoding="utf-8").splitlines()]})
                query = {"source": source, "query": "backup", "policy": "original", "budget_bytes": 8192}
                ordinary, _ = client.call("mousa_query", query)
                assert [p["item"] for p in ordinary["evidence"]] == ["backup"]
                declared, _ = client.call("mousa_query", dict(query, associations={
                    "schema": "mousa.association_declarations.v1", "associations": [DECLARATION]}))
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
                print(json.dumps({"ordinary": ordinary, "declared": declared, "trail": trail}, indent=2))
            finally:
                cleanup = client.close()
                if cleanup["exit_code"] != 0 or cleanup["forced"] or cleanup["cleanup_errors"]:
                    raise RuntimeError(cleanup)


if __name__ == "__main__":
    main()
