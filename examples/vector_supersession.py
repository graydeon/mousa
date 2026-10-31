"""Compare byte-packed vector/hybrid evidence with explicit supersession."""
import argparse
import json
from pathlib import Path
import subprocess
import tempfile

from memory.memory import Client
from token_supersession import identity


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mousa", type=Path, required=True)
    args = parser.parse_args()
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
                    if opted:
                        argv += ["--supersession"]
                    argv += ["--source", source]
                    if mode == "hybrid":
                        argv += ["Drain"]
                    report = command(*argv, value='{"schema":"mousa.vector_query.v1","values":[1,0]}')
                    assert [p["item"] for p in report["evidence"]] == (["peer"] if opted else ["old"])
                    reports.append(report)
                history = command("trail", "--source", source, reports[1]["trail_id"])
                assert history["historical"]["schema"] == "mousa.source_trail.v9"
                assert history["historical"]["supersession"]["activation_id"] == activation["id"]
                print(json.dumps({"surface": "native", "mode": mode, "packing": packing,
                                  "ordinary": reports[0], "enforced": reports[1], "history": history}))
        with (Path(temporary) / "wire.jsonl").open("w") as receipts:
            client = Client(binary, store, receipts, False, 30, source)
            try:
                client.initialize()
                for mode in ("vector", "hybrid"):
                    for packing in ("original", "exact-v1"):
                        query = dict(source=source, space_id=imported["space_id"], values=[1, 0],
                                     budget_bytes=24, packing_policy=packing)
                        if mode == "hybrid":
                            query.update(query="Drain", policy="original")
                        ordinary, _ = client.call("mousa_" + mode + "_query", query)
                        enforced, _ = client.call("mousa_" + mode + "_query", dict(query, supersession=True))
                        assert [p["item"] for p in ordinary["evidence"]] == ["old"]
                        assert [p["item"] for p in enforced["evidence"]] == ["peer"]
                        assert enforced["supersession_excluded"] == 1
                        history, _ = client.call("mousa_trail", dict(source=source, trail_id=enforced["trail_id"],
                                                                  supersession=True, retrieval_details=True))
                        assert history["historical"][mode] == enforced[mode]
                        print(json.dumps({"surface": "mcp", "mode": mode, "packing": packing,
                                          "ordinary": ordinary, "enforced": enforced, "history": history}))
            finally:
                cleanup = client.close()
                if cleanup["exit_code"] != 0 or cleanup["forced"] or cleanup["cleanup_errors"]:
                    raise RuntimeError(cleanup)


if __name__ == "__main__":
    main()
