#!/usr/bin/env python3
"""Compare local CLI lifecycle behavior on a generated, evolving UTF-8 corpus."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import statistics
import subprocess
import tempfile
import time


def run_process(args, timeout, **kwargs):
    process = subprocess.Popen(args, start_new_session=True, **kwargs)
    try:
        stdout, stderr = process.communicate(timeout=timeout)
        return process.returncode, stdout, stderr
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        stdout, stderr = process.communicate()
        return 124, stdout, stderr


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--baseline', type=Path, required=True, help='baseline mousa binary')
    parser.add_argument('--candidate', type=Path, required=True, help='candidate mousa binary')
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--documents', type=int, nargs='+', default=[24, 96])
    parser.add_argument('--repeats', type=int, default=3)
    parser.add_argument('--timeout', type=float, default=15)
    args = parser.parse_args()
    if any(n < 1 or n > 96 for n in args.documents) or args.repeats < 1 or args.timeout <= 0:
        parser.error('documents must be 1..96; repeats and timeout must be positive')
    binaries = {'baseline': args.baseline.resolve(), 'candidate': args.candidate.resolve()}
    time_binary = shutil.which('time')
    rows = []
    stores = []
    generations = ['epochzero', 'epochone', 'epochtwo', 'epochthree', 'epochfour']

    def text(item, generation):
        return f'{generation} marker{item:03d} evidence about the harbor maintenance schedule.\n'

    with tempfile.TemporaryDirectory(prefix='mousa-lifecycle-') as temporary:
        work = Path(temporary)
        root = work / 'corpus'
        root.mkdir()
        for count in args.documents:
            for repeat in range(args.repeats):
                order = ['baseline', 'candidate'] if repeat % 2 == 0 else ['candidate', 'baseline']
                for arm in order:
                    for file in root.iterdir():
                        file.unlink()
                    store = work / f'{arm}-{count}-{repeat}.sqlite'

                    def command(operation, stage, query=None, expected_items=None, expected_generation=None, expected_action=None, expected_count=None):
                        invocation = [str(binaries[arm]), '-store', str(store), operation, str(root)]
                        if query is not None:
                            invocation.append(query)
                        rss_path = work / 'rss.txt'
                        rss_path.unlink(missing_ok=True)
                        executed = invocation
                        if time_binary:
                            executed = [time_binary, '-f', '%M', '-o', str(rss_path), *invocation]
                        started = time.monotonic()
                        code, stdout, stderr = run_process(executed, args.timeout, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
                        elapsed = time.monotonic() - started
                        response = json.loads(stdout) if code == 0 else None
                        peak_rss = None
                        if rss_path.exists():
                            resource_lines = rss_path.read_text().splitlines()
                            if resource_lines and resource_lines[-1].isdigit():
                                peak_rss = int(resource_lines[-1])
                        row = {'arm': arm, 'documents': count, 'repeat': repeat, 'stage': stage,
                               'operation': operation, 'query': query, 'wall_seconds': elapsed,
                               'peak_rss_kib': peak_rss, 'exit_code': code, 'correct': code == 0,
                               'stderr': stderr}
                        if response is not None:
                            if operation == 'sync':
                                actions = {name: len(response.get(name) or []) for name in ['added', 'updated', 'restored', 'unchanged', 'deleted', 'absent']}
                                row['actions'] = actions
                                if expected_action is not None:
                                    row['correct'] = actions[expected_action] == expected_count
                                    if expected_action == 'unchanged':
                                        row['correct'] = row['correct'] and sum(actions.values()) == expected_count
                            else:
                                hits = response.get('evidence') or []
                                items = [hit['item'] for hit in hits]
                                row['returned_items'] = items
                                row['used_bytes'] = response['used_bytes']
                                row['budget_bytes'] = response['budget_bytes']
                                row['correct'] = (set(items) == expected_items and len(items) == len(expected_items)
                                                  and sum(len(hit['text'].encode()) for hit in hits) == response['used_bytes']
                                                  and response['used_bytes'] <= response['budget_bytes'])
                                if expected_generation is not None:
                                    row['correct'] = row['correct'] and all(hit['text'].startswith(expected_generation + ' ') for hit in hits)
                        rows.append(row)
                        print(json.dumps(row), flush=True)

                    all_items = {f'doc{i:03d}.md' for i in range(count)}
                    for i in range(count):
                        (root / f'doc{i:03d}.md').write_text(text(i, generations[0]))
                    command('sync', 'initial', expected_action='added', expected_count=count)
                    command('sync', 'noop-1', expected_action='unchanged', expected_count=count)
                    command('query', 'current-1', generations[0], all_items, generations[0])
                    for revision, generation in enumerate(generations[1:], start=2):
                        for i in range(count):
                            (root / f'doc{i:03d}.md').write_text(text(i, generation))
                        command('sync', f'update-{revision}', expected_action='updated', expected_count=count)
                        command('sync', f'noop-{revision}', expected_action='unchanged', expected_count=count)
                        command('query', f'current-{revision}', generation, all_items, generation)
                        command('query', f'stale-{revision}', generations[revision - 2], set())
                    (root / 'doc000.md').unlink()
                    command('sync', 'delete', expected_action='deleted', expected_count=1)
                    (root / 'doc000.md').write_text(text(0, generations[-1]))
                    command('sync', 'restore', expected_action='restored', expected_count=1)
                    command('query', 'restored-query', 'marker000', {'doc000.md'}, generations[-1])
                    stores.append({'arm': arm, 'documents': count, 'repeat': repeat, 'bytes': store.stat().st_size})

    summary = []
    for arm in binaries:
        for count in args.documents:
            stages = sorted({row['stage'] for row in rows})
            for stage in stages:
                samples = [row for row in rows if row['arm'] == arm and row['documents'] == count and row['stage'] == stage]
                summary.append({'arm': arm, 'documents': count, 'stage': stage, 'samples': len(samples),
                                'median_wall_seconds': statistics.median(row['wall_seconds'] for row in samples),
                                'max_wall_seconds': max(row['wall_seconds'] for row in samples),
                                'correct_samples': sum(row['correct'] for row in samples)})
    result = {'protocol': 'mousa-local-lifecycle-v1', 'documents': args.documents, 'repeats': args.repeats,
              'revisions': len(generations), 'timeout_seconds': args.timeout,
              'measurement': 'cold CLI processes; full startup and JSON output included; builds excluded; no cache dropping; alternating arm order',
              'peak_rss_method': 'GNU time maximum resident set size, KiB' if time_binary else 'NOT RUN: GNU time unavailable',
              'binary_sha256': {arm: hashlib.sha256(path.read_bytes()).hexdigest() for arm, path in binaries.items()},
              'rows': rows, 'summary': summary, 'stores': stores}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, indent=2) + '\n')
    print('LIFECYCLE_RESULTS=' + json.dumps(result), flush=True)
    return int(any(row['arm'] == 'candidate' and not row['correct'] for row in rows))


if __name__ == '__main__':
    raise SystemExit(main())
