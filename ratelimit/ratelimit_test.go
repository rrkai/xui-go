package ratelimit

import "testing"

func TestNormalizeRules(t *testing.T) {
	specs := normalizeRules([]Rule{
		{Port: 2000, UpKbps: 1000, DownKbps: 2000},
		{Port: 1000},
		{Port: 0, UpKbps: 100},
		{Port: 70000, DownKbps: 100},
		{Port: 3000, UpKbps: -5, DownKbps: 500},
		{Port: 2000, UpKbps: 500},
	})
	if len(specs) != 2 {
		t.Fatalf("expected 2 specs, got %d: %+v", len(specs), specs)
	}
	if specs[0].port != 2000 || specs[1].port != 3000 {
		t.Fatalf("specs are not sorted: %+v", specs)
	}
	// Duplicate port keeps the tighter limit in each direction.
	if specs[0].upBytes != 500*125 || specs[0].downBytes != 2000*125 {
		t.Fatalf("duplicate port not merged correctly: %+v", specs[0])
	}
	if specs[1].upBytes != 0 || specs[1].downBytes != 500*125 {
		t.Fatalf("negative rate not clamped correctly: %+v", specs[1])
	}
}

func TestKbpsToBytes(t *testing.T) {
	cases := map[int]uint64{-1: 0, 0: 0, 1: 125, 8000: 1000000, 100000: 12500000}
	for kbps, expected := range cases {
		if got := kbpsToBytes(kbps); got != expected {
			t.Errorf("kbpsToBytes(%d) = %d, want %d", kbps, got, expected)
		}
	}
}
