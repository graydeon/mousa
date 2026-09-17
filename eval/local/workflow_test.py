"""Run required client acceptance against an explicitly supplied Mousa CLI."""

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from lifecycle import run_process


class ClientWorkflowTest(unittest.TestCase):
    def invoke(self, executable, report, timeout="5"):
        code, stdout, stderr = run_process(
            [sys.executable, str(Path(__file__).with_name("workflow.py")),
             "--example-only", "--mousa", str(executable),
             "--output", str(report), "--timeout", timeout],
            120, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        return subprocess.CompletedProcess([], code, stdout, stderr)

    def test_failure_boundaries(self):
        cases = {
            "nonzero": ("import sys; print('rejected', file=sys.stderr); sys.exit(7)", 7),
            "malformed": ("print('not JSON')", 0),
            "wrong_shape": ("print('[]')", 0),
            "timeout": ("import signal; signal.pause()", 124),
        }
        for name, (body, code) in cases.items():
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                executable = root / "client ; literal"
                executable.write_text(f"#!{sys.executable}\n{body}\n")
                executable.chmod(0o700)
                report = root / "result.json"
                process = self.invoke(executable, report, "0.2" if name == "timeout" else "5")
                self.assertEqual(process.returncode, 1, process.stderr)
                result = json.loads(report.read_text())
                self.assertEqual(result["result"], "FAIL")
                self.assertEqual(len(result["rows"]), 1)
                self.assertEqual(result["rows"][0]["exit_code"], code)
                self.assertFalse(result["rows"][0]["correct"])
                self.assertIn("error", result)

    def test_supported_executable(self):
        if not os.environ.get("MOUSA_EXECUTABLE"):
            self.skipTest("set MOUSA_EXECUTABLE or use workflow_test.py --mousa PATH")
        with tempfile.TemporaryDirectory() as temporary:
            report = Path(temporary) / "result.json"
            process = self.invoke(Path(os.environ["MOUSA_EXECUTABLE"]), report)
            self.assertEqual(process.returncode, 0, process.stderr + process.stdout)
            result = json.loads(report.read_text())
            self.assertEqual(result["result"], "PASS", result.get("error"))
            self.assertEqual({row["engine"] for row in result["rows"]}, {"mousa_example"})
            self.assertEqual({row["segment_policy"] for row in result["rows"]}, {"fixed-v1", "passage-v1"})
            for policy in ("fixed-v1", "passage-v1"):
                rows = {row["stage"]: row for row in result["rows"] if row["segment_policy"] == policy}
                for stage, outcome in (("query", "evidence"), ("deleted_query", "no_matches"),
                                       ("budget_omitted", "budget_omitted"),
                                       ("denied_query", "policy_excluded"),
                                       ("withdrawn_query", "lifecycle_excluded"),
                                       ("directory_deleted_query", "no_matches")):
                    self.assertEqual(rows[stage]["response"]["outcome"], outcome)
                self.assertEqual(rows["updated_query"]["response"]["evidence"][0]["text"],
                                 "Cedar launch moved to Friday.")
                self.assertNotIn("historical", rows["denied_trail"]["response"])
                self.assertEqual(rows["cross_source_trail"]["exit_code"], 1)
                for stage in ("query", "directory_query", "policy_restored_query", "directory_restored_query"):
                    self.assertTrue(all(hit["segment_policy"] == policy for hit in rows[stage]["response"]["evidence"]))
        code, stdout, stderr = run_process(
            [sys.executable, str(Path(__file__).resolve().parents[2] / "examples/docs/docs_test.py"),
             "--mousa", os.environ["MOUSA_EXECUTABLE"]],
            120, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        self.assertEqual(code, 0, stderr + stdout)
        code, stdout, stderr = run_process(
            [sys.executable, str(Path(__file__).resolve().parents[2] / "examples/backup/backup_test.py"),
             "--mousa", os.environ["MOUSA_EXECUTABLE"]],
            120, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        self.assertEqual(code, 0, stderr + stdout)


class RequiredClientResult(unittest.TextTestResult):
    def wasSuccessful(self):
        return super().wasSuccessful() and not self.skipped


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mousa", type=Path, required=True,
                        help="path to the built supported Mousa executable")
    args = parser.parse_args()
    if sys.version_info < (3, 9) or sys.platform != "linux":
        parser.error("client acceptance requires Python 3.9 or later on Linux")
    try:
        executable = args.mousa.resolve(strict=True)
    except OSError as error:
        parser.error(f"cannot resolve Mousa executable: {error}")
    if not executable.is_file() or not os.access(executable, os.R_OK | os.X_OK):
        parser.error(f"Mousa executable must be a readable executable file: {executable}")
    os.environ["MOUSA_EXECUTABLE"] = str(executable)
    suite = unittest.defaultTestLoader.loadTestsFromTestCase(ClientWorkflowTest)
    result = unittest.TextTestRunner(verbosity=2, resultclass=RequiredClientResult).run(suite)
    if result.skipped:
        print("Required client acceptance cannot pass with skipped tests", file=sys.stderr)
    return int(not result.wasSuccessful())


if __name__ == "__main__":
    raise SystemExit(main())
