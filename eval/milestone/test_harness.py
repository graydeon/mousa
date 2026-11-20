"""Coverage-matching tests. Run: python3 -m unittest discover -s eval/milestone"""
import os
import random
import tempfile
import unittest

import blocks as b
import chunks
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


DOC = (b"<html><head><title>T</title><style>p{x}</style></head><body>"
       b"<h2>Pump room</h2><p>Intro text.</p>"
       b"<p>The pump sits on <code>bay 3</code>, valve 6 closed, gauge on.</p>"
       b"<h2>Other</h2><p>Unrelated.</p></body></html>")
ANCHOR = b"valve 6 closed"


class Blocks(unittest.TestCase):
    def test_html_blocks_and_heading(self):
        bl = b.html_blocks(DOC)
        self.assertEqual([x.text for x in bl], ["Pump room", "Intro text.",
                                                "The pump sits on bay 3 , valve 6 closed, gauge on.", "Other", "Unrelated."])
        self.assertEqual(bl[2].heading.text, "Pump room")
        self.assertEqual(DOC[bl[0].start:bl[0].end], b"Pump room")

    def test_md_blocks(self):
        raw = b"# Top\n\npara one\nline two\n\n- item\n## Sub\ntext\n"
        bl = b.md_blocks(raw)
        self.assertEqual([(x.text, x.heading.text) for x in bl],
                         [("Top", "Top"), ("para one line two", "Top"), ("- item", "Top"), ("Sub", "Sub"), ("text", "Sub")])


class AnswerCheck(unittest.TestCase):
    """Fails if the answer check credits a fragment: anchor present but block or heading missing."""

    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        os.makedirs(os.path.join(self.dir.name, "repo"))
        with open(os.path.join(self.dir.name, "repo", "a.html"), "wb") as f:
            f.write(DOC)
        self.corpus = b.Corpus(self.dir.name)
        s = DOC.index(ANCHOR)
        self.p = {"group": 1, "source": "repo/a.html", "anchor": ANCHOR.decode(), "byte_start": s, "byte_end": s + len(ANCHOR)}
        bl = b.html_blocks(DOC)
        self.block, self.head = bl[2], bl[0]

    def tearDown(self):
        self.dir.cleanup()

    def rec(self, results):
        return h.score_question(q([self.p]), results, 1000, self.corpus)

    def test_block_and_heading_by_range(self):
        r = self.rec([res("repo/a.html", "x", self.head.start, self.block.end)])
        self.assertTrue(r["covered"])
        self.assertTrue(r["answered"])

    def test_heading_in_separate_result(self):
        r = self.rec([res("repo/a.html", "x", self.block.start, self.block.end), res("repo/a.html", "y", self.head.start, self.head.end)])
        self.assertTrue(r["answered"])

    def test_fragment_without_heading_is_not_answered(self):
        r = self.rec([res("repo/a.html", "x", self.block.start, self.block.end)])
        self.assertTrue(r["covered"])
        self.assertFalse(r["answered"])

    def test_anchor_only_is_not_answered(self):
        r = self.rec([res("repo/a.html", "x", self.p["byte_start"], self.p["byte_end"]),
                      res("repo/a.html", "y", self.head.start, self.head.end)])
        self.assertTrue(r["covered"])
        self.assertFalse(r["answered"])

    def test_text_rule(self):
        good = res("repo/a.html", "Pump room\nIntro text.\nThe pump sits on bay 3, valve 6 closed, gauge on.")
        self.assertTrue(self.rec([good])["answered"])
        wrong_source = dict(good, source="repo/b.html")
        self.assertFalse(self.rec([wrong_source])["answered"])
        cut = res("repo/a.html", "Pump room. The pump sits on bay 3, valve 6 closed")
        self.assertFalse(self.rec([cut])["answered"])

    def test_heading_lost_to_budget(self):
        r = self.rec([res("repo/a.html", "word " * 990, self.block.start, self.block.end),
                      res("repo/a.html", "word " * 20, self.head.start, self.head.end)])
        self.assertFalse(r["answered"])


def qs_for_split():
    kinds = ["single-fact"] * 7 + ["paraphrase"] * 4 + ["multi-passage"] * 9 + ["no-answer"] * 5
    return [{"id": f"x-{i:02d}", "kind": k, "corpus": "homelab" if i % 3 else "skills", "question": "q",
             "answer_passages": []} for i, k in enumerate(kinds)]


class Split(unittest.TestCase):
    """Fails if the held-out half leaks tuning questions or is not fixed."""

    def test_halves_disjoint_cover_balanced(self):
        qs = qs_for_split()
        tune, held = h.select(qs, "tuning"), h.select(qs, "heldout")
        ti, hi = {x["id"] for x in tune}, {x["id"] for x in held}
        self.assertFalse(ti & hi)
        self.assertEqual(ti | hi, {x["id"] for x in qs})
        for kind in {x["kind"] for x in qs}:
            nt = sum(x["kind"] == kind for x in tune)
            nh = sum(x["kind"] == kind for x in held)
            self.assertLessEqual(abs(nt - nh), 1, kind)

    def test_fixed_under_reordering(self):
        qs = qs_for_split()
        ids = {x["id"] for x in h.select(qs, "heldout")}
        random.Random(7).shuffle(qs)
        self.assertEqual({x["id"] for x in h.select(qs, "heldout")}, ids)

    def test_corpus_filter_after_split(self):
        qs = qs_for_split()
        held = {x["id"] for x in h.select(qs, "heldout")}
        skills = h.select(qs, "heldout", {"skills"})
        self.assertTrue(skills)
        self.assertTrue({x["id"] for x in skills} <= held)
        self.assertEqual(len(h.select(qs, "all", {"skills"})), sum(x["corpus"] == "skills" for x in qs))


class NoAnswer(unittest.TestCase):
    def test_empty_packet_abstains_and_is_not_coverage(self):
        na = {"id": "na", "corpus": "homelab", "answer_passages": []}
        empty = h.score_question(na, [], 1000)
        full = h.score_question(na, [res("repo/a.md", "something")], 1000)
        self.assertTrue(empty["abstained"])
        self.assertFalse(full["abstained"])
        hit = h.score_question(q([P_RANGE]), [res("repo/a.md", s=100, e=130)], 1000)
        s = h.summarize([empty, full, hit])
        self.assertEqual((s["overall"]["questions"], s["overall"]["covered"]), (1, 1))
        self.assertEqual((s["no_answer"]["questions"], s["no_answer"]["abstained"]), (2, 1))


class Packing(unittest.TestCase):
    def test_skip_and_continue(self):
        rs = [res("a", "w " * 600), res("b", "w " * 600), res("c", "w " * 300)]
        self.assertEqual([r["source"] for r in h.pack_skip(rs, 1000)], ["a", "c"])
        self.assertEqual([r["source"] for r in h.apply_budget(rs, 1000)[0]], ["a"])


class Chunks(unittest.TestCase):
    def test_chunking_and_expression(self):
        bl = [b.Block(i * 10, i * 10 + 9, "w " * 200, None) for i in range(5)]
        cs = chunks.file_chunks(bl)
        self.assertEqual([(s, e) for s, e, _ in cs], [(0, 19), (20, 39), (40, 49)])
        self.assertEqual(chunks.match_expression("The CT400 and caddy, CT400 route"), '"ct400" OR "caddy" OR "route"')


if __name__ == "__main__":
    unittest.main()
