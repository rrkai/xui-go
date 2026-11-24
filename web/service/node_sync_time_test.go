package service

import "testing"

func TestResolveSyncTimezone(t *testing.T) {
	shanghai := int64(8 * 3600)
	utc := int64(0)

	location, offset, err := resolveSyncTimezone("Asia/Shanghai", &shanghai)
	if err != nil {
		t.Fatalf("valid timezone rejected: %v", err)
	}
	if location.String() != "Asia/Shanghai" || offset != shanghai {
		t.Fatalf("unexpected location %s offset %d", location, offset)
	}

	if _, offset, err = resolveSyncTimezone("UTC", &utc); err != nil || offset != 0 {
		t.Fatalf("UTC with zero offset should pass, got offset %d err %v", offset, err)
	}

	if _, _, err = resolveSyncTimezone("UTC", nil); err != nil {
		t.Fatalf("missing offset should skip the check: %v", err)
	}

	if _, _, err = resolveSyncTimezone("UTC", &shanghai); err == nil {
		t.Fatal("offset mismatch should be rejected")
	}

	if _, _, err = resolveSyncTimezone("Mars/Olympus", nil); err == nil {
		t.Fatal("invalid timezone should be rejected")
	}

	if _, _, err = resolveSyncTimezone("   ", nil); err == nil {
		t.Fatal("blank timezone should be rejected")
	}
}
