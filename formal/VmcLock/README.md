# VmcLock — Lean 4 proofs for VMC concurrency

Machine-checked model of the lock protocol fixed in **v0.2.0 / v0.2.1**.

## Claims proved

1. `status` / `logs` do **not** open shared `vmc.db`
2. `status` uses in-memory DuckDB
3. `status` + `daemon` do **not** conflict on the shared DB file
4. Detect runs HF dedup **before** Apple snapshot attach
5. Shard writes run **after** Apple detach
6. No schedule step runs HF while Apple is attached

## Build / check

```bash
export PATH="$HOME/.elan/bin:$PATH"
cd formal/VmcLock
lake build
```

Requires [elan](https://github.com/leanprover/elan) (Lean 4.14).

## Honesty bound

These theorems are about the **Lean model** in `VmcLock/Model.lean`, which is
written to mirror `cmd/root.go` and `internal/detect/detect.go`. They are not
an automated extraction of the Go binary. If Go drifts from the model, re-check
the `opensSharedVmcDb` / `detectSchedule` definitions.
