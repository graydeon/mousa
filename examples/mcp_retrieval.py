"""Run native/MCP vector support; synthetic values prove protocol, not relevance."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile

from memory.memory import Client
from token_supersession import identity


FIXTURE = {"primary": "Drain sockets before shutdown.",
           "peer": "Drain surviving guidance.",
           "support": "Nonmatching declared qualification.",
           "old": "Obsolete declared qualification.",
           "twin": "Obsolete declared qualification.",
           "new": "Explicit replacement attribution."}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mousa", type=Path, required=True)
    parser.add_argument("--tokenizer-python", type=Path)
    parser.add_argument("--tokenizer-cache", type=Path)
    parser.add_argument("--supersession", action="store_true")
    args = parser.parse_args()
    if (args.tokenizer_python is None) != (args.tokenizer_cache is None):
        parser.error("provide both tokenizer options")
    binary = args.mousa.absolute()
    token_flags = [] if args.tokenizer_python is None else [
        "--context-tokens", "4096", "--tokenizer-python", str(args.tokenizer_python.absolute()),
        "--tokenizer-cache", str(args.tokenizer_cache.absolute())]

    def counted(result):
        if not token_flags:
            return
        reference = subprocess.run([str(args.tokenizer_python.absolute()), "-I", "-c",
            "import sys,tiktoken,tiktoken.load\n"
            "assert tiktoken.__version__ == '0.12.0'\n"
            "def offline(*args): raise RuntimeError('prepare offline cache first')\n"
            "tiktoken.load.read_file = offline\n"
            "print(len(tiktoken.get_encoding('o200k_base').encode_ordinary(sys.stdin.read())))"],
            input=result["context"], text=True, capture_output=True, check=True, timeout=30,
            env={"TIKTOKEN_CACHE_DIR": str(args.tokenizer_cache.absolute())})
        assert result["token"]["measured_tokens"] == int(reference.stdout)
        assert result["token"]["rendered_sha256"] == hashlib.sha256(result["context"].encode()).hexdigest()
        assert result["token"]["rendered_bytes"] == len(result["context"].encode())

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
                    client = Client(binary, store, receipts, True, 30, source,
                                    args.tokenizer_python, args.tokenizer_cache, supersession_admin=True)
                    client.initialize()
                    client.call("mousa_sync", dict(source=source,
                        items=[dict(id=k, text=v) for k, v in FIXTURE.items()]))
                    exported, _ = client.call("mousa_vector_inputs", dict(source=source))
                    current, _ = client.call("mousa_supersession_status", dict(source=source))
                    assert current["current"] == dict(state="no_history", activation=None, declaration=None)
                else:
                    if form == "directory":
                        directory.mkdir()
                        for item, text in FIXTURE.items():
                            (directory / (item + suffix)).write_text(text, encoding="utf-8")
                        command("sync", *selector)
                    else:
                        command("sync", *selector, value="".join(json.dumps(dict(id=k, text=v)) + "\n"
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
                old, new = "old" + suffix, "new" + suffix
                sid = pins[old]["source_id"]
                declaration = dict(schema="mousa.supersession_declaration.v1", source_id=sid,
                    predecessor_item_id=old, predecessor_representation_id=pins[old]["representation_id"],
                    successor_item_id=new, successor_representation_id=pins[new]["representation_id"],
                    author="example.operator", basis="Explicit replacement, not inferred truth")
                declaration["id"] = identity(declaration["schema"], bytes.fromhex(sid), old,
                    bytes.fromhex(declaration["predecessor_representation_id"]), new,
                    bytes.fromhex(declaration["successor_representation_id"]), declaration["author"], declaration["basis"])
                if client:
                    draft = {k: v for k, v in declaration.items() if k not in ("id", "source_id")}
                    draft["schema"] = "mousa.supersession_declaration_input.v1"
                    accepted, _ = client.call("mousa_supersession_declare", dict(source=source, declaration=draft))
                    assert accepted == dict(declaration=declaration)
                else:
                    command("supersession", "declaration", "put", value=json.dumps(declaration))
                activation = dict(schema="mousa.supersession_activation.v1", source_id=sid,
                    expected_previous_activation_id=None, declaration_id=declaration["id"],
                    actor_id="example.operator", actor_version="1", occurred_at_usec=1, reason="Explicit replacement")
                activation["id"] = identity(activation["schema"], bytes.fromhex(sid), b"", bytes.fromhex(declaration["id"]))
                if client:
                    draft = {k: v for k, v in activation.items() if k not in ("id", "source_id")}
                    draft["schema"] = "mousa.supersession_activation_input.v1"
                    accepted, _ = client.call("mousa_supersession_activate", dict(source=source, activation=draft))
                    assert accepted == dict(activation=activation)
                    current, _ = client.call("mousa_supersession_status", dict(source=source))
                    assert current["current"] == dict(state="active", activation=activation, declaration=declaration)
                else:
                    command("supersession", "activation", "put", value=json.dumps(activation))
                associations = dict(schema="mousa.association_declarations.v1", associations=[
                    dict(from_item="primary" + suffix, to_item=item + suffix,
                         basis="Caller-declared qualification", author="example.operator") for item in ("old", "twin", "support")])
                path = root / (form + "-associations.json")
                path.write_text(json.dumps(associations), encoding="utf-8")
                for mode in (("lexical", "vector", "hybrid") if client else ("vector", "hybrid")):
                    for packing in ("original", "exact-v1"):
                        def query(associated):
                            if client:
                                fields = dict(source=source, budget_bytes=8192,
                                    packing_policy=packing, supersession=args.supersession)
                                if mode != "lexical":
                                    fields.update(space_id=imported["space_id"], values=[1, 0])
                                if mode != "vector":
                                    fields.update(query="Drain", policy="original")
                                if token_flags:
                                    fields["context_tokens"] = 4096
                                if associated:
                                    fields["associations"] = associations
                                return client.call("mousa_query" if mode == "lexical" else "mousa_" + mode + "_query", fields)[0]
                            return command("query", "--" + mode + "-space", imported["space_id"],
                                "--packing-policy", packing, "--budget-bytes", "8192", *token_flags,
                                "--supersession=" + str(args.supersession).lower(),
                                *(["--associations", path] if associated else []), *selector,
                                *(["Drain"] if mode == "hybrid" else []),
                                value='{"schema":"mousa.vector_query.v1","values":[1,0]}')
                        baseline, result = query(False), query(True)
                        primary = [p for p in result["evidence"] if p.get("origin") != "association"]
                        assert primary == baseline["evidence"]
                        if mode in result:
                            assert result[mode] == baseline[mode]
                        elif mode != "lexical":
                            for field in ("eligible", "indexed", "missing", "query_vector_id"):
                                assert result[field] == baseline[field]
                        support = (result["associated_evidence"] if mode != "lexical" else
                                   [p for p in result["evidence"] if p.get("origin") == "association"])
                        expected = {"support" + suffix, "twin" + suffix}
                        if not args.supersession:
                            expected = {"support" + suffix, old}
                            if packing == "original":
                                expected.add("twin" + suffix)
                        assert {p["item"] for p in support} == expected
                        assert all(p["origin"] == "association" for p in support)
                        assert all(p["segment_id"] not in {e["segment_id"] for e in supplied["embeddings"]} for p in support)
                        assert result["used_bytes"] == sum(len(p["text"].encode()) for p in primary + support)
                        counted(baseline)
                        counted(result)
                        if client:
                            history = client.call("mousa_trail", dict(source=source, trail_id=result["trail_id"],
                                retrieval_details=True, token_details=bool(token_flags), supersession=args.supersession))[0]
                        else:
                            history = command("trail", *selector, result["trail_id"])
                        version = ("13" if token_flags else "12") if mode == "lexical" and args.supersession else (
                            ("11" if token_flags else "3") if mode == "lexical" else ("15" if token_flags else "14"))
                        assert history["historical"]["schema"] == "mousa.source_trail.v" + version
                        if mode in result:
                            assert history["historical"][mode] == result[mode]
                        elif mode != "lexical":
                            for field in ("eligible", "indexed", "query_vector_id"):
                                assert history["historical"][mode][field] == result[field]
                        if token_flags and not client:
                            recount = command("trail", "--verify-tokens", *token_flags[2:], *selector, result["trail_id"])
                            assert recount["historical"]["token_verification"] == "official_recount_verified"
                            assert recount["historical"]["token"] == result["token"]
                        print(json.dumps(dict(surface=form, mode=mode, packing=packing, result=result, history=history)))
                if client:
                    before = result
                    deactivation = dict(schema="mousa.supersession_activation_input.v1",
                        expected_previous_activation_id=activation["id"], declaration_id=None,
                        actor_id="example.operator", actor_version="1", occurred_at_usec=2, reason=None)
                    accepted, _ = client.call("mousa_supersession_activate", dict(source=source, activation=deactivation))
                    current, _ = client.call("mousa_supersession_status", dict(source=source))
                    assert current["current"] == dict(state="deactivated", activation=accepted["activation"], declaration=None)
                    after = query(True)
                    counted(after)
                    assert {p["item"] for p in after["associated_evidence"]} == {old, "support"}
                    retained, _ = client.call("mousa_trail", dict(source=source, trail_id=before["trail_id"],
                        retrieval_details=True, token_details=bool(token_flags), supersession=args.supersession))
                    assert retained["historical"] == history["historical"]
            finally:
                if client:
                    cleanup = client.close()
                    if cleanup["exit_code"] != 0 or cleanup["forced"] or cleanup["cleanup_errors"]:
                        raise RuntimeError(cleanup)
                receipts.close()


if __name__ == "__main__":
    main()
