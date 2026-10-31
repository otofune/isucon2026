package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const row = `<tr><td><a href="/contestant/benchmark_jobs/%d">#%d</a></td><td class="has-text-right">%s</td><td><span class="tag is-success">%s</span></td><td><time>2026-10-31 14:56:10.570</time></td><td><span>2 minutes</span></td><td><div>otofune</div></td></tr>`

func page(rows ...string) string {
	return `<main><form><button class="button is-primary" type="submit">Enqueue</button></form><table class="table"><thead><tr><th>ID</th><th>Score</th><th>Status</th></tr></thead><tbody>` + strings.Join(rows, "") + `</tbody></table></main>`
}

func newStore() (*Store, *time.Time) {
	now := time.Date(2026, 10, 31, 6, 0, 0, 0, time.UTC)
	return &Store{now: func() time.Time { return now }}, &now
}

func observe(t *testing.T, s *Store, rows ...string) {
	t.Helper()
	jobs, err := parseJobs(page(rows...))
	if err != nil {
		t.Fatal(err)
	}
	s.Observe(jobs)
}

func TestParseJobs(t *testing.T) {
	jobs, err := parseJobs(page(fmt.Sprintf(row, 19943, 19943, "3845", "FINISHED")))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0] != (Job{ID: 19943, Score: "3845", Status: "FINISHED"}) {
		t.Fatalf("jobs = %+v", jobs)
	}
}

func TestParseJobDetail(t *testing.T) {
	const detail = `<main class="block"><section><div class="card mt-5"><header class="card-header"><h4 class="is-4 card-header-title">Summary</h4></header><div class="card-content"><p><b>ID:</b> 23283</p><p><b>Target:</b> #1: 192.168.1.10 (13.159.156.72) </p><p><b>Status:</b> <span class="tag is-warning">RUNNING</span></p><p><b>Enqueued By:</b> <div class="is-inline-flex is-align-items-center"><img width="24" height="24" class="mr-1"> otofune</div></p><p><b>Enqueued At:</b> <time datetime="2026-10-31T06:34:45.941Z">2026-10-31 15:34:45.941</time></p><p><b>Finished At:</b> N/A</p></div></div><div class="card mt-5"><header class="card-header"><h4 class="is-4 card-header-title">Result</h4></header><div class="card-content"><p><span class="tag is-warning">In progress</span></p><p><b>Score:</b> 0</p><p><b>Score Breakdown:</b> base=0, deduction=0</p></div></div></section></main>`
	jobs, err := parseJobs(detail)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0] != (Job{ID: 23283, Score: "0", Status: "RUNNING"}) {
		t.Fatalf("jobs = %+v", jobs)
	}
}

func TestParseJobDetailKeepsFirstValues(t *testing.T) {
	jobs, err := parseJobs(`<main><p><b>ID:</b> 7</p><p><b>Status:</b> RUNNING</p><p><b>Score:</b> 10</p></main><aside><p>ID: 1</p><p>Status: ERRORED</p><p>Score: 99</p></aside>`)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0] != (Job{ID: 7, Score: "10", Status: "RUNNING"}) {
		t.Fatalf("jobs = %+v", jobs)
	}
}

