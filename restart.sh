#!/usr/bin/env bash
# 各台の isucon ユーザーで git pull --ff-only の後に実行する。
set -Eeuo pipefail
cd "$(dirname "$0")"
export PATH="/home/isucon/local/go/bin:$PATH"

instance=$(awk -v host="$(hostname -s)" '$1 == host {print $2}' system/hosts.tsv)

# 既存ファイルの所有者と権限は cp では変わらない。
sudo cp -r system/common/. "system/${instance:?unknown host}/." /
sudo systemctl daemon-reload

# アプリ台の PostgreSQL は初期画像の読み取り専用ストア。再起動後も起動するよう有効にする。
if [[ $instance == asia2 ]]; then
  if systemctl is-active --quiet postgresql@16-main.service; then
    sudo systemctl restart postgresql@16-main.service
  fi
else
  sudo systemctl enable --quiet postgresql.service
  sudo systemctl start postgresql@16-main.service
fi

# 落ちている台にも反映できるよう、is-active ではなく is-enabled で判定する。
if systemctl is-enabled --quiet isureal-go.service; then
  (cd webapp/go && go build -o isureal ./cmd/isureal)
  rsync -a webapp/ /home/isucon/webapp/
  sudo nginx -t
  sudo systemctl restart isureal-go.service nginx.service
  status=$(curl -s -o /dev/null -w '%{http_code}' --retry 10 --retry-connrefused --retry-delay 1 http://127.0.0.1/api/me)
  [[ $status == 401 ]]
fi
