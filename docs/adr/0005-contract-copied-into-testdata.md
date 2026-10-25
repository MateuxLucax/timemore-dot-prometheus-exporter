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

## Consequences

- `go test ./...` needs nothing outside this repository.
- The copy can fall behind: after a contract change in the app repository, run the script and
  commit the result together with any code change.
