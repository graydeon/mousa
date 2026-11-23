#!/usr/bin/env python3
"""Check source revision attribution and fail-closed evidence rendering."""

import argparse
import copy
import contextlib
import hashlib
import json
import io
import time
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import memory


def evidence(item, text):
    digest = hashlib.sha256(text.encode()).hexdigest()
    return {"item": item, "representation_sha256": digest, "representation_id": "a" * 64,
            "segment_id": "b" * 64, "content_sha256": digest, "segment_policy": "passage-v1",
            "byte_start": 0, "byte_end": len(text.encode()), "byte_length": len(text.encode()), "text": text}


class HistoryTests(unittest.TestCase):
    def test_invalid_histories_are_rejected_before_startup(self):
        valid = json.loads(Path(memory.__file__).with_name("history.json").read_text())
        mutations = [
            lambda h: h["changes"][0].pop("author"),
            lambda h: h["changes"][0].update(date=None),
            lambda h: h["changes"][0].update(uri=1),
            lambda h: h["documents"][0].update(id=""),
            lambda h: h["documents"][1].update(id=h["documents"][0]["id"]),
            lambda h: h["documents"][0].update(text=None),
            lambda h: h["documents"][0].update(sha256="0" * 64),
            lambda h: h["changes"][0].update(id="missing"),
            lambda h: h["changes"].append(h["changes"][0]),
            lambda h: h["changes"][0].update(deleted=True),
            lambda h: h["changes"][1].update(deleted=1),
            lambda h: h["changes"][1].update(deleted=False),
            lambda h: h.update(documents={}),
            lambda h: h.update(changes=None),
            lambda h: h["documents"][0].update(author="\ud800"),
            lambda h: h["changes"][0].update(uri="\udfff"),
            lambda h: h["documents"][0].update(id="é" * 2049),
            lambda h: h["documents"][0].update(id="nul\x00id"),
            lambda h: h["changes"][0].pop("text"),
        ]
        collision = copy.deepcopy(valid)
        correction = collision["changes"][0]
        original = next(d for d in collision["documents"] if d["id"] == correction["id"])
        correction["text"] = "\ufeff" + original["text"].replace("\n", "\r\n")
        correction["sha256"] = hashlib.sha256(correction["text"].encode()).hexdigest()
        invalid = [[], collision]
        for mutate in mutations:
            history = copy.deepcopy(valid)
            mutate(history)
            invalid.append(history)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for index, history in enumerate(invalid):
                with self.subTest(index=index):
                    path = root / "history.json"
                    path.write_text(json.dumps(history))
                    with self.assertRaises(ValueError):
                        memory.load_history(path)
                    process = subprocess.run([sys.executable, memory.__file__,
                        "--mousa", sys.executable, "--history", str(path),
                        "--store", str(root / "store.sqlite"), "--receipts", str(root / "receipts"),
                        "change"], capture_output=True, timeout=5)
                    self.assertNotEqual(process.returncode, 0)
                    self.assertFalse((root / "receipts").exists())
                    self.assertFalse((root / "store.sqlite").exists())

    def test_native_text_and_frame_limits_fail_before_startup(self):
        history = json.loads(Path(memory.__file__).with_name("history.json").read_text())
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for count, text in ((1, "é" * 131073), (1, "\x01" * 180000), (24, "\x01" * 20000)):
                bad = copy.deepcopy(history)
                for row in bad["documents"][:count]:
                    row["text"] = text
                    row["sha256"] = hashlib.sha256(text.encode()).hexdigest()
                path = root / "history.json"
                path.write_text(json.dumps(bad))
                with self.assertRaises(ValueError):
                    memory.load_history(path)
                result = subprocess.run([sys.executable, memory.__file__, "--mousa", sys.executable,
                    "--history", str(path), "--store", str(root / "store"), "--receipts", str(root / "receipts"),
                    "ingest"], capture_output=True, timeout=5)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse((root / "receipts").exists())
                self.assertFalse((root / "store").exists())

    def test_utf8_boundary_and_compatible_attribution(self):
        history = json.loads(Path(memory.__file__).with_name("history.json").read_text())
        history["documents"][0].update(id="é" * 2048, author=" ", date="任意", uri="é")
        history["changes"] = []
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "history.json"
            path.write_text(json.dumps(history))
            actual, _ = memory.load_history(path)
            self.assertEqual(actual["documents"][0]["id"], "é" * 2048)
            self.assertEqual(actual["documents"][0]["uri"], "é")

