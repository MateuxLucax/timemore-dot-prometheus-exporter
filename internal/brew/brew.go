// Package brew validates upload bodies against the contract in testdata/contract (a copy of caparao/contract).
// The rules and messages are identical to caparao/stub/validate.go.
package brew

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
)

const MaxSamples = 20000

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Sample is one point of the weight/flow curve.
type Sample struct {
	TMs     int64   `json:"t_ms"`
	WeightG float64 `json:"weight_g"`
	FlowGps float64 `json:"flow_gps"`
}

// Brew is a validated upload. It mirrors contract/brew.schema.json.
type Brew struct {
	ID               string   `json:"id"`
	Mode             string   `json:"mode"`
	StartedAtMs      int64    `json:"started_at_ms"`
	EndedAtMs        int64    `json:"ended_at_ms"`
	DoseG            float64  `json:"dose_g"`
	YieldG           float64  `json:"yield_g"`
	TimeToFirstDropS *float64 `json:"time_to_first_drop_s"`
	FlowAvgGps       float64  `json:"flow_avg_gps"`
	FlowPeakGps      float64  `json:"flow_peak_gps"`
	Interrupted      bool     `json:"interrupted"`
	Samples          []Sample `json:"samples"`
}

// ValidID reports whether id is a lowercase UUID, the only form a brew id takes.
func ValidID(id string) bool { return uuidRe.MatchString(id) }

// Parse decodes and validates a request body against the contract rules.
func Parse(body []byte) (*Brew, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var raw any
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("empty body")
		}
		return nil, errors.New("body is not valid JSON")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("body has data after the JSON value")
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("body must be a JSON object")
	}
	if err := onlyKeys(obj, "", "id", "mode", "started_at_ms", "ended_at_ms", "dose_g", "yield_g",
		"time_to_first_drop_s", "flow_avg_gps", "flow_peak_gps", "interrupted", "samples"); err != nil {
		return nil, err
	}

	b := &Brew{}
	var err error

	if b.ID, err = str(obj, "id"); err != nil {
		return nil, err
	}
	if !uuidRe.MatchString(b.ID) {
		return nil, errors.New("id must be a lowercase UUID")
	}
	if b.Mode, err = str(obj, "mode"); err != nil {
		return nil, err
	}
	if b.Mode != "filter" && b.Mode != "espresso" {
		return nil, errors.New(`mode must be "filter" or "espresso"`)
	}
	if b.StartedAtMs, err = integer(obj, "started_at_ms", 0); err != nil {
		return nil, err
	}
	if b.EndedAtMs, err = integer(obj, "ended_at_ms", 1); err != nil {
		return nil, err
	}
	if b.EndedAtMs <= b.StartedAtMs {
		return nil, errors.New("ended_at_ms must be greater than started_at_ms")
	}
	if b.DoseG, err = number(obj, "dose_g"); err != nil {
		return nil, err
	}
	if b.DoseG <= 0 || b.DoseG > 1000 {
		return nil, errors.New("dose_g must be > 0 and <= 1000")
	}
	if b.YieldG, err = number(obj, "yield_g"); err != nil {
		return nil, err
	}
	if b.YieldG < 0 || b.YieldG > 10000 {
		return nil, errors.New("yield_g must be >= 0 and <= 10000")
	}
	v, present := obj["time_to_first_drop_s"]
	if !present {
		return nil, errors.New("time_to_first_drop_s is required (null when there is none)")
	}
	if v != nil {
		t, err := number(obj, "time_to_first_drop_s")
		if err != nil {
			return nil, err
		}
		if t < 0 {
			return nil, errors.New("time_to_first_drop_s must be >= 0")
		}
		b.TimeToFirstDropS = &t
	}
	if b.FlowAvgGps, err = number(obj, "flow_avg_gps"); err != nil {
		return nil, err
	}
	if b.FlowAvgGps < 0 {
		return nil, errors.New("flow_avg_gps must be >= 0")
	}
	if b.FlowPeakGps, err = number(obj, "flow_peak_gps"); err != nil {
		return nil, err
	}
	if b.FlowPeakGps < 0 {
		return nil, errors.New("flow_peak_gps must be >= 0")
	}
	iv, present := obj["interrupted"]
	if !present {
		return nil, errors.New("interrupted is required")
	}
	if b.Interrupted, ok = iv.(bool); !ok {
		return nil, errors.New("interrupted must be a boolean")
	}

	sv, present := obj["samples"]
	if !present {
		return nil, errors.New("samples is required")
	}
	arr, ok := sv.([]any)
	if !ok {
		return nil, errors.New("samples must be an array")
	}
	if len(arr) > MaxSamples {
		return nil, fmt.Errorf("samples has more than %d items", MaxSamples)
	}
	b.Samples = make([]Sample, 0, len(arr))
	for i, item := range arr {
		so, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("samples[%d] must be an object", i)
		}
		prefix := fmt.Sprintf("samples[%d].", i)
		if err := onlyKeys(so, prefix, "t_ms", "weight_g", "flow_gps"); err != nil {
			return nil, err
		}
		var s Sample
		if s.TMs, err = integer(so, "t_ms", 0); err != nil {
			return nil, fmt.Errorf("%s%w", prefix, err)
		}
		if s.WeightG, err = number(so, "weight_g"); err != nil {
			return nil, fmt.Errorf("%s%w", prefix, err)
		}
		if s.FlowGps, err = number(so, "flow_gps"); err != nil {
			return nil, fmt.Errorf("%s%w", prefix, err)
		}
		b.Samples = append(b.Samples, s)
	}
	return b, nil
}

func onlyKeys(obj map[string]any, prefix string, allowed ...string) error {
	set := make(map[string]bool, len(allowed))
	for _, k := range allowed {
		set[k] = true
	}
	for k := range obj {
		if !set[k] {
			return fmt.Errorf("unknown property %q", prefix+k)
		}
	}
	return nil
}

func str(obj map[string]any, key string) (string, error) {
	v, ok := obj[key]
	if !ok {
		return "", fmt.Errorf("%s is required", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return s, nil
}

func number(obj map[string]any, key string) (float64, error) {
	v, ok := obj[key]
	if !ok {
		return 0, fmt.Errorf("%s is required", key)
	}
	n, ok := v.(json.Number)
	if !ok {
		return 0, fmt.Errorf("%s must be a number", key)
	}
	f, err := n.Float64()
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, fmt.Errorf("%s is not a finite number", key)
	}
	return f, nil
}

func integer(obj map[string]any, key string, min int64) (int64, error) {
	f, err := number(obj, key)
	if err != nil {
		return 0, err
	}
	if f != math.Trunc(f) || math.Abs(f) > 1<<53 {
		return 0, fmt.Errorf("%s must be a whole number", key)
	}
	if int64(f) < min {
		return 0, fmt.Errorf("%s must be >= %d", key, min)
	}
	return int64(f), nil
}
