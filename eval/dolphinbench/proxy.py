"""Model-free evidence delivery on a local DolphinBench release."""

import argparse
from collections import Counter, defaultdict
from contextlib import closing, contextmanager
import csv
from dataclasses import dataclass
from datetime import date, datetime
from hashlib import sha256
from http.client import HTTPConnection
import json
from pathlib import Path
import re
import secrets
import select
import sqlite3
import subprocess
import sys
import time

import yaml

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "milestone"))
import chunks
import harness


BUDGETS = (1024, 4096, 16384)


@dataclass(frozen=True)
class Span:
    item: str
    start: int
    end: int


@dataclass(frozen=True)
class Message:
    item: str
    date: datetime
    text: str
    evidence_bytes: bytes

    @property
    def evidence(self):
        end = len(self.text.encode())
        return Span(self.item, end - len(self.evidence_bytes), end)


@dataclass(frozen=True)
class Case:
    persona: str
    id: str
    query: str
    needs: tuple[tuple[Span, ...], ...]


def load_release(root):
    manifest = json.loads((root / "manifest.json").read_bytes())
    files = manifest.get("files", {})

    def load(path):
        raw = (root / path).read_bytes()
        if files and sha256(raw).hexdigest() != files.get(path.as_posix()):
            raise ValueError(f"release file hash mismatch: {path}")
        return yaml.load(raw, Loader=yaml.CSafeLoader)

    histories, cases = {}, []
    for persona, meta in sorted(manifest["sources"].items()):
        if not re.fullmatch(r"[a-z][a-z0-9-]*", persona):
            raise ValueError(f"invalid persona: {persona!r}")
        base = Path("registry/personas") / persona
        messages = histories[persona] = {}
        for row in load(base / "life_sim.yaml")["sessions"]:
            stamp = row["narrative_date"]
            when = datetime.fromisoformat(stamp)
            sid = row["id"]
            if not re.fullmatch(r"[a-zA-Z0-9_-]+", sid) or sid in messages or when.tzinfo is None:
                raise ValueError(f"{persona}: invalid session {sid!r}")
            raw, = row["messages"]
            raw = raw.strip().replace("\r\n", "\n").replace("\r", "\n")
            if not raw:
                raise ValueError(f"{persona}: empty session {sid}")
            item = f"personas/{persona}/sessions/{when.date()}-{sid}"
            messages[sid] = Message(item, when, f"[{stamp}] user: {raw}", raw.encode())
        if not messages or len(messages) != meta["sessions"]:
            raise ValueError(f"{persona}: incorrect session count")
        facts = {}
        for row in load(base / "facts.yaml")["facts"]:
            fid, sources = row["id"], row["source_session_ids"]
            if fid in facts or not sources:
                raise ValueError(f"{persona}: invalid fact {fid}")
            try:
                facts[fid] = tuple(messages[sid] for sid in dict.fromkeys(sources))
            except KeyError as error:
                raise ValueError(f"{persona}: fact {fid} references missing session {error}") from error
        for index in range(1, meta["tests"] + 1):
            test_id = f"{index:03d}"
            test = load(Path("tests") / persona / f"{test_id}.yaml")
            anchor = date.fromisoformat(test["narrative_anchor_date"])
            refs = test["load_bearing_facts"]
            if str(test["id"]).zfill(3) != test_id or not refs or len(set(refs)) != len(refs):
                raise ValueError(f"{persona}/{test_id}: incorrect ID or fact references")
            refs = [facts[fid] for fid in refs]
            if any(msg.date.date() >= anchor for group in refs for msg in group):
                raise ValueError(f"{persona}/{test_id}: evidence is not earlier than the test")
            cases.append(Case(persona, test_id, f"[{anchor}] {test['test']}",
                              tuple(tuple(msg.evidence for msg in group) for group in refs)))
    if not cases:
        raise ValueError("release has no tests")
    return histories, cases


def load_temporal(path, histories, cases):
    """Optional manually reviewed current/obsolete spans, never used for retrieval."""
    labels = defaultdict(list)
    if path is None:
        return labels
    by_id = {(case.persona, case.id): case for case in cases}

    def locate(msg, text):
        value = text.encode()
        if not value or msg.evidence_bytes.count(value) != 1:
            raise ValueError(f"{msg.item}: temporal anchor must match exactly once")
        offset = msg.evidence.start + msg.evidence_bytes.index(value)
        return Span(msg.item, offset, offset + len(value))

    with path.open(encoding="utf-8", newline="") as source:
        for persona, test, new_id, new_text, old_id, old_text in csv.reader(source, delimiter="\t"):
            case = by_id[persona, test]
            new, old = histories[persona][new_id], histories[persona][old_id]
            if new.date <= old.date or not any(new.evidence in group for group in case.needs):
                raise ValueError(f"{persona}/{test}: invalid current or obsolete temporal evidence")
            labels[persona, test].append((locate(new, new_text), locate(old, old_text)))
    return labels


