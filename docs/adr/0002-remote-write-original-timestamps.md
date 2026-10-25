# 2. Remote write with the original timestamps and a 30-day out-of-order window

Status: Accepted

## Context

A brew's curve happened at specific times, often long before it reaches the server (offline
phone, upload backoff, 15-minute sync). Scraping would stamp it with the scrape time. By default
Prometheus also rejects samples older than the newest one it already holds for a series.

## Decision

- Write with remote write 1.0 (protobuf `WriteRequest`, snappy) to
  `/api/v1/write`; Prometheus runs with `--web.enable-remote-write-receiver`.
- Summaries are stamped at the brew's end (`ended_at_ms`); the curve at
  `started_at_ms + t_ms`. Points of one series are sorted and a repeated `t_ms` keeps the last
  value, so one request never carries two values for a timestamp.
- `prometheus.yml` sets `storage.tsdb.out_of_order_time_window: 30d`.
- Only HTTP 400 means "rejected for good" (too old, duplicate with another value, out of
  bounds): the brew is retried series by series so the good series still land, then the reason
  is logged (`WARN sync: brew <id> rejected: ...`) and stored in `sync_error`, and the
  checkpoint advances. Anything else (connection refused, 5xx, 404 when the receiver is off,
  429) fails the run and is retried next time.
- Brews that end more than 1 hour in the future are not sent (recorded as rejected).

## Consequences

- Brews up to 30 days old are accepted. Older ones stay in SQLite only; to load them, raise the
  window and run `resend`. The integration test checks that a 60-day-old brew is reported.
- Re-sending identical data is accepted silently (checked against a real Prometheus by the
  integration test), so `resend` is safe.
- Prometheus cannot overwrite a value: if a brew is replaced with different values at the same
  timestamps, those series are rejected as duplicates and reported; SQLite holds the correct
  brew. New timestamps (e.g. a longer curve) are added.
- A larger window costs Prometheus memory only for out-of-order data actually received.
