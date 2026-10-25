// Package remotewrite is a minimal Prometheus remote write 1.0 client: a hand-encoded
// WriteRequest protobuf, snappy-compressed (github.com/golang/snappy).
package remotewrite

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"

	"github.com/golang/snappy"
)

// Label is one label pair. The metric name is the label "__name__".
type Label struct{ Name, Value string }

// Point is one sample: a value at a Unix timestamp in milliseconds.
type Point struct {
	Value float64
	TsMs  int64
}

// Series is one time series. Labels are sorted by name and Points by timestamp by Normalize.
type Series struct {
	Labels []Label
	Points []Point
}

// Normalize sorts labels and points and keeps the last point for a repeated timestamp,
// so a request never carries two values for one timestamp of one series.
func (s *Series) Normalize() {
	sort.Slice(s.Labels, func(i, j int) bool { return s.Labels[i].Name < s.Labels[j].Name })
	sort.SliceStable(s.Points, func(i, j int) bool { return s.Points[i].TsMs < s.Points[j].TsMs })
	out := s.Points[:0]
	for _, p := range s.Points {
		if n := len(out); n > 0 && out[n-1].TsMs == p.TsMs {
			out[n-1] = p
			continue
		}
		out = append(out, p)
	}
	s.Points = out
}

// RejectedError is an HTTP 400 from the receiver: the data itself was refused (a sample
// older than the out-of-order window, a different value for an existing timestamp).
// Retrying cannot help. Any other failure is a plain error and should be retried later.
type RejectedError struct{ Msg string }

func (e *RejectedError) Error() string { return e.Msg }

// Client posts to one remote write endpoint.
type Client struct {
	url    string
	client *http.Client
}

// NewClient returns a Client that posts to url.
func NewClient(url string, client *http.Client) *Client { return &Client{url: url, client: client} }

// Encode returns the snappy-compressed WriteRequest for series.
func Encode(series []Series) []byte { return snappy.Encode(nil, encodeWriteRequest(series)) }

// Write sends series in one request. Prometheus applies a request all or nothing.
func (c *Client) Write(ctx context.Context, series []Series) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(Encode(series)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Encoding", "snappy")
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode/100 == 2:
		return nil
	case resp.StatusCode == http.StatusBadRequest:
		return &RejectedError{Msg: fmt.Sprintf("remote write rejected (400): %s", bytes.TrimSpace(msg))}
	default:
		return fmt.Errorf("remote write failed (%d): %s", resp.StatusCode, bytes.TrimSpace(msg))
	}
}