def unit(item, start, end, text):
    return {"source": item, "byte_start": start, "byte_end": end, "text": text}


def span(row):
    return Span(row["source"], row["byte_start"], row["byte_end"])


def pack(rows, byte_budget, token_budget):
    kept = []
    for row in rows:
        size = len(row["text"].encode())
        if size <= byte_budget:
            kept.append(row)
            byte_budget -= size
    return harness.pack_skip(kept, token_budget)


def recent(rows, byte_budget, token_budget):
    kept = []
    for row in rows:
        byte_budget -= len(row["text"].encode())
        if byte_budget < 0:
            break
        kept.append(row)
    return harness.apply_budget(kept, token_budget)[0]


class Floor:
    def __init__(self, items):
        self.db = sqlite3.connect(":memory:")
        self.db.execute("CREATE VIRTUAL TABLE c USING fts5(text, tokenize='unicode61')")
        self.rows = []
        for item, text in sorted(items.items()):
            offsets = [m.start() for m in harness._PIECE.finditer(text)]
            cuts = [0, *offsets[chunks.CHUNK_TOKENS::chunks.CHUNK_TOKENS], len(text)]
            byte_start = 0
            for start, end in zip(cuts, cuts[1:]):
                fragment = text[start:end]
                byte_end = byte_start + len(fragment.encode())
                self.rows.append(unit(item, byte_start, byte_end, fragment))
                byte_start = byte_end
        self.db.executemany("INSERT INTO c(text) VALUES (?)", ((row["text"],) for row in self.rows))

    def search(self, query):
        expression = chunks.match_expression(query)
        if not expression:
            return []
        return [self.rows[i - 1] for (i,) in self.db.execute(
            "SELECT rowid FROM c WHERE c MATCH ? ORDER BY bm25(c), rowid LIMIT ?",
            (expression, chunks.LIMIT))]


def covering_rank(want, ranked):
    """Rank where all bytes become available, or None if any range is missing."""
    ranges = []
    for rank, offered in enumerate(ranked, 1):
        if offered.item != want.item or offered.end <= want.start or offered.start >= want.end:
            continue
        ranges.append((offered.start, offered.end))
        at = want.start
        for start, end in sorted(ranges):
            if start > at:
                break
            at = max(at, end)
            if at >= want.end:
                return rank
    return None


def score(case, packet, candidates, temporal):
    released = [span(row) for row in packet]
    needed = set(want for group in case.needs for want in group)
    covered = {want for want in needed if covering_rank(want, released) is not None}
    complete = sum(all(want in covered for want in group) for group in case.needs)
    outcome = "hit" if complete == len(case.needs) else "partial" if complete else "miss"
    reasons = set()
    for want in needed - covered:
        reasons.add("not retrieved" if covering_rank(want, candidates) is None else "retrieved but not packed")
    if any(len({want.item for want in group}) > 1 and any(w in covered for w in group)
           and not all(w in covered for w in group) for group in case.needs):
        reasons.add("split across items")
    latest = None
    if temporal:
        latest = True
        for new, old in temporal:
            new_rank, old_rank = covering_rank(new, released), covering_rank(old, released)
            if new_rank is None:
                latest = False
            elif old_rank is not None and old_rank <= new_rank:
                latest = False
                reasons.add("superseded value ranked first")
    return outcome, latest, tuple(sorted(reasons))


def adapt_reply(reply, representation_items, item_bytes):
    trail = reply["trail"]
    if [source["outcome"] for source in trail["sources"]] != ["allow"]:
        raise ValueError("expected exactly one allowed Mousa source")
    rows = []
    for passage in sorted(reply["passages"] or [], key=lambda row: row["rank"]):
        start, end = passage["range"]["start"], passage["range"]["end"]
        item, text = passage["item"], passage["text"]
        if not 0 <= start < end <= len(item_bytes[item]) or text.encode() != item_bytes[item][start:end]:
            raise ValueError(f"Mousa passage disagrees with indexed byte range: {item}")
        rows.append(unit(item, start, end, text))
    candidates = [
        Span(representation_items[row["representation_id"]], row["range"]["start"], row["range"]["end"])
        for row in sorted(trail.get("candidates") or [], key=lambda row: row["rank"])
    ]
    return rows, candidates


