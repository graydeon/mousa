"""Check the example's duplicate assertions against both canonical tie orders."""
import argparse
import copy
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

from association_supersession import FIXTURE, check_duplicates
from token_supersession import identity


class DuplicateSelectionTest(unittest.TestCase):
    def test_both_tie_orders(self):
        # Fixed JSONL source identities produce opposite fixed-v1 segment orders.
        for source, first in (("synthetic-composed-tie-0", "old"),
                              ("synthetic-composed-tie-4", "twin")):
            with self.subTest(first=first), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)

                def command(*argv, value=None):
                    result = subprocess.run([str(self.binary), "-store", str(root / "store.sqlite"),
                        *map(str, argv)], input=value, text=True, capture_output=True,
                        check=True, timeout=30)
                    return json.loads(result.stdout)

                selector = ["--source", source]
                command("sync", *selector, value="".join(json.dumps(dict(id=k, text=v)) + "\n"
                                                       for k, v in FIXTURE.items()))
                pins = {p["item"]: p for p in command("vector", "inputs", *selector)["passages"]}
                order = sorted(("old", "twin"), key=lambda item: pins[item]["segment_id"])
                self.assertEqual(order[0], first)
                sid = pins["old"]["source_id"]
                declaration = dict(schema="mousa.supersession_declaration.v1", source_id=sid,
                    predecessor_item_id="old", predecessor_representation_id=pins["old"]["representation_id"],
                    successor_item_id="new", successor_representation_id=pins["new"]["representation_id"],
                    author="example.maintainer", basis="Explicit replacement, not inferred truth")
                declaration["id"] = identity(declaration["schema"], bytes.fromhex(sid), "old",
                    bytes.fromhex(declaration["predecessor_representation_id"]), "new",
                    bytes.fromhex(declaration["successor_representation_id"]), declaration["author"], declaration["basis"])
                command("supersession", "declaration", "put", value=json.dumps(declaration))
                activation = dict(schema="mousa.supersession_activation.v1", source_id=sid,
                    expected_previous_activation_id=None, declaration_id=declaration["id"],
                    actor_id="example.maintainer", actor_version="1", occurred_at_usec=1,
                    reason="Explicit replacement")
                activation["id"] = identity(activation["schema"], bytes.fromhex(sid), b"", bytes.fromhex(declaration["id"]))
                command("supersession", "activation", "put", value=json.dumps(activation))
                associations = dict(schema="mousa.association_declarations.v1", associations=[
                    dict(from_item="primary", to_item=item, basis="Caller-declared qualification",
                         author="example.maintainer") for item in ("old", "twin", "support")])
                path = root / "associations.json"
                path.write_text(json.dumps(associations), encoding="utf-8")
                for packing in ("original", "exact-v1"):
                    omitted = None
                    for enabled in (None, False, True):
                        with self.subTest(packing=packing, supersession=enabled):
                            flags = [] if enabled is None else ["--supersession=" + str(enabled).lower()]
                            result = command("query", "--associations", path, "--packing-policy", packing,
                                "--context-tokens", "4096", "--tokenizer-python", self.python,
                                "--tokenizer-cache", self.cache, *flags, *selector, "needle")
                            candidates = result["token"]["candidate_ids"]
                            duplicate_ids = {pins[item]["segment_id"] for item in order}
                            if not enabled:
                                self.assertEqual([s for s in candidates if s in duplicate_ids],
                                                 [pins[item]["segment_id"] for item in order])
                            check_duplicates(result, packing, pins, "old", "twin", bool(enabled))
                            if enabled is None:
                                omitted = result
                            elif not enabled:
                                self.assertEqual(result["evidence"], omitted["evidence"])
                                self.assertEqual(result["context"], omitted["context"])
                            else:
                                h = command("trail", *selector, result["trail_id"])["historical"]
                                self.assertEqual(h["token"], result["token"])
                                self.assertEqual(h["supersession"]["activation_id"], activation["id"])
                                self.assertEqual(h["supersession"]["declaration_id"], declaration["id"])
                            if packing == "exact-v1" and enabled is None:
                                wrong = copy.deepcopy(result)
                                index = next(i for i, p in enumerate(wrong["evidence"])
                                             if p["segment_id"] in duplicate_ids)
                                wrong["evidence"][index] = pins[order[1]]
                                with self.assertRaises(AssertionError):
                                    check_duplicates(wrong, packing, pins, "old", "twin")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mousa", type=Path, required=True)
    parser.add_argument("--python", type=Path, required=True)
    parser.add_argument("--cache", type=Path, required=True)
    args = parser.parse_args()
    DuplicateSelectionTest.binary = args.mousa.absolute()
    DuplicateSelectionTest.python = args.python.absolute()
    DuplicateSelectionTest.cache = args.cache.absolute()
    suite = unittest.defaultTestLoader.loadTestsFromTestCase(DuplicateSelectionTest)
    raise SystemExit(not unittest.TextTestRunner(verbosity=2).run(suite).wasSuccessful())
