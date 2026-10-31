# ISUCON2026

## レギュレーション

まず、レギュレーションを閲覧してください。
docs/regulation.txt(総論), docs/manual.md(細部)

## SSH 接続方法

```
ssh isu2026-1 # asia1
ssh isu2026-2 # europe1
ssh isu2026-3 # america1
ssh isu2026-4 # asia2
```

### サーバーの IP アドレスについて

name	Public IPv4	Private IPv4
asia1	13.159.156.72	192.168.1.10
europe1	54.248.98.5	192.168.2.10
america1	16.76.105.190	192.168.3.10
asia2	13.196.113.52	192.168.4.10

#### ルーターIP

- IP 35.77.209.211
- URL http://35.77.209.211:8080

## 実装の反映

リポジトリは `otofune/isucon2026`、通常使用するブランチは `master`。
各台の作業コピーは `/home/isucon/isucon2026`、実際の稼働先は `/home/isucon/webapp`。
各台には読み取り専用デプロイキーを設定済み。push はローカルから行う。

アプリケーションを変更したら、ローカルで commit / push し、対象のアプリサーバーで以下を実行する。
`git pull` だけでは稼働先に反映されない。

```sh
ssh isu2026-1  # europe1 は isu2026-2、america1 は isu2026-3
cd /home/isucon/isucon2026
git pull --ff-only
./restart.sh
```

`restart.sh` は稼働中の `isureal-{言語}.service` を検出し、次の処理を行う。

1. Go / Rust の場合は新しいバイナリをビルドする。
2. 台別の `env.sh` を `/home/isucon/env.sh` へ、`system/common/` の設定を対応する `/etc/` のパスへコピーし、構文を確認して `systemctl daemon-reload` する。
3. `webapp/` を `/home/isucon/webapp/` に rsync する。依存ライブラリ、既存のバイナリ、実行時ファイルは削除しない。
4. 対象言語のバイナリを置き換えるか依存ライブラリをインストールし、アプリを restart、nginx を reload する。
5. 認証なしの `GET /api/me` が `401` を返すことを確認する。

asia1 / europe1 / america1 の3台へ順番に反映する。稼働言語の切り替えは当日マニュアルに従う。
スクリプトはサービスの enable / disable や言語切り替えを行わない。
DB 初期化やベンチマークはデプロイ処理に含めない。

## systemd・nginx・PostgreSQL 設定

- `system/common/etc/systemd/system/isureal-{言語}.service`: 全7言語の unit。
- `system/common/etc/nginx/`: nginx 本体と ISUReal のサイト設定。
- `system/asia2/etc/postgresql/16/main/`: asia2 の PostgreSQL 設定と認証ルール。
- `system/{asia1,europe1,america1,asia2}/home/isucon/env.sh`: 各台のアプリ環境設定。大会用の接続情報も平文で管理する。
- `system/hosts.tsv`: ホスト名と台名の対応。

設定はこのリポジトリで編集し、push / pull 後に `restart.sh` でコピーする。リポジトリへの symlink は作らない。
コピー前のファイルは各台の `/var/backups/isucon2026/<日時>-<PID>/` 以下に、元のディレクトリ構成・所有者・権限ごと保存する。
失敗時はこのバックアップと `journalctl -u isureal-go.service` などを確認する。
設定の検証に失敗しても自動復元はしない。必要なら元の設定を戻してから `daemon-reload` する。

設定のコピーと `daemon-reload` だけを行う場合（サービスの restart / reload は行わない）:

```sh
./restart.sh --config-only
```

asia2 は共有DBサーバー。PostgreSQL 設定を変更した場合は、asia2 上で明示的に再起動する。
この操作中は全リージョンのDB接続に影響がある。

```sh
ssh isu2026-4
cd /home/isucon/isucon2026
git pull --ff-only
./restart.sh --postgresql
```

`--config-only` と `--postgresql` は、稼働中の PostgreSQL を検出した台でのみ `system/asia2/` の設定をコピーする。
新しい設定ファイルを管理対象に追加した場合は、`restart.sh` のコピー対象も追加する。
`isucon-register`・`envcheck` 関連、`isuadmin`、固定リージョン、ルーター設定は変更しない。

## 環境設定の変更

このリポジトリは大会用の非公開リポジトリ。`.env` や `env.sh` の接続情報は平文で管理する。
ローカルで対象の台の `system/<台名>/home/isucon/env.sh` を編集し、commit / push 後にその台で pull / `restart.sh` を実行する。
環境変数は systemd の `EnvironmentFile` と SQL 初期化スクリプトが読む。
`KEY=value` 形式で記述し、空白を含む値は引用符で囲む。`export` やシェル変数展開は使わない。
`ISUREAL_REGION` は固定リージョンと一致させる（スクリプトでも検証する）。
`--config-only` は環境ファイルもコピーするが、稼働中のプロセスへの反映には再起動が必要。
`ISUREAL_DB_PASSWORD` を書き換えても PostgreSQL のロールのパスワード自体は変更されない。

`.gitignore` は依存ライブラリ・キャッシュ・ビルド成果物などの再生成できるファイルを中心にする。
設定ファイルを一律に除外しない。デプロイ秘密鍵は各台の `~/.ssh/` にあり、アプリの環境設定とは別に扱う。
