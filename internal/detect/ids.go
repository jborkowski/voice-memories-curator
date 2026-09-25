package detect

import "fmt"

// Slot-based recording_id scheme.
//
// Downstream consumers watermark on max(recording_id), so every newly
// published row (Apple or DJI) must sort above anything already published.
// New IDs therefore live above SlotBase:
//
//	Apple: SlotBase + zpk*SlotWidth
//	DJI:   SlotBase + S*SlotWidth + n   (S = highest known Apple Z_PK, n in 1..MaxDJIPerSlot)
//
// Legacy rows published before this scheme keep their raw Apple Z_PK as
// recording_id (always < SlotBase) and are never rewritten; DecodeAppleZPK
// accepts both forms so Apple dedup works across old and new rows.
const (
	SlotBase      int64 = 1_000_000_000_000
	SlotWidth     int64 = 1000
	MaxDJIPerSlot int64 = SlotWidth - 1
)

// EncodeApple returns the slot ID for an Apple Voice Memos Z_PK.
func EncodeApple(zpk int64) int64 {
	return SlotBase + zpk*SlotWidth
}

// EncodeDJI returns the ID for the n-th DJI recording in slot s.
func EncodeDJI(s, n int64) (int64, error) {
	if s < 0 {
		return 0, fmt.Errorf("dji slot %d: must be >= 0", s)
	}
	if n < 1 || n > MaxDJIPerSlot {
		return 0, fmt.Errorf("dji index %d in slot %d: must be in 1..%d", n, s, MaxDJIPerSlot)
	}
	return SlotBase + s*SlotWidth + n, nil
}

// IsLegacyID reports whether id is a raw Z_PK published before slot IDs.
func IsLegacyID(id int64) bool {
	return id < SlotBase
}

// DecodeAppleZPK returns the Apple Z_PK (or, for DJI IDs, the slot) that id belongs to.
func DecodeAppleZPK(id int64) int64 {
	if IsLegacyID(id) {
		return id
	}
	return (id - SlotBase) / SlotWidth
}

// DecodeDJIIndex returns the in-slot index n of id; 0 means an Apple or legacy ID.
func DecodeDJIIndex(id int64) int64 {
	if IsLegacyID(id) {
		return 0
	}
	return (id - SlotBase) % SlotWidth
}

// IsDJIID reports whether id was produced by EncodeDJI.
func IsDJIID(id int64) bool {
	return DecodeDJIIndex(id) != 0
}

// NextDJIIndex returns the next free n in a slot whose highest used n is
// highestUsed (0 if the slot is empty).
func NextDJIIndex(highestUsed int64) (int64, error) {
	if highestUsed < 0 {
		return 0, fmt.Errorf("highest used dji index %d: must be >= 0", highestUsed)
	}
	n := highestUsed + 1
	if n > MaxDJIPerSlot {
		return 0, fmt.Errorf("dji slot full: %d recordings already allocated", MaxDJIPerSlot)
	}
	return n, nil
}

// HighestDJIIndex returns the highest n used in slot s among ids (0 if none).
func HighestDJIIndex(ids []int64, s int64) int64 {
	var highest int64
	for _, id := range ids {
		if IsLegacyID(id) || DecodeAppleZPK(id) != s {
			continue
		}
		if n := DecodeDJIIndex(id); n > highest {
			highest = n
		}
	}
	return highest
}

// NextDJIID allocates the next DJI ID in slot s given already-used ids.
func NextDJIID(ids []int64, s int64) (int64, error) {
	n, err := NextDJIIndex(HighestDJIIndex(ids, s))
	if err != nil {
		return 0, fmt.Errorf("slot %d: %w", s, err)
	}
	return EncodeDJI(s, n)
}
