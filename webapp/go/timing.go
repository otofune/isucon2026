package isureal

import (
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// timing は処理時間の累積を記録する計測用のカウンター。
type timingCounter struct {
	count atomic.Int64
	nanos atomic.Int64
}

var timings sync.Map

func observe(name string, elapsed time.Duration) {
	value, _ := timings.LoadOrStore(name, &timingCounter{})
	counter := value.(*timingCounter)
	counter.count.Add(1)
	counter.nanos.Add(int64(elapsed))
}

func resetTimings() {
	timings.Clear()
}

func init() {
	http.HandleFunc("/debug/timing", func(w http.ResponseWriter, _ *http.Request) {
		result := map[string]map[string]float64{}
		timings.Range(func(name, value any) bool {
			counter := value.(*timingCounter)
			count, nanos := counter.count.Load(), counter.nanos.Load()
			result[name.(string)] = map[string]float64{
				"count": float64(count), "total_ms": float64(nanos) / 1e6, "mean_ms": float64(nanos) / 1e6 / float64(max(count, 1)),
			}
			return true
		})
		json.NewEncoder(w).Encode(result)
	})
}
