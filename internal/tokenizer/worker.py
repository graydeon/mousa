"""Bounded ordinary-text counter; vocabulary preparation is a separate operation."""
import importlib.metadata
import os
import socket
import struct
import sys

MAX_BYTES = 524288
MAX_WORK = 33554432
MAX_CALLS = 102


def unavailable(*args, **kwargs):
    raise RuntimeError("offline tokenizer requires a prepared vocabulary cache")


def main():
    import tiktoken
    import tiktoken.load

    if importlib.metadata.version("tiktoken") != "0.12.0":
        raise RuntimeError("requires tiktoken==0.12.0")
    if not os.environ.get("TIKTOKEN_CACHE_DIR"):
        raise RuntimeError("requires explicit prepared cache")
    # The pinned loader verifies its expected SHA256 on a cache hit. A missing
    # or corrupt entry must stop at read_file, never fetch its public URL.
    socket.socket = unavailable
    tiktoken.load.read_file = unavailable
    encoder = tiktoken.get_encoding("o200k_base")
    stdin, stdout = sys.stdin.buffer, sys.stdout.buffer
    stdout.write(b"mousa.tokens.v1/tiktoken/0.12.0/o200k_base/ordinary\n")
    stdout.flush()
    work = 0
    for _ in range(MAX_CALLS):
        header = stdin.read(4)
        if not header:
            return
        if len(header) != 4:
            raise RuntimeError("truncated input")
        size = struct.unpack(">I", header)[0]
        work += size
        if size > MAX_BYTES or work > MAX_WORK:
            raise RuntimeError("tokenizer input bound")
        data = stdin.read(size)
        if len(data) != size:
            raise RuntimeError("truncated input")
        count = len(encoder.encode_ordinary(data.decode("utf-8", errors="strict")))
        stdout.write(struct.pack(">Q", count))
        stdout.flush()
    if stdin.read(1):
        raise RuntimeError("tokenizer request bound")


if __name__ == "__main__":
    try:
        main()
    except Exception:
        # Never echo input, paths or exception content to a caller.
        sys.exit(1)
