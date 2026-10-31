// bench-enqueue はポータルの Enqueue 要求を管理するローカルサーバー。
// ブラウザ拡張が送る HTML から、要求したベンチマークの開始と結果を判定する。
package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const (
	staleAfter   = time.Minute
	htmlFreshFor = 30 * time.Second
	maxBodyBytes = 32 << 20
)

const (
	statusPending  = "pending"  // クリック待ち
	statusClicked  = "clicked"  // クリック済みで、表にジョブが出るのを待っている
	statusEnqueued = "enqueued" // ジョブと対応付いた。結果は job を見る
	statusStale    = "stale"    // 1分進まなかった。pending だったものは今後クリックしない
)

type Job struct {
	ID     int    `json:"id"`
	Score  string `json:"score"`
	Status string `json:"status"`
}

func (j Job) active() bool {
	switch j.Status {
	case "FINISHED", "ERRORED", "CANCELLED":
		return false
	}
	return true
}

type Enqueue struct {
	ID          int        `json:"id"`
	Note        string     `json:"note,omitempty"`
	Status      string     `json:"status"`
	RequestedAt time.Time  `json:"requested_at"`
	ClickedAt   *time.Time `json:"clicked_at,omitempty"`
	Job         *Job       `json:"job,omitempty"`

	since time.Time // stale 判定の起点
}

type Store struct {
	mu       sync.Mutex
	now      func() time.Time
	enqueues []*Enqueue
	lastHTML time.Time
	maxJobID int
	active   bool
}

var errNotClickable = errors.New("not clickable now")

var jobDetailPath = regexp.MustCompile(`^/contestant/benchmark_jobs/(\d+)$`)

func (s *Store) expire() {
	now := s.now()
	for _, e := range s.enqueues {
		if (e.Status == statusPending || e.Status == statusClicked) && now.Sub(e.since) >= staleAfter {
			e.Status = statusStale
		}
	}
}

func (s *Store) Add(note string) Enqueue {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire()
	now := s.now()
	e := &Enqueue{ID: len(s.enqueues) + 1, Note: note, Status: statusPending, RequestedAt: now, since: now}
	s.enqueues = append(s.enqueues, e)
	return *e
}

func (s *Store) List() []Enqueue {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire()
	list := make([]Enqueue, 0, len(s.enqueues))
	for _, e := range s.enqueues {
		list = append(list, *e)
	}
	return list
}

// Claim はクリックの権利を一度だけ渡す。多重クリックを防ぐため、最新の HTML で
// 実行中のジョブがなく、クリック済みの要求も残っていないときに限り、最も古い要求だけを許可する。
func (s *Store) Claim(id int) (Enqueue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire()
	now := s.now()
	if s.lastHTML.IsZero() || now.Sub(s.lastHTML) >= htmlFreshFor || s.active {
		return Enqueue{}, errNotClickable
	}
	for _, e := range s.enqueues {
		switch e.Status {
		case statusClicked:
			return Enqueue{}, errNotClickable
		case statusPending:
			if e.ID != id {
				return Enqueue{}, errNotClickable
			}
			e.Status = statusClicked
			e.ClickedAt = &now
			e.since = now
			return *e, nil
		}
	}
	return Enqueue{}, errNotClickable
}

// Observe はページから読んだジョブを要求に反映し、実行中のジョブの有無を返す。
// 初回のページは基準点で、要求と対応付けない。
func (s *Store) Observe(jobs []Job) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	first := s.lastHTML.IsZero()
	s.lastHTML = now
	slices.SortFunc(jobs, func(a, b Job) int { return cmp.Compare(a.ID, b.ID) })

	s.active = false
	byID := map[int]Job{}
	for _, job := range jobs {
		byID[job.ID] = job
		s.active = s.active || job.active()
		if job.ID <= s.maxJobID {
			continue
		}
		s.maxJobID = job.ID
		if first {
			continue
		}
		// 遅れて出たジョブも拾えるよう、クリック後に stale になった要求も対象にする。
		for _, e := range s.enqueues {
			if e.ClickedAt != nil && e.Job == nil {
				matched := job
				e.Job = &matched
				e.Status = statusEnqueued
				break
			}
		}
	}

	for _, e := range s.enqueues {
		if e.Job != nil {
			if job, ok := byID[e.Job.ID]; ok {
				*e.Job = job
			}
		}
		// 実行中のジョブを待っている時間は異常ではない。
		if e.Status == statusPending && s.active {
			e.since = now
		}
	}
	s.expire()
	return s.active
}

