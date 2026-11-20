"""Plain-FTS chunk baseline: SQLite FTS5 BM25 over markup-stripped 450-token chunks.

Every .html, .md and .txt file under the root (files over 2,000,000 bytes skipped) is cut
into blocks (blocks.py); consecutive blocks join into one chunk until the next block would
take it past 450 harness tokens. A chunk's text is its block texts joined by newlines, and
its byte range runs from its first block's start to its last block's end. The query is an
OR of the question's distinct lower-case alphanumeric terms minus a small stop list, ranked
by bm25 with the unicode61 tokenizer, top 300. Index lives in memory.
"""
import os
import re
import sqlite3

import blocks as B
import harness as H

CHUNK_TOKENS = 450
LIMIT = 300
MAX_FILE_BYTES = 2_000_000
_TERM = re.compile(r"[a-z0-9]+")
STOP = set("the a an of to in on for and or is are was be with by at as it this that from".split())


def match_expression(question):
    terms = dict.fromkeys(t for t in _TERM.findall(question.lower()) if t not in STOP)
    return " OR ".join(f'"{t}"' for t in terms)


def file_chunks(blocks):
    out, cur, n = [], [], 0
    for b in blocks:
        t = H.count_tokens(b.text)
        if cur and n + t > CHUNK_TOKENS:
            out.append(cur)
            cur, n = [], 0
        cur.append(b)
        n += t
    if cur:
        out.append(cur)
    return [(c[0].start, c[-1].end, "\n".join(b.text for b in c)) for c in out]


class Index:
    def __init__(self, root):
        self.db = sqlite3.connect(":memory:")
        self.db.execute("CREATE VIRTUAL TABLE c USING fts5(text, tokenize='unicode61')")
        self.units = []
        for dirpath, dirnames, filenames in os.walk(root):
            dirnames[:] = sorted(d for d in dirnames if d != ".git")
            for name in sorted(filenames):
                path = os.path.join(dirpath, name)
                if not name.lower().endswith(B.EXTENSIONS) or os.path.getsize(path) > MAX_FILE_BYTES:
                    continue
                source = os.path.relpath(path, root)
                for start, end, text in file_chunks(B.file_blocks(path)):
                    self.units.append({"source": source, "text": text, "byte_start": start, "byte_end": end})
                    self.db.execute("INSERT INTO c(rowid, text) VALUES (?, ?)", (len(self.units), text))

    def search(self, question):
        expr = match_expression(question)
        if not expr:
            return []
        rows = self.db.execute("SELECT rowid FROM c WHERE c MATCH ? ORDER BY bm25(c), rowid LIMIT ?",
                               (expr, LIMIT)).fetchall()
        return [self.units[i - 1] for (i,) in rows]
