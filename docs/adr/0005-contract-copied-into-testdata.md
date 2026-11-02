# 5. The upload contract is copied into testdata

Status: Accepted

## Context

The contract (JSON Schema, valid and invalid bodies) lives in the Caparaó repository and is
tested against the app and the stub there. This repository must pass the same files, but it
is separate (Caparaó ADR 0014) and should build and test on its own.

## Decision

`testdata/contract` is a verbatim copy. `scripts/sync-contract.sh [path]` re-copies it. The
validation code in `internal/brew` is the stub's `validate.go` with the same rules and messages,
and `internal/api` keeps the stub's test suite (statuses, error bodies, auth, sizes, methods),
running against a real SQLite file.

One local addition is carried on top of the copy: `valid/french-press.json`, the example body
the Caparaó stub tests inline until the fixture lands there with the app mode (#125). The copy
also includes the curve-origin wording clarification from Caparaó PR #141 (commit `e0dd847`,
issue #140): `t_ms = 0` is the first drop for espresso and the first water for filter and French
press. Neither changes the schema or runtime semantics.

## Consequences

- `go test ./...` needs nothing outside this repository.
- The copy can fall behind: after a contract change in the app repository, run the script and
  commit the result together with any code change. The script replaces the whole directory, so
  re-add the local French press fixture until Caparaó #125 provides it upstream.
