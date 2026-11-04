# AGENTS.md

Timemore Dot Prometheus Exporter: the server for the Caparaó app, receiving brews over HTTP,
storing them in SQLite, and shipping them to Prometheus via remote write.

## Contract

- `testdata/contract` is the canonical upload contract (issues #11-#15, esp. #15). A change
  there requires the Caparaó app to bump its pin (`scripts/exporter.sh` there) and update itself.
- SQLite is the source of truth; 2xx only after commit. Remote write keeps original timestamps
  (summaries at brew end, curve at `started_at_ms + t_ms`), 30-day out-of-order window.
- Labels: `mode` and `id` only, no others (cardinality). Each brew makes 7-8 short-lived series.

## Dependencies

- Only `modernc.org/sqlite` (pure-Go SQLite, CGO-free static image) and `github.com/golang/snappy`.
- No `prometheus/prometheus` / `gogo/protobuf`: the remote-write protobuf is hand-encoded.

## Tests

- `go vet ./... && go test ./...` must pass. `scripts/integration.sh` runs against real Prometheus.

## Public repo rules

- Examples only: `coffee.example.com`, `dev-token`. Never real domains, tokens, or passwords.

## Process

- Never merge: the user merges. Issues are the only tracker and decision log; each PR links its
  issue. Create issues and PRs with assignee `MateuxLucax`.
