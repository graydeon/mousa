#!/usr/bin/env python3
"""Prepare a caller-reviewed SQLite backup checklist from verified passages."""

import argparse
import hashlib
import json
from pathlib import Path
import sys
import tarfile
import time

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "docs"))
import docs

CASES = json.loads(Path(__file__).with_name("cases.json").read_text())


def require(condition, message):
    if not condition:
        raise ValueError(message)


def load_json(raw):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            require(key not in result, "duplicate JSON key: " + key)
            result[key] = value
        return result
    return json.loads(raw, object_pairs_hook=unique)


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def case_named(name):
    return next(case for case in CASES["cases"] if case["id"] == name)


def load_declarations(path):
    """Read one declared-association file; the consumer passes it through to the CLI."""
    if path is None:
        return None
    raw = path.read_bytes()
    declarations = {"sha256": digest(raw)}
    declarations.update(load_json(raw))
    require(declarations.get("schema") == "mousa.association_declarations.v1",
            "declaration file schema mismatch")
    entries = declarations.get("associations")
    require(isinstance(entries, list) and entries, "declaration file must declare associations")
    for entry in entries:
        require(set(entry) == {"from_item", "to_item", "basis", "author"}, "invalid declaration fields")
        require(entry["from_item"] != entry["to_item"], "declaration self reference")
        for field in ("basis", "author"):
            require(isinstance(entry[field], str) and entry[field].strip(), "declaration needs " + field)
    return declarations


def association_matches(hit, declarations):
    for entry in declarations["associations"]:
        if (hit["association"]["from_item"] == entry["from_item"]
                and hit["association"]["to_item"] == entry["to_item"]
                and hit["association"]["basis"] == entry["basis"]
                and hit["association"]["author"] == entry["author"]):
            return True
    return False


def verify_packet(packet, directory, declarations=None):
    manifest, documents, manifest_digest = docs.load_corpus(directory)
    expected = {key: manifest[key] for key in ("name", "version", "revision", "attribution")}
    expected["manifest_sha256"] = manifest_digest
    require(packet["corpus"] == expected, "packet corpus revision or manifest mismatch")
    require(packet["answer"] is None and packet["support"] == "not_assessed",
            "retrieval must remain separate from caller judgment")
    response = packet["response"]
    evidence = response["evidence"] or []
    require(not evidence or (response["decision_outcome"] == "allow" and response["outcome"] == "evidence"),
            "evidence without an allowed retrieval")
    references = {}
    used = 0
    for hit in evidence:
        require(hit["item"] in documents, "evidence outside the corpus")
        if hit.get("origin") == "association":
            require(declarations is not None and association_matches(hit, declarations),
                    "associated passage without a matching declared association")
        else:
            require("association" not in hit, "lexical passage carries association metadata")
        selected = docs.verify_evidence(hit, documents[hit["item"]])
        normalized = documents[hit["item"]].removeprefix(b"\xef\xbb\xbf").replace(b"\r\n", b"\n").replace(b"\r", b"\n")
        expected_location = {"path": hit["item"], "line_start": normalized[:hit["byte_start"]].count(b"\n") + 1,
                             "line_end": normalized[:hit["byte_end"] - 1].count(b"\n") + 1,
                             "url": manifest["files"][hit["item"]]["url"]}
        require(hit["location"] == expected_location, "passage location mismatch")
        key = (response["packet_id"], hit["segment_id"])
        require(key not in references, "duplicate evidence reference")
        references[key] = hit
        used += len(selected)
    require(type(response["budget_bytes"]) is int and 0 < response["budget_bytes"] <= 65536,
            "invalid packet byte budget")
    require(used == response["used_bytes"] and used <= response["budget_bytes"], "packet accounting mismatch")
    require(bool(evidence) == (response["outcome"] == "evidence"), "packet outcome mismatch")
    return references


