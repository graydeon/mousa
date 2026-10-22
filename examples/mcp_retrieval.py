"""Run synthetic MCP cosine/RRF queries with operator-prepared native embeddings."""
import argparse
import json
import hashlib
from pathlib import Path
import subprocess
import tempfile

from memory.memory import Client


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mousa", type=Path, required=True)
    parser.add_argument("--tokenizer-python", type=Path)
    parser.add_argument("--tokenizer-cache", type=Path)
    args = parser.parse_args()
    binary = args.mousa.resolve()
    if (args.tokenizer_python is None) != (args.tokenizer_cache is None):
        parser.error("provide both tokenizer options")
    source = "synthetic-mcp-retrieval"
    with tempfile.TemporaryDirectory(prefix="mousa-mcp-example-") as temporary:
        store = Path(temporary) / "evidence.sqlite"

        def native(*arguments, value=None):
            stdin = "" if value is None else json.dumps(value) + "\n"
            result = subprocess.run([str(binary), "-store", str(store), *arguments],
                                    input=stdin, text=True, capture_output=True,
                                    timeout=30, check=True)
            return json.loads(result.stdout)

        fixture = {"overlap.md": "Drain sockets before stopping the host.",
                   "vector.md": "Radishes prefer damp soil.", "lexical.md": "Drain sockets."}
        subprocess.run([str(binary), "-store", str(store), "sync", "--source", source],
                       input="".join(json.dumps({"id": k, "text": v}) + "\n" for k, v in fixture.items()),
                       text=True, capture_output=True, timeout=30, check=True)
        passages = native("vector", "inputs", "--source", source)["passages"]
        # Only this synthetic fixture assigns vectors by hand. Real producers run
        # externally over the exported current passage bytes and retain their pins.
        pins = ("source_id", "segment_id", "representation_id", "representation_sha256", "content_sha256")
        imported = native("vector", "import", "--source", source, value={
            "schema": "mousa.vector_import.v1",
            "space": {"schema": "mousa.vector_space.v1", "provider": "synthetic",
                      "model": "two-axis-example", "version": "1", "dimensions": 2, "metric": "cosine"},
            "embeddings": [{**{key: p[key] for key in pins},
                            "values": [1, 0] if p["item"] == "vector.md" else [0, 1]}
                           for p in passages if p["item"] != "lexical.md"]})
        with (Path(temporary) / "receipts.jsonl").open("w") as receipts:
            client = Client(binary, store, receipts, False, 30, source,
                            args.tokenizer_python, args.tokenizer_cache)
            try:
                client.initialize()
                modes = ("lexical", "vector", "hybrid") if args.tokenizer_python else ("vector", "hybrid")
                for mode in modes:
                    arguments = {"source": source, "space_id": imported["space_id"],
                                 "values": [1, 0], "budget_bytes": 40}
                    if mode == "hybrid":
                        arguments.update(query="Drain", policy="original")
                    tool = "mousa_" + mode + "_query"
                    if mode == "lexical":
                        arguments = {"source": source, "query": "Drain", "policy": "original", "budget_bytes": 40}
                        tool = "mousa_query"
                    if args.tokenizer_python:
                        arguments.update(context_tokens=256, window_tokens=512,
                                         prompt_reserve_tokens=100, output_reserve_tokens=156)
                    result, _ = client.call(tool, arguments)
                    expected = {"lexical": "lexical.md", "vector": "vector.md", "hybrid": "overlap.md"}[mode]
                    assert [p["item"] for p in result["evidence"]] == [expected]
                    if args.tokenizer_python:
                        text = result["context"]
                        reference = subprocess.run(
                            [str(args.tokenizer_python), "-I", "-c",
                             "import sys,tiktoken; print(len(tiktoken.get_encoding('o200k_base').encode_ordinary(sys.stdin.read())))"],
                            input=text, text=True, capture_output=True, timeout=30, check=True,
                            env={"TIKTOKEN_CACHE_DIR": str(args.tokenizer_cache)})
                        count = int(reference.stdout)
                        assert result["token"]["measured_tokens"] == count <= 256
                        assert result["token"]["rendered_sha256"] == hashlib.sha256(text.encode()).hexdigest()
                        boundary = dict(arguments, context_tokens=count)
                        exact, _ = client.call(tool, boundary)
                        assert exact["context"] == text
                        short, _ = client.call(tool, dict(boundary, context_tokens=count - 1))
                        assert short["context"] != text and short["token"]["measured_tokens"] <= count - 1
                    history, _ = client.call("mousa_trail", {"source": source,
                        "trail_id": result["trail_id"], "retrieval_details": True,
                        "token_details": bool(args.tokenizer_python)})
                    if mode != "lexical":
                        assert history["historical"][mode] == result[mode]
                    if args.tokenizer_python:
                        assert history["historical"]["token"] == result["token"]
                    assert history["historical"]["packet_id"] == result["packet_id"]
                    print(json.dumps({"mode": mode, "query_result": result, "inspection": history}))
            finally:
                cleanup = client.close()
                if cleanup["exit_code"] != 0 or cleanup["forced"] or cleanup["cleanup_errors"]:
                    raise RuntimeError(cleanup)


if __name__ == "__main__":
    main()
