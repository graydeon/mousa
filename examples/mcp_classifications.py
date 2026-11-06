"""Record synthetic attributed labels through MCP, without inference or filtering."""
import argparse
import io
import json
from pathlib import Path
import tempfile

from memory.memory import Client


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mousa", type=Path, required=True)
    args = parser.parse_args()
    source = "synthetic-mcp-classifications"
    with tempfile.TemporaryDirectory(prefix="mousa-mcp-classifications-") as temporary:
        receipts = io.StringIO()
        client = Client(args.mousa.resolve(), Path(temporary) / "evidence.sqlite",
                        receipts, True, 30, source, classification_admin=True)
        try:
            client.initialize()

            def call(name, **arguments):
                return client.call(name, dict(source=source, **arguments))[0]

            call("mousa_sync", items=[dict(id="doc", text="cedar synthetic evidence; no factual review")])
            exported = call("mousa_vector_inputs")
            assert exported["decision_outcome"] == "allow"
            pin, = exported["passages"]
            before = call("mousa_query", query="cedar", policy="original", budget_bytes=8192)
            draft = dict(schema="mousa.classification_input.v1",
                         subject=dict(kind="segment", id=pin["segment_id"]),
                         taxonomy="example.caller-review", taxonomy_version="1",
                         label="synthetic-caller-reviewed", asserter_id="example.caller",
                         asserter_version="1", asserted_at_usec=1, confidence_ppm=0,
                         basis=[dict(kind="representation", id=pin["representation_id"])])
            first = call("mousa_classification_put", classification=draft)["classification"]
            assert first == dict(draft, schema="mousa.classification.v1", id=first["id"])
            assert type(first["confidence_ppm"]) is int and first["confidence_ppm"] == 0
            assert call("mousa_classification_put", classification=draft)["classification"] == first
            second_draft = dict(draft, asserter_version="2", label="synthetic-disputed", asserted_at_usec=2)
            second = call("mousa_classification_put", classification=second_draft)["classification"]
            assert first["id"] != second["id"]
            for record in (first, second):
                fetched = call("mousa_classification_get", classification_id=record["id"])
                assert fetched["decision_outcome"] == "allow" and fetched["classification"] == record
            after = call("mousa_query", query="cedar", policy="original", budget_bytes=8192)
            for key in ("packet_id", "evidence", "used_bytes"):
                assert before[key] == after[key], key
            print(json.dumps(dict(result="PASS", source_id=pin["source_id"], first=first, second=second,
                                 packet_id=before["packet_id"], boundary="Synthetic attribution only; no classifier, calibrated confidence, factual truth or retrieval filtering."), indent=2))
        finally:
            cleanup = client.close()
            assert cleanup == dict(exit_code=0, forced=False, stderr="", cleanup_errors=[]), cleanup


if __name__ == "__main__":
    main()
