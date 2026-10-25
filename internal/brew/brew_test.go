package brew

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func files(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join("..", "..", "testdata", "contract", dir, "*.json"))
	if len(paths) == 0 {
		t.Fatalf("no contract files in %s (run scripts/sync-contract.sh)", dir)
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

func TestValidFilesParse(t *testing.T) {
	for name, body := range files(t, "valid") {
		b, err := Parse(body)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if b.EndedAtMs <= b.StartedAtMs || b.DoseG <= 0 || b.Samples == nil {
			t.Errorf("%s: parsed badly: %+v", name, b)
		}
	}
}

func TestInvalidFilesFail(t *testing.T) {
	for name, body := range files(t, "invalid") {
		if _, err := Parse(body); err == nil {
			t.Errorf("%s: accepted, want an error", name)
		}
	}
}

func TestParseFields(t *testing.T) {
	b, err := Parse(files(t, "valid")["espresso.json"])
	if err != nil {
		t.Fatal(err)
	}
	if b.Mode != "espresso" || b.TimeToFirstDropS == nil || *b.TimeToFirstDropS != 6.2 || len(b.Samples) != 4 ||
		b.Samples[3].TMs != 28600 || b.Samples[3].WeightG != 36.4 {
		t.Fatalf("unexpected parse: %+v", b)
	}
	f, err := Parse(files(t, "valid")["filter.json"])
	if err != nil {
		t.Fatal(err)
	}
	if f.Mode != "filter" || f.TimeToFirstDropS != nil {
		t.Fatalf("filter: %+v", f)
	}
}

func TestErrorMessages(t *testing.T) {
	for body, want := range map[string]string{
		``:        "empty body",
		`[]`:      "JSON object",
		`{"x":1}`: `unknown property "x"`,
		`{} {}`:   "data after",
		`{"id":"A0000000-0000-0000-0000-000000000000"}`: "lowercase UUID",
	} {
		_, err := Parse([]byte(body))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want it to mention %q", body, err, want)
		}
	}
}
