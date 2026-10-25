# Acceptance checklist (Caparaó roadmap, stage 8)

Verified on 2026-10-25 with Go 1.27.2, Prometheus v3.5.0 and Grafana 12.1.1 (Docker).

| Criterion | Status | How |
|-----------|--------|-----|
| Separate repository, Go | Done | This repository. |
| SQLite is the source of truth; 2xx only after commit | Done | `internal/api` calls `store.Upsert` (one transaction) before writing the status; `internal/api` tests run on a real SQLite file. |
| The `contract/` files pass exactly as against the stub | Done | `internal/api/server_test.go` is the stub's suite against `testdata/contract`: every valid file `201` then `200`, every invalid file `400` with `{"error": ...}`, `401`, `413`, `405`, 20000/20001 samples. `internal/brew` tests also run every file through the validator. |
| Connection test: `GET /healthz` without token, authenticated `GET /api/v1/brews` creates nothing | Done | `TestHealthz`, `TestAuth`, `TestValidFilesAreIdempotent` (list). |
| Upsert on a real SQLite: re-sending an `id` duplicates neither brew nor samples | Done | `internal/store` `TestUpsertIsIdempotent` (temp dir file; also a changed body replaces brew and samples); `TestMigrationsAreVersionedAndReopenable`. |
| Separate sync every 15 min (configurable), original timestamps, checkpoint after success | Done | `internal/syncer`; `SYNC_INTERVAL`; `TestCheckpoint`, `TestReplacedBrewIsResent`. |
| Prometheus down: API keeps accepting; when back, the next sync sends everything, nothing lost or duplicated | Done | `TestPrometheusDownThenUp` (fake writer: nothing advanced while down, every point written exactly once after), `TestRealClientDownThenUp` (real HTTP client, 503 then 204). |
| Per-brew summaries with label `mode`, curve with each sample's timestamp | Done | `TestSeriesTimestamps`; integration test. |
| A command re-sends everything | Done | `resend` subcommand; integration test checks it is accepted and duplicates nothing. |
| Remote write 1.0, protobuf + snappy | Done | `internal/remotewrite` tests: known bytes, round trip through an independent decoder, headers, status handling. |
| Integration test with a real Prometheus: summaries at end time, curve at each sample's time | Done | `scripts/integration.sh` (passed): exact timestamps and values for all 8 metrics; resend; a 60-day-old brew reported as rejected. |
| Docker Compose with Prometheus and Grafana and a dashboard | Done | Smoke-tested with the image built locally: upload `201`, sync sent the brew, Grafana provisioned the datasource and the "Caparaó brews" dashboard and answered its queries. |
| `scripts/e2e.sh` passes against the exporter and the brew shows in Grafana at the right time | **Not verified here** | `scripts/e2e.sh` lives in the Caparaó repository and needs a device; switching it from the stub to this server is a change in that repository. |
| `go vet ./...`, `go test ./...` | Done | Both pass; `go vet -tags integration ./...` too. |
