#!/usr/bin/env python3
"""Check explicit epochs, normalization ambiguity and source isolation."""

import hashlib
import json
from pathlib import Path
import tempfile
import unittest

import memory
from memory_test import evidence, query_result


def revision(label, text, author="Ada"):
    return {"revision": label, "id": "shared", "text": text,
            "sha256": hashlib.sha256(text.encode()).hexdigest(),
            "author": author, "date": "synthetic-epoch", "uri": "synthetic://" + author}


def fixture():
    return {"schema": "mousa-project-history-v2", "source": "named-alpha",
            "revisions": [revision("first", "beacon initial\n"),
                          revision("second", "beacon corrected\n", "Bea")],
            "epochs": [{"epoch": "create", "operations": [{"id": "shared", "revision": "first"}]},
                       {"epoch": "correct", "operations": [{"id": "shared", "revision": "second"}]},
                       {"epoch": "delete", "operations": [{"id": "shared", "deleted": True}]},
                       {"epoch": "restore", "operations": [{"id": "shared", "revision": "second"}]},
                       {"epoch": "revert", "operations": [{"id": "shared", "revision": "first"}]}]}


class ChronologyTests(unittest.TestCase):
    def test_selected_epoch_never_replays_predecessors(self):
        h = fixture()
        memory.validate_chronology(h)
        self.assertEqual(memory.epoch_items(h, "revert"), [{"id": "shared", "text": "beacon initial\n"}])
        self.assertEqual(memory.epoch_items(h, "delete"), [{"id": "shared", "deleted": True}])
        with self.assertRaisesRegex(ValueError, "unknown explicit epoch"):
            memory.epoch_items(h, "missing")

    def test_each_selected_revision_keeps_its_own_attribution(self):
        h = fixture()
        for row in h["revisions"]:
            rendered = memory.render(query_result(h["source"], [evidence("shared", row["text"], h["source"])]), h, "beacon")
            self.assertEqual(rendered["passages"][0]["attribution"]["author"], row["author"])
            self.assertEqual(rendered["passages"][0]["revision_labels"], [row["revision"]])
        with self.assertRaisesRegex(ValueError, "source identity"):
            memory.render(query_result("named-beta", []), h, "beacon")

    def test_equivalent_normalized_content_requires_equal_attribution(self):
        h = fixture()
        h["revisions"].append(revision("equivalent", "\ufeffbeacon initial\r\n"))
        memory.validate_chronology(h)
        rendered = memory.render(query_result(h["source"], [evidence("shared", "beacon initial\n", h["source"])]), h, "beacon")
        self.assertEqual(rendered["passages"][0]["revision_labels"], ["first", "equivalent"])
        h["revisions"][-1]["author"] = "Conflicting"
        with self.assertRaisesRegex(ValueError, "ambiguous normalized"):
            memory.validate_chronology(h)

    def test_complete_epoch_preflight_rejects_later_invalid_operations(self):
        for mutate in (
            lambda h: h["epochs"][-1]["operations"].append({"id": "shared", "deleted": True}),
            lambda h: h["epochs"][-1]["operations"].append({"id": "unknown", "revision": "first"}),
            lambda h: h["epochs"][-1]["operations"].append({"id": "bad", "deleted": False}),
            lambda h: h["revisions"][-1].update(text="x" * 262145),
            lambda h: h.update(source="bad\x00argv"),
        ):
            h = fixture()
            mutate(h)
            with tempfile.TemporaryDirectory() as directory:
                p = Path(directory) / "history.json"
                p.write_text(json.dumps(h))
                with self.assertRaises(ValueError):
                    memory.load_history(p)

    def test_only_one_leading_bom_is_normalized(self):
        h = fixture()
        h["revisions"][0] = revision("first", "\ufeff\ufeffbeacon café\r\n")
        normalized = "\ufeffbeacon café\n"
        hit = evidence("shared", normalized, h["source"])
        rendered = memory.render(query_result(h["source"], [hit]), h, "beacon")
        self.assertEqual(rendered["passages"][0]["evidence"]["text"], normalized)


if __name__ == "__main__":
    unittest.main()
