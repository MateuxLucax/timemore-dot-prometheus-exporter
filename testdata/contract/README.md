# Upload contract

The wire format of `POST /api/v1/brews` (and of `GET /api/v1/brews/{id}`, which returns the same body), shared by the Android client and every server (the stub in `../stub`, later
`timemore-dot-prometheus-exporter`). The same files are checked on both sides, so they cannot drift apart.

| Path | What it is |
|------|------------|
| `brew.schema.json` | JSON Schema (draft 2020-12) of the request body |
| `valid/*.json` | Bodies every server must accept (`201` the first time, `200` when resent) |
| `invalid/*.json` | Bodies every server must reject with `400`; each breaks one rule and the file name says which |

Tests iterate over both directories, so adding a file adds a test case. Use example values only, never real data.

## Rules and decisions

- All fields are required except the brew notes below, and unknown properties (also inside samples) are rejected. A
  new field means a change to the contract here first.
- Brew notes ([ADR 0021](../docs/adr/0021-brew-notes.md)), optional and left out when unset, never null: `recipe_name`
  (≤ 80 characters), `coffee` (≤ 80), `grind` (≤ 40), `water_temp_c` (integer 1–100), `rating` (integer 1–5), `notes`
  (≤ 1000, kept out of Prometheus). An edit on the phone re-sends the brew, and the upsert replaces them.
- `id`: lowercase UUID string (8-4-4-4-12 hex), generated on the phone. The server upserts on it
  ([ADR 0007](../docs/adr/0007-idempotent-upload.md)); the same `id` re-sent with a different body replaces the stored brew.
- `mode`: `"filter"` or `"espresso"`.
- `started_at_ms`, `ended_at_ms`: whole Unix epoch milliseconds, and `ended_at_ms > started_at_ms` strictly. This is
  the one rule JSON Schema cannot express; servers check it in code and `invalid/` has files for it.
- `dose_g`: number, `> 0` and at most 1000. Required, never null (a brew always has a dose).
- `yield_g`: number, `>= 0` and at most 10000. An interrupted brew may have 0.
- `time_to_first_drop_s`: number `>= 0`, or `null`. The key is always present; filter brews send `null`.
- `flow_avg_gps`, `flow_peak_gps`: numbers `>= 0`.
- `interrupted`: boolean, `true` when the brew ended early (cup lifted, connection lost, stopped by hand).
- `samples`: array, may be empty, at most 20000 items (about 33 minutes at 10 Hz). Each item has `t_ms` (whole,
  `>= 0`, relative to `started_at_ms`), `weight_g` and `flow_gps` (any number: drift and a lifted cup can go
  negative). Order is not enforced.
- Numbers are plain JSON numbers (no strings). `18` and `18.0` are the same value; integer fields must be whole.
- Body size limit: 4 MiB (`413` above it). A maximal body is under 2 MiB.

## Responses

| Status | Meaning | Client behavior |
|--------|---------|-----------------|
| `201` | New brew stored | mark synced |
| `200` | A brew with this `id` existed and was replaced | mark synced |
| `400` | Invalid body | do not retry |
| `401` | Missing or wrong Bearer token | do not retry, show an error |
| `405` | Wrong method | do not retry |
| `413` | Body over 4 MiB | do not retry |
| `5xx`, timeouts | Server trouble | retry with backoff |

Authentication is `Authorization: Bearer <token>`. Error bodies are always `{"error": "<message>"}`. Every other
status is treated like its class: other `4xx` (also `404`, `408`, `429`) are not retried, `3xx` is shown as an error and
never followed, so the token is not sent to another location.

## Reading a brew back

`GET /api/v1/brews/{id}` with the Bearer token returns one stored brew in the same format as the upload body above,
samples included in the order they were uploaded, so the response itself validates against `brew.schema.json`.
`GET /api/v1/brews` stays the lightweight list: summaries without samples, with `sample_count`.

| Status | Meaning |
|--------|---------|
| `200` | The brew, as last uploaded |
| `401` | Missing or wrong Bearer token |
| `404` | No brew with this `id` (also for an `id` that is not a lowercase UUID) |
| `405` | Any method other than `GET` |

## Connection test

The app's "Test connection" sends two requests that create nothing, so every server must answer them:

| Request | Expected |
|---------|----------|
| `GET /healthz` | `2xx`, no token needed |
| `GET /api/v1/brews` with the Bearer token | `2xx`; `401` for a wrong token |
