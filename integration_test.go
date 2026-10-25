//go:build integration

package main

// Integration test: the compiled binary against a real Prometheus.
//
//	scripts/integration.sh
//
// or, with a Prometheus already running with --web.enable-remote-write-receiver and this
// repo's prometheus.yml (out-of-order window):
//
//	PROMETHEUS_URL=http://localhost:9090 go test -tags integration -count=1 -run TestIntegration .
//
// Every run uses random brew ids, so runs against the same Prometheus do not interfere.

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

var testClient = &http.Client{Timeout: 10 * time.Second}

type itPoint struct {
	TsMs  int64
	Value float64
}

func randomUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func run(t *testing.T, bin string, env []string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func startServer(t *testing.T, bin string, env []string) string {
	t.Helper()
	addr := freeAddr(t)
	var logs bytes.Buffer
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), append(env, "LISTEN_ADDR="+addr)...)
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		if t.Failed() {
			t.Logf("server log:\n%s", logs.String())
		}
	})
	base := "http://" + addr
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if resp, err := testClient.Get(base + "/healthz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return base
			}
		}
	}
	t.Fatalf("server never became healthy:\n%s", logs.String())
	return ""
}

func post(t *testing.T, base, token string, body []byte) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/brews", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// query runs an instant query at atMs and returns the points per __name__.
func query(t *testing.T, prom, q string, atMs int64) map[string][]itPoint {
	t.Helper()
	v := url.Values{"query": {q}, "time": {fmt.Sprintf("%d.%03d", atMs/1000, atMs%1000)}}
	resp, err := testClient.Get(prom + "/api/v1/query?" + v.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Status string
		Data   struct {
			Result []struct {
				Metric map[string]string
				Value  []json.Number
				Values [][]json.Number
			}
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil || out.Status != "success" {
		t.Fatalf("query %q: %v %s", q, err, raw)
	}
	res := map[string][]itPoint{}
	for _, r := range out.Data.Result {
		vals := r.Values
		if r.Value != nil {
			vals = append(vals, r.Value)
		}
		for _, v := range vals {
			sec, frac, _ := strings.Cut(v[0].String(), ".")
			ms, _ := strconv.ParseInt(sec+(frac + "000")[:3], 10, 64)
			f, _ := strconv.ParseFloat(v[1].String(), 64)
			res[r.Metric["__name__"]] = append(res[r.Metric["__name__"]], itPoint{ms, f})
		}
	}
	return res
}

func TestIntegration(t *testing.T) {
	prom := strings.TrimRight(os.Getenv("PROMETHEUS_URL"), "/")
	if prom == "" {
		t.Skip("PROMETHEUS_URL not set")
	}
	bin := filepath.Join(t.TempDir(), "exporter")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	const token = "integration-test-token"
	env := []string{
		"AUTH_TOKEN=" + token,
		"REMOTE_WRITE_URL=" + prom + "/api/v1/write",
		"DB_PATH=" + filepath.Join(t.TempDir(), "it.db"),
		"SYNC_INTERVAL=1h", // the test triggers syncs with the sync subcommand
	}
	base := startServer(t, bin, env)

	// The contract's espresso brew, moved to an hour ago with a fresh id.
	raw, err := os.ReadFile("testdata/contract/valid/espresso.json")
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	json.Unmarshal(raw, &d)
	id := randomUUID()
	start := time.Now().Add(-time.Hour).UnixMilli()
	end := start + 28600
	d["id"], d["started_at_ms"], d["ended_at_ms"] = id, start, end
	body, _ := json.Marshal(d)

	if code := post(t, base, token, body); code != 201 {
		t.Fatalf("POST = %d, want 201", code)
	}
	if code := post(t, base, token, body); code != 200 {
		t.Fatalf("resend POST = %d, want 200", code)
	}
	// The server's own loop ran at startup with nothing pending; sync now.
	if out, err := run(t, bin, env, "sync"); err != nil || !strings.Contains(out, "sent 1, rejected 0") {
		t.Fatalf("sync: %v\n%s", err, out)
	}

	check := func(t *testing.T) {
		t.Helper()
		got := query(t, prom, fmt.Sprintf(`{__name__=~"caparao_brew_.*",id=%q,mode="espresso"}[2h]`, id), time.Now().UnixMilli())
		want := map[string][]itPoint{
			"caparao_brew_yield_grams":                {{end, 36.4}},
			"caparao_brew_dose_grams":                 {{end, 18}},
			"caparao_brew_ratio":                      {{end, 36.4 / 18}},
			"caparao_brew_duration_seconds":           {{end, 28.6}},
			"caparao_brew_time_to_first_drop_seconds": {{end, 6.2}},
			"caparao_brew_flow_avg_grams_per_second":  {{end, 1.3}},
			"caparao_brew_weight_grams":               {{start, 0}, {start + 6200, 0.4}, {start + 15000, 17.9}, {end, 36.4}},
			"caparao_brew_flow_grams_per_second":      {{start, 0}, {start + 6200, 0.3}, {start + 15000, 2.1}, {end, 1.1}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("stored:\n%+v\nwant:\n%+v", got, want)
		}
	}
	t.Run("summaries at end time, curve at sample times", check)

	t.Run("resend everything is accepted and duplicates nothing", func(t *testing.T) {
		// A newer brew first, so the resent one is out of order for Prometheus' head.
		d2 := map[string]any{}
		for k, v := range d {
			d2[k] = v
		}
		d2["id"], d2["started_at_ms"], d2["ended_at_ms"] = randomUUID(), time.Now().UnixMilli()-30000, time.Now().UnixMilli()
		b2, _ := json.Marshal(d2)
		if code := post(t, base, token, b2); code != 201 {
			t.Fatalf("POST newer = %d", code)
		}
		if out, err := run(t, bin, env, "sync"); err != nil {
			t.Fatalf("sync: %v\n%s", err, out)
		}
		out, err := run(t, bin, env, "resend")
		if err != nil || !strings.Contains(out, "sent 2, rejected 0") {
			t.Fatalf("resend: %v\n%s", err, out)
		}
		check(t)
	})

	t.Run("brew older than the out-of-order window is rejected and reported", func(t *testing.T) {
		d3 := map[string]any{}
		for k, v := range d {
			d3[k] = v
		}
		old := time.Now().Add(-60 * 24 * time.Hour).UnixMilli()
		d3["id"], d3["started_at_ms"], d3["ended_at_ms"] = randomUUID(), old, old+28600
		b3, _ := json.Marshal(d3)
		if code := post(t, base, token, b3); code != 201 { // the API still stores it
			t.Fatalf("POST old = %d", code)
		}
		out, err := run(t, bin, env, "sync")
		if err != nil || !strings.Contains(out, "rejected 1") || !strings.Contains(out, "WARN sync: brew "+d3["id"].(string)+" rejected") {
			t.Fatalf("sync: %v\n%s", err, out)
		}
	})
}
