import VmcLock.Model

namespace VmcLock

/-! ## Detect ordering: no HF while Apple ATTACH is live -/

/-- Concrete indices in `detectSchedule` (must stay aligned with `Model.lean`). -/
def idxHf : Nat := 0
def idxLocal : Nat := 1
def idxAttach : Nat := 2
def idxDetach : Nat := 3
def idxWrite : Nat := 4

theorem detect_schedule_length : detectSchedule.length = 5 := by
  native_decide

theorem schedule_idx_hf : scheduleIndex .hfDedup = some idxHf := by
  native_decide

theorem schedule_idx_attach : scheduleIndex .appleSnapshotAttach = some idxAttach := by
  native_decide

theorem schedule_idx_detach : scheduleIndex .appleDetach = some idxDetach := by
  native_decide

theorem schedule_idx_write : scheduleIndex .writeShards = some idxWrite := by
  native_decide

theorem hf_before_attach : idxHf < idxAttach := by
  native_decide

theorem attach_before_detach : idxAttach < idxDetach := by
  native_decide

theorem detach_before_write : idxDetach < idxWrite := by
  native_decide

theorem hf_not_under_apple_attach : appleAttachedAt idxHf = false := by
  native_decide

theorem local_dedup_not_under_apple_attach : appleAttachedAt idxLocal = false := by
  native_decide

theorem write_shards_not_under_apple_attach : appleAttachedAt idxWrite = false := by
  native_decide

theorem apple_attached_only_at_attach_index :
    (appleAttachedAt idxAttach = true) ∧
    (appleAttachedAt idxHf = false) ∧
    (appleAttachedAt idxLocal = false) ∧
    (appleAttachedAt idxDetach = false) ∧
    (appleAttachedAt idxWrite = false) := by
  native_decide

/--
Main safety claim for Voice Memos UX:

HF network dedup is scheduled strictly before the Apple attach window opens,
and shard writes strictly after detach. Therefore HF never runs under ATTACH.
-/
theorem no_hf_during_apple_attach_window :
    idxHf < idxAttach ∧ appleAttachedAt idxHf = false ∧
    idxWrite > idxDetach ∧ appleAttachedAt idxWrite = false := by
  native_decide

/-- Every HF-capable step index in the finite schedule is outside the attach window. -/
theorem all_non_attach_indices_safe :
    appleAttachedAt 0 = false ∧
    appleAttachedAt 1 = false ∧
    appleAttachedAt 3 = false ∧
    appleAttachedAt 4 = false := by
  native_decide

end VmcLock
