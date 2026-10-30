"""Run lexical supersession plus whole-context token budgets through native and MCP clients."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile

from memory.memory import Client


def identity(*fields):
    digest = hashlib.sha256()
    for field in fields:
        value = field.encode() if isinstance(field, str) else field
        digest.update(len(value).to_bytes(8, "big"))
        digest.update(value)
    return digest.hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mousa", type=Path, required=True)
    parser.add_argument("--tokenizer-python", type=Path, required=True)
    parser.add_argument("--tokenizer-cache", type=Path, required=True)
    args = parser.parse_args()
    binary, python, cache = (p.absolute() for p in (args.mousa, args.tokenizer_python, args.tokenizer_cache))
    source = "synthetic-token-supersession"
    with tempfile.TemporaryDirectory(prefix="mousa-combined-") as temporary:
        store = Path(temporary) / "evidence.sqlite"

        def command(*argv, value=None):
            result = subprocess.run([str(binary), "-store", str(store), *map(str, argv)],
                input=value, text=True, capture_output=True, timeout=30, check=True)
            return json.loads(result.stdout)

        command("sync", "--source", source, value='{"id":"old","text":"Drain predecessor"}\n'
                '{"id":"new","text":"Drain successor"}\n')
        exported = command("vector", "inputs", "--source", source)
        pins = {p["item"]: p for p in exported["passages"]}
        sid = pins["old"]["source_id"]
        declaration = dict(schema="mousa.supersession_declaration.v1", source_id=sid,
            predecessor_item_id="old", predecessor_representation_id=pins["old"]["representation_id"],
            successor_item_id="new", successor_representation_id=pins["new"]["representation_id"],
            author="example.operator", basis="new explicitly replaces old")
        declaration["id"] = identity(declaration["schema"], bytes.fromhex(sid), "old",
            bytes.fromhex(declaration["predecessor_representation_id"]), "new",
            bytes.fromhex(declaration["successor_representation_id"]), declaration["author"], declaration["basis"])
        command("supersession", "declaration", "put", value=json.dumps(declaration))
        activation = dict(schema="mousa.supersession_activation.v1", source_id=sid,
            expected_previous_activation_id=None, declaration_id=declaration["id"],
            actor_id="example.operator", actor_version="1", occurred_at_usec=1, reason="explicit replacement")
        activation["id"] = identity(activation["schema"], bytes.fromhex(sid), b"", bytes.fromhex(declaration["id"]))
        command("supersession", "activation", "put", value=json.dumps(activation))

        def reference(result):
            counted = subprocess.run([str(python), "-I", "-c",
                "import sys,tiktoken;print(len(tiktoken.get_encoding('o200k_base').encode_ordinary(sys.stdin.read())))"],
                input=result["context"], text=True, capture_output=True, check=True, timeout=30,
                env={"TIKTOKEN_CACHE_DIR": str(cache)})
            count = int(counted.stdout)
            assert result["token"]["measured_tokens"] == count <= result["token"]["limit"]
            assert result["token"]["rendered_sha256"] == hashlib.sha256(result["context"].encode()).hexdigest()
            assert result["supersession_excluded"] == 1
            assert [p["text"] for p in result["evidence"]] == ["Drain successor"]
            return count

        for packing in ("original", "exact-v1"):
            native = command("query", "--supersession", "--context-tokens", "4096",
                "--packing-policy", packing, "--tokenizer-python", python, "--tokenizer-cache", cache,
                "--source", source, "Drain")
            count = reference(native)
            print(json.dumps({"surface": "native", "packing": packing, "reference_tokens": count, "result": native}))
        with (Path(temporary) / "receipts.jsonl").open("w") as receipts:
            client = Client(binary, store, receipts, False, 30, source, python, cache)
            try:
                client.initialize()
                for packing in ("original", "exact-v1"):
                    query = dict(source=source, query="Drain", policy="original", budget_bytes=4096,
                                 context_tokens=4096, supersession=True, packing_policy=packing)
                    result, _ = client.call("mousa_query", query)
                    count = reference(result)
                    exact, _ = client.call("mousa_query", dict(query, context_tokens=count))
                    assert exact["context"] == result["context"]
                    history, _ = client.call("mousa_trail", dict(source=source, trail_id=result["trail_id"],
                                                                 supersession=True, token_details=True))
                    h = history["historical"]
                    assert h["schema"] == "mousa.source_trail.v8" and h["token"] == result["token"]
                    assert h["supersession"]["activation_id"] == activation["id"]
                    print(json.dumps({"surface": "mcp", "packing": packing, "reference_tokens": count,
                                      "result": result, "history": history}))
            finally:
                cleanup = client.close()
                if cleanup["exit_code"] != 0 or cleanup["forced"] or cleanup["cleanup_errors"]:
                    raise RuntimeError(cleanup)


if __name__ == "__main__":
    main()
