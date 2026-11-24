#!/usr/bin/env python3
"""Forward committed external revisions from a Mousa daemon's verified
request history to a local webhook receiver.

Stdlib only, loopback endpoints only, one-shot bounded run. Reads
MOUSA_OPERATOR_TOKEN (daemon operator credential) and
MOUSA_WEBHOOK_TOKEN (receiver credential) from the environment; the two
must be present and distinct. The cursor file records the last fully
handled history sequence; it is dedicated to exactly one service, one
external source and one receiver — point it elsewhere only by starting a
new cursor, never by reusing or editing the existing one.

    python3 examples/automation/forward_revision.py \
        --mousa http://127.0.0.1:PORT \
        --receiver http://127.0.0.1:PORT/webhook \
        --source EXTERNAL_ID --cursor PATH
"""
from __future__ import annotations

import argparse
import ipaddress
import json
import os
import re
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

EVENT_TYPE = "mousa.document.revised.v1"
MAX_PAGES = 8
PAGE_LIMIT = 64
READ_BOUND = 1 << 20
TIMEOUT = 3.0
RETRY_DELAYS = (0.1, 0.2)
REQUEST_ID = re.compile(r"^[A-Z2-7]{26}$")


class Refusal(Exception):
    """A fixed-label failure safe to print; never carries URLs, responses or secrets."""


def refuse(message: str) -> None:
    raise Refusal(message)


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):  # noqa: ANN001
        return None


_OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}), _NoRedirect)


def _endpoint(raw: str, role: str) -> tuple[str, str]:
    """Validate an endpoint and return (origin, path)."""
    try:
        parsed = urllib.parse.urlsplit(raw)
    except ValueError:
        refuse(f"{role} URL is not parseable")
    if parsed.scheme != "http":
        refuse(f"{role} URL must use http on loopback")
    if parsed.username or parsed.password:
        refuse(f"{role} URL must not embed credentials")
    if parsed.query or parsed.fragment:
        refuse(f"{role} URL must not carry a query or fragment")
    try:
        host = ipaddress.ip_address(parsed.hostname or "")
    except ValueError:
        refuse(f"{role} URL host must be an IP literal")
    if not host.is_loopback:
        refuse(f"{role} URL host must be a loopback address")
    try:
        port = parsed.port
    except ValueError:
        refuse(f"{role} URL port is invalid")
    if port is None:
        refuse(f"{role} URL requires an explicit port")
    hostport = f"[{host}]:{port}" if host.version == 6 else f"{host}:{port}"
    return f"http://{hostport}", parsed.path


def _read_json(response) -> dict:
    data = response.read(READ_BOUND + 1)
    if len(data) > READ_BOUND:
        refuse("reply exceeded the read bound")
    try:
        payload = json.loads(data)
    except ValueError:
        refuse("reply was not JSON")
    if not isinstance(payload, dict):
        refuse("reply was not a JSON object")
    return payload


def _get_json(url: str, token: str, what: str) -> dict:
    request = urllib.request.Request(url, headers={"authorization": f"Bearer {token}"})
    try:
        with _OPENER.open(request, timeout=TIMEOUT) as response:
            return _read_json(response)
    except urllib.error.HTTPError as err:
        err.close()
        refuse(f"{what} refused with status {err.code}")
    except (urllib.error.URLError, OSError):
        refuse(f"{what} unreachable")


def _deliver(receiver: str, token: str, body: bytes, event_id: str) -> bool:
    for attempt in range(len(RETRY_DELAYS) + 1):
        if attempt:
            time.sleep(RETRY_DELAYS[attempt - 1])
        try:
            request = urllib.request.Request(
                receiver, method="POST", data=body,
                headers={
                    "authorization": f"Bearer {token}",
                    "content-type": "application/json",
                    "idempotency-key": event_id,
                },
            )
            with _OPENER.open(request, timeout=TIMEOUT):
                return True
        except urllib.error.HTTPError as err:
            err.close()
            retryable = err.code == 429 or err.code >= 500
        except (urllib.error.URLError, OSError):
            retryable = True
        if not retryable:
            return False
    return False


def _read_cursor(path: Path) -> int:
    try:
        text = path.read_text()
    except FileNotFoundError:
        return 0
    except OSError:
        refuse("cursor is not readable")
    text = text.strip()
    if not text or not text.isdigit():
        refuse("cursor is not a nonnegative integer")
    return int(text)


