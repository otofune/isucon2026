#!/usr/bin/env python3
"""run.py の出力から API、SQL 差分、サーバー資源の要約を生成する。"""
import argparse
from collections import Counter
import csv
from datetime import datetime
import json
from pathlib import Path
import re
import statistics
import subprocess

GROUPS = ','.join([
    r'^/api/images/[^/]+$', r'^/api/posts/[^/]+/comments$', r'^/api/posts/[^/]+/realmojis$',
    r'^/api/posts/[0-9a-f-]{36}$', r'^/api/users/[^/]+$',
    r'^/api/comments/[^/]+$', r'^/api/realmojis/[^/]+$',
    r'^/api/friend-requests/[^/]+/accept$', r'^/api/friend-requests/[^/]+/reject$',
    r'^/api/friend-requests/[^/]+/cancel$', r'^/api/friendships/[^/]+$',
    r'^/api/follows/[^/]+$', r'^/api/blocks/[^/]+$', r'^/api/memories/[^/]+$',
])


def query_delta(directory):
    with (directory / 'queries-before.csv').open() as source:
        before = {row['queryid']: row for row in csv.DictReader(source)}
    with (directory / 'queries-after.csv').open() as source:
        after = list(csv.DictReader(source))
    result = []
    counters = ['calls', 'total_exec_time', 'rows', 'shared_blks_hit',
                'shared_blks_read', 'temp_blks_written', 'blk_read_time', 'blk_write_time', 'wal_bytes']
    for row in after:
        previous = before.get(row['queryid'], {})
        delta = {key: float(row[key])-float(previous.get(key, 0)) for key in counters}
        if delta['calls'] <= 0:
            continue
        result.append({'queryid': row['queryid'], 'query': row['query'], **delta,
                       'mean_exec_time': delta['total_exec_time']/delta['calls']})
    return sorted(result, key=lambda row: row['total_exec_time'], reverse=True)


def host_summary(directory, start=None, end=None):
    cpu, iowait, available, rx, tx = [], [], [], [], []
    def in_window(parts):
        return not start or start <= parts[0] < end
    for line in (directory / 'cpu.txt').read_text().splitlines():
        parts = line.split()
        if len(parts) == 12 and re.match(r'^\d\d:', parts[0]) and parts[1] == 'all' and in_window(parts):
            cpu.append(100-float(parts[-1]))
            iowait.append(float(parts[5]))
    header = None
    for line in (directory / 'memory.txt').read_text().splitlines():
        parts = line.split()
        if 'kbavail' in parts:
            header = parts
        elif header and len(parts) == len(header) and re.match(r'^\d\d:', parts[0]) and in_window(parts):
            available.append(float(parts[header.index('kbavail')])/1024)
    for line in (directory / 'network.txt').read_text().splitlines():
        parts = line.split()
        if len(parts) >= 6 and re.match(r'^\d\d:', parts[0]) and parts[1] not in ('lo', 'IFACE') and in_window(parts):
            rx.append(float(parts[4])/1024)
            tx.append(float(parts[5])/1024)
    return {
        'cpu_samples': len(cpu), 'cpu_busy_mean_pct': statistics.mean(cpu) if cpu else None,
        'cpu_busy_max_pct': max(cpu) if cpu else None,
        'cpu_busy_peak_10s_pct': max(statistics.mean(cpu[i:i+10]) for i in range(max(1, len(cpu)-9))) if cpu else None,
        'cpu_samples_over_90_pct': sum(value >= 90 for value in cpu),
        'iowait_mean_pct': statistics.mean(iowait) if iowait else None,
        'available_min_mb': min(available) if available else None,
        'rx_max_mib_s': max(rx) if rx else None, 'tx_max_mib_s': max(tx) if tx else None,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('directory', type=Path)
    parser.add_argument('--alp', default='/tmp/isureal-profile-bin/alp')
    parser.add_argument('--from-time', help='アクセスの完了時刻で絞る ISO 8601 時刻')
    parser.add_argument('--until-time', help='区間の終端。終端の時刻は含まない')
    args = parser.parse_args()
    if bool(args.from_time) != bool(args.until_time):
        parser.error('--from-time と --until-time は両方指定してください')
    start = datetime.fromisoformat(args.from_time) if args.from_time else None
    end = datetime.fromisoformat(args.until_time) if args.until_time else None
    if start and (start >= end or start.date() != end.date()):
        parser.error('区間は同じ UTC 日付の開始 < 終了で指定してください')
    tag = '-window' if start else ''
    sources = {}
    for host in ('asia1', 'europe1', 'america1'):
        source = args.directory / host / 'access.jsonl'
        if start:
            filtered = args.directory / host / 'access-window.jsonl'
            with filtered.open('w') as target:
                for line in source.read_text().splitlines():
                    row = json.loads(line)
                    if start <= datetime.fromisoformat(row['time']) < end:
                        target.write(line + '\n')
            source = filtered
        sources[host] = source
    combined = args.directory / ('access' + tag + '-all.jsonl')
    with combined.open('w') as target:
        for host in ('asia1', 'europe1', 'america1'):
            target.write(sources[host].read_text())
    for name, source in [('all', combined), *sources.items()]:
        with (args.directory / ('alp' + tag + '-' + name + '.md')).open('w') as output:
            subprocess.run([args.alp, 'json', '--file', str(source), '--nosave-pos',
                            '--sort', 'sum', '--reverse', '--format', 'markdown',
                            '--percentiles', '50,95,99', '-o', 'count,method,uri,min,avg,p99,max,sum',
                            '--matching-groups', GROUPS], stdout=output, check=True)
    queries = query_delta(args.directory / 'asia2')
    (args.directory / 'queries-delta.json').write_text(json.dumps(queries, ensure_ascii=False, indent=2) + '\n')
    hosts = {host: host_summary(args.directory/host) for host in ('asia1', 'europe1', 'america1', 'asia2')}
    waits, blockers, states = Counter(), Counter(), Counter()
    for line in (args.directory / 'asia2/db-activity.jsonl').read_text().splitlines():
        for backend in json.loads(line).get('backends') or []:
            states[backend['state'] or 'unknown'] += 1
            if backend['state'] != 'active':
                continue
            waits[(backend['wait_type'] or 'Running') + ':' + (backend['wait_event'] or '')] += 1
            if backend['blocking']:
                blockers[re.sub(r'\s+', ' ', backend['query']).strip()] += 1
    summary = {'hosts': hosts, 'backend_state_samples': dict(states),
               'active_backend_samples': dict(waits), 'blocked_queries': dict(blockers)}
    if start:
        summary['window'] = {'start': args.from_time, 'end': args.until_time,
                             'hosts': {host: host_summary(args.directory/host, start.strftime('%H:%M:%S'), end.strftime('%H:%M:%S')) for host in hosts}}
    (args.directory / ('summary' + tag + '.json')).write_text(json.dumps(summary, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    print('Top SQL deltas: seconds, calls, mean_ms, rows, query')
    for query in queries[:20]:
        print(f"{query['total_exec_time']/1000:.3f}\t{query['calls']:.0f}\t{query['mean_exec_time']:.3f}\t{query['rows']:.0f}\t" + re.sub(r'\s+', ' ', query['query'])[:300])


if __name__ == '__main__':
    main()
