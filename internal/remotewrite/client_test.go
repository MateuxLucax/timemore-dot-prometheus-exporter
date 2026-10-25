package remotewrite

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/golang/snappy"
)

// decodeWriteRequest is an independent protobuf decoder written from the wire format
// description (https://protobuf.dev/programming-guides/encoding/), used to check the encoder.
func decodeWriteRequest(b []byte) ([]Series, error) {
	var out []Series
	err := fields(b, func(f uint64, wt int, v []byte, _ uint64) error {
		if f != 1 || wt != 2 {
			return fmt.Errorf("WriteRequest: unexpected field %d/%d", f, wt)
		}
		var s Series
		err := fields(v, func(f uint64, wt int, v []byte, _ uint64) error {
			switch {
			case f == 1 && wt == 2:
				var l Label
				err := fields(v, func(f uint64, wt int, v []byte, _ uint64) error {
					switch f {
					case 1:
						l.Name = string(v)
					case 2:
						l.Value = string(v)
					default:
						return fmt.Errorf("Label: field %d", f)
					}
					return nil
				})
				s.Labels = append(s.Labels, l)
				return err
			case f == 2 && wt == 2:
				var p Point
				err := fields(v, func(f uint64, wt int, v []byte, n uint64) error {
					switch {
					case f == 1 && wt == 1:
						p.Value = math.Float64frombits(binary.LittleEndian.Uint64(v))
					case f == 2 && wt == 0:
						p.TsMs = int64(n)
					default:
						return fmt.Errorf("Sample: field %d/%d", f, wt)
					}
					return nil
				})
				s.Points = append(s.Points, p)
				return err
			}
			return fmt.Errorf("TimeSeries: field %d/%d", f, wt)
		})
		out = append(out, s)
		return err
	})
	return out, err
}

func fields(b []byte, fn func(field uint64, wireType int, v []byte, n uint64) error) error {
	for len(b) > 0 {
		key, k := binary.Uvarint(b)
		if k <= 0 {
			return errors.New("bad key")
		}
		b = b[k:]
		f, wt := key>>3, int(key&7)
		switch wt {
		case 0:
			n, k := binary.Uvarint(b)
			if k <= 0 {
				return errors.New("bad varint")
			}
			b = b[k:]
			if err := fn(f, wt, nil, n); err != nil {
				return err
			}
		case 1:
			if len(b) < 8 {
				return errors.New("short fixed64")
			}
			if err := fn(f, wt, b[:8], 0); err != nil {
				return err
			}
			b = b[8:]
		case 2:
			n, k := binary.Uvarint(b)
			if k <= 0 || uint64(len(b)-k) < n {
				return errors.New("bad length")
			}
			if err := fn(f, wt, b[k:k+int(n)], 0); err != nil {
				return err
			}
			b = b[k+int(n):]
		default:
			return fmt.Errorf("wire type %d", wt)
		}
	}
	return nil
}

func TestEncodeKnownBytes(t *testing.T) {
	// One series {__name__="a"} with one sample (1.0 @ 1).
	got := encodeWriteRequest([]Series{{Labels: []Label{{"__name__", "a"}}, Points: []Point{{1, 1}}}})
	want := []byte{
		0x0a, 0x1c, // timeseries, 28 bytes
		0x0a, 0x0d, 0x0a, 0x08, '_', '_', 'n', 'a', 'm', 'e', '_', '_', 0x12, 0x01, 'a', // label
		0x12, 0x0b, 0x09, 0, 0, 0, 0, 0, 0, 0xf0, 0x3f, 0x10, 0x01, // sample
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  % x\nwant % x", got, want)
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	in := []Series{
		{Labels: []Label{{"__name__", "caparao_brew_weight_grams"}, {"id", "0f8e1c2a-5b7d-4c3e-9a10-2f6d8b4e7c91"}, {"mode", "espresso"}},
			Points: []Point{{0, 1792931400000}, {-0.3, 1792931406200}, {36.4, 1792931428600}}},
		{Labels: []Label{{"__name__", "caparao_brew_ratio"}, {"mode", "filter"}}, Points: []Point{{16.6666, 1}}},
	}
	raw, err := snappy.Decode(nil, Encode(in))
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeWriteRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, in)
	}
}

func TestNormalize(t *testing.T) {
	s := Series{Labels: []Label{{"mode", "x"}, {"__name__", "m"}}, Points: []Point{{3, 30}, {1, 10}, {2, 30}, {4, 20}}}
	s.Normalize()
	if s.Labels[0].Name != "__name__" {
		t.Errorf("labels not sorted: %+v", s.Labels)
	}
	// Duplicate timestamp 30: the later one in input order (2) wins.
	if want := []Point{{1, 10}, {4, 20}, {2, 30}}; !reflect.DeepEqual(s.Points, want) {
		t.Errorf("points = %+v, want %+v", s.Points, want)
	}
}

func TestWriteStatuses(t *testing.T) {
	for status, check := range map[int]func(error) bool{
		204: func(err error) bool { return err == nil },
		400: func(err error) bool { var r *RejectedError; return errors.As(err, &r) },
		404: func(err error) bool { var r *RejectedError; return err != nil && !errors.As(err, &r) },
		503: func(err error) bool { var r *RejectedError; return err != nil && !errors.As(err, &r) },
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Content-Encoding") != "snappy" || r.Header.Get("Content-Type") != "application/x-protobuf" ||
				r.Header.Get("X-Prometheus-Remote-Write-Version") != "0.1.0" {
				t.Errorf("bad headers: %v", r.Header)
			}
			body, _ := io.ReadAll(r.Body)
			if raw, err := snappy.Decode(nil, body); err != nil {
				t.Error(err)
			} else if _, err := decodeWriteRequest(raw); err != nil {
				t.Error(err)
			}
			w.WriteHeader(status)
		}))
		err := NewClient(srv.URL, srv.Client()).Write(t.Context(), []Series{{Labels: []Label{{"__name__", "a"}}, Points: []Point{{1, 1}}}})
		if !check(err) {
			t.Errorf("status %d: err = %v", status, err)
		}
		srv.Close()
	}
}
