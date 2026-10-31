# ISUCON2026

競技インスタンスの `/home/isucon/webapp` から取り込んだ ISUReal の初期実装です。
レギュレーションは [docs/regulation.txt](docs/regulation.txt)、当日マニュアルは
[docs/manual.md](docs/manual.md) を参照してください。

| ディレクトリ | 内容 |
| --- | --- |
| `webapp/go` | Go |
| `webapp/nodejs` | Node.js / TypeScript |
| `webapp/perl` | Perl |
| `webapp/php` | PHP |
| `webapp/python` | Python |
| `webapp/ruby` | Ruby |
| `webapp/rust` | Rust |
| `webapp/sql` | スキーマ・初期化処理 |
| `webapp/public` | 配布された静的ファイル |

依存ライブラリ、ビルド済みバイナリ、キャッシュなどの再生成できるファイルは Git 管理から除外しています。
各言語の依存関係を固定するロックファイルと、配布された静的ファイルは含めています。
各台の `/home/isucon/env.sh` は `system/<台名>/home/isucon/env.sh` に平文で収録しています。
ローカルで編集した設定は `restart.sh` で対象の台へコピーします。
DB の実データと、各台の `~/.ssh/` にあるデプロイ秘密鍵は収録していません。

## インスタンスでの更新

各インスタンスの `isucon` ユーザーに専用の読み取り専用デプロイキーを設定しています。
作業コピーは `/home/isucon/isucon2026` にあります。

```sh
ssh isu2026-1  # asia1。2: europe1、3: america1、4: asia2
cd /home/isucon/isucon2026
git pull --ff-only
./restart.sh
```

Git の接続先は `git@github.com:otofune/isucon2026.git`、ブランチは `master` です。
専用鍵と GitHub のホスト鍵は各インスタンスの `~/.ssh/` に保存し、
作業コピーの `core.sshCommand` で指定しています。
同じインスタンス上の別の場所に clone する場合は、設定済みの作業コピーを参照します。

```sh
git clone --config core.sshCommand="$(git -C /home/isucon/isucon2026 config core.sshCommand)" \
  git@github.com:otofune/isucon2026.git
```

アプリサーバーでは `./restart.sh` が `/home/isucon/webapp` へのコピー、必要なビルド、
設定のコピーと `daemon-reload`、アプリ再起動、nginx の reload を行います。
設定のみの反映は `./restart.sh --config-only`、対象台の PostgreSQL 設定を反映して再起動する場合は
`./restart.sh --postgresql` を使います。詳しい反映手順は [AGENTS.md](AGENTS.md) を参照してください。
