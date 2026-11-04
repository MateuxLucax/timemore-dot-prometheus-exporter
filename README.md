# Timemore Dot Prometheus Exporter

![Go](https://img.shields.io/badge/go-%2300ADD8.svg?style=for-the-badge&logo=go&logoColor=white)
![SQLite](https://img.shields.io/badge/sqlite-%2307405e.svg?style=for-the-badge&logo=sqlite&logoColor=white)
![Docker](https://img.shields.io/badge/docker-%230db7ed.svg?style=for-the-badge&logo=docker&logoColor=white)
![Prometheus](https://img.shields.io/badge/Prometheus-E6522C?style=for-the-badge&logo=Prometheus&logoColor=white)
![Grafana](https://img.shields.io/badge/grafana-%23F46800.svg?style=for-the-badge&logo=grafana&logoColor=white)

The server for the [Caparaó](https://github.com/MateuxLucax/caparao) Android app, which records
espresso, filter and French press brews from a Timemore scale. It receives each brew over HTTP,
stores it in SQLite, and copies it to Prometheus with the
[remote write](https://prometheus.io/docs/specs/prw/remote_write_spec/) 1.0 protocol, keeping
the brew's original timestamps.

```mermaid
flowchart LR
    scale["Timemore scale"] -->|BLE| app["Caparaó<br/>(Android app)"]
    app -->|"POST /api/v1/brews"| api["this server: API"]
    api -->|"upsert, commit, then 2xx"| db[("SQLite")]
    db --> sync["this server: sync loop<br/>every 15 min"]
    sync -->|"remote write"| prom[("Prometheus")] --> graf["Grafana"]
```

SQLite is the source of truth: the app gets its 2xx as soon as the brew is committed, so a
Prometheus outage never blocks or loses an upload. A sync loop in the same process sends what
Prometheus has not seen yet and only then advances its per-brew checkpoint.

## API

The wire format is the Caparaó upload contract, which is canonical in [`testdata/contract`](testdata/contract)
(see its [README](testdata/contract/README.md)). A contract change starts here; the Caparaó app
then bumps its pinned commit and updates itself to match.

| Request | Auth | Answer |
|---------|------|--------|
| `POST /api/v1/brews` | Bearer | `201` new brew, `200` existing `id` replaced (brew and all its samples), `400` invalid body, `401` bad token, `413` over 4 MiB, `405` wrong method. Errors are `{"error": "..."}`. 2xx only after the SQLite commit. |
| `GET /api/v1/brews` | Bearer | `200`, every brew without samples (with `sample_count`), newest first. Creates nothing; used by the app's connection test. |
| `GET /api/v1/brews/{id}` | Bearer | `200`, the brew with all its samples in the upload format (samples in upload order), `404` unknown id, `405` other methods. |
| `GET /healthz` | none | `200 ok` |

`mode` is `espresso`, `filter` or `french_press`. Filter and French press brews send
`time_to_first_drop_s: null`; only espresso has a first drop.

## Metrics

All carry the labels `mode` (`espresso`, `filter` or `french_press`) and `id` (the brew's UUID;
why, see the [labels decision](https://github.com/MateuxLucax/timemore-dot-prometheus-exporter/issues/14)).

| Metric | Timestamp | |
|--------|-----------|---|
| `caparao_brew_yield_grams` | brew end | |
| `caparao_brew_dose_grams` | brew end | |
| `caparao_brew_ratio` | brew end | yield / dose |
| `caparao_brew_duration_seconds` | brew end | end - start |
| `caparao_brew_time_to_first_drop_seconds` | brew end | espresso only (filter and French press have no first drop) |
| `caparao_brew_flow_avg_grams_per_second` | brew end | |
| `caparao_brew_weight_grams` | each sample (`started_at_ms + t_ms`) | the curve |
| `caparao_brew_flow_grams_per_second` | each sample | the curve |

Example queries:

```promql
# Brews per day
sum by (mode) (count_over_time(caparao_brew_yield_grams[1d]))
# Average espresso ratio over the last week
avg_over_time(caparao_brew_ratio{mode="espresso"}[7d])
```

## Configuration

| Variable | Default | |
|----------|---------|---|
| `AUTH_TOKEN` | (required) | Bearer token the app sends. Use a long random value (`openssl rand -hex 32`); shorter than 32 characters logs a warning (fine for `dev-token` in development). |
| `REMOTE_WRITE_URL` | (required) | e.g. `http://prometheus:9090/api/v1/write`. Credentials in the URL are redacted in the log. |
| `LISTEN_ADDR` | `:8080` | |
| `DB_PATH` | `/data/caparao.db` | SQLite file. Created and migrated on start. |
| `SYNC_INTERVAL` | `15m` | Go duration. A sync also runs at start. |

## Commands

```sh
timemore-dot-prometheus-exporter          # serve the API and run the sync loop (default; also "serve")
timemore-dot-prometheus-exporter sync     # one sync now, then exit (exit 1 if Prometheus failed)
timemore-dot-prometheus-exporter resend   # mark every brew unsent and sync: re-sends everything
```

In compose: `docker compose exec exporter /timemore-dot-prometheus-exporter resend`. Re-sending
identical data is harmless: Prometheus accepts a sample identical to one it already has.

## Prometheus requirements

- `--web.enable-remote-write-receiver`.
- `storage.tsdb.out_of_order_time_window` in `prometheus.yml` ([`30d` here](prometheus.yml)).
  Brews arrive late (the phone was offline, the upload retried, the sync runs every 15 minutes)
  and are older than what Prometheus already holds. **Brews older than the window are rejected**
  by Prometheus (`400 too old sample`): the exporter logs
  `WARN sync: brew <id> rejected: ...`, stores the reason in the brew's `sync_error` column and
  does not retry it. The brew stays in SQLite. To load old brews (e.g. after restoring a backup
  into an empty Prometheus) raise the window, then run `resend`.
- A replaced brew (same `id`, new body) is sent again, but Prometheus cannot change a value it
  already stores: changed values at the same timestamps are rejected as duplicates and reported
  the same way. SQLite keeps the corrected brew. See the [timestamps decision](https://github.com/MateuxLucax/timemore-dot-prometheus-exporter/issues/12).
- Retention defaults to 15 days; the example sets `--storage.tsdb.retention.time=10y`.

## Run with Docker Compose

```sh
cp docker-compose.example.yml docker-compose.yml
printf 'AUTH_TOKEN=%s\nGRAFANA_ADMIN_PASSWORD=%s\n' "$(openssl rand -hex 32)" "$(openssl rand -hex 16)" > .env
docker compose up -d
```

- The exporter listens on port 8080. In the app, set the server to e.g.
  `https://coffee.example.com` (behind a reverse proxy) and the same token.
- Grafana is on `http://localhost:3000` (user `admin`, password from `.env`) with the
  Prometheus datasource and the **Caparaó brews** dashboard provisioned: brews per day, yield /
  dose, ratio and duration per brew, and the weight and flow curves (pick a brew in the `id`
  variable or zoom into it). The `mode` variable offers espresso, filter and French press.
- Back up the `exporter-data` volume: it is the source of truth. Prometheus can be rebuilt from
  it with `resend` (within the out-of-order window).

To build the image from source, replace `image:` with `build: .` in the compose file, or
`docker build -t timemore-dot-prometheus-exporter .` The image is distroless, static (no cgo)
and runs as a non-root user.

## Security

- Use HTTPS (a reverse proxy) whenever the phone does not reach the server over a trusted LAN,
  in particular from the internet. The server speaks plain HTTP.
- The token is compared in constant time (SHA-256 of both sides, `subtle.ConstantTimeCompare`)
  and never logged. Access log lines show method, path, status and the brew id only.
- Prometheus and Grafana have no published ports beyond localhost in the example; Prometheus
  has no authentication.
- Bodies are limited to 4 MiB and validated strictly before anything is stored.

## Development

```sh
go vet ./... && go test ./...
AUTH_TOKEN=dev-token REMOTE_WRITE_URL=http://localhost:9090/api/v1/write DB_PATH=./dev.db go run .
```

`main.go` (configuration, subcommands, wiring) stays at the root; the logic is in `internal/`:
`brew` (validation), `store` (SQLite), `api` (HTTP), `syncer` (sync loop), `remotewrite`
(Prometheus client).

## Architecture

One binary, one process: `main.go` reads the environment, opens the database and starts the
HTTP server and the sync loop.

```mermaid
flowchart TB
    app["Caparaó app"] -->|"POST/GET /api/v1/brews, GET /healthz"| api
    subgraph exporter["timemore-dot-prometheus-exporter"]
        api["internal/api<br/>auth (constant time), 4 MiB limit, routing, access log"]
        brew["internal/brew<br/>contract validation (validates testdata/contract)"]
        store["internal/store<br/>SQLite: migrations, upsert, pending, checkpoint"]
        syncer["internal/syncer<br/>loop every SYNC_INTERVAL, brew -> series"]
        rw["internal/remotewrite<br/>protobuf (hand-encoded) + snappy, HTTP"]
        api --> brew
        api --> store
        syncer --> store
        syncer --> rw
    end
    store --- db[("caparao.db")]
    rw -->|"remote write 1.0"| prom[("Prometheus")]
    graf["Grafana"] --> prom
```

The `brews` table holds one row per brew id with the contract fields plus `received_at_ms`,
`version` (bumped by every upsert), `sent_version`, `sent_at_ms` and `sync_error`; `samples`
holds the curve keyed by `(brew_id, seq)` in upload order. The connection uses WAL so the
`sync` and `resend` subcommands can run next to the server.

### Sync and checkpoint

Every `SYNC_INTERVAL` (and at start) the sync loop reads brews with `version > sent_version`
in one transaction, sends them with remote write (summaries at the brew's end, curve at
`started_at_ms + t_ms`), then sets `sent_version` to the version it read. Only HTTP 400 is
rejected for good: the brew is retried series by series so good series still land, then the
reason is logged and stored in `sync_error`. Network errors, 5xx, 404 and 429 fail the run
and are retried next time. A brew replaced while being sent keeps its newer version pending.
`resend` sets every `sent_version` to 0 and syncs.

### Contract files

`testdata/contract` is the canonical upload contract for this repository. A contract change
starts here; the Caparaó app then bumps its pinned commit and updates itself to match.

### Integration test

`integration_test.go` (build tag `integration`) runs the compiled binary against a real
Prometheus. With Docker:

```sh
scripts/integration.sh
```

or against a Prometheus you started with `--web.enable-remote-write-receiver` and this repo's
`prometheus.yml`:

```sh
PROMETHEUS_URL=http://localhost:9090 go test -tags integration -count=1 -run TestIntegration .
```

Without `PROMETHEUS_URL` it is skipped. It checks summaries at the brew's end time and the curve
at each sample's time, that `resend` duplicates nothing, and that a brew older than the window is
reported as rejected.

## Decisions

GitHub Issues are the decision log (label `decision`); the former ADRs are closed issues
#11-#15. `AGENTS.md` holds the durable contracts for this repository.

## License

[MIT](LICENSE)
