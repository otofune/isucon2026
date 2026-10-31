#!/usr/bin/env python3
"""4台の計測を開始し、bench スキルで1走行してログを回収する。"""
import argparse
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
import json
from pathlib import Path
import shlex
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
HOSTS = {'asia1': 'isu2026-1', 'europe1': 'isu2026-2', 'america1': 'isu2026-3', 'asia2': 'isu2026-4'}
ACCESS_LOG = '/var/log/nginx/isureal-profile.json'
DB_LOG = '/var/log/postgresql/postgresql-16-main.log'


def ssh(host, command, **kwargs):
    return subprocess.run(['ssh', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=8', host, command],
                          check=True, text=True, **kwargs)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('note')
    args = parser.parse_args()
    run = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ')
    output = ROOT / 'artifacts' / 'profiles' / run
    output.mkdir(parents=True)
    remote = '/tmp/isureal-profile-' + run
    commit = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
    metadata = {'run': run, 'commit': commit, 'note': args.note, 'hosts': HOSTS}
    (output / 'metadata.json').write_text(json.dumps(metadata, indent=2) + '\n')
    processes, offsets, handles = {}, {}, []

    def prepare(item):
        name, host = item
        directory = output / name
        directory.mkdir()
        ssh(host, 'mkdir -p ' + shlex.quote(remote))
        subprocess.run(['scp', '-q', str(ROOT / 'tools/profile/capture.py'),
                        str(ROOT / 'tools/profile/queries.sql'), host + ':' + remote + '/'], check=True)
        with (directory / 'environment.txt').open('w') as env:
            ssh(host, 'date -Is; git -C /home/isucon/isucon2026 rev-parse HEAD; free -m; nproc', stdout=env)
        log = DB_LOG if name == 'asia2' else ACCESS_LOG
        offsets[name] = int(ssh(host, 'sudo -n stat -c %s ' + shlex.quote(log), capture_output=True).stdout)
        if name == 'asia2':
            with (directory / 'queries-before.csv').open('w') as before:
                ssh(host, 'sudo -n -u postgres psql -X -v ON_ERROR_STOP=1 -d isureal -f ' + remote + '/queries.sql', stdout=before)
        handle = (directory / 'capture-output.txt').open('w')
        handles.append(handle)
        command = ['python3', remote + '/capture.py', remote, '--seconds', '420']
        if name == 'asia2':
            command.append('--database')
        processes[name] = subprocess.Popen(['ssh', '-o', 'BatchMode=yes', host, shlex.join(command)],
                                          stdout=handle, stderr=subprocess.STDOUT)

    def collect(item):
        name, host = item
        directory = output / name
        ssh(host, 'if test -f {0}/capture.pid; then kill -TERM $(cat {0}/capture.pid); fi'.format(shlex.quote(remote)))
        processes[name].wait(timeout=15)
        subprocess.run(['scp', '-qr', host + ':' + remote + '/.', str(directory)], check=True)
        log = DB_LOG if name == 'asia2' else ACCESS_LOG
        with (directory / ('slow.log' if name == 'asia2' else 'access.jsonl')).open('w') as target:
            ssh(host, 'sudo -n tail -c +' + str(offsets[name] + 1) + ' ' + shlex.quote(log), stdout=target)
        if name == 'asia2':
            with (directory / 'queries-after.csv').open('w') as after:
                ssh(host, 'sudo -n -u postgres psql -X -v ON_ERROR_STOP=1 -d isureal -f ' + remote + '/queries.sql', stdout=after)
            table_query = "COPY (SELECT relname,n_live_tup,seq_scan,seq_tup_read,idx_scan,n_tup_ins,n_tup_upd,n_tup_del FROM pg_stat_user_tables ORDER BY seq_tup_read DESC) TO STDOUT WITH CSV HEADER"
            with (directory / 'tables.csv').open('w') as tables:
                ssh(host, shlex.join(['sudo', '-n', '-u', 'postgres', 'psql', '-X', '-d', 'isureal', '-c', table_query]), stdout=tables)

    try:
        with ThreadPoolExecutor(max_workers=4) as pool:
            list(pool.map(prepare, HOSTS.items()))
        print('Capturing to ' + str(output), flush=True)
        with (output / 'bench.txt').open('w') as bench:
            process = subprocess.Popen([sys.executable, str(ROOT / '.claude/skills/bench/scripts/bench.py'),
                                        commit[:7] + ' ' + args.note], cwd=ROOT,
                                       stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
            for line in process.stdout:
                print(line, end='', flush=True)
                bench.write(line)
                bench.flush()
            result = process.wait()
    finally:
        with ThreadPoolExecutor(max_workers=4) as pool:
            # 部分的な準備失敗でも起動済みの collector を止める。
            list(pool.map(collect, [(name, HOSTS[name]) for name in processes]))
        for handle in handles:
            handle.close()
    print('Saved ' + str(output), flush=True)
    return result


if __name__ == '__main__':
    sys.exit(main())
