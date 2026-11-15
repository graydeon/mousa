"""Check indexed content before resuming; a match does not guard later work."""
import argparse
import io
import json
from pathlib import Path
import subprocess
import tempfile

from memory.memory import Client


def require_match(result):
    if (result.get("decision_outcome") != "allow" or
            not isinstance(result.get("manifest"), dict) or result.get("matches") is not True):
        raise RuntimeError("Refusing to resume: source denied, incomplete or changed")


def refusal(result):
    try:
        require_match(result)
    except RuntimeError:
        return
    raise AssertionError("unsafe resumption accepted")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mousa", required=True, type=Path)
    binary = parser.parse_args().mousa.resolve()
    with tempfile.TemporaryDirectory(prefix="mousa-manifest-") as temporary:
        root = Path(temporary)
        store = root / "evidence.sqlite"
        directory = root / "notes"
        directory.mkdir()
        document = directory / "note.txt"
        document.write_text("cedar first revision\n", encoding="utf-8")

        def native(*arguments, stdin="", exit_code=0):
            completed = subprocess.run([str(binary), "-store", str(store), *map(str, arguments)],
                                       input=stdin, capture_output=True, text=True, timeout=30)
            if completed.returncode != exit_code:
                raise RuntimeError((arguments, completed.returncode, completed.stderr))
            return json.loads(completed.stdout)

        native("sync", directory)
        first = native("manifest", directory)["manifest"]
        require_match(native("manifest", "--expect", first["id"], directory))
        document.write_text("cedar changed but not yet synchronized\n", encoding="utf-8")
        require_match(native("manifest", "--expect", first["id"], directory))
        native("sync", directory)
        changed = native("manifest", "--expect", first["id"], directory, exit_code=1)
        refusal(changed)
        assert changed["matches"] is False and changed["manifest"]["id"] != first["id"]
        document.unlink()
        native("sync", directory)
        deleted = native("manifest", directory)["manifest"]
        assert deleted["items"] == [] and deleted["id"] != changed["manifest"]["id"]

        source = "manifest-example"
        records = [{"id": "note", "text": "cedar JSONL revision"}, {"id": "empty", "text": ""}]
        native("sync", "--source", source, stdin="".join(json.dumps(row) + "\n" for row in records))
        baseline = native("manifest", "--source", source)["manifest"]
        native("sync", "--source", source, stdin=json.dumps(records[0]) + "\n")
        require_match(native("manifest", "--source", source, "--expect", baseline["id"]))

        def mcp_check(expected, denied=False):
            client = Client(binary, store, io.StringIO(), False, 30, source)
            try:
                client.initialize()
                result = client.call("mousa_source_manifest", dict(source=source, expected_manifest_id=expected))[0]
                if denied:
                    refusal(result)
                    assert set(result) == {"decision_id", "decision_outcome", "decision_reasons"}
                else:
                    require_match(result)
                    assert result["manifest"]["id"] == expected
            finally:
                cleanup = client.close()
                assert cleanup == dict(exit_code=0, forced=False, stderr="", cleanup_errors=[]), cleanup

        mcp_check(baseline["id"])
        native("sync", "--source", source, stdin='{"id":"note","deleted":true}\n')
        refusal(native("manifest", "--source", source, "--expect", baseline["id"], exit_code=1))
        native("access", "--source", source, "deny")
        denied = native("manifest", "--source", source, "--expect", baseline["id"], exit_code=1)
        refusal(denied)
        assert "manifest" not in denied and "matches" not in denied
        mcp_check(baseline["id"], denied=True)
        print(json.dumps(dict(result="PASS", directory_id=first["id"], jsonl_id=baseline["id"],
                             boundary="Indexed snapshots only; no lease, action-completion, query-stability or model-window guarantee."), indent=2))


if __name__ == "__main__":
    main()