class TransportTests(unittest.TestCase):
    def peer(self, body, timeout=0.2):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        path = Path(directory.name) / "peer"
        path.write_text("#!" + sys.executable + "\n" + body)
        path.chmod(0o700)
        client = memory.Client(path, "unused", io.StringIO(), False, timeout, "memory")
        def cleanup():
            if client.process.poll() is None:
                client.process.kill()
            if not client.errors.closed:
                client.close()
        self.addCleanup(cleanup)
        return client

    def test_unread_large_request_obeys_write_deadline(self):
        client = self.peer("import sys\nsys.stdin.buffer.read(1)\nimport signal\nsignal.pause()\n")
        started = time.monotonic()
        with self.assertRaises(TimeoutError):
            client.request("blocked", {"text": "x" * 1000000})
        self.assertLess(time.monotonic() - started, 2)
        self.assertEqual(client.receipts.getvalue(), "")

    def test_partial_writes_and_fragmented_coalesced_responses(self):
        client = self.peer('import sys,json,os\nrequest=json.loads(sys.stdin.buffer.readline())\n'
            'assert len(request["params"]["text"]) == 1000000\n'
            'wire=(json.dumps({"jsonrpc":"2.0","method":"notice"})+"\\n"+'
            'json.dumps({"jsonrpc":"2.0","id":request["id"],"result":{"received":1000000}})+"\\n").encode()\n'
            'os.write(1,wire[:7]);os.write(1,wire[7:])\n', timeout=5)
        os_write = memory.os.write
        partial = []
        def observed_write(fd, data):
            written = os_write(fd, data)
            partial.append(written < len(data))
            return written
        with patch.object(memory.os, "write", observed_write):
            result, _ = client.request("large", {"text": "x" * 1000000})
        self.assertTrue(any(partial))
        self.assertEqual(result, {"received": 1000000})
        receipts = [json.loads(line) for line in client.receipts.getvalue().splitlines()]
        self.assertEqual(len(receipts[0]["message"]["params"]["text"]), 1000000)
        self.assertEqual(receipts[-1]["message"]["result"], result)

    def test_buffered_notifications_cannot_extend_deadline(self):
        client = self.peer('import sys,os,json\nsys.stdin.buffer.readline()\n'
            'wire=(json.dumps({"jsonrpc":"2.0","method":"notice"})+"\\n").encode()*1000\n'
            'while True: os.write(1,wire)\n')
        started = time.monotonic()
        with self.assertRaises(TimeoutError):
            client.request("notifications", {})
        self.assertLess(time.monotonic() - started, 2)

    def test_eof_malformed_and_wrong_shaped_responses_fail(self):
        for wire in ("", "bad\n", "[]\n", '{"jsonrpc":"2.0","id":1}\n',
                     '{"jsonrpc":"2.0","id":1,"error":{},"result":{}}\n'):
            with self.subTest(wire=wire):
                client = self.peer("import sys\nsys.stdin.buffer.readline()\nsys.stdout.write(" + repr(wire) + ")\nsys.stdout.flush()\n")
                with self.assertRaises((RuntimeError, ValueError)):
                    client.request("bad", {})

    def test_receipt_failure_and_nonzero_exit_preserve_owned_cleanup(self):
        client = self.peer("import sys\nsys.stdin.buffer.readline()\nsys.stderr.write('peer failure')\nsys.exit(7)\n")
        class FailedReceipt:
            def write(self, value):
                raise OSError("receipt unavailable")
        client.receipts = FailedReceipt()
        with self.assertRaisesRegex(OSError, "receipt unavailable"):
            client.request("receipt", {})
        report = client.close()
        self.assertEqual(report["exit_code"], 7)
        self.assertFalse(report["forced"])
        self.assertEqual(report["stderr"], "peer failure")
        self.assertTrue(client.process.stdin.closed)
        self.assertTrue(client.process.stdout.closed)
        self.assertTrue(client.errors.closed)
        self.assertFalse(client.capture_complete)

    def test_constructor_setup_failure_reaps_and_closes_owned_pipes(self):
        launch = subprocess.Popen
        class FailedClose:
            def __init__(self, file):
                self.file = file
            def __getattr__(self, name):
                return getattr(self.file, name)
            def close(self):
                self.file.close()
                raise OSError("stdin close failed")
        for fail_close in (False, True):
            with self.subTest(fail_close=fail_close):
                processes, errors = [], []
                def capture(*args, **kwargs):
                    process = launch(*args, **kwargs)
                    if fail_close:
                        process.stdin = FailedClose(process.stdin)
                    processes.append(process)
                    errors.append(kwargs["stderr"])
                    return process
                try:
                    with patch.object(memory.subprocess, "Popen", capture), patch.object(
                            memory.os, "set_blocking", side_effect=OSError("setup failed")):
                        with self.assertRaisesRegex(OSError, "setup failed") as raised:
                            memory.Client(sys.executable, "unused", io.StringIO(), False, 1, "memory")
                    if fail_close:
                        self.assertIn("stdin close failed", str(raised.exception.__cause__))
                    process = processes[0]
                    self.assertIsNotNone(process.returncode)
                    self.assertTrue(process.stdin.closed)
                    self.assertTrue(process.stdout.closed)
                    self.assertTrue(errors[0].closed)
                finally:
                    for file in (processes[0].stdin, processes[0].stdout, errors[0]):
                        if not file.closed:
                            file.close()

    def test_unrelated_ids_do_not_supply_a_result(self):
        client = self.peer('import sys,json,os\nr=json.loads(sys.stdin.readline())\n'
            'wire="".join(json.dumps({"jsonrpc":"2.0","id":ident,"result":{"wrong":True}})+"\\n" for ident in (True,0,99))\n'
            'wire+=json.dumps({"jsonrpc":"2.0","id":r["id"],"result":{"correct":True}})+"\\n"\n'
            'wire+=json.dumps({"jsonrpc":"2.0","id":r["id"]+1,"result":{"premature":True}})+"\\n"\n'
            'os.write(1,wire.encode())\nr=json.loads(sys.stdin.readline())\n'
            'print(json.dumps({"jsonrpc":"2.0","id":r["id"],"result":{"current":True}}),flush=True)\n')
        result, _ = client.request("identity", {})
        self.assertEqual(result, {"correct": True})
        result, _ = client.request("next", {})
        self.assertEqual(result, {"current": True})

    def test_nested_identity_and_empty_tool_results_fail_closed(self):
        for payload, error in ((None, False), ({}, False), ({"source": "other"}, False),
                               ({"source": "memory"}, True)):
            content = {"schema": "mousa.mcp_result.v1", "operation": "mousa_query",
                       "source": "memory", "result": payload}
            if error:
                content["error"] = {}
            envelope = {"structuredContent": content,
                        "content": [{"type": "text", "text": json.dumps(content)}]}
            client = self.peer('import sys,json\nr=json.loads(sys.stdin.readline())\n'
                'print(json.dumps({"jsonrpc":"2.0","id":r["id"],"result":'+repr(envelope)+'}),flush=True)\n')
            with self.assertRaises(ValueError):
                client.call("mousa_query", {})

    def test_query_result_cannot_change_requested_identity(self):
        arguments = {"source": "memory", "query": "cedar", "policy": "original", "budget_bytes": 2048}
        for field, value in (("query", "different"), ("query_policy", "dedup"), ("budget_bytes", 1),
                             ("packing_policy", "exact-v1")):
            result = {"source": "memory", "query": "cedar", "query_policy": "original",
                      "budget_bytes": 2048, "evidence": [], "used_bytes": 0}
            result[field] = value
            content = {"schema": "mousa.mcp_result.v1", "operation": "mousa_query",
                       "source": "memory", "result": result}
            envelope = {"structuredContent": content, "content": [{"type": "text", "text": json.dumps(content)}]}
            client = self.peer('import sys,json\nr=json.loads(sys.stdin.readline())\n'
                'print(json.dumps({"jsonrpc":"2.0","id":r["id"],"result":'+repr(envelope)+'}),flush=True)\n')
            with self.assertRaisesRegex(ValueError, "query result identity mismatch"):
                client.call("mousa_query", arguments)


