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

依存ライブラリ、ビルド済みバイナリ、キャッシュ、実行時ファイル、秘密情報は Git 管理から除外しています。
各言語の依存関係を固定するロックファイルと、配布された静的ファイルは含めています。
DB の実データ、インスタンス固有の `/home/isucon/env.sh`、デプロイ秘密鍵は含めていません。

## インスタンスでの更新

各インスタンスの `isucon` ユーザーに専用の読み取り専用デプロイキーを設定しています。
作業コピーは `/home/isucon/isucon2026` にあります。

```sh
ssh isu2026-1  # asia1。2: europe1、3: america1、4: asia2
cd /home/isucon/isucon2026
git pull --ff-only
```

Git の接続先は `git@github.com:otofune/isucon2026.git`、ブランチは `master` です。
専用鍵と GitHub のホスト鍵は各インスタンスの `~/.ssh/` に保存し、
作業コピーの `core.sshCommand` で指定しています。
同じインスタンス上の別の場所に clone する場合は、設定済みの作業コピーを参照します。

```sh
git clone --config core.sshCommand="$(git -C /home/isucon/isucon2026 config core.sshCommand)" \
  git@github.com:otofune/isucon2026.git
```

稼働中のアプリケーションは引き続き `/home/isucon/webapp` を使用します。
この作業コピーでの `git pull` 後、稼働中のアプリケーションへの反映は別途必要です。
