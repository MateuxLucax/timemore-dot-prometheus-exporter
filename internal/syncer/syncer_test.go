package syncer

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MateuxLucax/timemore-dot-prometheus-exporter/internal/brew"
	"github.com/MateuxLucax/timemore-dot-prometheus-exporter/internal/remotewrite"
	"github.com/MateuxLucax/timemore-dot-prometheus-exporter/internal/store"
)

func contract(t *testing.T, name string) *brew.Brew {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "contract", "valid", name))
	if err != nil {
		t.Fatal(err)
	}
	b, err := brew.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeProm records what it accepted, keyed by series and timestamp, and can be "down".
type fakeProm struct {
	mu       sync.Mutex
	down     bool
	reject   func(remotewrite.Series) bool
	requests int
	points   map[string]int // "name id ts" -> times written
}

func (f *fakeProm) Write(_ context.Context, series []remotewrite.Series) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	if f.down {
		return fmt.Errorf("dial tcp: connection refused")
	}
	for _, s := range series {
		if f.reject != nil && f.reject(s) {
			return &remotewrite.RejectedError{Msg: "remote write rejected (400): too old sample"}
		}
	}
	for _, s := range series {
		for _, p := range s.Points {
			f.points[fmt.Sprintf("%s %s %d", labelValue(s, "__name__"), labelValue(s, "id"), p.TsMs)]++
		}
	}
	return nil
}

func setup(t *testing.T) (*store.Store, *fakeProm, *Syncer, *bytes.Buffer) {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fp := &fakeProm{points: map[string]int{}}
	var logs bytes.Buffer
	now := time.UnixMilli(1792950000000) // shortly after the contract brews
	return st, fp, &Syncer{Store: st, Writer: fp, Logger: log.New(&logs, "", 0), Now: func() time.Time { return now }}, &logs
}

