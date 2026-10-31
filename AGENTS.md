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
2. `system/common/` の設定ファイルを対応する `/etc/` のパスへコピーし、構文を確認して `systemctl daemon-reload` する。
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

設定はこのリポジトリで編集し、push / pull 後に `restart.sh` でコピーする。リポジトリへの symlink は作らない。
コピー前のファイルは各台の `/var/backups/isucon2026/<日時>-<PID>/etc/` に所有者・権限ごと保存する。
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
`/home/isucon/env.sh`、秘密鍵、証明書の秘密鍵、DB実データは Git に入れない。
`isucon-register`・`envcheck` 関連、`isuadmin`、固定リージョン、ルーター設定は変更しない。
