#!/usr/bin/env python3
"""Run explicit replacement and declared support with byte and exact token budgets."""
import argparse
import hashlib
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
    parser.add_argument("--python", type=Path, required=True)
    parser.add_argument("--cache", type=Path, required=True)
    args = parser.parse_args()
    binary = args.mousa.absolute()
    python, cache = args.python.absolute(), args.cache.absolute()
    token_flags = ["--tokenizer-python", python, "--tokenizer-cache", cache]

    def reference(result):
        counted = subprocess.run([str(python), "-I", "-c",
            "import sys,tiktoken,tiktoken.load\n"
            "assert tiktoken.__version__ == '0.12.0'\n"
            "def offline(*args):\n"
            "    raise RuntimeError('prepare tokenizer cache before running this example')\n"
            "tiktoken.load.read_file = offline\n"
            "print(len(tiktoken.get_encoding('o200k_base').encode_ordinary(sys.stdin.read())))"],
            input=result["context"], text=True, capture_output=True, check=True, timeout=30,
            env={"TIKTOKEN_CACHE_DIR": str(cache)})
        count = int(counted.stdout)
        assert result["token"]["measured_tokens"] == count <= result["token"]["limit"]
        assert result["token"]["rendered_sha256"] == hashlib.sha256(result["context"].encode()).hexdigest()
        assert result["token"]["rendered_bytes"] == len(result["context"].encode())
        return count
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
                flags = ["--associations", path, "--packing-policy", packing, *token_flags]
                plain = command("query", *flags, "--context-tokens", "4096", *selector, "needle")
                suppressed = command("query", *flags, "--supersession",
                                     "--context-tokens", "4096", *selector, "needle")
                reference(plain)
                reference(suppressed)
                assert any(p["item"] == old for p in plain["evidence"])
                assert all(p["item"] != old for p in suppressed["evidence"])
                assert suppressed["supersession_excluded"] == 1
                full = command("query", *flags, "--supersession",
                               "--context-tokens", "4096", *selector, "instruction")
                check(full)
                count = reference(full)
                exact = command("query", *flags, "--supersession",
                                "--context-tokens", count, *selector, "instruction")
                assert exact["context"] == full["context"]
                reference(exact)
                short = command("query", *flags, "--supersession",
                                "--context-tokens", count - 1, *selector, "instruction")
                reference(short)
                assert len(short["evidence"]) < len(full["evidence"])
                for recount in (False, True):
                    history = command("trail", *(["--verify-tokens", *token_flags] if recount else []),
                                      *selector, exact["trail_id"])
                    h = history["historical"]
                    assert h["schema"] == "mousa.source_trail.v13"
                    assert h["token"] == exact["token"]
                    assert h["associated"][0]["omission"] == "superseded"
                    assert h["token_verification"] == ("official_recount_verified" if recount else
                                                       "retained_digest_verified_count_recorded")
                    print(json.dumps(dict(surface="native-" + form, packing=packing,
                        reference_tokens=count, recount=recount, result=exact, history=history)))
            if form == "jsonl":
                with (root / "stdio.jsonl").open("w", encoding="utf-8") as receipts:
                    client = Client(binary, store, receipts, False, 30, source, python, cache)
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
                            query = dict(source=source, query="instruction", policy="original",
                                budget_bytes=8192, packing_policy=packing, supersession=True,
                                associations=associations, context_tokens=4096)
                            full, _ = client.call("mousa_query", query)
                            check(full)
                            count = reference(full)
                            exact, _ = client.call("mousa_query", dict(query, context_tokens=count))
                            assert exact["context"] == full["context"]
                            reference(exact)
                            history, _ = client.call("mousa_trail", dict(source=source,
                                trail_id=exact["trail_id"], supersession=True, token_details=True))
                            assert history["historical"]["schema"] == "mousa.source_trail.v13"
                            assert history["historical"]["token"] == exact["token"]
                            print(json.dumps(dict(surface="mcp-jsonl", packing=packing,
                                reference_tokens=count, result=exact, history=history)))
                    finally:
                        cleanup = client.close()
                        if cleanup["exit_code"] != 0 or cleanup["forced"] or cleanup["cleanup_errors"]:
                            raise RuntimeError(cleanup)


if __name__ == "__main__":
    main()