func pending(t *testing.T, st *store.Store) int {
	_, _, p, err := st.Counts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPrometheusDownThenUp(t *testing.T) {
	st, fp, sy, _ := setup(t)
	ctx := t.Context()
	names := []string{"espresso.json", "filter.json", "interrupted.json", "empty-samples.json"}
	fp.down = true
	for _, n := range names {
		if _, err := st.Upsert(ctx, contract(t, n)); err != nil {
			t.Fatal(err) // the API keeps accepting while Prometheus is down
		}
	}
	if _, err := sy.Run(ctx); err == nil {
		t.Fatal("run with Prometheus down must fail")
	}
	if p := pending(t, st); p != len(names) {
		t.Fatalf("pending after failed run = %d, want %d (nothing advanced)", p, len(names))
	}

	fp.down = false
	res, err := sy.Run(ctx)
	if err != nil || res.Sent != len(names) || res.Rejected != 0 {
		t.Fatalf("run = %+v, %v", res, err)
	}
	if p := pending(t, st); p != 0 {
		t.Fatalf("pending = %d after successful run", p)
	}
	total := 0
	for k, n := range fp.points {
		if n != 1 {
			t.Errorf("%s written %d times", k, n)
		}
		total++
	}
	wantPoints := 0
	for _, n := range names {
		for _, s := range Series(contract(t, n)) {
			wantPoints += len(s.Points)
		}
	}
	if total != wantPoints {
		t.Errorf("%d points written, want %d", total, wantPoints)
	}

	// Next run: nothing left to send.
	before := fp.requests
	if res, err := sy.Run(ctx); err != nil || res.Sent != 0 || fp.requests != before {
		t.Fatalf("idle run = %+v, %v, %d requests", res, err, fp.requests-before)
	}
}

func TestReplacedBrewIsResent(t *testing.T) {
	st, fp, sy, _ := setup(t)
	ctx := t.Context()
	b := contract(t, "espresso.json")
	st.Upsert(ctx, b)
	sy.Run(ctx)
	st.Upsert(ctx, b) // same id again
	if res, err := sy.Run(ctx); err != nil || res.Sent != 1 {
		t.Fatalf("resend after replace = %+v, %v", res, err)
	}
	if fp.points["caparao_brew_yield_grams "+b.ID+" 1792931428600"] != 2 {
		t.Errorf("summary not re-sent: %v", fp.points)
	}
}

func TestRejectedBrewIsRecordedAndOthersStillSent(t *testing.T) {
	st, fp, sy, logs := setup(t)
	ctx := t.Context()
	old := contract(t, "filter.json")
	ok := contract(t, "espresso.json")
	fp.reject = func(s remotewrite.Series) bool { return labelValue(s, "id") == old.ID }
	st.Upsert(ctx, old)
	st.Upsert(ctx, ok)
	res, err := sy.Run(ctx)
	if err != nil || res.Sent != 1 || res.Rejected != 1 {
		t.Fatalf("run = %+v, %v", res, err)
	}
	if pending(t, st) != 0 {
		t.Error("a rejected brew must not be retried forever")
	}
	if !bytes.Contains(logs.Bytes(), []byte("WARN sync: brew "+old.ID+" rejected")) {
		t.Errorf("rejection not logged: %s", logs)
	}
}

func TestPartialRejectionSendsGoodSeries(t *testing.T) {
	st, fp, sy, _ := setup(t)
	b := contract(t, "espresso.json")
	fp.reject = func(s remotewrite.Series) bool { return labelValue(s, "__name__") == "caparao_brew_yield_grams" }
	st.Upsert(t.Context(), b)
	if res, _ := sy.Run(t.Context()); res.Rejected != 1 {
		t.Fatalf("res = %+v", res)
	}
	if fp.points["caparao_brew_weight_grams "+b.ID+" 1792931428600"] != 1 {
		t.Errorf("curve should still be written: %v", fp.points)
	}
}

func TestFutureBrewNotSent(t *testing.T) {
	st, fp, sy, _ := setup(t)
	b := contract(t, "espresso.json")
	b.StartedAtMs += 10 * 24 * 3600 * 1000
	b.EndedAtMs += 10 * 24 * 3600 * 1000
	st.Upsert(t.Context(), b)
	if res, err := sy.Run(t.Context()); err != nil || res.Rejected != 1 || fp.requests != 0 {
		t.Fatalf("res = %+v, %v, %d requests", res, err, fp.requests)
	}
}

func TestSeriesTimestamps(t *testing.T) {
	b := contract(t, "espresso.json")
	got := map[string][]remotewrite.Point{}
	for _, s := range Series(b) {
		if labelValue(s, "mode") != "espresso" || labelValue(s, "id") != b.ID || len(s.Labels) != 3 {
			t.Errorf("labels = %+v", s.Labels)
		}
		got[labelValue(s, "__name__")] = s.Points
	}
	end := b.EndedAtMs
	for name, v := range map[string]float64{
		"caparao_brew_yield_grams": 36.4, "caparao_brew_dose_grams": 18, "caparao_brew_ratio": 36.4 / 18,
		"caparao_brew_duration_seconds": 28.6, "caparao_brew_time_to_first_drop_seconds": 6.2,
		"caparao_brew_flow_avg_grams_per_second": 1.3,
	} {
		if p := got[name]; len(p) != 1 || p[0].TsMs != end || p[0].Value != v {
			t.Errorf("%s = %+v, want %v at %d", name, p, v, end)
		}
	}
	w := got["caparao_brew_weight_grams"]
	if len(w) != 4 || w[1].TsMs != b.StartedAtMs+6200 || w[1].Value != 0.4 || w[3].TsMs != end {
		t.Errorf("weight curve = %+v", w)
	}
	if f := got["caparao_brew_flow_grams_per_second"]; len(f) != 4 || f[2].Value != 2.1 {
		t.Errorf("flow curve = %+v", f)
	}

	fb := contract(t, "filter.json")
	for _, s := range Series(fb) {
		if labelValue(s, "__name__") == "caparao_brew_time_to_first_drop_seconds" {
			t.Error("filter brews have no time to first drop")
		}
	}
	if n := len(Series(contract(t, "empty-samples.json"))); n < 5 || n > 6 {
		t.Errorf("empty-samples: %d series", n)
	}
}

// The real client against an HTTP server that refuses, then accepts.
func TestRealClientDownThenUp(t *testing.T) {
	st, _, sy, _ := setup(t)
	var mu sync.Mutex
	status := http.StatusServiceUnavailable
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.WriteHeader(status)
	}))
	defer srv.Close()
	sy.Writer = remotewrite.NewClient(srv.URL, srv.Client())
	st.Upsert(t.Context(), contract(t, "espresso.json"))
	if _, err := sy.Run(t.Context()); err == nil || pending(t, st) != 1 {
		t.Fatalf("503: err=%v pending=%d", err, pending(t, st))
	}
	mu.Lock()
	status = http.StatusNoContent
	mu.Unlock()
	if res, err := sy.Run(t.Context()); err != nil || res.Sent != 1 || pending(t, st) != 0 {
		t.Fatalf("204: %+v %v", res, err)
	}
}
