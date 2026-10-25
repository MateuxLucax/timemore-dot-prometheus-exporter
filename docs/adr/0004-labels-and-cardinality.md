# 4. Labels `mode` and `id`

Status: Accepted

## Context

Every metric needs `mode` (`espresso` / `filter`). The question is whether to also identify the
brew. Without it, all brews of one mode share one series per metric: curves of different brews
join into one line, a brew cannot be picked in Grafana, and two brews ending in the same
millisecond would collide.

## Decision

Every series has `mode` and `id` (the brew's UUID). No other labels (`interrupted`, dose, beans
would add series without adding queries we need).

## Consequences

- Each brew creates 7 to 8 short-lived series (one point for each summary, the curve for the
  other two). A home user brewing 5 times a day creates about 15,000 series a year, each active
  for minutes: trivial for Prometheus, whose cost is active series in the head.
- This would not suit a café with thousands of brews a day; then drop `id` from the summaries.
- Panels aggregate across brews with `max by (mode) (last_over_time(...[$__interval]))` or
  `count_over_time`; the curve panels filter by `id`.
- A short label (e.g. the first 8 hex characters) was rejected: it saves nothing measurable and
  can collide.
