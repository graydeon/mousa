#!/usr/bin/env python3
"""Acceptance for forward_revision.py against a real daemon and a
deterministic local receiver. Stdlib only; no network beyond loopback.

Run: python3 examples/automation/forward_revision_test.py --mousa ./mousa
"""
from __future__ import annotations

import argparse
import hashlib
import http.server
import json
import os
import secrets
import signal
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request
from pathlib import Path

SCRIPT = Path(__file__).with_name("forward_revision.py")
BODY_BOUND = 1 << 20


def fail(message: str) -> None:
    print(f"forward_revision_test: FAIL {message}", file=sys.stderr)
    raise SystemExit(1)


def run_cli(binary: str, store: str, *args: str, stdin: bytes = b"") -> dict:
    proc = subprocess.run(
        [binary, "-store", store, *args], input=stdin,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60,
    )
    if proc.returncode != 0:
        fail(f"cli {' '.join(args)} -> {proc.returncode}: {proc.stderr[:400]!r}")
    return json.loads(proc.stdout)


def post(url: str, token: str, body: dict) -> tuple[int, dict, bytes]:
    request = urllib.request.Request(
        url, method="POST", data=json.dumps(body).encode(),
        headers={"content-type": "application/json", "authorization": f"Bearer {token}"},
    )
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with opener.open(request, timeout=10) as response:
            headers = {key.lower(): val for key, val in response.headers.items()}
            return response.status, headers, response.read(BODY_BOUND)
    except urllib.error.HTTPError as err:
        headers = {key.lower(): val for key, val in err.headers.items()}
        return err.code, headers, err.read(BODY_BOUND)


def get(url: str, token: str) -> dict:
    request = urllib.request.Request(url, headers={"authorization": f"Bearer {token}"})
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(request, timeout=10) as response:
        return json.loads(response.read(BODY_BOUND))


class Receiver(http.server.BaseHTTPRequestHandler):
    """Deterministic webhook endpoint: separate verifier, idempotent by
    event_id, simulates an applied effect with a lost ack on first delivery."""

    attempts: list[dict] = []
    effects: set[str] = set()
    mode = "fail_first"  # fail_first | fail_all

    def do_POST(self) -> None:  # noqa: N802 - stdlib handler name
        length = int(self.headers.get("content-length") or "0")
        body = self.rfile.read(length) if 0 < length <= BODY_BOUND else b""
        auth = self.headers.get("authorization") or ""
        idem = self.headers.get("idempotency-key") or ""
        Receiver.attempts.append({"path": self.path, "auth": auth, "idem": idem, "body": body})
        if self.path == "/redirect":
            self.send_response(302)
            self.send_header("location", "/target")
            self.send_header("content-length", "0")
            self.end_headers()
            return
        if auth != f"Bearer {self.server.webhook_token}":
            self._reply(401)
            return
        try:
            event = json.loads(body)
        except (ValueError, UnicodeDecodeError):
            self._reply(400)
            return
        if not isinstance(event, dict) or event.get("event_id") != idem or not idem:
            self._reply(400)
            return
        if self.mode == "fail_all":
            self._reply(500)
            return
        if event["event_id"] not in self.effects:
            # The effect applies exactly once; the lost ack forces a retry
            # that must not apply it again.
            self.effects.add(event["event_id"])
            self._reply(503)
            return
        self._reply(200)

    def _reply(self, status: int) -> None:
        self.send_response(status)
        self.send_header("content-length", "0")
        self.end_headers()

    def log_message(self, *_args: object) -> None:
        pass


def start_daemon(binary: str, store: str, config: Path) -> tuple[subprocess.Popen, str]:
    proc = subprocess.Popen(
        [binary, "-store", store, "serve", "--listen", "127.0.0.1:0", "--config", str(config)],
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
    )
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        line = proc.stdout.readline() if proc.stdout else ""
        if line.startswith("http://"):
            return proc, line.strip()
        if proc.poll() is not None:
            fail(f"daemon exited {proc.returncode}: {proc.stderr.read()[:400]}")
    proc.kill()
    fail("daemon did not announce its address")
    raise AssertionError


