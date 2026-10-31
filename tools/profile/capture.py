#!/usr/bin/env python3
"""競技サーバーで sysstat と PostgreSQL の待機状態を記録する。"""
import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('directory', type=Path)
    parser.add_argument('--seconds', type=int, default=420)
    parser.add_argument('--database', action='store_true')
    args = parser.parse_args()
    args.directory.mkdir(parents=True, exist_ok=True)
    pidfile = args.directory / 'capture.pid'
    pidfile.write_text(str(os.getpid()))
    stopped = False

    def stop(_signum, _frame):
        nonlocal stopped
        stopped = True

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    env = {**os.environ, 'LC_ALL': 'C', 'S_TIME_FORMAT': 'ISO'}
    processes, files = [], []
    commands = {
        'cpu.txt': ['mpstat', '-P', 'ALL', '1'],
        'processes.txt': ['pidstat', '-h', '-u', '-r', '-d', '-p', 'ALL', '1'],
        'disk.txt': ['iostat', '-y', '-x', '-t', '1'],
        'network.txt': ['sar', '-n', 'DEV', '1'],
        'memory.txt': ['sar', '-r', 'ALL', '1'],
        'vmstat.txt': ['vmstat', '-t', '1'],
    }
    try:
        for name, command in commands.items():
            output = (args.directory / name).open('w')
            files.append(output)
            processes.append(subprocess.Popen(command, stdout=output, stderr=subprocess.STDOUT, env=env))
        activity = (args.directory / 'db-activity.jsonl').open('w')
        files.append(activity)
        query = """
SELECT json_build_object('at', clock_timestamp(), 'backends', (
  SELECT json_agg(json_build_object(
    'pid', pid, 'client', client_addr, 'state', state,
    'wait_type', wait_event_type, 'wait_event', wait_event,
    'seconds', extract(epoch FROM clock_timestamp()-query_start),
    'blocking', pg_blocking_pids(pid), 'query', left(query, 500)))
  FROM pg_stat_activity WHERE datname='isureal' AND pid<>pg_backend_pid()
))
"""
        deadline = time.monotonic() + args.seconds
        while not stopped and time.monotonic() < deadline:
            started = time.monotonic()
            if args.database:
                result = subprocess.run(
                    ['sudo', '-n', '-u', 'postgres', 'psql', '-XAt', '-d', 'isureal', '-c', query],
                    capture_output=True, text=True, timeout=5, env=env,
                )
                if result.returncode:
                    activity.write(json.dumps({'error': result.stderr}) + '\n')
                else:
                    activity.write(result.stdout)
                activity.flush()
            time.sleep(max(0, 1-(time.monotonic()-started)))
    finally:
        for process in processes:
            process.terminate()
        for process in processes:
            process.wait(timeout=10)
        for output in files:
            output.close()
        pidfile.unlink(missing_ok=True)


if __name__ == '__main__':
    main()