class RenderingTests(unittest.TestCase):
    def setUp(self):
        self.history, _ = memory.load_history(Path(__file__).with_name("history.json"))

    def test_revision_digest_selects_correction_attribution(self):
        original = self.history["documents"][18]
        correction = self.history["changes"][0]
        old = memory.render({"source": "memory", "evidence": [evidence(original["id"], original["text"])]}, self.history, "schedule")
        new = memory.render({"source": "memory", "evidence": [evidence(correction["id"], correction["text"])]}, self.history, "schedule")
        self.assertEqual(old["passages"][0]["attribution"]["uri"], original["uri"])
        self.assertEqual(new["passages"][0]["attribution"]["uri"], correction["uri"])
        self.assertEqual(new["passages"][0]["attribution"]["date"], correction["date"])
        self.assertIn(correction["text"], new["context"])
        self.assertNotIn(original["text"], new["context"])

    def test_normalized_original_and_correction_keep_revision_attribution(self):
        for ending in ("\r\n", "\r"):
            with self.subTest(ending=ending):
                history = copy.deepcopy(self.history)
                original = history["documents"][18]
                correction = history["changes"][0]
                for row in (original, correction):
                    normalized = row["text"] + "\n\nCafé rehearsal notes."
                    row["text"] = "\ufeff" + normalized.replace("\n", ending)
                    row["sha256"] = hashlib.sha256(row["text"].encode()).hexdigest()
                    hit = evidence(row["id"], normalized)
                    rendered = memory.render({"source": "memory", "evidence": [hit]}, history, "schedule")
                    self.assertEqual(rendered["passages"][0]["attribution"]["uri"], row["uri"])
                    self.assertEqual(rendered["passages"][0]["evidence"]["text"], normalized)
                    tampered = copy.deepcopy(hit)
                    tampered["byte_end"] -= 1
                    with self.assertRaises(RuntimeError):
                        memory.render({"source": "memory", "evidence": [tampered]}, history, "schedule")

    def test_normalization_collision_does_not_choose_an_author(self):
        history = copy.deepcopy(self.history)
        original = history["documents"][18]
        correction = history["changes"][0]
        correction["text"] = "\ufeff" + original["text"].replace("\n", "\r\n")
        correction["sha256"] = hashlib.sha256(correction["text"].encode()).hexdigest()
        with self.assertRaises(ValueError):
            memory.render({"source": "memory", "evidence": [evidence(original["id"], original["text"])]}, history, "schedule")

    def test_tampered_bytes_coordinates_and_item_are_rejected(self):
        document = self.history["documents"][0]
        valid = evidence(document["id"], document["text"])
        mutations = ({"text": document["text"].replace("Save", "Erase")},
                     {"byte_start": 1}, {"item": self.history["documents"][1]["id"]},
                     {"content_sha256": "0" * 64})
        for mutation in mutations:
            with self.subTest(mutation=mutation):
                hit = copy.deepcopy(valid)
                hit.update(mutation)
                with self.assertRaises((ValueError, RuntimeError)):
                    memory.render({"source": "memory", "evidence": [hit]}, self.history, "saving")


