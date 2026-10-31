# bench-enqueue

ポータルの Enqueue ボタンを、ローカルの API から押すためのサーバーとブラウザ拡張。

## 起動

```sh
cd tools/bench-enqueue
go run .
```

Chrome の `chrome://extensions` でデベロッパーモードを有効にし、`extension/` を読み込む。
<https://portal.isucon.net/contestant/benchmark_jobs> を開いたままにする。
Enqueue 後はポータルがジョブ詳細へ遷移するので、拡張はそこでも状態を送り、ジョブが終わると一覧へ戻る。
タブが隠れていると Chrome がタイマーを1分に1回まで間引くので、専用ウィンドウで表示しておく。

## API

```sh
curl -X POST -H 'Content-Type: application/json' -d '{"note":"fix feed"}' http://127.0.0.1:8787/enqueues
curl http://127.0.0.1:8787/enqueues
```

| status | 意味 |
| --- | --- |
| `pending` | クリック待ち。実行中のジョブがあれば終わるまで待つ |
| `clicked` | クリック済み。表にジョブが出るのを待っている |
| `enqueued` | ジョブと対応付いた。`job.status` と `job.score` で結果を見る |
| `stale` | 1分進まなかった。拡張の停止やポータル側の拒否を疑う |

`pending` から `stale` になった要求は、後から勝手に走らないよう二度とクリックしない。必要なら要求し直す。
`clicked` から `stale` になった要求は、遅れてジョブが表に出れば `enqueued` に戻る。
要求はメモリ上にあり、サーバーを再起動すると消える。

## ジョブ詳細の保存

拡張がジョブ詳細ページを開いている間、サーバーはそのページを `data/<ジョブID>.html` に上書き保存する。
再起動しても残り、git には含めない。
Conclusion のログは API からテキストで取得できる。ページが未保存か、ジョブが終わっていないと 404 を返す。

```sh
curl http://127.0.0.1:8787/jobs/23283/conclusion
```

過去のジョブも、ポータルで詳細ページを開けば保存される。
