"""Compare vector/hybrid evidence under byte and optional measured token budgets."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile

from memory.memory import Client
from token_supersession import identity


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mousa", type=Path, required=True)
    parser.add_argument("--tokenizer-python", type=Path)
    parser.add_argument("--tokenizer-cache", type=Path)
    args = parser.parse_args()
    if (args.tokenizer_python is None) != (args.tokenizer_cache is None):
        parser.error("configure both tokenizer interpreter and prepared cache")
    token_args = []
    if args.tokenizer_python is not None:
        token_args = ["--context-tokens", "4096", "--tokenizer-python",
                      str(args.tokenizer_python.absolute()), "--tokenizer-cache",
                      str(args.tokenizer_cache.resolve())]

    def verify_tokens(report):
        if not token_args:
            return
        context = report["context"]
        result = subprocess.run(
            [str(args.tokenizer_python.absolute()), "-I", "-c",
             "import sys,tiktoken,tiktoken.load\n"
             "def offline(*args, **kwargs): raise RuntimeError('prepare the offline cache first')\n"
             "tiktoken.load.read_file=offline\n"
             "print(len(tiktoken.get_encoding('o200k_base').encode_ordinary(sys.stdin.read())))"],
            input=context, text=True, capture_output=True, timeout=30, check=True,
            env=dict(os.environ, TIKTOKEN_CACHE_DIR=str(args.tokenizer_cache.resolve())))
        assert report["token"]["measured_tokens"] == int(result.stdout)
        assert report["token"]["measured_tokens"] <= report["token"]["limit"]
        assert report["token"]["rendered_sha256"] == hashlib.sha256(context.encode()).hexdigest()
        assert report["token"]["rendered_bytes"] == len(context.encode())

    binary = args.mousa.resolve()
    source = "synthetic-vector-supersession"
    with tempfile.TemporaryDirectory(prefix="mousa-vector-supersession-") as temporary:
        store = Path(temporary) / "evidence.sqlite"

        def command(*argv, value=None):
            result = subprocess.run([str(binary), "-store", str(store), *map(str, argv)],
                                    input=value, text=True, capture_output=True, timeout=30, check=True)
            return json.loads(result.stdout)

        command("sync", "--source", source, value='{"id":"old","text":"Drain predecessor"}\n'
                '{"id":"new","text":"Separate successor"}\n'
                '{"id":"peer","text":"Drain surviving guidance"}\n')
        exported = command("vector", "inputs", "--source", source)
        pins = {p["item"]: p for p in exported["passages"]}
        # A real producer supplies values externally over the exported bytes.
        # Deliberately omit the successor: its vector coverage cannot gate withholding.
        fields = ("source_id", "segment_id", "representation_id", "representation_sha256", "content_sha256")
        supplied = {"schema": "mousa.vector_import.v1",
                    "space": {"schema": "mousa.vector_space.v1", "provider": "synthetic",
                              "model": "arithmetic-only", "version": "1", "dimensions": 2, "metric": "cosine"},
                    "embeddings": [{**{k: pins[item][k] for k in fields}, "values": values}
                                   for item, values in (("old", [1, 0]), ("peer", [0, 1]))]}
        imported = command("vector", "import", "--source", source, value=json.dumps(supplied))
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
        for mode in ("vector", "hybrid"):
            for packing in ("original", "exact-v1"):
                reports = []
                for opted in (False, True):
                    argv = ["query", "--" + mode + "-space", imported["space_id"],
                            "--packing-policy", packing, "--budget-bytes", "24"]
                    argv += token_args
                    if opted:
                        argv += ["--supersession"]
                    argv += ["--source", source]
                    if mode == "hybrid":
                        argv += ["Drain"]
                    report = command(*argv, value='{"schema":"mousa.vector_query.v1","values":[1,0]}')
                    expected = "peer" if opted else "old"
                    if token_args:
                        assert [p["segment"]["id"] for p in report["evidence"]] == [pins[expected]["segment_id"]]
                    else:
                        assert [p["item"] for p in report["evidence"]] == [expected]
                    verify_tokens(report)
                    reports.append(report)
                history = command("trail", "--source", source, reports[1]["trail_id"])
                assert history["historical"]["schema"] == ("mousa.source_trail.v10" if token_args else "mousa.source_trail.v9")
                assert history["historical"]["supersession"]["activation_id"] == activation["id"]
                if token_args:
                    recounted = command("trail", "--verify-tokens", "--tokenizer-python",
                                        args.tokenizer_python.absolute(), "--tokenizer-cache",
                                        args.tokenizer_cache.resolve(), "--source", source,
                                        reports[1]["trail_id"])
                    assert recounted["historical"]["token_verification"] == "official_recount_verified"
                    count = reports[1]["token"]["measured_tokens"]
                    cap_index = argv.index("--context-tokens") + 1
                    argv[cap_index] = str(count)
                    exact = command(*argv, value='{"schema":"mousa.vector_query.v1","values":[1,0]}')
                    assert exact["context"] == reports[1]["context"]
                    argv[cap_index] = str(count - 1)
                    short = command(*argv, value='{"schema":"mousa.vector_query.v1","values":[1,0]}')
                    assert short["context"] == "" and short["outcome"] == "budget_omitted"
                    verify_tokens(short)
                print(json.dumps({"surface": "native", "mode": mode, "packing": packing,
                                  "ordinary": reports[0], "enforced": reports[1], "history": history}))
        with (Path(temporary) / "wire.jsonl").open("w") as receipts:
            client = Client(binary, store, receipts, False, 30, source,
                            tokenizer_python=args.tokenizer_python, tokenizer_cache=args.tokenizer_cache)
            try:
                client.initialize()
                for mode in ("vector", "hybrid"):
                    for packing in ("original", "exact-v1"):
                        query = dict(source=source, space_id=imported["space_id"], values=[1, 0],
                                     budget_bytes=24, packing_policy=packing)
                        if mode == "hybrid":
                            query.update(query="Drain", policy="original")
                        if token_args:
                            query["context_tokens"] = 4096
                        ordinary, _ = client.call("mousa_" + mode + "_query", query)
                        enforced, _ = client.call("mousa_" + mode + "_query", dict(query, supersession=True))
                        assert [p["item"] for p in ordinary["evidence"]] == ["old"]
                        assert [p["item"] for p in enforced["evidence"]] == ["peer"]
                        verify_tokens(ordinary)
                        verify_tokens(enforced)
                        assert enforced["supersession_excluded"] == 1
                        history, _ = client.call("mousa_trail", dict(source=source, trail_id=enforced["trail_id"],
                                                                  supersession=True, retrieval_details=True,
                                                                  token_details=bool(token_args)))
                        assert history["historical"][mode] == enforced[mode]
                        print(json.dumps({"surface": "mcp", "mode": mode, "packing": packing,
                                          "ordinary": ordinary, "enforced": enforced, "history": history}))
            finally:
                cleanup = client.close()
                if cleanup["exit_code"] != 0 or cleanup["forced"] or cleanup["cleanup_errors"]:
                    raise RuntimeError(cleanup)


if __name__ == "__main__":
    main()