func TestHTMLSavesOnlyJobDetail(t *testing.T) {
	s, _ := newStore()
	dir := t.TempDir()
	h := routes(s, dir)
	for _, path := range []string{"/contestant/benchmark_jobs/23283", "/contestant/benchmark_jobs", "/contestant/benchmark_jobs/../../x", ""} {
		body := fmt.Sprintf(`{"path":%q,"html":"<main>Conclusion</main>"}`, path)
		r := httptest.NewRequest(http.MethodPost, "/html", strings.NewReader(body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: code = %d", path, w.Code)
		}
	}
	files, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || filepath.Base(files[0]) != "23283.html" {
		t.Fatalf("files = %v", files)
	}
	got, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "<main>Conclusion</main>" {
		t.Fatalf("content = %q", got)
	}
}

func TestConclusionAPI(t *testing.T) {
	s, _ := newStore()
	dir := t.TempDir()
	h := routes(s, dir)
	files := map[string]string{
		"1.html": `<div class="card mt-5"><header class="card-header"><h4 class="is-4 card-header-title">Result</h4></header><div class="card-content"><p><b>Score:</b> 4553</p></div></div><div class="card mt-5"><header class="card-header"><h4 class="is-4 card-header-title">Conclusion</h4></header><div class="card-content"><p><b>Reason:</b> 実行終了</p><div class="mt-3"><h5 class="subtitle is-5">Stdout</h5><pre><span>line 1` + "\n" + `</span><span>  line 2` + "\n" + `</span></pre></div></div></div><aside><p>同一ベンチ対象サーバーで再度 Enqueue できます。</p></aside>`,
		"2.html": `<div class="card"><header><h4>Result</h4></header></div>`,
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		id   string
		code int
		body string
	}{
		{"1", http.StatusOK, "Reason: 実行終了\n\n## Stdout\nline 1\n  line 2\n"},
		{"2", http.StatusNotFound, ""},
		{"3", http.StatusNotFound, ""},
		{"x", http.StatusBadRequest, ""},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/jobs/"+tc.id+"/conclusion", nil))
		if w.Code != tc.code {
			t.Errorf("%s: code = %d, want %d", tc.id, w.Code, tc.code)
		}
		if tc.body != "" && w.Body.String() != tc.body {
			t.Errorf("%s: body = %q", tc.id, w.Body.String())
		}
	}
}

func TestEnqueueLifecycle(t *testing.T) {
	s, _ := newStore()
	observe(t, s, fmt.Sprintf(row, 1, 1, "100", "FINISHED"))
	e := s.Add("after fix")
	if _, err := s.Claim(e.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(e.ID); err == nil {
		t.Fatal("second claim must fail")
	}
	observe(t, s, fmt.Sprintf(row, 2, 2, "", "RUNNING"), fmt.Sprintf(row, 1, 1, "100", "FINISHED"))
	got := s.List()[0]
	if got.Status != statusEnqueued || got.Job.ID != 2 {
		t.Fatalf("got %+v", got)
	}
	observe(t, s, fmt.Sprintf(row, 2, 2, "500", "FINISHED"), fmt.Sprintf(row, 1, 1, "100", "FINISHED"))
	if got := s.List()[0]; got.Job.Score != "500" || got.Job.Status != "FINISHED" {
		t.Fatalf("got %+v", got.Job)
	}
}

func TestExistingJobsAndHumanJobsAreNotMatched(t *testing.T) {
	s, _ := newStore()
	e := s.Add("")
	observe(t, s, fmt.Sprintf(row, 5, 5, "", "FINISHED"))
	observe(t, s, fmt.Sprintf(row, 6, 6, "", "RUNNING"))
	got := s.List()[0]
	if got.Job != nil || got.Status != statusPending {
		t.Fatalf("got %+v", got)
	}
	if _, err := s.Claim(e.ID); err == nil {
		t.Fatal("claim must fail while a job is active")
	}
}

func TestStale(t *testing.T) {
	t.Run("pending is never clicked afterwards", func(t *testing.T) {
		s, now := newStore()
		e := s.Add("")
		*now = now.Add(time.Minute)
		observe(t, s)
		if got := s.List()[0].Status; got != statusStale {
			t.Fatalf("status = %s", got)
		}
		if _, err := s.Claim(e.ID); err == nil {
			t.Fatal("stale must not be clickable")
		}
	})
	t.Run("clicked job that appears late recovers", func(t *testing.T) {
		s, now := newStore()
		observe(t, s)
		e := s.Add("")
		if _, err := s.Claim(e.ID); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(time.Minute)
		if got := s.List()[0].Status; got != statusStale {
			t.Fatalf("status = %s", got)
		}
		observe(t, s, fmt.Sprintf(row, 1, 1, "", "PENDING"))
		if got := s.List()[0]; got.Status != statusEnqueued {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("waiting for an active job is not stale", func(t *testing.T) {
		s, now := newStore()
		s.Add("")
		for range 4 {
			*now = now.Add(50 * time.Second)
			observe(t, s, fmt.Sprintf(row, 1, 1, "", "RUNNING"))
		}
		if got := s.List()[0].Status; got != statusPending {
			t.Fatalf("status = %s", got)
		}
	})
}

func TestClaimRules(t *testing.T) {
	s, now := newStore()
	first, second := s.Add(""), s.Add("")
	if _, err := s.Claim(first.ID); err == nil {
		t.Fatal("claim needs a received HTML")
	}
	observe(t, s)
	if _, err := s.Claim(second.ID); err == nil {
		t.Fatal("only the oldest request can be claimed")
	}
	*now = now.Add(htmlFreshFor)
	if _, err := s.Claim(first.ID); err == nil {
		t.Fatal("claim needs a fresh HTML")
	}
	observe(t, s)
	if _, err := s.Claim(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(second.ID); err == nil {
		t.Fatal("no claim while another click is unconfirmed")
	}
}

func TestGuard(t *testing.T) {
	s, _ := newStore()
	h := guard("127.0.0.1:8787", routes(s, t.TempDir()))
	for _, tc := range []struct {
		host, contentType string
		want              int
	}{
		{"127.0.0.1:8787", "application/json", http.StatusCreated},
		{"127.0.0.1:8787", "text/plain", http.StatusUnsupportedMediaType},
		{"evil.example:8787", "application/json", http.StatusForbidden},
	} {
		r := httptest.NewRequest(http.MethodPost, "/enqueues", strings.NewReader(`{}`))
		r.Host = tc.host
		r.Header.Set("Content-Type", tc.contentType)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s %s: code = %d, want %d", tc.host, tc.contentType, w.Code, tc.want)
		}
	}
}
