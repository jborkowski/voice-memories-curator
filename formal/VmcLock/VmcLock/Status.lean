import VmcLock.Model

namespace VmcLock

/-! ## Status vs daemon: no shared `vmc.db` contention -/

theorem status_does_not_open_shared_db :
    opensSharedVmcDb .status = false := by
  native_decide

theorem status_uses_in_memory_duck :
    duckStore .status = .inMemory := by
  native_decide

theorem logs_does_not_open_shared_db :
    opensSharedVmcDb .logs = false := by
  native_decide

/-- The bug report: daemon holds `vmc.db` while status runs.
With the v0.2.1 model, they do not both open the shared file. -/
theorem status_and_daemon_do_not_conflict_on_shared_db :
    conflictsSharedDb .status .daemon = false := by
  native_decide

theorem status_and_detect_do_not_conflict_on_shared_db :
    conflictsSharedDb .status .detect = false := by
  native_decide

/-- Conversely: two writers still conflict (model of DuckDB exclusivity). -/
theorem daemon_and_detect_conflict_on_shared_db :
    conflictsSharedDb .daemon .detect = true := by
  native_decide

/-- If a command does not open the shared DB, it cannot conflict with daemon. -/
theorem no_shared_open_no_daemon_conflict (c : Cmd)
    (h : opensSharedVmcDb c = false) :
    conflictsSharedDb c .daemon = false := by
  simp [conflictsSharedDb, h]

end VmcLock
