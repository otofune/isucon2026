---
name: bench
description: ISUCON2026 のポータルでベンチマーク（負荷走行）をエンキューし、完了まで待ってスコアを報告する。必要に応じて tools/profile で4台の計測も同時に行い、API・SQL・サーバー資源を集計する。ユーザーが「ベンチを回して」「負荷走行して」「スコアを確認して」「エンキューして」「プロファイルして」「ボトルネックを調べて」「計測して」「alp を見て」「スロークエリを調べて」と言ったとき、または依頼された改善の効果測定でベンチマークの実行が必要なときに使う。tools/bench-enqueue のローカルサーバーを経由し、ポータルの Enqueue はブラウザ拡張が押す。
---

# bench

`tools/bench-enqueue` のサーバーへ要求を出し、ブラウザ拡張がポータルの Enqueue を押して、結果が出るまで待つ。

## 手順

1. **反映を確かめる。** ベンチマークは各台で動いているコードを計測する。測りたい変更が commit / push され、対象台で `git pull --ff-only` と `./restart.sh` まで済んでいることを確認する。未反映なら、古いコードのスコアを新しい結果と取り違えないよう、先に反映するかユーザーに確認する。

2. **実行する。** 完了まで数分かかり、前のジョブの待ちも入るので、Bash の `run_in_background` で実行する。終了すると通知が来るので、途中でポーリングしない。

   ```sh
   python3 .claude/skills/bench/scripts/bench.py "$(git rev-parse --short HEAD) feedのN+1修正"
   ```

   引数は何を測ったかのメモ。commit と変更内容が分かるように書く。

3. **結果を報告する。** 最終行の JSON と終了コードを見て、スコア、ジョブ ID、状態、ポータルの URL を伝える。比較できるときは、`curl -s http://127.0.0.1:8787/enqueues` の過去の結果との差も添える。

   スコアの増減やエラーの原因を調べるときは、ベンチマーカーの行動ログを読む。詳細ページの HTML は `tools/bench-enqueue/data/<ジョブID>.html` に残る。

   ```sh
   curl -s http://127.0.0.1:8787/jobs/<ジョブID>/conclusion
   ```

## 計測付きで実行する

ボトルネックの調査や、改善前後の比較を求められたときは、手順2の `bench.py` の代わりに `tools/profile/run.py` を使う。スコアを見るだけなら `bench.py` で足りる。`run.py` は4台の計測を開始し、`bench.py` で1走行してからログを回収する。commit は自動で記録されるので、メモに書くのは内容だけでよい。

```sh
python3 tools/profile/run.py '内容のメモ'   # run_in_background で実行する
```

完了したら `Saved` の行に出る保存先を使って集計する。`summarize.py` の alp は `/tmp/isureal-profile-bin/` にあり、消えていたら `tools/profile/README.md` の手順で入れ直す。

```sh
run=artifacts/profiles/<取得時刻>
curl -s http://127.0.0.1:8787/jobs/<ジョブID>/conclusion > $run/conclusion.txt
python3 tools/profile/summarize.py $run
```

全体の集計には初期化や最終チェックも混ざるので、負荷テストの区間だけを見るときは `conclusion.txt` の最初と最後のログ時刻を渡す。区間指定の結果は `alp-window-*.md` と `summary-window.json` に出る。

```sh
python3 tools/profile/summarize.py $run --from-time 2026-10-31T06:45:00+00:00 --until-time 2026-10-31T06:46:00+00:00
```

結果を解釈する前に、`tools/profile/README.md` の「計測上の注意」を読む。SSE の待機時間や、nginx ログと採点件数の違いなど、読み違えやすい点がまとまっている。

`run.py` の終了コードは `bench.py` と同じ。ただし準備中の SSH 失敗などでも 1 で終わるので、出力に `enqueue #` の行がなければベンチは要求されていない。エンキューに失敗しても計測は回収される。

## 終了コード

| コード | 意味 | 対応 |
| --- | --- | --- |
| 0 | `FINISHED` まで完了した | スコアを報告する |
| 1 | `ERRORED` / `CANCELLED` で終わった | 失敗として報告する。原因は Conclusion のログから調べる |
| 2 | クリックされなかった（`stale`） | 下の「拡張の確認」を伝える。再要求はしてよい |
| 3 | クリック後にジョブを確認できなかった | **再要求しない。** ベンチは走っている可能性があるので、ポータルで確認してもらう |
| 4 | サーバーに接続できない | 下の「サーバーの起動」を行う |
| 5 | 要求から5分以内に終わらなかった | 異常として報告する。ジョブが残っている可能性があるので、ポータルと拡張を確認してもらう |

3 と 5 で自動的に再要求しないのは、走行中のジョブに重ねて要求するとスコアの判断を誤り、待ち時間も無駄になるため。

## サーバーの起動

サーバーが動いていなければ、`run_in_background` で起動する。要求はメモリ上にしかないので、再起動すると履歴は消える。

```sh
cd tools/bench-enqueue && go run .
```

## 拡張の確認

要求が `stale` になる原因はほとんど拡張側にある。ブラウザ操作はユーザーにしかできないので、次を確認してもらう。

- Chrome に `tools/bench-enqueue/extension` が読み込まれている。
- <https://portal.isucon.net/contestant/benchmark_jobs> が開いていて、ログイン済みで、Status の絞り込みが空になっている。
- タブが隠れていない。隠れているとタイマーが間引かれる。
