import json
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
from proxy import Case, Floor, Span, adapt_reply, load_release, pack, recent, score, unit


class ProxyTest(unittest.TestCase):
    def test_release_links_questions_to_only_earlier_user_messages(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            registry = root / "registry/personas/reader"
            registry.mkdir(parents=True)
            tests = root / "tests/reader"
            tests.mkdir(parents=True)
            (root / "manifest.json").write_text(json.dumps({"sources": {"reader": {"sessions": 2, "tests": 1}}}))
            (registry / "life_sim.yaml").write_text(
                'sessions:\n- id: "000001"\n  narrative_date: "2025-01-01T09:00:00-06:00"\n'
                '  messages: ["Use café now."]\n- id: "000002"\n'
                '  narrative_date: "2025-01-02T09:00:00-06:00"\n  messages: ["A second fact."]\n',
                encoding="utf-8",
            )
            facts = registry / "facts.yaml"
            facts.write_text('facts:\n- id: 5\n  source_session_ids: ["000001", "000002"]\n')
            (tests / "001.yaml").write_text(
                'id: "001"\nnarrative_anchor_date: "2025-01-04"\ntest: "Send it.  "\n'
                'load_bearing_facts: [5]\nexpected_tool_calls: ["not-query-text"]\n',
            )
            histories, cases = load_release(root)
            case, = cases
            self.assertEqual(case.query, "[2025-01-04] Send it.  ")
            older, newer = histories["reader"].values()
            self.assertEqual(older.item, "personas/reader/sessions/2025-01-01-000001")
            self.assertEqual(older.text, "[2025-01-01T09:00:00-06:00] user: Use café now.")
            self.assertEqual(case.needs, ((older.evidence, newer.evidence),))
            self.assertEqual(len(older.evidence_bytes), older.evidence.end - older.evidence.start)
            facts.write_text('facts:\n- id: 5\n  source_session_ids: ["000003"]\n')
            with self.assertRaisesRegex(ValueError, "000003"):
                load_release(root)

    def test_coverage_requires_complete_ranges_and_correct_provenance(self):
        a, b = Span("a", 1, 5), Span("b", 0, 3)
        case = Case("persona", "001", "ask", ((a,), (b,)))
        first = unit("a", 1, 3, "ab")
        second = unit("a", 3, 5, "cd")
        other = unit("b", 0, 3, "xyz")
        self.assertEqual(score(case, [first, second, other], [a, b], [])[:2], ("hit", None))
        self.assertEqual(score(case, [first, other], [a, b], [])[:2], ("partial", None))
        self.assertEqual(score(case, [first, second], [a, b], [])[2], ("retrieved but not packed",))
        self.assertEqual(score(case, [first], [Span("a", 1, 3)], [])[2], ("not retrieved",))
        first_span = Span("a", 1, 3)
        self.assertEqual(score(case, [first, second], [first_span, Span("a", 3, 5)], [])[0], "partial")
        self.assertEqual(score(case, [first], [first_span, b], [])[2],
                         ("not retrieved", "retrieved but not packed"))
        self.assertEqual(score(case, [unit("wrong", 1, 5, "abcd")], [], [])[0], "miss")
        split = Case("persona", "002", "ask", ((a, b),))
        self.assertEqual(score(split, [first, second], [a, b], [])[:3],
                         ("miss", None, ("retrieved but not packed", "split across items")))

    def test_temporal_rank_requires_new_value_and_all_old_values_below_it(self):
        new, old = Span("new", 0, 3), Span("old", 0, 3)
        case = Case("persona", "002", "ask", ((new,),))
        newer, older = unit("new", 0, 3, "new"), unit("old", 0, 3, "old")
        pairs = [(new, old)]
        self.assertIs(score(case, [newer], [new, old], pairs)[1], True)
        self.assertEqual(score(case, [older, newer], [new, old], pairs)[1:],
                         (False, ("superseded value ranked first",)))
        self.assertIs(score(case, [older], [new, old], pairs)[1], False)

    def test_mousa_reply_preserves_candidates_omitted_from_packet(self):
        reply = {"trail": {"sources": [{"outcome": "allow"}], "result_sha256": "native",
                            "candidates": [{"rank": 1, "representation_id": "aa", "packed": "none",
                                            "range": {"start": 0, "end": 7}}]},
                 "passages": [{"item": "doc", "rank": 1, "range": {"start": 0, "end": 2}, "text": "é"}]}
        rows, candidates = adapt_reply(reply, {"aa": "doc"}, {"doc": "élan!!".encode()})
        self.assertEqual(rows, [unit("doc", 0, 2, "é")])
        self.assertEqual(candidates, [Span("doc", 0, 7)])
        reply["passages"][0]["text"] = "ab"
        with self.assertRaisesRegex(ValueError, "byte range"):
            adapt_reply(reply, {"aa": "doc"}, {"doc": "élan!!".encode()})

    def test_floor_preserves_text_ranges_and_recent_does_not_skip(self):
        text = "é " * 451
        floor = Floor({"doc": text})
        rows = floor.search("é term")
        self.assertEqual(rows, [])  # same ASCII query tokenizer as milestone
        rows = Floor({"doc": "term " + text + "term"}).search("term")
        self.assertEqual(len(rows), 2)
        self.assertEqual("".join(r["text"] for r in sorted(rows, key=lambda r: r["byte_start"])),
                         "term " + text + "term")
        ordered = [unit("new", 0, 2, "é"), unit("big", 0, 5, "large"), unit("old", 0, 2, "ok")]
        self.assertEqual(pack(ordered, 4, 2), [ordered[0], ordered[2]])
        self.assertEqual(recent(ordered, 4, 2), [ordered[0]])


if __name__ == "__main__":
    unittest.main()
