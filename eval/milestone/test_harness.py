"""Coverage-matching tests. Run: python3 -m unittest discover -s eval/milestone"""
import unittest

import harness as h

P_RANGE = {"group": 1, "source": "repo/a.md", "anchor": "pump 3 is closed on valve 6", "byte_start": 100, "byte_end": 130}
P_ANCHOR = {"group": 1, "source": "mem://x", "anchor": "pumps  open\nvalve 6"}


def q(passages, distractors=()):
    return {"id": "t", "corpus": "homelab", "question": "x", "answer_passages": passages,
            "distractor_passages": list(distractors)}


def res(source, text="filler", s=None, e=None):
    r = {"source": source, "text": text}
    if s is not None:
        r["byte_start"], r["byte_end"] = s, e
    return r


class Coverage(unittest.TestCase):
    def test_range_overlap_covers(self):
        self.assertTrue(h.passage_covered(P_RANGE, res("repo/a.md", s=120, e=300)))

    def test_off_by_range_does_not_cover(self):
        # right source, adjacent range: end-exclusive, must not count
        self.assertFalse(h.passage_covered(P_RANGE, res("repo/a.md", s=130, e=400)))
        self.assertFalse(h.passage_covered(P_RANGE, res("repo/a.md", s=0, e=100)))

    def test_range_wins_over_anchor_text(self):
        # anchor text present but range disjoint: the range decides
        self.assertFalse(h.passage_covered(P_RANGE, res("repo/a.md", P_RANGE["anchor"], 500, 600)))

    def test_wrong_source_does_not_cover(self):
        self.assertFalse(h.passage_covered(P_RANGE, res("repo/b.md", s=100, e=130)))
        self.assertFalse(h.passage_covered(P_ANCHOR, res("mem://y", "pumps open valve 6")))

    def test_anchor_normalised(self):
        self.assertTrue(h.passage_covered(P_ANCHOR, res("mem://x", "see pumps open &#32;valve 6 now")))
        self.assertFalse(h.passage_covered(P_ANCHOR, res("mem://x", "pumps open valve 7")))


class Scoring(unittest.TestCase):
    def test_all_groups_required_and_rank(self):
        question = q([dict(P_RANGE, group=1), dict(P_RANGE, group=2, source="repo/b.md")])
        r = [res("repo/b.md", s=100, e=110), res("repo/z.md"), res("repo/a.md", s=100, e=110)]
        rec = h.score_question(question, r, 1000)
        self.assertTrue(rec["covered"])
        self.assertEqual(rec["groups"], {"1": 3, "2": 1})
        self.assertEqual(rec["rank"], 3)
        self.assertFalse(h.score_question(question, r[:2], 1000)["covered"])

    def test_alternatives_in_one_group(self):
        question = q([P_RANGE, dict(P_RANGE, source="repo/copy.md")])
        self.assertTrue(h.score_question(question, [res("repo/copy.md", s=100, e=101)], 1000)["covered"])

    def test_budget_cuts_tail(self):
        big = res("repo/a.md", "word " * 600, 100, 130)  # 600 tokens
        question = q([P_RANGE])
        rec = h.score_question(question, [res("repo/x.md", "word " * 600), big], 1000)
        self.assertFalse(rec["covered"])  # second result would take total to 1200
        self.assertTrue(h.score_question(question, [res("repo/x.md", "word " * 600), big], 4000)["covered"])

    def test_distractor_before_answer(self):
        d = {"source": "repo/old.md", "anchor": "old"}
        rec = h.score_question(q([P_RANGE], [d]), [res("repo/old.md", "old value"), res("repo/a.md", s=100, e=130)], 1000)
        self.assertEqual(rec["distractor_rank"], 1)
        self.assertEqual(h.summarize([rec])["overall"]["distractor_before_answer"], 1)


if __name__ == "__main__":
    unittest.main()