@contextmanager
def mousa(binary, root, policy, histories, timings):
    """Create a scratch store and expose only authenticated loopback retrieval."""
    spool = root / policy
    spool.mkdir(mode=0o700)
    store = spool / "store.sqlite"
    env = {"PATH": "/usr/bin:/bin", "HOME": str(spool), "XDG_CONFIG_HOME": str(spool)}
    cli = [str(binary), "-store", str(store)]
    # status opens and initializes a new store; the CLI has no init subcommand.
    subprocess.run([*cli, "status", "--source", next(iter(histories))],
                   env=env, check=True, stdout=subprocess.DEVNULL)
    for persona, messages in histories.items():
        data = "".join(json.dumps({"id": msg.item, "path": msg.item, "text": msg.text},
                                  ensure_ascii=False) + "\n" for msg in messages.values()).encode()
        args = ["--segment-policy", policy] if policy != "fixed-v1" else []
        start = time.monotonic()
        result = json.loads(subprocess.check_output(
            [*cli, "sync", "--allow", "--source", persona, *args], input=data, env=env))
        if result["segment_policy"] != policy or set(result["added"]) != {msg.item for msg in messages.values()}:
            raise ValueError(f"{policy}/{persona}: incomplete scratch ingest")
        timings[policy][persona] = {"items": len(messages), "seconds": time.monotonic() - start}
        print(f"{policy}/{persona}: ingested {len(messages)} items in "
              f"{timings[policy][persona]['seconds']:.2f}s", flush=True)

    # The API exposes representation IDs for unpacked candidates but not their
    # item paths. Read this mapping once, from the newly ingested scratch store.
    representations = defaultdict(dict)
    with closing(sqlite3.connect(store.as_uri() + "?mode=ro", uri=True)) as db:
        for source, rep, item in db.execute(
                "SELECT json_extract(s.record_json, '$.external_source_id'), lower(hex(i.representation_id)), i.item_id "
                "FROM local_items i JOIN sources s ON s.id = i.source_id WHERE i.active = 1"):
            if rep in representations[source]:
                raise ValueError(f"{source}: ambiguous representation {rep}")
            representations[source][rep] = item
    for persona, messages in histories.items():
        if set(representations[persona].values()) != {msg.item for msg in messages.values()}:
            raise ValueError(f"{persona}: incomplete scratch item mapping")

    token = secrets.token_hex(32)
    config = spool / "principals.json"
    config.write_text(json.dumps({"principals": [{"id": "proxy",
        "token_sha256": sha256(token.encode()).hexdigest(), "read_sources": list(histories),
        "write_sources": []}]}), encoding="utf-8")
    with (spool / "serve.stderr").open("wb") as log, subprocess.Popen(
            [*cli, "serve", "--listen", "127.0.0.1:0", "--config", str(config)],
            env=env, stdout=subprocess.PIPE, stderr=log, text=True) as process:
        try:
            if not select.select([process.stdout], [], [], 30)[0]:
                raise RuntimeError("scratch Mousa daemon did not announce its address")
            address = process.stdout.readline().strip()
            match = re.fullmatch(r"http://127\.0\.0\.1:(\d+)", address)
            if match is None:
                raise RuntimeError(f"scratch Mousa did not bind IPv4 loopback; see {log.name}")
            headers = {"Authorization": "Bearer " + token, "Content-Type": "application/json"}
            with closing(HTTPConnection("127.0.0.1", int(match[1]), timeout=60)) as connection:
                def query(case, budget):
                    connection.request("POST", "/v1/query", json.dumps({
                        "scope": {"source": case.persona}, "text": case.query, "budget_bytes": budget,
                        "client": {"harness": "dolphinbench-proxy"},
                    }), headers)
                    response = connection.getresponse()
                    body = json.loads(response.read())
                    if response.status != 200:
                        raise RuntimeError(f"{case.persona}/{case.id}: query HTTP {response.status}")
                    return body["result"], representations[case.persona]

                yield query
        finally:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()


def fingerprint(value):
    return sha256(json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()).hexdigest()


