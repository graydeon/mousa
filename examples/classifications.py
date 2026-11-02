"""Record synthetic caller-reviewed labels without changing evidence selection."""
import argparse
import json
from pathlib import Path
import subprocess
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mousa", type=Path, required=True)
    args = parser.parse_args()
    binary = args.mousa.resolve()
    with tempfile.TemporaryDirectory(prefix="mousa-classifications-") as temporary:
        store = Path(temporary) / "evidence.sqlite"

        def command(*argv, value=None):
            result = subprocess.run(
                [str(binary), "-store", str(store), *argv], input=value,
                text=True, capture_output=True, timeout=30, check=True)
            return result.stdout, json.loads(result.stdout)

        def sync(text):
            return command("sync", "--source", "synthetic-labels",
                           value=json.dumps({"id": "doc", "text": text}) + "\n")

        def query():
            return command("query", "--source", "synthetic-labels", "cedar")[1]

        sync("cedar synthetic evidence; no factual review is performed by this example")
        before = query()
        passage = before["evidence"][0]
        draft = {
            "schema": "mousa.classification_input.v1",
            "subject": {"kind": "segment", "id": passage["segment_id"]},
            "taxonomy": "example.caller-review", "taxonomy_version": "1",
            "label": "synthetic-caller-reviewed", "asserter_id": "example.caller",
            "asserter_version": "1",
            "basis": [{"kind": "representation", "id": passage["representation_id"]}],
            "asserted_at_usec": 1, "confidence_ppm": 0,
        }
        submitted = json.dumps(draft)
        original_bytes, original = command("classification", "put", value=submitted)
        retry_bytes, _ = command("classification", "put", value=submitted)
        read_bytes, _ = command("classification", "get", original["id"])
        assert original_bytes == retry_bytes == read_bytes
        assert original["subject"] == draft["subject"]
        assert original["basis"] == draft["basis"]
        draft["label"] = "synthetic-disputed"
        _, changed = command("classification", "put", value=json.dumps(draft))
        assert changed["id"] != original["id"]
        assert command("classification", "get", original["id"])[0] == original_bytes
        after = query()
        assert after["packet_id"] == before["packet_id"]
        assert after["evidence"] == before["evidence"]
        sync("cedar replacement revision; the previous label does not label this segment")
        replacement = query()["evidence"][0]
        assert replacement["segment_id"] != passage["segment_id"]
        assert replacement["representation_id"] != passage["representation_id"]
        assert command("classification", "get", original["id"])[0] == original_bytes
        assert original["subject"]["id"] != replacement["segment_id"]
        print(json.dumps({
            "result": "PASS", "original": original, "changed": changed,
            "replacement_segment_id": replacement["segment_id"],
            "replacement_representation_id": replacement["representation_id"],
            "boundary": "Synthetic attribution only; no classifier, factual verification, "
                        "retrieval filtering, or reconstruction of retired text.",
        }, indent=2))


if __name__ == "__main__":
    main()