def inspect(raw, directory, declarations=None):
    saved = load_json(raw)
    if "original" in saved:
        require(set(saved) == {"original", "assessment", "packet"}, "invalid follow-up file")
        original_raw = saved["original"].encode("utf-8")
        original = load_json(original_raw)
        require("original" not in original, "follow-up budget exhausted")
        prior = assess(original_raw, saved["assessment"], directory, declarations)
        require(prior["decision"] == "retrieve", "follow-up was not requested by the original assessment")
        choice = saved["assessment"]["next_retrieval"]
        require(saved["packet"]["response"]["query"] == choice["question"] and
                saved["packet"]["response"]["budget_bytes"] == choice["budget_bytes"],
                "follow-up differs from caller request")
        require(saved["packet"]["response"]["source"] == original["packet"]["response"]["source"],
                "follow-up source changed")
        packets = [original["packet"], saved["packet"]]
        original_wrapper = original
    else:
        original_wrapper = saved
        packets = [saved["packet"]]
    require(set(original_wrapper) in ({"case", "requirements", "packet"},
                                      {"case", "requirements", "packet", "associations_sha256"}),
            "invalid initial packet file")
    original = {"case": original_wrapper["case"], "requirements": original_wrapper["requirements"],
                "packet": original_wrapper["packet"]}
    if "associations_sha256" in original_wrapper:
        require(declarations is not None, "packet declares associations; pass --associations")
        require(original_wrapper["associations_sha256"] == declarations["sha256"],
                "declaration file does not match the saved packet")
    case = case_named(original["case"])
    requirements = {key: CASES["facts"][key] for key in case["facts"]}
    require(original["requirements"] == requirements, "task requirements changed")
    references = {}
    for packet in packets:
        for key, hit in verify_packet(packet, directory, declarations).items():
            require(key not in references or references[key] == hit, "same reference has inconsistent evidence")
            references[key] = hit
    return original, packets, references


def template(raw, directory, caller, declarations=None):
    original, _, _ = inspect(raw, directory, declarations)
    require(isinstance(caller, str) and caller.strip(), "caller identity is required")
    return {"packet_file_sha256": digest(raw), "caller": caller,
            "facts": [{"id": key, "judgment": "unassessed", "reason": "", "references": []}
                      for key in original["requirements"]], "next_retrieval": None}