class ExecutableTests(unittest.TestCase):
    binary = None

    def test_relative_executable_survives_consumer_restart(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            def invoke(label, command):
                argv = [sys.executable, str(Path(memory.__file__).resolve()),
                        "--mousa", "./" + self.binary.name, "--store", str(root / "memory.sqlite"),
                        "--receipts", str(root / (label + ".jsonl")), *command]
                result = subprocess.run(argv, cwd=self.binary.parent, capture_output=True, text=True, timeout=60)
                self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
                return json.loads(result.stdout)
            invoke("ingest", ["ingest"])
            subprocess.run([self.binary, "-store", root / "memory.sqlite", "access", "--source", "memory", "allow"],
                           capture_output=True, check=True)
            response = invoke("ask", ["ask", "on-disk outbox"])
            target = [p for p in response["rendered"]["passages"] if p["attribution"]["id"] == "entry-01"]
            self.assertEqual([p["evidence"]["text"] for p in target],
                             [memory.load_history(Path(memory.__file__).with_name("history.json"))[0]["documents"][0]["text"]])

    def test_receipt_close_failure_reports_failure_after_native_cleanup(self):
        open_file = Path.open
        class FailedClose:
            def __init__(self, file):
                self.file = file
            def write(self, value):
                return self.file.write(value)
            def flush(self):
                return self.file.flush()
            def close(self):
                self.file.close()
                raise OSError("receipt close unavailable")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            receipt = root / "receipt.jsonl"
            def opening(path, *args, **kwargs):
                file = open_file(path, *args, **kwargs)
                return FailedClose(file) if path == receipt else file
            output = io.StringIO()
            argv = ["memory", "--mousa", str(self.binary), "--store", str(root / "store.sqlite"),
                    "--receipts", str(receipt), "ingest"]
            with patch.object(sys, "argv", argv), patch.object(Path, "open", opening), contextlib.redirect_stdout(output):
                with self.assertRaisesRegex(OSError, "receipt close unavailable"):
                    memory.main()
            report = json.loads(output.getvalue())
            self.assertEqual(report["status"], "FAIL")
            self.assertFalse(report["capture_complete"])
            self.assertEqual(report["process"]["exit_code"], 0)
            self.assertFalse(report["process"]["forced"])
            self.assertEqual(report["result"]["added"], [
                row["id"] for row in memory.load_history(Path(memory.__file__).with_name("history.json"))[0]["documents"]])


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--mousa", type=Path, required=True)
    args, remaining = parser.parse_known_args()
    ExecutableTests.binary = args.mousa.resolve(strict=True)
    unittest.main(argv=[sys.argv[0], *remaining])
