# DolphinBench retrieval proxy

This measures whether annotated history makes it into a packet. It does not run
agents, execute app actions, use a model or judge, or produce a DolphinBench
leaderboard score.

Use Python 3.10+ with PyYAML and SQLite FTS5. Use a local checkout of the
DolphinBench `single_message_release` layout and a built Mousa binary. The
adapter reads data files, not code from the benchmark checkout.

```sh
python3 -I -B eval/dolphinbench/test_proxy.py
python3 -I -B eval/dolphinbench/proxy.py \
  --release /path/to/dolphinbench --mousa /path/to/mousa \
  --out /path/to/new-scratch-run
```

`--out` must not exist. The adapter initializes fresh stores there and queries
authenticated ephemeral loopback daemons. It does not use the configured
Mousa client or modify any other store. `summary.json` contains counts,
ingest timings and result digests. `results.jsonl` records each packet's ranked
item ranges and text digests, with the provenance needed to recover exact text
from the pinned history. Keep these outputs outside the repository.

## Measurement

- Each released session becomes one UTF-8 item. One persona is one source.
  Keep the timestamp and speaker and exclude generator metadata. Query with
  the harness's dated user message, unchanged.
- Arms are Mousa `fixed-v1`, Mousa `passage-v1`, 450-token SQLite FTS5 chunks
  with the milestone query and BM25 policy, and a newest-first contiguous
  message window. FTS chunks preserve exact offsets and whitespace; they do not
  cross item boundaries.
- Each packet has both the nominal byte budget (8,192, 32,768 or 131,072) and
  the common milestone regex cap (1,024, 4,096 or 16,384 pieces). Ranked arms
  apply the byte cap before the regex cap and skip pieces that do not fit. The
  recent window stops at the first message that does not fit.
- Each required fact is covered only if the packet contains the complete
  user text of all explicitly linked messages. Neighboring packed ranges in
  the same item can supply that text together. A hit covers all required
  facts; partial covers some but not all; miss covers none. This deliberately
  measures full annotated evidence, not just an item reference or paraphrase.
- Optionally pass `--temporal` with a reviewed UTF-8 TSV, no header:
  `persona`, `test_id`, `new_session_id`, `new_exact_text`, `old_session_id`,
  `old_exact_text`. Repeat for each known old value. Each new span must come
  from a required message. Latest-correct requires every new span in the
  packet and strictly ahead of any paired old span that is also present. A
  span's rank is where its last needed byte arrives. Unlabeled tests have no
  temporal verdict.
- `result_digests` compare retrieval and scoring across fresh runs.
  `native_trail_digests` fingerprint the native result IDs separately.
  The native trail binds audit identities such as timestamped access records,
  so its hash is not a cross-store packet digest.
