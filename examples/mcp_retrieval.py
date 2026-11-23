"""Run native/MCP vector support; synthetic values prove protocol, not relevance."""
import argparse
import json
from pathlib import Path
import subprocess
import tempfile

from memory.memory import Client


FIXTURE = {"primary": "Drain sockets before shutdown.",
           "peer": "Drain surviving guidance."}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mousa", type=Path, required=True)
    args = parser.parse_args()
    binary = args.mousa.absolute()
    with tempfile.TemporaryDirectory(prefix="mousa-retrieval-support-") as temporary:
        root = Path(temporary)
        for form in ("directory", "jsonl", "mcp"):
            store = root / (form + ".sqlite")
            source = "synthetic-retrieval-support"
            suffix = ".txt" if form == "directory" else ""
            directory = root / "notes"
            selector = [str(directory)] if form == "directory" else ["--source", source]

            def command(*argv, value=None):
                result = subprocess.run([str(binary), "-store", str(store), *map(str, argv)],
                    input=value, text=True, capture_output=True, timeout=30, check=True)
                return json.loads(result.stdout)

            receipts = (root / (form + "-wire.jsonl")).open("w", encoding="utf-8")
            client = None
            try:
                if form == "mcp":
                    client = Client(binary, store, receipts, True, 30, source)
                    client.initialize()
                    client.call("mousa_sync", dict(source=source,
                        items=[dict(id=k, text=v) for k, v in FIXTURE.items()]))
                    command("access", "--source", source, "allow")
                    exported, _ = client.call("mousa_vector_inputs", dict(source=source))
                else:
                    if form == "directory":
                        directory.mkdir()
                        for item, text in FIXTURE.items():
                            (directory / (item + suffix)).write_text(text, encoding="utf-8")
                        command("sync", "--allow", *selector)
                    else:
                        command("sync", "--allow", *selector, value="".join(json.dumps(dict(id=k, text=v)) + "\n"
                            for k, v in FIXTURE.items()))
                    exported = command("vector", "inputs", *selector)
                pins = {p["item"]: p for p in exported["passages"]}
                fields = ("source_id", "segment_id", "representation_id", "representation_sha256", "content_sha256")
                supplied = dict(schema="mousa.vector_import.v1", space=dict(schema="mousa.vector_space.v1",
                    provider="synthetic", model="arithmetic-only", version="1", dimensions=2, metric="cosine"),
                    embeddings=[dict(**{k: pins[item + suffix][k] for k in fields}, values=values)
                        for item, values in (("primary", [1, 0]), ("peer", [0, 1]))])
                if client:
                    imported, _ = client.call("mousa_vector_import", dict(source=source, **{"import": supplied}))
                else:
                    imported = command("vector", "import", *selector, value=json.dumps(supplied))
                for mode in ("lexical", "vector", "hybrid") if client else ("vector", "hybrid"):
                    for packing in ("original", "exact-v1"):
                        if client:
                            fields = dict(source=source, budget_bytes=8192, packing_policy=packing)
                            if mode != "lexical":
                                fields.update(space_id=imported["space_id"], values=[1, 0])
                            if mode != "vector":
                                fields.update(query="Drain", policy="original")
                            result = client.call("mousa_query" if mode == "lexical" else "mousa_" + mode + "_query", fields)[0]
                        else:
                            result = command("query", "--" + mode + "-space", imported["space_id"],
                                "--packing-policy", packing, "--budget-bytes", "8192", *selector,
                                *(["Drain"] if mode == "hybrid" else []),
                                value='{"schema":"mousa.vector_query.v1","values":[1,0]}')
                        assert result["evidence"]
                        assert result["used_bytes"] == sum(len(p["text"].encode()) for p in result["evidence"])
                        if client:
                            history = client.call("mousa_trail", dict(source=source, trail_id=result["trail_id"],
                                retrieval_details=True))[0]
                        else:
                            history = command("trail", *selector, result["trail_id"])
                        if mode in result:
                            assert history["historical"][mode] == result[mode]
                        elif mode != "lexical":
                            for field in ("eligible", "indexed", "query_vector_id"):
                                assert history["historical"][mode][field] == result[field]
                        print(json.dumps(dict(surface=form, mode=mode, packing=packing, result=result, history=history)))
            finally:
                if client:
                    cleanup = client.close()
                    if cleanup["exit_code"] != 0 or cleanup["forced"] or cleanup["cleanup_errors"]:
                        raise RuntimeError(cleanup)
                receipts.close()


if __name__ == "__main__":
    main()
