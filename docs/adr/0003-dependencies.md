# 3. Dependencies: modernc.org/sqlite and golang/snappy only

Status: Accepted

## Context

The health-connect exporter uses only the standard library. This server also needs a database,
and the image should stay static (distroless, no cgo).

## Decision

| Module | Why |
|--------|-----|
| `modernc.org/sqlite` | SQLite in pure Go (a C-to-Go translation), so `CGO_ENABLED=0` works and the image needs no libc. `mattn/go-sqlite3` needs cgo. Its own requirements (`modernc.org/libc`, `mathutil`, `memory`, `golang.org/x/sys`, `google/uuid`, `dustin/go-humanize`, `mattn/go-isatty`, `ncruces/go-strftime`, `remyoudompheng/bigfft`) come with it. |
| `github.com/golang/snappy` | Snappy block encoding for remote write, one small package with no dependencies. The health-connect exporter's literal-only encoder would also work; a library gives real compression for curves of up to 20000 samples and an independent decoder for the tests. |

Not used: `prometheus/prometheus` (`prompb`) and `gogo/protobuf`. The `WriteRequest` is four
message types and five fields, hand-encoded in `internal/remotewrite/protobuf.go` (the same
encoder as the health-connect exporter). The tests decode it back with an independent decoder
written from the protobuf wire format and compare one request to known bytes; the integration
test checks it against a real Prometheus.

## Consequences

- A static binary on `distroless/static`.
- `modernc.org/sqlite` is large to compile (first build takes a while) and must be updated now
  and then with `go get -u modernc.org/sqlite && go mod tidy`.
