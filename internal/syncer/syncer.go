// Package syncer copies brews from the store to Prometheus with remote write, keeping their
// original timestamps, and advances each brew's checkpoint only after Prometheus answered.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/MateuxLucax/timemore-dot-prometheus-exporter/internal/brew"
	"github.com/MateuxLucax/timemore-dot-prometheus-exporter/internal/remotewrite"
	"github.com/MateuxLucax/timemore-dot-prometheus-exporter/internal/store"
)

// MaxFuture is how far after now a brew may end. A later end time means a wrong phone clock;
// sending it would move Prometheus' head time forward and make normal brews "too old".
const MaxFuture = time.Hour

const batchSize = 50

// Writer is the remote write client (remotewrite.Client, or a fake in tests).
type Writer interface {
	Write(ctx context.Context, series []remotewrite.Series) error
}

// Syncer sends pending brews.
type Syncer struct {
	Store  *store.Store
	Writer Writer
	Logger *log.Logger
	Now    func() time.Time // defaults to time.Now
}

// Result counts what one run did.
type Result struct {
	Sent, Rejected int
}

// Run sends every pending brew once. It stops at the first error that is not a rejection
// (Prometheus unreachable, 5xx, 404, ...): that brew and the ones after it stay pending for
// the next run. A rejection (HTTP 400) is logged and stored in the brew's sync_error, and the
// brew is not retried until it is replaced or re-sent with the resend command.
func (s *Syncer) Run(ctx context.Context) (Result, error) {
	var res Result
	seen := map[string]bool{}
	for {
		batch, err := s.Store.Pending(ctx, batchSize+len(seen))
		if err != nil {
			return res, err
		}
		progressed := false
		for _, p := range batch {
			if seen[p.Brew.ID] {
				continue // replaced while this run was sending it: next run
			}
			seen[p.Brew.ID] = true
			progressed = true
			reason, err := s.send(ctx, &p.Brew)
			if err != nil {
				return res, fmt.Errorf("brew %s: %w", p.Brew.ID, err)
			}
			if reason != "" {
				res.Rejected++
				s.Logger.Printf("WARN sync: brew %s rejected: %s", p.Brew.ID, reason)
			} else {
				res.Sent++
			}
			if err := s.Store.MarkSent(ctx, p.Brew.ID, p.Version, reason); err != nil {
				return res, err
			}
		}
		if !progressed {
			return res, nil
		}
	}
}

// send writes one brew. It returns a non-empty reason if Prometheus rejected (some of) it for good.
func (s *Syncer) send(ctx context.Context, b *brew.Brew) (string, error) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	if b.EndedAtMs > now().Add(MaxFuture).UnixMilli() {
		return "ended_at_ms is more than 1h in the future (phone clock wrong?); not sent", nil
	}
	series := Series(b)
	err := s.Writer.Write(ctx, series)
	var rej *remotewrite.RejectedError
	if !errors.As(err, &rej) {
		return "", err
	}
	// Prometheus refuses the whole request if one sample is bad. Send series one by one so
	// those that are fine (e.g. an unchanged curve of a replaced brew) still land.
	var reasons []string
	for _, one := range series {
		err := s.Writer.Write(ctx, []remotewrite.Series{one})
		if errors.As(err, &rej) {
			reasons = append(reasons, labelValue(one, "__name__")+": "+rej.Msg)
		} else if err != nil {
			return "", err
		}
	}
	if len(reasons) == 0 {
		return "", nil
	}
	return strings.Join(reasons, "; "), nil
}

func labelValue(s remotewrite.Series, name string) string {
	for _, l := range s.Labels {
		if l.Name == name {
			return l.Value
		}
	}
	return ""
}

// Loop runs a sync now and then every interval until ctx is done. Errors are logged; the
// next tick retries.
func (s *Syncer) Loop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		start := time.Now()
		res, err := s.Run(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			s.Logger.Printf("ERROR sync: %v (sent %d, rejected %d; retrying in %s)", err, res.Sent, res.Rejected, interval)
		case res.Sent+res.Rejected > 0:
			s.Logger.Printf("sync: sent %d, rejected %d in %s", res.Sent, res.Rejected, time.Since(start).Round(time.Millisecond))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Series maps a brew to its time series: summaries at the brew's end time, the curve at
// started_at_ms + t_ms. Labels: mode and id (see docs/adr/0004-labels-and-cardinality.md).
func Series(b *brew.Brew) []remotewrite.Series {
	end := b.EndedAtMs
	summary := func(name string, v float64) remotewrite.Series {
		return remotewrite.Series{Labels: labels(name, b), Points: []remotewrite.Point{{Value: v, TsMs: end}}}
	}
	out := []remotewrite.Series{
		summary("caparao_brew_yield_grams", b.YieldG),
		summary("caparao_brew_dose_grams", b.DoseG),
		summary("caparao_brew_ratio", b.YieldG/b.DoseG), // dose_g > 0 by validation
		summary("caparao_brew_duration_seconds", float64(b.EndedAtMs-b.StartedAtMs)/1000),
		summary("caparao_brew_flow_avg_grams_per_second", b.FlowAvgGps),
	}
	if b.Mode == "espresso" && b.TimeToFirstDropS != nil {
		out = append(out, summary("caparao_brew_time_to_first_drop_seconds", *b.TimeToFirstDropS))
	}
	if len(b.Samples) > 0 {
		weight := remotewrite.Series{Labels: labels("caparao_brew_weight_grams", b)}
		flow := remotewrite.Series{Labels: labels("caparao_brew_flow_grams_per_second", b)}
		for _, sm := range b.Samples {
			ts := b.StartedAtMs + sm.TMs
			weight.Points = append(weight.Points, remotewrite.Point{Value: sm.WeightG, TsMs: ts})
			flow.Points = append(flow.Points, remotewrite.Point{Value: sm.FlowGps, TsMs: ts})
		}
		out = append(out, weight, flow)
	}
	for i := range out {
		out[i].Normalize()
	}
	return out
}

func labels(name string, b *brew.Brew) []remotewrite.Label {
	return []remotewrite.Label{{Name: "__name__", Value: name}, {Name: "id", Value: b.ID}, {Name: "mode", Value: b.Mode}}
}
