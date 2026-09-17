"""Exercise the documentation consumer with a real Mousa executable."""

import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import docs


class DocumentationConsumerTest(unittest.TestCase):
    binary = None

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.directory = self.root / "corpus"
        self.store = self.root / "store.sqlite"
        self.directory.mkdir()

    def corpus(self, text, version, second=True):
        files = {"manual.txt": text.encode("utf-8")}
        if second:
            files["removed.txt"] = b"Obsolete zephyr procedure.\n"
        manifest = {"name": "Synthetic lifecycle fixture", "version": version,
                    "revision": version, "attribution": "Synthetic test text; not upstream Git documentation.",
                    "documents": list(files), "files": {}}
        for name, data in files.items():
            (self.directory / name).write_bytes(data)
            manifest["files"][name] = {"sha256": hashlib.sha256(data).hexdigest(), "url": None}
        (self.directory / "corpus.json").write_text(json.dumps(manifest))

    def example(self, *arguments, expected=0):
        command = [sys.executable, str(Path(docs.__file__)), "--mousa", str(self.binary),
                   "--store", str(self.store), "--directory",
                   str(self.directory.parent / "unused" / ".." / self.directory.name), *arguments]
        result = subprocess.run(command, capture_output=True, text=True, timeout=60)
        self.assertEqual(result.returncode, expected, result.stderr + result.stdout)
        if expected:
            self.assertEqual(result.stdout, "")
            return json.loads(result.stderr)
        return json.loads(result.stdout)

    def cli(self, *arguments):
        return docs.invoke(self.binary, self.store, list(arguments), 30)

    def test_current_evidence_lifecycle_and_snapshot(self):
        original = "\ufeffCedar repair starts Tuesday.\r\nCafé 東京.\r"
        self.corpus(original, "one")
        self.example("sync")
        first = self.example("ask", "--budget-bytes", "256", "cedar")
        hit = first["response"]["evidence"][0]
        self.assertEqual(hit["text"], "Cedar repair starts Tuesday.\nCafé 東京.\n")
        self.assertEqual(hit["location"], {"path": "manual.txt", "line_start": 1, "line_end": 2, "url": None})
        self.assertIsNone(first["answer"])
        self.assertEqual(first["support"], "not_assessed")
        saved = self.root / "saved.json"
        saved.write_text(json.dumps(first))
        self.corpus("Cedar repair moved to Friday.\n", "two", second=False)
        # A changed local revision must not acquire citations for an old store response.
        self.example("ask", "cedar", expected=1)
        self.example("sync")
        current = self.example("ask", "cedar")
        self.assertEqual(current["response"]["evidence"][0]["text"], "Cedar repair moved to Friday.\n")
        removed = self.example("ask", "zephyr")
        self.assertEqual(removed["outcome"], "insufficient_evidence")
        self.assertEqual(removed["response"]["outcome"], "no_matches")
        self.assertFalse(removed["response"]["evidence"])
        limited = self.example("ask", "--budget-bytes", "1", "cedar")
        self.assertEqual(limited["outcome"], "insufficient_evidence")
        self.assertEqual(limited["response"]["outcome"], "budget_omitted")
        self.cli("access", str(self.directory), "deny")
        denied = self.example("ask", "cedar")
        self.assertEqual(denied["response"]["outcome"], "policy_excluded")
        self.assertFalse(denied["response"]["evidence"])
        trail = self.cli("trail", str(self.directory), first["response"]["trail_id"])
        self.assertNotIn("historical", trail)
        self.assertEqual(json.loads(saved.read_text()), first)
        docs.verify_evidence(hit, original.encode("utf-8"))
        self.cli("access", str(self.directory), "allow")
        self.cli("withdraw", str(self.directory))
        self.cli("access", str(self.directory), "allow")
        withdrawn = self.example("ask", "cedar")
        self.assertEqual(withdrawn["response"]["outcome"], "lifecycle_excluded")
        self.assertFalse(withdrawn["response"]["evidence"])

    def test_source_isolation_and_changed_corpus(self):
        self.corpus("Cedar local procedure.\n", "one")
        self.example("sync")
        peer = self.root / "peer"
        peer.mkdir()
        (peer / "peer.txt").write_text("Cedar peer-exclusive procedure.\n")
        self.cli("sync", str(peer))
        response = self.example("ask", "cedar")
        self.assertEqual([h["item"] for h in response["response"]["evidence"]], ["manual.txt"])
        self.assertNotIn("peer-exclusive", json.dumps(response))
        (self.directory / "manual.txt").write_text("Changed without a manifest update.\n")
        self.example("ask", "cedar", expected=1)
        self.example("sync", expected=1)
        (self.directory / "manual.txt").unlink()
        (self.directory / "manual.txt").symlink_to(peer / "peer.txt")
        self.example("sync", expected=1)

    def test_bundled_corpus_and_insufficient_evidence(self):
        self.directory.rmdir()
        self.example("prepare")
        self.example("prepare", expected=1)
        self.example("sync")
        missing = self.example("ask", "frobnicatequantum")
        self.assertEqual(missing["outcome"], "insufficient_evidence")
        self.assertIsNone(missing["answer"])
        self.assertFalse(missing["response"]["evidence"])
        packet = self.example("ask", "--budget-bytes", "4096", "overlay")
        self.assertEqual(packet["outcome"], "evidence_available")
        self.assertLessEqual(packet["response"]["used_bytes"], 4096)
        for hit in packet["response"]["evidence"]:
            self.assertIn("/c44beea485f0f2feaf460e2ac87fdd5608d63cf0/", hit["location"]["url"])


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mousa", type=Path, required=True)
    arguments = parser.parse_args()
    DocumentationConsumerTest.binary = arguments.mousa.resolve(strict=True)
    suite = unittest.defaultTestLoader.loadTestsFromTestCase(DocumentationConsumerTest)
    result = unittest.TextTestRunner(verbosity=2).run(suite)
    raise SystemExit(not result.wasSuccessful())
