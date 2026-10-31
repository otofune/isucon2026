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

もしくは

```
ssh isu2026-asia1
ssh isu2026-europe1
ssh isu2026-america1
ssh isu2026-asia2
```

### サーバーの IP アドレスについて

```tsv
name	Public IPv4	Private IPv4
asia1	13.159.156.72	192.168.1.10
europe1	54.248.98.5	192.168.2.10
america1	16.76.105.190	192.168.3.10
asia2	13.196.113.52	192.168.4.10
```

#### ルーターIP

- IP 35.77.209.211
- URL http://35.77.209.211:8080

## 調査の進め方

ファイルから分かることは、実際に該当ファイルを確認すること。
構成やサービスの配置は変更されるため、この文書の記述だけで決めつけず、設定ファイルと対象台の稼働状態を確認する。

## ファイルの場所

- `webapp/`: 各言語の実装、SQL、静的ファイル。
- `restart.sh`: 実装・設定の反映処理。処理内容とオプションはファイルを確認する。
- `system/common/etc/systemd/system/`: アプリの systemd unit。
- `system/common/etc/nginx/`: nginx 設定。
- `system/<台名>/etc/postgresql/16/main/`: PostgreSQL 設定と認証ルール。
- `system/<台名>/home/isucon/env.sh`: 台別の環境設定。
- `system/hosts.tsv`: ホスト名と台名の対応。

## 実装・設定の反映

ローカルで変更を commit / push し、対象台の `/home/isucon/isucon2026` で以下を実行する。

```sh
git pull --ff-only
./restart.sh
```

設定のみの反映は `./restart.sh --config-only`、PostgreSQL の反映・再起動は対象台で `./restart.sh --postgresql` を使う。
反映先と手順を変える場合は `restart.sh` も更新する。

このリポジトリは大会用の非公開リポジトリ。`.env` や `env.sh` の接続情報は平文で管理する。
設定ファイルを一律に除外せず、`.gitignore` は依存ライブラリ・キャッシュ・ビルド成果物などの再生成できるファイルを中心にする。
