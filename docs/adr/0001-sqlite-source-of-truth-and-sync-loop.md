# 1. SQLite is the source of truth; the sync loop is a goroutine in the same process

Status: Accepted

## Context

The app uploads a brew until it gets a 2xx and then forgets about the upload. Prometheus can be
down or restarting, and a remote write can fail halfway. Forwarding synchronously (as the
health-connect exporter does) would make Prometheus' availability the app's problem, and the
app would need its own retry for something the server can do better.

## Decision

- The API writes the brew and all its samples to SQLite in one transaction (upsert by `id`,
  Caparaó ADR 0007) and answers 2xx only after the commit.
- A sync loop sends what Prometheus has not seen yet, every `SYNC_INTERVAL` (default 15 minutes)
  and once at start. The checkpoint is per brew: `version` is bumped by every upsert and
  `sent_version` records the version last handled, so a replaced brew is sent again. The
  checkpoint advances only after Prometheus answered for that brew.
- The loop is a goroutine in the server process, not a separate container or cron job. The
  roadmap calls it a "separate process" in the sense of separate from the request path; a
  goroutine gives that separation (the API never waits for Prometheus) with one binary, one
  container, one database owner and no scheduler. `sync` and `resend` subcommands run a one-off
  run from the same binary (e.g. `docker compose exec exporter ... resend`); SQLite in WAL mode
  with a busy timeout lets them run next to the server.

## Consequences

- Prometheus down: uploads keep working; the next run sends everything, once.
- The SQLite file must be backed up; Prometheus can be rebuilt from it (within the
  out-of-order window, ADR 0002).
- Data reaches Prometheus up to `SYNC_INTERVAL` late. Lower it if that matters.
- One process means the sync shares the server's lifetime: a crash stops both, a restart runs a
  sync at once.
