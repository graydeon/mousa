"""Actual-CLI boundaries for caller-reviewed backup decisions."""

import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "docs"))
import docs


class BackupConsumerTest(unittest.TestCase):
    binary = None

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.directory = self.root / "corpus"
        self.store = self.root / "store.sqlite"
        self.program = Path(__file__).with_name("backup.py")
        self.directory.mkdir()
        texts = {
            "backup.rst": "Original fixture: backup pages progress concurrency.\n",
            "context.rst": "Original fixture: context manager never closes the connection.\n",
        }
        manifest = {"name": "Original boundary fixture", "version": "one", "revision": "fixture-one",
                    "attribution": "Original deterministic test text, not Python documentation",
                    "documents": list(texts), "files": {}}
        for name, text in texts.items():
            content = text.encode()
            (self.directory / name).write_bytes(content)
            manifest["files"][name] = {"sha256": hashlib.sha256(content).hexdigest(), "url": None}
        (self.directory / "corpus.json").write_text(json.dumps(manifest))
        self.call("sync")
        self.packet = self.root / "packet.json"
        self.packet.write_text(json.dumps(self.call("retrieve", "--case", "complete")))

    def call(self, *args, expected=0):
        code, stdout, stderr = docs.run_process(
            [sys.executable, str(self.program), "--mousa", str(self.binary),
             "--store", str(self.store), "--directory", str(self.directory), *args],
            30, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        self.assertEqual(code, expected, stderr + stdout)
        return json.loads(stdout if code == 0 else stderr)

    def assessment(self):
        value = self.call("template", "--packet", str(self.packet), "--caller", "test reviewer")
        path = self.root / "assessment.json"
        path.write_text(json.dumps(value))
        return value, path

    def test_incomplete_is_unresolved_and_references_are_packet_bound(self):
        value, path = self.assessment()
        result = self.call("assess", "--packet", str(self.packet), "--assessment", str(path))
        self.assertEqual(result["decision"], "unresolved")
        self.assertEqual(set(result["unresolved_facts"]), {"concurrency", "pages", "progress", "closure"})
        value["facts"] = [{"id": "concurrency", "judgment": "supported", "reason": "Claimed support",
                           "references": [{"packet_id": "0" * 64, "segment_id": "1" * 64}]}]
        path.write_text(json.dumps(value))
        self.call("assess", "--packet", str(self.packet), "--assessment", str(path), expected=1)

    def test_changed_packet_revision_digest_and_range_refuse(self):
        value, path = self.assessment()
        original = self.packet.read_bytes()
        for field in ("revision", "packet_id", "representation_sha256", "byte_start"):
            with self.subTest(field=field):
                packet = json.loads(original)
                response = packet["packet"]["response"]
                if field == "revision":
                    packet["packet"]["corpus"][field] = "other revision"
                elif field == "packet_id":
                    response[field] = "0" * 64
                else:
                    response["evidence"][0][field] = 1 if field == "byte_start" else "0" * 64
                self.packet.write_text(json.dumps(packet))
                self.call("assess", "--packet", str(self.packet), "--assessment", str(path), expected=1)
                if field != "packet_id":
                    value["packet_file_sha256"] = hashlib.sha256(self.packet.read_bytes()).hexdigest()
                    path.write_text(json.dumps(value))
                    self.call("assess", "--packet", str(self.packet), "--assessment", str(path), expected=1)
                self.packet.write_bytes(original)
                value["packet_file_sha256"] = hashlib.sha256(original).hexdigest()
                path.write_text(json.dumps(value))

    def test_unsupported_contradictory_and_unassessed_are_visible(self):
        value, path = self.assessment()
        evidence = json.loads(self.packet.read_bytes())["packet"]["response"]
        reference = {"packet_id": evidence["packet_id"], "segment_id": evidence["evidence"][0]["segment_id"]}
        value["facts"] = [
            {"id": "concurrency", "judgment": "unsupported", "references": [], "reason": "No support asserted"},
            {"id": "pages", "judgment": "contradictory", "references": [reference], "reason": "Caller disputes applicability"}]
        path.write_text(json.dumps(value))
        result = self.call("assess", "--packet", str(self.packet), "--assessment", str(path))
        self.assertEqual(result["decision"], "unresolved")
        self.assertEqual(result["coverage"]["pages"]["judgment"], "contradictory")
        self.assertEqual(result["coverage"]["progress"]["judgment"], "unassessed")
        value["facts"][0]["judgment"] = "supported"
        path.write_text(json.dumps(value))
        self.call("assess", "--packet", str(self.packet), "--assessment", str(path), expected=1)

    def test_followup_retains_original_and_stops_after_one_attempt(self):
        value, path = self.assessment()
        value["next_retrieval"] = {"fact": "closure", "question": "context manager neither closes connection", "budget_bytes": 4096}
        path.write_text(json.dumps(value))
        original = self.packet.read_text()
        result = self.call("assess", "--packet", str(self.packet), "--assessment", str(path))
        self.assertEqual(result["decision"], "retrieve")
        followup = self.call("followup", "--packet", str(self.packet), "--assessment", str(path))
        self.assertEqual(followup["original"], original)
        self.assertEqual(followup["packet"]["response"]["query"], value["next_retrieval"]["question"])
        self.packet.write_text(json.dumps(followup))
        value, path = self.assessment()
        value["next_retrieval"] = {"fact": "closure", "question": "close", "budget_bytes": 4096}
        path.write_text(json.dumps(value))
        result = self.call("assess", "--packet", str(self.packet), "--assessment", str(path))
        self.assertEqual(result["decision"], "unresolved")
        self.assertEqual(result["followup_count"], 1)
        self.call("followup", "--packet", str(self.packet), "--assessment", str(path), expected=1)

    def test_saved_evidence_survives_update_denial_and_withdrawal(self):
        value, path = self.assessment()
        old_directory = self.directory
        saved = self.root / "saved-corpus"
        shutil.copytree(old_directory, saved)
        manifest = json.loads((old_directory / "corpus.json").read_bytes())
        replacement = b"Backup procedure replaced. Consult the new operations manual.\n"
        (old_directory / "backup.rst").write_bytes(replacement)
        manifest["files"]["backup.rst"]["sha256"] = hashlib.sha256(replacement).hexdigest()
        manifest["revision"] = "original test revision two"
        (old_directory / "corpus.json").write_text(json.dumps(manifest))
        self.call("assess", "--packet", str(self.packet), "--assessment", str(path), expected=1)
        self.call("sync")
        current = self.call("retrieve", "--case", "complete")
        self.assertNotEqual(current["packet"]["response"]["packet_id"], json.loads(self.packet.read_bytes())["packet"]["response"]["packet_id"])
        for operation in ("deny", "withdraw"):
            if operation == "deny":
                docs.invoke(self.binary, self.store, ["access", str(old_directory), "deny"], 30)
            else:
                docs.invoke(self.binary, self.store, ["access", str(old_directory), "allow"], 30)
                docs.invoke(self.binary, self.store, ["withdraw", str(old_directory)], 30)
            denied = self.call("retrieve", "--case", "complete")
            self.assertFalse(denied["packet"]["response"]["evidence"])
            self.directory = saved
            result = self.call("assess", "--packet", str(self.packet), "--assessment", str(path))
            self.assertEqual(result["citation_consistency"], "verified_against_saved_corpus")
            self.assertEqual(result["current_authorization"], "not_checked")
            self.directory = old_directory

    def test_equal_text_does_not_transfer_source_references(self):
        peer = self.root / "peer"
        shutil.copytree(self.directory, peer)
        docs.sync(self.binary, self.store, peer, 30)
        other = docs.ask(self.binary, self.store, peer, "backup pages progress context manager closes connection", 8192, 30)
        local = json.loads(self.packet.read_bytes())["packet"]["response"]
        self.assertEqual({h["text"] for h in local["evidence"]}, {h["text"] for h in other["response"]["evidence"]})
        self.assertTrue({h["segment_id"] for h in local["evidence"]}.isdisjoint({h["segment_id"] for h in other["response"]["evidence"]}))
        value, path = self.assessment()
        value["facts"] = [{"id": "pages", "judgment": "supported", "reason": "Wrong provenance",
                           "references": [{"packet_id": local["packet_id"], "segment_id": other["response"]["evidence"][0]["segment_id"]}]}]
        path.write_text(json.dumps(value))
        self.call("assess", "--packet", str(self.packet), "--assessment", str(path), expected=1)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mousa", type=Path, required=True)
    args = parser.parse_args()
    BackupConsumerTest.binary = args.mousa.resolve(strict=True)
    result = unittest.TextTestRunner(verbosity=2).run(unittest.defaultTestLoader.loadTestsFromTestCase(BackupConsumerTest))
    raise SystemExit(not result.wasSuccessful())
