# Architecture Decision Records

| # | Decision |
|---|----------|
| [0001](0001-sqlite-source-of-truth-and-sync-loop.md) | SQLite is the source of truth; the sync loop is a goroutine in the same process |
| [0002](0002-remote-write-original-timestamps.md) | Remote write with the original timestamps and a 30-day out-of-order window |
| [0003](0003-dependencies.md) | Dependencies: modernc.org/sqlite and golang/snappy only |
| [0004](0004-labels-and-cardinality.md) | Labels `mode` and `id` |
| [0005](0005-contract-copied-into-testdata.md) | The upload contract is copied into testdata |

Decisions about the app and the contract live in the Caparaó repository (`docs/adr`), notably
0007 (idempotent upload) and 0014 (stub in the app repo, exporter separate).