// parseJobs は一覧の表、または詳細ページの Summary と Result からジョブを読む。
// 表の列は ID, Score, Status, ... の順。
func parseJobs(src string) ([]Job, error) {
	root, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return nil, err
	}
	jobs := []Job{}
	detail := Job{}
	for n := range root.Descendants() {
		if n.DataAtom == atom.P {
			label, value, _ := strings.Cut(strings.TrimSpace(text(n)), ":")
			value = strings.TrimSpace(value)
			// ログに同じ見出しの行があっても、先に出る Summary と Result を優先する。
			switch {
			case label == "ID" && detail.ID == 0:
				detail.ID, _ = strconv.Atoi(value)
			case label == "Status" && detail.Status == "":
				detail.Status = value
			case label == "Score" && detail.Score == "":
				detail.Score = value
			}
			continue
		}
		if n.DataAtom != atom.Tr || n.Parent == nil || n.Parent.DataAtom != atom.Tbody {
			continue
		}
		cells := []string{}
		for c := range n.ChildNodes() {
			if c.DataAtom == atom.Td {
				cells = append(cells, strings.TrimSpace(text(c)))
			}
		}
		if len(cells) < 3 {
			continue
		}
		id, err := strconv.Atoi(strings.TrimPrefix(cells[0], "#"))
		if err != nil {
			continue
		}
		jobs = append(jobs, Job{ID: id, Score: cells[1], Status: cells[2]})
	}
	if detail.ID != 0 && detail.Status != "" {
		jobs = append(jobs, detail)
	}
	return jobs, nil
}

// conclusionText はジョブ詳細の Conclusion カードをプレーンテキストにする。
// カードがないとき（実行中など）は ok が false になる。
func conclusionText(src string) (string, bool, error) {
	root, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return "", false, err
	}
	for n := range root.Descendants() {
		if n.DataAtom != atom.H4 || strings.TrimSpace(text(n)) != "Conclusion" {
			continue
		}
		var b strings.Builder
		// h4 は card > header > h4 にある。
		for d := range n.Parent.Parent.Descendants() {
			switch d.DataAtom {
			case atom.P:
				b.WriteString(strings.TrimSpace(text(d)) + "\n")
			case atom.H5:
				b.WriteString("\n## " + strings.TrimSpace(text(d)) + "\n")
			case atom.Pre:
				b.WriteString(text(d))
			}
		}
		return b.String(), true, nil
	}
	return "", false, nil
}

func text(n *html.Node) string {
	var b strings.Builder
	for d := range n.Descendants() {
		if d.Type == html.TextNode {
			b.WriteString(d.Data)
		}
	}
	return b.String()
}

// guard はブラウザで開いた他サイトからの操作を防ぐ。
// Host の確認は DNS rebinding 対策、JSON の強制は CORS の preflight で弾くため。
func guard(host string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != host {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost && r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "application/json required", http.StatusUnsupportedMediaType)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// saveJobDetail はジョブ詳細ページを data/<ジョブID>.html に残す。
// path は URL から ID だけを取り出すので、データディレクトリの外には書かない。
func saveJobDetail(dataDir, path, src string) error {
	m := jobDetailPath.FindStringSubmatch(path)
	if m == nil {
		return nil
	}
	return os.WriteFile(filepath.Join(dataDir, m[1]+".html"), []byte(src), 0o644)
}

func routes(s *Store, dataDir string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /enqueues", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.List())
	})
	mux.HandleFunc("POST /enqueues", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Note string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, s.Add(body.Note))
	})
	mux.HandleFunc("POST /enqueues/{id}/claim", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		e, err := s.Claim(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusOK, e)
	})
	mux.HandleFunc("GET /jobs/{id}/conclusion", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		src, err := os.ReadFile(filepath.Join(dataDir, strconv.Itoa(id)+".html"))
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "job page is not saved", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		conclusion, ok, err := conclusionText(string(src))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "conclusion is not available yet", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, conclusion)
	})
	mux.HandleFunc("POST /html", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			HTML string `json:"html"`
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		jobs, err := parseJobs(body.HTML)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// FINISHED が見えた時点でファイルが揃っているよう、Observe より先に保存する。
		if err := saveJobDetail(dataDir, body.Path, body.HTML); err != nil {
			log.Printf("save %s: %v", body.Path, err)
		}
		active := s.Observe(jobs)
		writeJSON(w, http.StatusOK, map[string]any{"active": active, "jobs": jobs})
	})
	return mux
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8787", "listen address")
	dataDir := flag.String("data", "data", "directory for job detail pages")
	flag.Parse()
	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatal(err)
	}
	s := &Store{now: time.Now}
	log.Printf("listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, guard(*addr, routes(s, *dataDir))))
}
