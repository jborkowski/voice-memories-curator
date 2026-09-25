package detect

import "testing"

func TestEncodeDecodeAppleRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		zpk  int64
		want int64
	}{
		{"zero", 0, SlotBase},
		{"one", 1, SlotBase + 1000},
		{"typical", 4321, SlotBase + 4_321_000},
		{"large", 999_999, SlotBase + 999_999_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := EncodeApple(tt.zpk)
			if id != tt.want {
				t.Fatalf("EncodeApple(%d) = %d, want %d", tt.zpk, id, tt.want)
			}
			if got := DecodeAppleZPK(id); got != tt.zpk {
				t.Fatalf("DecodeAppleZPK(%d) = %d, want %d", id, got, tt.zpk)
			}
			if IsDJIID(id) || IsLegacyID(id) {
				t.Fatalf("id %d misclassified", id)
			}
		})
	}
}

func TestEncodeDecodeDJIRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		s, n int64
		want int64
	}{
		{"first", 42, 1, SlotBase + 42_001},
		{"last", 42, 999, SlotBase + 42_999},
		{"slot zero", 0, 5, SlotBase + 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := EncodeDJI(tt.s, tt.n)
			if err != nil {
				t.Fatal(err)
			}
			if id != tt.want {
				t.Fatalf("EncodeDJI(%d,%d) = %d, want %d", tt.s, tt.n, id, tt.want)
			}
			if got := DecodeAppleZPK(id); got != tt.s {
				t.Fatalf("DecodeAppleZPK = %d, want %d", got, tt.s)
			}
			if got := DecodeDJIIndex(id); got != tt.n {
				t.Fatalf("DecodeDJIIndex = %d, want %d", got, tt.n)
			}
			if !IsDJIID(id) {
				t.Fatalf("IsDJIID(%d) = false", id)
			}
		})
	}
}

func TestEncodeDJIBounds(t *testing.T) {
	tests := []struct {
		name    string
		s, n    int64
		wantErr bool
	}{
		{"n zero", 10, 0, true},
		{"n negative", 10, -1, true},
		{"n min", 10, 1, false},
		{"n max", 10, 999, false},
		{"n overflow", 10, 1000, true},
		{"negative slot", -1, 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := EncodeDJI(tt.s, tt.n)
			if (err != nil) != tt.wantErr {
				t.Fatalf("EncodeDJI(%d,%d) err = %v, wantErr %v", tt.s, tt.n, err, tt.wantErr)
			}
		})
	}
}

func TestDecodeLegacy(t *testing.T) {
	for _, id := range []int64{0, 1, 4321, SlotBase - 1} {
		if !IsLegacyID(id) {
			t.Fatalf("IsLegacyID(%d) = false", id)
		}
		if got := DecodeAppleZPK(id); got != id {
			t.Fatalf("DecodeAppleZPK(%d) = %d, want raw", id, got)
		}
		if IsDJIID(id) {
			t.Fatalf("IsDJIID(%d) = true for legacy", id)
		}
	}
}

func TestOrdering(t *testing.T) {
	const s = 4321
	legacy := int64(999_999_999) // any raw Z_PK already published
	apple := EncodeApple(s)
	dji1, _ := EncodeDJI(s, 1)
	djiMax, _ := EncodeDJI(s, MaxDJIPerSlot)
	nextApple := EncodeApple(s + 1)

	seq := []struct {
		name string
		id   int64
	}{
		{"legacy", legacy},
		{"new apple", apple},
		{"dji n=1", dji1},
		{"dji n=max", djiMax},
		{"next apple", nextApple},
	}
	for i := 1; i < len(seq); i++ {
		if seq[i-1].id >= seq[i].id {
			t.Fatalf("%s (%d) >= %s (%d)", seq[i-1].name, seq[i-1].id, seq[i].name, seq[i].id)
		}
	}
}

func TestNextDJIIndex(t *testing.T) {
	tests := []struct {
		name        string
		highestUsed int64
		want        int64
		wantErr     bool
	}{
		{"empty slot", 0, 1, false},
		{"after first", 1, 2, false},
		{"last free", 998, 999, false},
		{"full", 999, 0, true},
		{"negative", -1, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NextDJIIndex(tt.highestUsed)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("NextDJIIndex(%d) = %d, want %d", tt.highestUsed, got, tt.want)
			}
		})
	}
}

func TestNextDJIID(t *testing.T) {
	mustDJI := func(s, n int64) int64 {
		id, err := EncodeDJI(s, n)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	tests := []struct {
		name    string
		ids     []int64
		s       int64
		want    int64
		wantErr bool
	}{
		{"no ids", nil, 7, SlotBase + 7001, false},
		{"only apple and legacy", []int64{7, EncodeApple(7)}, 7, SlotBase + 7001, false},
		{"existing in slot", []int64{mustDJI(7, 1), mustDJI(7, 3)}, 7, SlotBase + 7004, false},
		{"other slots ignored", []int64{mustDJI(6, 9), mustDJI(8, 9)}, 7, SlotBase + 7001, false},
		{"slot full", []int64{mustDJI(7, 999)}, 7, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NextDJIID(tt.ids, tt.s)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("NextDJIID = %d, want %d", got, tt.want)
			}
		})
	}
}