def adapter(binary_env: dict, mousa: str, receiver: str, source: str, cursor: Path) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, str(SCRIPT), "--mousa", mousa, "--receiver", receiver,
         "--source", source, "--cursor", str(cursor)],
        env=binary_env, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        text=True, timeout=120,
    )


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mousa", required=True, help="path to the mousa binary")
    args = parser.parse_args()
    binary = str(Path(args.mousa).resolve())
    if not os.path.isfile(binary):
        fail(f"mousa binary missing: {binary}")
    if not SCRIPT.is_file():
        fail(f"forward_revision entrypoint absent: {SCRIPT}")

    work = Path(tempfile.mkdtemp(prefix="forward-revision-"))
    store = str(work / "store")
    run_cli(binary, store, "access", "--source", "docs", "--create", "allow")
    run_cli(binary, store, "access", "--source", "secret", "--create", "allow")
    run_cli(binary, store, "access", "--source", "memory", "--create", "allow")
    run_cli(binary, store, "sync", "--source", "docs",
            stdin=b'{"id":"seed.md","text":"Seed document first revision."}\n'
                  b'{"id":"tomb.md","text":"Tombstone candidate."}\n')
    run_cli(binary, store, "sync", "--source", "secret",
            stdin=b'{"id":"ghost.md","text":"Outside source first revision."}\n')

    op_token = secrets.token_hex(24)
    member_token = secrets.token_hex(24)
    webhook_token = secrets.token_hex(24)
    config = work / "native.json"
    config.write_text(json.dumps({"principals": [
        {"id": "op", "token_sha256": hashlib.sha256(op_token.encode()).hexdigest(),
         "read_sources": ["docs", "secret", "memory"],
         "write_sources": ["docs", "secret", "memory"], "operator": True},
        {"id": "member", "token_sha256": hashlib.sha256(member_token.encode()).hexdigest(),
         "read_sources": ["docs"], "write_sources": []},
    ]}))
    config.chmod(0o600)

    receiver = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Receiver)
    receiver.webhook_token = webhook_token
    threading.Thread(target=receiver.serve_forever, daemon=True).start()
    receiver_base = f"http://127.0.0.1:{receiver.server_address[1]}"

    daemon, base = start_daemon(binary, store, config)
    try:
        posts = [
            post(f"{base}/v1/documents", op_token, {"source": "secret", "record": {"id": "ghost.md", "text": "Outside source second revision."}}),
            post(f"{base}/v1/documents", op_token, {"source": "docs", "record": {"id": "brand.md", "text": "A new item, not a revision."}}),
            post(f"{base}/v1/documents", op_token, {"source": "docs", "record": {"id": "seed.md", "text": "Seed document second revision."}}),
            post(f"{base}/v1/documents", op_token, {"source": "docs", "record": {"id": "seed.md", "text": "Seed document second revision."}}),
            post(f"{base}/v1/notes", op_token, {"scope": {"source": "memory"}, "text": "unrelated note"}),
            post(f"{base}/v1/documents", member_token, {"source": "docs", "record": {"id": "denied.md", "text": "denied write"}}),
            post(f"{base}/v1/documents", op_token, {"source": "docs", "record": {"id": "tomb.md", "deleted": True}}),
            post(f"{base}/v1/documents", op_token, {"source": "docs", "record": {"id": "tomb.md", "text": "Tombstone restored."}}),
        ]
        statuses = [p[0] for p in posts]
        if statuses != [200, 200, 200, 200, 200, 403, 200, 200]:
            fail(f"seed matrix statuses {statuses}")
        delivered_id = posts[2][1].get("x-mousa-request-id", "")
        if not delivered_id:
            fail("revision reply carried no request id")

        env = dict(os.environ)
        env["MOUSA_OPERATOR_TOKEN"] = op_token
        env["MOUSA_WEBHOOK_TOKEN"] = webhook_token
        cursor = work / "cursor"

        # Phase A: the receiver never acknowledges; the run fails bounded and
        # leaves the cursor before the failed delivery.
        Receiver.mode = "fail_all"
        proc = adapter(env, base, receiver_base + "/webhook", "docs", cursor)
        if proc.returncode == 0:
            fail(f"always-failing receiver exited 0: {proc.stdout}{proc.stderr}")
        if Receiver.effects:
            fail(f"failed delivery applied an effect: {Receiver.effects}")
        if cursor.is_file():
            left = int(cursor.read_text().strip())
        else:
            left = 0
        history = get(f"{base}/v1/requests?limit=128", op_token)["result"]["requests"]
        delivered_seq = next(
            e["outcome"]["seq"] for e in history if e["outcome"]["request_id"] == delivered_id)
        if left >= delivered_seq:
            fail(f"cursor {left} advanced past failed delivery seq {delivered_seq}")

        # Phase B: first delivery applies once and loses the ack; the retry
        # is acknowledged without a second effect.
        Receiver.attempts.clear()
        Receiver.mode = "fail_first"
        proc = adapter(env, base, receiver_base + "/webhook", "docs", cursor)
        if proc.returncode != 0:
            fail(f"retryable receiver exited {proc.returncode}: {proc.stderr[:400]}")
        if len(Receiver.effects) != 1:
            fail(f"effects = {Receiver.effects}, want exactly one")
        event_attempts = [a for a in Receiver.attempts if a["path"] == "/webhook" and a["body"]]
        if len(event_attempts) != 2:
            fail(f"delivery attempts = {len(event_attempts)}, want 2")
        if event_attempts[0]["body"] != event_attempts[1]["body"]:
            fail("retry body differed from first attempt")
        if event_attempts[0]["auth"] != event_attempts[1]["auth"]:
            fail("retry auth differed from first attempt")
        event = json.loads(event_attempts[0]["body"])
        allowed = {"type", "event_id", "request_id", "source", "item_id",
                   "current_representation_id", "previous_representation_id"}
        if set(event) != allowed:
            fail(f"event fields {sorted(event)}")
        if event["type"] != "mousa.document.revised.v1":
            fail(f"event type {event['type']!r}")
        change_id = event["event_id"].removeprefix("mousa.document.revised.v1:")
        if not change_id or event["event_id"] != f"mousa.document.revised.v1:{change_id}":
            fail(f"event id shape {event['event_id']!r}")
        if event["request_id"] != delivered_id:
            fail(f"event request {event['request_id']!r} != served {delivered_id!r}")
        if event["source"] != "docs" or event["item_id"] != "seed.md":
            fail(f"event identity {event}")
        if (not event["current_representation_id"] or not event["previous_representation_id"]
                or event["current_representation_id"] == event["previous_representation_id"]):
            fail(f"event revision ids {event}")
        for attempt in event_attempts:
            if attempt["auth"] != f"Bearer {webhook_token}":
                fail("receiver saw a credential that is not the webhook token")
            if attempt["idem"] != event["event_id"]:
                fail("idempotency key disagrees with event id")
        if op_token in event_attempts[0]["body"].decode() or op_token in (event_attempts[0]["auth"]):
            fail("receiver saw the operator credential")

        # Phase C: a bounded rerun consumes nothing and delivers nothing.
        attempts_before = len(Receiver.attempts)
        proc = adapter(env, base, receiver_base + "/webhook", "docs", cursor)
        if proc.returncode != 0:
            fail(f"replay run exited {proc.returncode}: {proc.stderr[:400]}")
        if len(Receiver.attempts) != attempts_before:
            fail("rerun delivered a previously acked event")

        # Phase D: unsafe endpoints are refused before any request is sent.
        for label, mousa_url, recv_url in (
            ("non-loopback service", "http://192.0.2.1:8080", receiver_base + "/webhook"),
            ("non-loopback receiver", base, "http://192.0.2.1:8080/hook"),
            ("https scheme", "https://127.0.0.1:443", receiver_base + "/webhook"),
            ("missing port", "http://127.0.0.1", receiver_base + "/webhook"),
            ("userinfo", "http://u:p@127.0.0.1:8080", receiver_base + "/webhook"),
            ("service path", base + "/v1", receiver_base + "/webhook"),
            ("empty receiver path", base, receiver_base),
        ):
            proc = adapter(env, mousa_url, recv_url, "docs", work / f"cursor-{label[:6]}")
            if proc.returncode == 0:
                fail(f"{label} accepted: {proc.stdout}")
        # A redirect is never followed.
        proc = adapter(env, base, receiver_base + "/redirect", "docs", work / "cursor-redirect")
        if proc.returncode == 0:
            fail("redirect receiver accepted")
        if any(a["path"] == "/target" for a in Receiver.attempts):
            fail("adapter followed a redirect into /target")
        # Missing and equal tokens refuse before contacting anything.
        broken = dict(env)
        del broken["MOUSA_WEBHOOK_TOKEN"]
        if adapter(broken, base, receiver_base + "/webhook", "docs", work / "c1").returncode == 0:
            fail("missing webhook token accepted")
        broken["MOUSA_WEBHOOK_TOKEN"] = op_token
        if adapter(broken, base, receiver_base + "/webhook", "docs", work / "c2").returncode == 0:
            fail("equal tokens accepted")
        # Nothing the adapter printed carries either credential.
        if op_token in proc.stdout + proc.stderr or webhook_token in proc.stdout + proc.stderr:
            fail("adapter output leaked a credential")
        print("forward_revision_test: PASS")
    finally:
        receiver.server_close()
        daemon.send_signal(signal.SIGINT)
        try:
            daemon.wait(timeout=10)
        except subprocess.TimeoutExpired:
            daemon.kill()


if __name__ == "__main__":
    main()