def run(release, binary, output, temporal_path=None):
    start = time.monotonic()
    histories, cases = load_release(release)
    temporal = load_temporal(temporal_path, histories, cases)
    item_bytes = {msg.item: msg.text.encode() for messages in histories.values() for msg in messages.values()}
    output.mkdir(mode=0o700)
    timings = defaultdict(dict)
    counts = defaultdict(Counter)
    digests = defaultdict(sha256)
    native = defaultdict(sha256)
    with (output / "results.jsonl").open("x", encoding="utf-8") as stream:
        def record(arm, budget, case, packet, candidates, native_digest=None, ranked=None):
            outcome, latest, reasons = score(case, packet, candidates, temporal[case.persona, case.id])
            groups = ("all", case.persona, "one-fact" if len(case.needs) == 1 else "many-facts",
                      "one-message" if len({s.item for g in case.needs for s in g}) == 1 else "many-messages")
            for group in groups:
                counts[arm, budget, group][outcome] += 1
                if latest is not None:
                    counts[arm, budget, group]["latest_total"] += 1
                    counts[arm, budget, group]["latest_correct"] += latest
            row = {"arm": arm, "budget": budget, "persona": case.persona, "id": case.id,
                   "groups": groups, "outcome": outcome, "latest": latest, "reasons": reasons,
                   "packet": [(r["source"], r["byte_start"], r["byte_end"], sha256(r["text"].encode()).hexdigest())
                              for r in packet], "tokens": sum(harness.count_tokens(r["text"]) for r in packet),
                   "bytes": sum(len(r["text"].encode()) for r in packet),
                   "candidates_sha256": fingerprint(ranked if ranked is not None else
                                                    [(s.item, s.start, s.end) for s in candidates])}
            if row["tokens"] > budget or row["bytes"] > budget * 8:
                raise ValueError("packet exceeded its common cap")
            digests[arm].update(bytes.fromhex(fingerprint(row)))
            if native_digest is not None:
                native[arm].update(bytes.fromhex(native_digest))
                row["native_result_sha256"] = native_digest
            stream.write(json.dumps(row, separators=(",", ":")) + "\n")

        for policy in ("fixed-v1", "passage-v1"):
            with mousa(binary, output, policy, histories, timings) as query:
                for case in cases:
                    for budget in BUDGETS:
                        reply, items = query(case, budget * 8)
                        rows, candidates = adapt_reply(reply, items, item_bytes)
                        trail = reply["trail"]
                        ranked = [(s.item, s.start, s.end, c["cost"], c["packed"], c.get("omitted"), c["scores"])
                                  for s, c in zip(candidates, sorted(trail.get("candidates") or [],
                                                                    key=lambda c: c["rank"]))]
                        record(policy, budget, case, harness.pack_skip(rows, budget), candidates,
                               trail["result_sha256"], ranked)
            print(f"{policy}: scored {len(cases) * len(BUDGETS)} packets", flush=True)

        for persona, messages in histories.items():
            index_start = time.monotonic()
            floor = Floor({msg.item: msg.text for msg in messages.values()})
            timings["fts"][persona] = {"items": len(messages), "chunks": len(floor.rows),
                                       "seconds": time.monotonic() - index_start}
            ordered = [unit(msg.item, 0, len(item_bytes[msg.item]), msg.text)
                       for msg in sorted(messages.values(), key=lambda msg: msg.date, reverse=True)]
            ordered_spans = [span(row) for row in ordered]
            with closing(floor.db):
                for case in (case for case in cases if case.persona == persona):
                    ranked = floor.search(case.query)
                    candidates = [span(row) for row in ranked]
                    for budget in BUDGETS:
                        record("fts", budget, case, pack(ranked, budget * 8, budget), candidates)
                        record("recent", budget, case, recent(ordered, budget * 8, budget), ordered_spans)
    summary = {"tests": len(cases), "temporal_tests": sum(bool(v) for v in temporal.values()),
               "ingest": timings, "wall_seconds": time.monotonic() - start,
               "result_digests": {arm: digest.hexdigest() for arm, digest in digests.items()},
               "native_trail_digests": {arm: digest.hexdigest() for arm, digest in native.items()},
               "scores": [{"arm": a, "budget": b, "group": g, **c} for (a, b, g), c in sorted(counts.items())]}
    (output / "summary.json").write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")
    print(f"{len(cases)} tests, {len(cases) * len(BUDGETS) * len(digests)} packets, "
          f"{summary['wall_seconds']:.2f}s wall", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--release", type=Path, required=True)
    parser.add_argument("--mousa", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True, help="new scratch directory")
    parser.add_argument("--temporal", type=Path, help="optional reviewed temporal TSV")
    args = parser.parse_args()
    run(args.release.resolve(strict=True), args.mousa.resolve(strict=True), args.out.absolute(), args.temporal)


if __name__ == "__main__":
    main()
