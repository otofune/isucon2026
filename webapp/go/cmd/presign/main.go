// presign は初期画像の署名を事前計算する。走行の合間にアイドルの台で実行する。
package main

import (
	"fmt"
	"runtime"

	"example.com/isureal-company/isureal"
)

func main() {
	path, count, err := isureal.PrecomputeSignatures(isureal.ConfigFromEnv(), runtime.NumCPU())
	if err != nil {
		panic(err)
	}
	fmt.Println(path, count)
}
