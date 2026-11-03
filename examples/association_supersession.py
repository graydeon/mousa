#!/usr/bin/env python3
"""Run explicit byte-only replacement and caller-declared support; no truth inference."""
import argparse
import json
from pathlib import Path
import subprocess
import tempfile

from memory.memory import Client
from token_supersession import identity


FIXTURE = {
    "old": "needle obsolete qualification",
    "primary": "needle retained instruction",
    "twin": "needle obsolete qualification",
    "support": "Nonmatching caller-declared qualification.",
    "new": "Explicit replacement attribution.",
}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mousa", type=Path, required=True)
    args = parser.parse_args()
    binary = args.mousa.absolute()
    with tempfile.TemporaryDirectory(prefix="mousa-composed-") as temporary:
        root = Path(temporary)
        store = root / "evidence.sqlite"

        def command(*argv, value=None):
            result = subprocess.run([str(binary), "-store", str(store), *map(str, argv)],
                                    input=value, text=True, capture_output=True, timeout=30, check=True)
            return json.loads(result.stdout)

        for form in ("jsonl", "directory"):
            source = "synthetic-composed"
            directory = root / "notes"
            suffix = ".txt" if form == "directory" else ""
            if form == "directory":
                directory.mkdir()
                for item, text in FIXTURE.items():
                    (directory / (item + suffix)).write_text(text, encoding="utf-8")
                selector = [directory]
                command("sync", *selector)
            else:
                selector = ["--source", source]
                command("sync", *selector, value="".join(json.dumps(dict(id=k, text=v)) + "\n"
                                                        for k, v in FIXTURE.items()))
            pins = {p["item"]: p for p in command("vector", "inputs", *selector)["passages"]}
            old, new = "old" + suffix, "new" + suffix
            sid = pins[old]["source_id"]
            declaration = dict(schema="mousa.supersession_declaration.v1", source_id=sid,
                predecessor_item_id=old, predecessor_representation_id=pins[old]["representation_id"],
                successor_item_id=new, successor_representation_id=pins[new]["representation_id"],
                author="example.maintainer", basis="Explicit replacement, not inferred truth")
            declaration["id"] = identity(declaration["schema"], bytes.fromhex(sid), old,
                bytes.fromhex(declaration["predecessor_representation_id"]), new,
                bytes.fromhex(declaration["successor_representation_id"]), declaration["author"], declaration["basis"])
            command("supersession", "declaration", "put", value=json.dumps(declaration))
            activation = dict(schema="mousa.supersession_activation.v1", source_id=sid,
                expected_previous_activation_id=None, declaration_id=declaration["id"],
                actor_id="example.maintainer", actor_version="1", occurred_at_usec=1, reason="Explicit replacement")
            activation["id"] = identity(activation["schema"], bytes.fromhex(sid), b"", bytes.fromhex(declaration["id"]))
            command("supersession", "activation", "put", value=json.dumps(activation))
            associations = dict(schema="mousa.association_declarations.v1", associations=[
                dict(from_item="primary" + suffix, to_item=item + suffix,
                     basis="Caller-declared qualification, not semantic sufficiency", author="example.maintainer")
                for item in ("old", "twin", "support")])
            path = root / "associations.json"
            path.write_text(json.dumps(associations), encoding="utf-8")

            def check(result):
                assert [p["item"] for p in result["evidence"]] == ["primary" + suffix, "twin" + suffix, "support" + suffix]
                assert result["evidence"][0]["origin"] == "lexical"
                assert all(p["origin"] == "association" for p in result["evidence"][1:])
                assert result["association_omissions"][0]["reason"] == "superseded"
                assert result["association_omissions"][0]["segment_id"] == pins[old]["segment_id"]
                assert result["used_bytes"] == sum(len(p["text"].encode()) for p in result["evidence"])

            for packing in ("original", "exact-v1"):
                result = command("query", "--supersession", "--associations", path,
                                 "--packing-policy", packing, *selector, "instruction")
                check(result)
                history = command("trail", *selector, result["trail_id"])
                h = history["historical"]
                assert h["schema"] == "mousa.source_trail.v12"
                assert h["associated"][0]["omission"] == "superseded"
                assert h["supersession"]["activation_id"] == activation["id"]
                print(json.dumps(dict(surface="native-" + form, packing=packing, result=result, history=history)))
            if form == "jsonl":
                with (root / "stdio.jsonl").open("w", encoding="utf-8") as receipts:
                    client = Client(binary, store, receipts, False, 30, source)
                    try:
                        client.initialize()
                        for packing in ("original", "exact-v1"):
                            result, _ = client.call("mousa_query", dict(source=source, query="instruction", policy="original",
                                budget_bytes=8192, packing_policy=packing, supersession=True, associations=associations))
                            check(result)
                            history, _ = client.call("mousa_trail", dict(source=source, trail_id=result["trail_id"], supersession=True))
                            assert history["historical"]["schema"] == "mousa.source_trail.v12"
                            assert history["historical"]["associated"][0]["omission"] == "superseded"
                            print(json.dumps(dict(surface="mcp-jsonl", packing=packing, result=result, history=history)))
                    finally:
                        cleanup = client.close()
                        if cleanup["exit_code"] != 0 or cleanup["forced"] or cleanup["cleanup_errors"]:
                            raise RuntimeError(cleanup)


if __name__ == "__main__":
    main()
