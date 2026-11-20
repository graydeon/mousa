"""Block parser shared by the answer check and the chunk baseline.

A block is one paragraph, list item, table row, heading or code line, with its byte range
in the raw file and its normalised text. Each block also points at the nearest heading at
or before it (h1 to h6 in HTML, `#` lines in Markdown and text). HTML <title> is not a block.
"""
import collections
import html
import os
import re

Block = collections.namedtuple("Block", "start end text heading")  # heading: Block or None

_TAG = re.compile(rb"<(/?)([a-zA-Z0-9]+)[^>]*>|<!--.*?-->", re.S)
_BLOCK_TAGS = {b"p", b"li", b"tr", b"h1", b"h2", b"h3", b"h4", b"h5", b"h6", b"div", b"pre",
               b"section", b"table", b"ul", b"ol", b"title", b"header", b"main", b"br", b"dt",
               b"dd", b"blockquote", b"details", b"summary"}
_HEADING = re.compile(rb"h[1-6]")
_MD_HEADING = re.compile(rb"#{1,6}\s+")
_MD_LINE_BLOCK = re.compile(rb"\s*([-*+]|\d+\.)\s|\|")
EXTENSIONS = (".html", ".md", ".txt")


def norm(text):
    return re.sub(r"\s+", " ", html.unescape(text)).strip()


def _link(raw_blocks):
    """raw_blocks: (start, end, text, is_heading). Returns Blocks with heading pointers."""
    out, heading = [], None
    for s, e, t, is_heading in raw_blocks:
        b = Block(s, e, t, None)
        if is_heading:
            heading = b._replace(heading=None)
            b = heading
        out.append(b._replace(heading=heading))
    return out


def html_blocks(raw):
    found, cur, pos, skip, kind = [], [], 0, False, None

    def flush():
        if cur:
            text = norm(" ".join(t for _, _, t in cur))
            if text and kind != b"title":
                found.append((cur[0][0], cur[-1][1], text, kind is not None))
        cur.clear()

    for m in _TAG.finditer(raw):
        if not skip and m.start() > pos:
            seg = raw[pos:m.start()]
            if seg.strip():
                lead = len(seg) - len(seg.lstrip())
                cur.append((pos + lead, pos + len(seg.rstrip()), seg.decode("utf8", "replace")))
        pos = m.end()
        name, close = (m.group(2) or b"").lower(), bool(m.group(1))
        if name in (b"style", b"script"):
            skip = not close
            continue
        if name in _BLOCK_TAGS:
            flush()
            if _HEADING.fullmatch(name) or name == b"title":
                kind = None if close else name
    if not skip and pos < len(raw) and raw[pos:].strip():
        seg = raw[pos:]
        lead = len(seg) - len(seg.lstrip())
        cur.append((pos + lead, pos + len(seg.rstrip()), seg.decode("utf8", "replace")))
    flush()
    return _link(found)


def md_blocks(raw):
    found, para, pos, incode = [], [], 0, False

    def flush():
        if para:
            s, e = para[0][0], para[-1][1]
            text = norm(raw[s:e].decode("utf8", "replace"))
            if text:
                found.append((s, e, text, False))
        para.clear()

    for line in raw.split(b"\n"):
        s, e = pos, pos + len(line)
        pos = e + 1
        st = line.strip()
        if st.startswith(b"```"):
            incode = not incode
            flush()
            continue
        if not st:
            flush()
            continue
        if not incode and _MD_HEADING.match(st):
            flush()
            found.append((s, e, norm(_MD_HEADING.sub(b"", st, 1).decode("utf8", "replace")), True))
            continue
        if incode or _MD_LINE_BLOCK.match(line):
            flush()
            para.append((s, e))
            flush()
            continue
        para.append((s, e))
    flush()
    return _link(found)


def file_blocks(path):
    with open(path, "rb") as f:
        raw = f.read()
    return html_blocks(raw) if path.lower().endswith(".html") else md_blocks(raw)


class Corpus:
    """Blocks per question-set source, read lazily from `root/<source>`."""

    def __init__(self, root):
        self.root = root
        self._cache = {}

    def blocks(self, source):
        if source not in self._cache:
            path = os.path.join(self.root, source)
            self._cache[source] = file_blocks(path) if os.path.isfile(path) else None
        return self._cache[source]
