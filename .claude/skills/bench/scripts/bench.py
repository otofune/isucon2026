#!/usr/bin/env python3
"""ベンチマークをエンキューし、結果が出るまで待つ。終了コードは SKILL.md を参照。"""
import argparse
import json
import sys
import time
import urllib.error
import urllib.request

EXIT_OK, EXIT_FAILED, EXIT_NOT_CLICKED, EXIT_LOST, EXIT_NO_SERVER, EXIT_TIMEOUT = range(6)
# クリック後の stale は、ジョブが遅れて表に出れば enqueued に戻る。
CLICKED_STALE_GRACE = 60
JOB_FAILED = ("ERRORED", "CANCELLED")

opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def api(server, path, body=None):
    data = None if body is None else json.dumps(body).encode()
    request = urllib.request.Request(server + path, data=data, headers={"Content-Type": "application/json"})
    with opener.open(request, timeout=10) as response:
        return json.load(response)


def finish(code, enqueue, message):
    print(message, flush=True)
    print(json.dumps({"exit_code": code, "enqueue": enqueue}, ensure_ascii=False), flush=True)
    sys.exit(code)


def describe(enqueue):
    job = enqueue.get("job")
    return f'{enqueue["status"]} job=#{job["id"]} {job["status"]} score={job["score"]}' if job else enqueue["status"]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("note", nargs="?", default="")
    parser.add_argument("--server", default="http://127.0.0.1:8787")
    parser.add_argument("--interval", type=float, default=5)
    parser.add_argument("--timeout", type=float, default=300)
    args = parser.parse_args()

    try:
        enqueue = api(args.server, "/enqueues", {"note": args.note})
    except (urllib.error.URLError, OSError) as err:
        finish(EXIT_NO_SERVER, None, f"bench-enqueue サーバーに接続できません: {err}")
    print(f'enqueue #{enqueue["id"]} を要求しました', flush=True)

    deadline = time.monotonic() + args.timeout
    last, stale_since = None, None
    while True:
        try:
            enqueue = next(e for e in api(args.server, "/enqueues") if e["id"] == enqueue["id"])
        except StopIteration:
            finish(EXIT_LOST, enqueue, "要求が一覧から消えました。サーバーが再起動された可能性があります")
        except (urllib.error.URLError, OSError) as err:
            print(f"一覧の取得に失敗しました: {err}", flush=True)
        else:
            if describe(enqueue) != last:
                last = describe(enqueue)
                print(last, flush=True)

            job = enqueue.get("job")
            if job and job["status"] == "FINISHED":
                finish(EXIT_OK, enqueue, f'完了: job #{job["id"]} score={job["score"]}')
            if job and job["status"] in JOB_FAILED:
                finish(EXIT_FAILED, enqueue, f'失敗: job #{job["id"]} {job["status"]}')
            if enqueue["status"] == "stale":
                if not enqueue.get("clicked_at"):
                    finish(EXIT_NOT_CLICKED, enqueue, "クリックされないまま stale になりました")
                stale_since = stale_since or time.monotonic()
                if time.monotonic() - stale_since >= CLICKED_STALE_GRACE:
                    finish(EXIT_LOST, enqueue, "クリック後にジョブを確認できません。ポータルを確認してください")
            else:
                stale_since = None

        if time.monotonic() >= deadline:
            finish(EXIT_TIMEOUT, enqueue, f"{args.timeout:.0f} 秒以内に終わりませんでした: {describe(enqueue)}")
        time.sleep(args.interval)


if __name__ == "__main__":
    main()
