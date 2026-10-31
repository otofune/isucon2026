# ベンチマークの計測

nginx の JSON ログを alp で集計し、DB は `pg_stat_statements` と100ms以上のスローログを使う。4台の CPU・メモリ・プロセス・ディスク・ネットワークを sysstat で1秒間隔で記録する。

CloudWatch Agent は2026年10月31日の確認時点で4台とも未導入。sysstat は導入済みで、短い負荷走行の時系列を直接取得できる。

## ツール

```sh
GOBIN=/tmp/isureal-profile-bin go install github.com/tkuchiki/alp@v1.0.22
GOBIN=/tmp/isureal-profile-bin go install github.com/mazrean/isucrud@v1.5.0
```

[alp](https://github.com/tkuchiki/alp) は回数・累積時間・分位値を API 単位で集計する。[isucrud](https://github.com/mazrean/isucrud) は Go の関数とテーブル操作を静的に対応付ける。isucrud の結果にアクセス頻度は含まれず、動的に構築する SQL を取りこぼす場合もあるので、実測とコードを突き合わせる。

```sh
cd webapp/go
/tmp/isureal-profile-bin/isucrud -dst ../../artifacts/profiles/db-access.md ./...
```

## 計測上の注意

- 設定を反映した commit と、実際に稼働するコードを記録する。
- 初期化・準備・負荷走行・最終チェックを区別する。特に SSE の request_time は通知待機時間を含むため、CPU を消費した時間として加算しない。
- nginx のログは完了したリクエストが対象。負荷終了時に中断された要求や、ベンチの採点成功件数とは一致しない。
- `pg_stat_statements` は DB 内の処理・待機時間を集計する。ネットワーク、接続プール待ち、アプリ処理は別に見る。
- CPU が低いことだけで余裕があるとは判断しない。`pg_stat_activity` のロック・ClientWrite・接続数、ネットワーク転送量も照合する。
- メモリは free だけでなく available とプロセス RSS を見る。DB の page cache は即座にメモリ不足を意味しない。
- SQL 統計は計測前後の差分を取り、関係ない走行と混ぜない。スローログには短いが多数回の SQL が出ないので、総時間・呼出回数は SQL 統計で見る。

設定は [nginx.conf](../../system/common/etc/nginx/nginx.conf) と [PostgreSQL 設定](../../system/asia2/etc/postgresql/16/main/conf.d/isucon.conf)。`pg_stat_statements` の初回有効化には PostgreSQL の再起動と `CREATE EXTENSION IF NOT EXISTS pg_stat_statements` が必要。

生ログは `artifacts/profiles/` に保存し、Git 管理から除外する。結果の要約と改善の優先順位は `docs/` に残す。