def _write_cursor(path: Path, seq: int) -> None:
    try:
        temp = tempfile.NamedTemporaryFile(
            mode="w", dir=path.parent, prefix=f".{path.name}.", delete=False)
    except OSError:
        refuse("cursor directory is not writable")
    try:
        temp.write(f"{seq}\n")
        temp.flush()
        os.fsync(temp.fileno())
        temp.close()
        os.chmod(temp.name, 0o600)
        os.replace(temp.name, path)
    except BaseException:
        try:
            os.unlink(temp.name)
        except OSError:
            pass
        raise


def _candidate(entry: dict) -> bool:
    """Cheap history-level filter; the detail fetch re-verifies everything."""
    admission = entry.get("admission") or {}
    outcome = entry.get("outcome") or {}
    evidence_ref = outcome.get("evidence") or {}
    return (
        admission.get("operation") == "document"
        and admission.get("authenticated") is True
        and outcome.get("state") == "committed"
        and bool(evidence_ref.get("item_change_id"))
    )


def _verified_event(entry: dict, mousa: str, token: str, source: str) -> tuple[bytes, str] | None:
    outcome = entry["outcome"]
    request_id = outcome.get("request_id")
    if not isinstance(request_id, str) or not REQUEST_ID.fullmatch(request_id):
        refuse("history carried a request id outside the canonical shape")
    detail = _get_json(f"{mousa}/v1/requests/{request_id}", token, "request detail")
    result = detail.get("result") or {}
    if result.get("outcome") != outcome:
        refuse("verified detail disagrees with the history entry")
    evidence = result.get("evidence") or {}
    change_id = outcome["evidence"]["item_change_id"]
    if evidence.get("kind") != "item_change" or evidence.get("id") != change_id:
        refuse("verified detail does not carry the history's item change")
    current = evidence.get("current")
    previous = evidence.get("previous")
    item_id = evidence.get("item_id")
    if (
        evidence.get("source") != source
        or not isinstance(current, str)
        or not current
        or not isinstance(previous, str)
        or not previous
        or current == previous
        or not isinstance(item_id, str)
    ):
        return None
    event_id = f"{EVENT_TYPE}:{change_id}"
    body = json.dumps({
        "type": EVENT_TYPE,
        "event_id": event_id,
        "request_id": request_id,
        "source": source,
        "item_id": item_id,
        "current_representation_id": current,
        "previous_representation_id": previous,
    }).encode()
    return body, event_id


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--mousa", required=True, help="service origin, e.g. http://127.0.0.1:PORT")
    parser.add_argument("--receiver", required=True, help="webhook URL, e.g. http://127.0.0.1:PORT/webhook")
    parser.add_argument("--source", required=True, help="external source ID whose revisions to forward")
    parser.add_argument("--cursor", required=True, help="cursor file dedicated to this service/source/receiver")
    args = parser.parse_args()
    mousa, service_path = _endpoint(args.mousa, "service")
    if service_path not in ("", "/"):
        refuse("service URL must be the origin only")
    receiver_origin, receiver_path = _endpoint(args.receiver, "receiver")
    if not receiver_path.strip("/"):
        refuse("receiver URL requires a nonempty path")
    receiver = receiver_origin + receiver_path
    operator = os.environ.get("MOUSA_OPERATOR_TOKEN")
    webhook = os.environ.get("MOUSA_WEBHOOK_TOKEN")
    if not operator or not webhook or operator == webhook:
        refuse("distinct MOUSA_OPERATOR_TOKEN and MOUSA_WEBHOOK_TOKEN are required")

    cursor_path = Path(args.cursor)
    cursor = _read_cursor(cursor_path)
    pages = 0
    while pages < MAX_PAGES:
        pages += 1
        page = _get_json(
            f"{mousa}/v1/requests?after={cursor}&limit={PAGE_LIMIT}", operator, "history")
        result = page.get("result")
        entries = result.get("requests") if isinstance(result, dict) else None
        if not isinstance(entries, list):
            refuse("history reply was malformed")
        for entry in entries:
            outcome = entry.get("outcome") or {}
            seq = outcome.get("seq")
            if not isinstance(seq, int) or seq <= cursor:
                continue
            if _candidate(entry):
                emitted = _verified_event(entry, mousa, operator, args.source)
                if emitted is not None:
                    body, event_id = emitted
                    if not _deliver(receiver, webhook, body, event_id):
                        refuse("delivery failed after bounded retries")
            cursor = seq
            _write_cursor(cursor_path, cursor)
        if not result.get("more"):
            break
    else:
        print("forward_revision: page bound reached with history remaining; run again",
              file=sys.stderr)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Refusal as err:
        print(f"forward_revision: {err}", file=sys.stderr)
        raise SystemExit(1)
