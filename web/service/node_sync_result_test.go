package service

import "testing"

func TestSyncAppliedText(t *testing.T) {
	if got := syncAppliedText(false, SyncKindCount{Added: 1}, SyncKindCount{}, SyncKindCount{}, true); got != "无变更" {
		t.Fatalf("unchanged side should report no change, got %q", got)
	}
	got := syncAppliedText(true, SyncKindCount{Added: 1}, SyncKindCount{Updated: 2}, SyncKindCount{Deleted: 3}, false)
	want := "有变更（入站 +1 ~0 -0；出站 +0 ~2 -0；路由 +0 ~0 -3）；热加载：待重启"
	if got != want {
		t.Fatalf("unexpected text:\n got %q\nwant %q", got, want)
	}
}
