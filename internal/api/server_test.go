package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MateuxLucax/timemore-dot-prometheus-exporter/internal/brew"
	"github.com/MateuxLucax/timemore-dot-prometheus-exporter/internal/store"
)

const testToken = "test-token"

func newTestServer(t *testing.T) (*httptest.Server, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ts := httptest.NewServer(NewServer(testToken, st, log.New(&logs, "", 0)))
	t.Cleanup(ts.Close)
	return ts, &logs
}

func do(t *testing.T, method, url, token string, body []byte) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func contractFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", "testdata", "contract", dir, "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no contract files in %s: %v", dir, err)
	}
	out := map[string][]byte{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(p)] = b
	}
	return out
}

func TestValidFilesAreIdempotent(t *testing.T) {
	ts, _ := newTestServer(t)
	files := contractFiles(t, "valid")
	for name, body := range files {
		resp, b := do(t, "POST", ts.URL+"/api/v1/brews", testToken, body)
		if resp.StatusCode != 201 {
			t.Errorf("%s: first POST = %d, want 201 (%s)", name, resp.StatusCode, b)
		}
	}
	for name, body := range files {
		resp, b := do(t, "POST", ts.URL+"/api/v1/brews", testToken, body)
		if resp.StatusCode != 200 {
			t.Errorf("%s: resend = %d, want 200 (%s)", name, resp.StatusCode, b)
		}
	}
	_, b := do(t, "GET", ts.URL+"/api/v1/brews", testToken, nil)
	var list []map[string]any
	if err := json.Unmarshal(b, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != len(files) {
		t.Fatalf("list has %d brews, want %d", len(list), len(files))
	}
	ids := map[any]bool{}
	for i, item := range list {
		if _, has := item["samples"]; has {
			t.Error("list must not include samples")
		}
		if _, has := item["sample_count"]; !has {
			t.Error("list must include sample_count")
		}
		ids[item["id"]] = true
		if i > 0 && list[i-1]["started_at_ms"].(float64) < item["started_at_ms"].(float64) {
			t.Error("list is not newest first")
		}
	}
	if len(ids) != len(files) {
		t.Error("duplicate ids in list")
	}
}

func TestResendWithChangedBodyReplaces(t *testing.T) {
	ts, _ := newTestServer(t)
	body := contractFiles(t, "valid")["espresso.json"]
	do(t, "POST", ts.URL+"/api/v1/brews", testToken, body)
	changed := bytes.Replace(body, []byte(`"yield_g": 36.4`), []byte(`"yield_g": 40.0`), 1)
	if bytes.Equal(changed, body) {
		t.Fatal("test fixture did not change")
	}
	if resp, _ := do(t, "POST", ts.URL+"/api/v1/brews", testToken, changed); resp.StatusCode != 200 {
		t.Fatalf("resend changed = %d, want 200", resp.StatusCode)
	}
	_, b := do(t, "GET", ts.URL+"/api/v1/brews", testToken, nil)
	var list []map[string]any
	_ = json.Unmarshal(b, &list)
	if len(list) != 1 || list[0]["yield_g"].(float64) != 40.0 {
		t.Fatalf("brew not replaced: %s", b)
	}
}

func TestInvalidFilesAreRejected(t *testing.T) {
	ts, _ := newTestServer(t)
	for name, body := range contractFiles(t, "invalid") {
		resp, b := do(t, "POST", ts.URL+"/api/v1/brews", testToken, body)
		if resp.StatusCode != 400 {
			t.Errorf("%s: status = %d, want 400", name, resp.StatusCode)
			continue
		}
		var e map[string]string
		if json.Unmarshal(b, &e) != nil || e["error"] == "" {
			t.Errorf("%s: body is not {\"error\": ...}: %s", name, b)
		}
	}
	_, b := do(t, "GET", ts.URL+"/api/v1/brews", testToken, nil)
	if strings.TrimSpace(string(b)) != "[]" {
		t.Errorf("invalid bodies were stored: %s", b)
	}
}

// The sample limit (20000) is generated here rather than kept as a contract file: 20001 samples is ~800 KB of JSON.
func TestSampleLimit(t *testing.T) {
	ts, _ := newTestServer(t)
	var d map[string]any
	_ = json.Unmarshal(contractFiles(t, "valid")["espresso.json"], &d)
	for count, want := range map[int]int{brew.MaxSamples: 201, brew.MaxSamples + 1: 400} {
		samples := make([]map[string]any, count)
		for i := range samples {
			samples[i] = map[string]any{"t_ms": i, "weight_g": 0, "flow_gps": 0}
		}
		d["samples"] = samples
		body, _ := json.Marshal(d)
		if resp, b := do(t, "POST", ts.URL+"/api/v1/brews", testToken, body); resp.StatusCode != want {
			t.Errorf("%d samples = %d (%s), want %d", count, resp.StatusCode, b, want)
		}
	}
}

func TestAuth(t *testing.T) {
	ts, logs := newTestServer(t)
	body := contractFiles(t, "valid")["espresso.json"]
	for _, tc := range []struct{ method, token string }{
		{"POST", ""}, {"POST", "wrong"}, {"GET", ""}, {"GET", "wrong"},
	} {
		resp, _ := do(t, tc.method, ts.URL+"/api/v1/brews", tc.token, body)
		if resp.StatusCode != 401 {
			t.Errorf("%s token=%q = %d, want 401", tc.method, tc.token, resp.StatusCode)
		}
	}
	do(t, "POST", ts.URL+"/api/v1/brews", testToken, body)
	if strings.Contains(logs.String(), testToken) || strings.Contains(logs.String(), "wrong") {
		t.Errorf("token leaked into logs: %s", logs)
	}
	if !strings.Contains(logs.String(), `POST "/api/v1/brews" 201 id=0f8e1c2a-5b7d-4c3e-9a10-2f6d8b4e7c91`) {
		t.Errorf("log line missing: %s", logs)
	}
}

func TestOversizeBody(t *testing.T) {
	ts, _ := newTestServer(t)
	big := bytes.Repeat([]byte(" "), MaxBodyBytes+1)
	resp, _ := do(t, "POST", ts.URL+"/api/v1/brews", testToken, big)
	if resp.StatusCode != 413 {
		t.Fatalf("oversize = %d, want 413", resp.StatusCode)
	}
}

func TestWrongMethod(t *testing.T) {
	ts, _ := newTestServer(t)
	for _, m := range []string{"PUT", "DELETE", "PATCH"} {
		resp, _ := do(t, m, ts.URL+"/api/v1/brews", testToken, nil)
		if resp.StatusCode != 405 {
			t.Errorf("%s = %d, want 405", m, resp.StatusCode)
		}
	}
	if resp, _ := do(t, "POST", ts.URL+"/healthz", "", nil); resp.StatusCode != 405 {
		t.Errorf("POST /healthz = %d, want 405", resp.StatusCode)
	}
}

func TestHealthz(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, b := do(t, "GET", ts.URL+"/healthz", "", nil)
	if resp.StatusCode != 200 || string(b) != "ok" {
		t.Fatalf("healthz = %d %q", resp.StatusCode, b)
	}
}

func TestUnknownPath(t *testing.T) {
	ts, _ := newTestServer(t)
	if resp, _ := do(t, "GET", ts.URL+"/nope", testToken, nil); resp.StatusCode != 404 {
		t.Fatalf("unknown path = %d, want 404", resp.StatusCode)
	}
}

func TestGetBrewReturnsSamples(t *testing.T) {
	ts, _ := newTestServer(t)
	for name, body := range contractFiles(t, "valid") {
		do(t, "POST", ts.URL+"/api/v1/brews", testToken, body)
		want, err := brew.Parse(body)
		if err != nil {
			t.Fatal(err)
		}
		resp, b := do(t, "GET", ts.URL+"/api/v1/brews/"+want.ID, testToken, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("%s: GET = %d (%s)", name, resp.StatusCode, b)
		}
		// The response is a valid upload body describing the same brew, samples in upload order.
		got, err := brew.Parse(b)
		if err != nil {
			t.Fatalf("%s: response is not a valid brew: %v (%s)", name, err, b)
		}
		if gj, _ := json.Marshal(got); string(gj) != mustJSON(t, want) {
			t.Errorf("%s: got %s, want %s", name, gj, mustJSON(t, want))
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGetBrewErrors(t *testing.T) {
	ts, logs := newTestServer(t)
	const unknown = "00000000-0000-4000-8000-000000000000"
	cases := []struct {
		method, path, token string
		want                int
	}{
		{"GET", "/api/v1/brews/" + unknown, testToken, 404},
		{"GET", "/api/v1/brews/NOT-A-UUID", testToken, 404},
		{"GET", "/api/v1/brews/" + unknown + "/x", testToken, 404},
		{"GET", "/api/v1/brews/", testToken, 404},
		{"GET", "/api/v1/brews/" + unknown, "", 401},
		{"GET", "/api/v1/brews/" + unknown, "wrong", 401},
		{"POST", "/api/v1/brews/" + unknown, testToken, 405},
		{"DELETE", "/api/v1/brews/" + unknown, testToken, 405},
	}
	for _, c := range cases {
		resp, b := do(t, c.method, ts.URL+c.path, c.token, nil)
		if resp.StatusCode != c.want {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, resp.StatusCode, c.want)
		}
		var e map[string]string
		if json.Unmarshal(b, &e) != nil || e["error"] == "" {
			t.Errorf("%s %s: error body %q", c.method, c.path, b)
		}
	}
	if strings.Contains(logs.String(), "wrong") {
		t.Error("token leaked into the log")
	}
}
