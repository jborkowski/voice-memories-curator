/-!
# VMC lock model (abstract, machine-checked)

This formalizes the *intended* concurrency contract of Voice Memories Curator
as implemented in Go (`cmd/root.go`, `cmd/status.go`, `internal/detect/detect.go`).

We do **not** extract Go into Lean. We prove theorems about a faithful model of
the lock protocol. If the Go code matches the model, the theorems apply.
-/

namespace VmcLock

/-- CLI commands that may run under the shared process lock story. -/
inductive Cmd where
  | daemon
  | detect
  | process
  | upload
  | status
  | logs
  | help
  deriving DecidableEq, Repr

/-- Backing store for DuckDB. -/
inductive DuckStore where
  | sharedVmcDb   -- `~/.local/share/vmc/vmc.db` (exclusive writer lock)
  | inMemory      -- `sql.Open("duckdb", "")` (no shared file lock)
  | none
  deriving DecidableEq, Repr

/-- Whether `PersistentPreRun` opens the shared `vmc.db` for this command.
Corresponds to `cmd/root.go` skipping `initDB` for `status` and `logs`. -/
def opensSharedVmcDb : Cmd → Bool
  | .daemon | .detect | .process | .upload => true
  | .status | .logs | .help => false

/-- DuckDB store actually used by the command body. -/
def duckStore : Cmd → DuckStore
  | .daemon | .detect | .process | .upload => .sharedVmcDb
  | .status => .inMemory
  | .logs | .help => .none

/-- Detect-phase events in temporal order (simplified). -/
inductive DetectEvent where
  | hfDedup
  | localDedup
  | appleSnapshotAttach
  | appleDetach
  | writeShards
  deriving DecidableEq, Repr

/-- Canonical detect schedule after v0.2.0. -/
def detectSchedule : List DetectEvent :=
  [.hfDedup, .localDedup, .appleSnapshotAttach, .appleDetach, .writeShards]

/-- Index of an event in the schedule (none if absent). -/
def scheduleIndex (e : DetectEvent) : Option Nat :=
  let rec go (i : Nat) : List DetectEvent → Option Nat
    | [] => none
    | x :: xs => if x == e then some i else go (i + 1) xs
  go 0 detectSchedule

/-- Apple attach is live only between attach and detach (half-open). -/
def appleAttachedAt (i : Nat) : Bool :=
  match scheduleIndex .appleSnapshotAttach, scheduleIndex .appleDetach with
  | some a, some d => decide (a ≤ i ∧ i < d)
  | _, _ => false

/-- Two commands conflict on the shared DuckDB file iff both open it. -/
def conflictsSharedDb (c₁ c₂ : Cmd) : Bool :=
  opensSharedVmcDb c₁ && opensSharedVmcDb c₂

end VmcLock