def assess(raw, assessment, directory, declarations=None):
    require(set(assessment) == {"packet_file_sha256", "caller", "facts", "next_retrieval"},
            "unexpected or missing assessment fields")
    require(assessment["packet_file_sha256"] == digest(raw), "assessment does not bind this exact packet file")
    require(isinstance(assessment["caller"], str) and assessment["caller"].strip(), "caller identity is required")
    original, packets, references = inspect(raw, directory, declarations)
    coverage = {key: {"judgment": "unassessed", "reason": "No caller judgment supplied", "references": []}
                for key in original["requirements"]}
    require(isinstance(assessment["facts"], list), "facts must be a list")
    seen = set()
    for fact in assessment["facts"]:
        require(set(fact) == {"id", "judgment", "reason", "references"}, "invalid fact judgment fields")
        key = fact["id"]
        require(key in coverage and key not in seen, "unknown or duplicate required fact")
        seen.add(key)
        judgment = fact["judgment"]
        require(judgment in ("supported", "partial", "unsupported", "contradictory", "unassessed"), "unknown judgment")
        require(isinstance(fact["reason"], str) and (judgment == "unassessed" or fact["reason"].strip()),
                "assessed facts require a reason")
        require(isinstance(fact["references"], list), "references must be a list")
        require(judgment not in ("supported", "partial", "contradictory") or fact["references"],
                "this judgment requires evidence references")
        unique = set()
        for reference in fact["references"]:
            require(set(reference) == {"packet_id", "segment_id"}, "invalid evidence reference fields")
            identity = (reference["packet_id"], reference["segment_id"])
            require(identity in references, "reference absent from assessed packets")
            require(identity not in unique, "duplicate fact reference")
            unique.add(identity)
        coverage[key] = {field: fact[field] for field in ("judgment", "reason", "references")}
    unresolved = [key for key, fact in coverage.items() if fact["judgment"] != "supported"]
    choice = assessment["next_retrieval"]
    if choice is not None:
        require(set(choice) == {"fact", "question", "budget_bytes"}, "invalid follow-up request")
        require(choice["fact"] in unresolved, "follow-up must target an unresolved required fact")
        require(isinstance(choice["question"], str) and choice["question"].strip(), "follow-up query is required")
        require(type(choice["budget_bytes"]) is int and 0 < choice["budget_bytes"] <= 65536, "invalid follow-up budget")
    followups = len(packets) - 1
    decision = "covered" if not unresolved else "retrieve" if choice is not None and followups == 0 else "unresolved"
    return {"decision": decision, "caller": assessment["caller"], "coverage": coverage,
            "requirements": original["requirements"], "unresolved_facts": unresolved,
            "citation_consistency": "verified_against_saved_corpus", "semantic_support": "caller_assessed",
            "current_authorization": "not_checked", "current_source_state": "not_checked",
            "followup_count": followups, "followup_remaining": 1 - followups,
            "requested_retrieval": choice, "released_bytes": sum(p["response"]["used_bytes"] for p in packets),
            "original_query": original["packet"]["response"]["query"],
            "packet_file_sha256": digest(raw)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mousa", type=Path, default=Path("./mousa"))
    parser.add_argument("--store", type=Path, default=Path("backup.sqlite"))
    parser.add_argument("--directory", type=Path, required=True)
    parser.add_argument("--timeout", type=float, default=30)
    commands = parser.add_subparsers(dest="operation", required=True)
    commands.add_parser("prepare")
    commands.add_parser("sync")
    retrieve = commands.add_parser("retrieve")
    retrieve.add_argument("--case", choices=[case["id"] for case in CASES["cases"]], default="complete")
    retrieve.add_argument("--question", help="explicit initial query override")
    retrieve.add_argument("--budget-bytes", type=int)
    retrieve.add_argument("--associations", type=Path, help="declared-association JSON file for opt-in associated context")
    for name in ("template", "assess", "followup"):
        command = commands.add_parser(name)
        command.add_argument("--packet", type=Path, required=True)
        command.add_argument("--associations", type=Path, help="declaration file bound to the packet, for associated evidence")
        if name == "template":
            command.add_argument("--caller", required=True)
        else:
            command.add_argument("--assessment", type=Path, required=True)
    args = parser.parse_args()
    started = time.perf_counter_ns()
    try:
        require(args.timeout > 0, "timeout must be positive")
        directory = args.directory.absolute()
        declarations = load_declarations(getattr(args, "associations", None) or None)
        if args.operation == "prepare":
            result = docs.prepare(directory, Path(__file__).with_name("python-docs.tar.xz"))
        elif args.operation == "sync":
            result = docs.sync(args.mousa.resolve(strict=True), args.store, directory, args.timeout)
        elif args.operation == "retrieve":
            case = case_named(args.case)
            budget = case["budget"] if args.budget_bytes is None else args.budget_bytes
            require(0 < budget <= 65536, "byte budget must be between 1 and 65536")
            question = case["question"] if args.question is None else args.question
            require(question.strip(), "query is required")
            response = docs.ask(args.mousa.resolve(strict=True), args.store, directory, question, budget, args.timeout,
                                arguments=["--associations", str(args.associations.resolve(strict=True))] if declarations else [])
            result = {"case": args.case, "requirements": {key: CASES["facts"][key] for key in case["facts"]},
                      "packet": response}
            if declarations:
                result["associations_sha256"] = declarations["sha256"]
        else:
            raw = args.packet.read_bytes()
            if args.operation == "template":
                result = template(raw, directory, args.caller, declarations)
            else:
                assessment = load_json(args.assessment.read_bytes())
                result = assess(raw, assessment, directory, declarations)
                if args.operation == "followup":
                    require(result["decision"] == "retrieve", "no available caller-requested follow-up")
                    original, _, _ = inspect(raw, directory, declarations)
                    require(original["packet"]["response"]["source"] == str(directory), "follow-up requires the original source path")
                    choice = assessment["next_retrieval"]
                    response = docs.ask(args.mousa.resolve(strict=True), args.store, directory,
                                        choice["question"], choice["budget_bytes"], args.timeout,
                                        arguments=["--associations", str(args.associations.resolve(strict=True))] if declarations else [])
                    result = {"original": raw.decode("utf-8"), "assessment": assessment,
                              "packet": response}
                else:
                    result["elapsed_ms"] = (time.perf_counter_ns() - started) / 1e6
        print(json.dumps(result, ensure_ascii=False, indent=2))
        return 0
    except (OSError, ValueError, RuntimeError, KeyError, TypeError, StopIteration, tarfile.TarError) as error:
        print(json.dumps({"outcome": "error", "error": str(error)}), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
