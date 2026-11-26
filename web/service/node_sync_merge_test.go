package service

import (
	"testing"

	"github.com/alireza0/x-ui/database/model"
)

func testInbound(tag string, port int, remark string) *model.Inbound {
	return &model.Inbound{
		Tag:            tag,
		Port:           port,
		Listen:         "",
		Protocol:       "vmess",
		Settings:       "{}",
		StreamSettings: "{}",
		Remark:         remark,
		Enable:         true,
	}
}

func testState(ts int64) SyncItemState {
	return SyncItemState{ChangedAt: ts}
}

func testTombstone(ts int64) SyncItemState {
	return SyncItemState{ChangedAt: ts, Deleted: true}
}

func inboundTags(data *SyncDataSet) map[string]bool {
	tags := make(map[string]bool, len(data.Inbounds))
	for _, inbound := range data.Inbounds {
		tags[inbound.Tag] = true
	}
	return tags
}

func TestMergeUnion(t *testing.T) {
	local := &SyncDataSet{
		Inbounds: []*model.Inbound{testInbound("a", 1001, "a")},
		State:    map[string]SyncItemState{SyncStateKey(SyncKindInbound, "a"): testState(100)},
	}
	remote := &SyncDataSet{
		Inbounds: []*model.Inbound{testInbound("b", 1002, "b")},
		State:    map[string]SyncItemState{SyncStateKey(SyncKindInbound, "b"): testState(200)},
	}
	merged := mergeSyncData(&SyncDataSet{}, local, remote, "panel-a", "panel-b")
	tags := inboundTags(merged.Data)
	if !tags["a"] || !tags["b"] || len(tags) != 2 {
		t.Fatalf("expected union of a and b, got %v", tags)
	}
}

func TestMergeLaterChangeWins(t *testing.T) {
	base := &SyncDataSet{
		Inbounds: []*model.Inbound{testInbound("a", 1001, "v1")},
		State:    map[string]SyncItemState{SyncStateKey(SyncKindInbound, "a"): testState(100)},
	}
	local := &SyncDataSet{
		Inbounds: []*model.Inbound{testInbound("a", 1001, "local-v2")},
		State:    map[string]SyncItemState{SyncStateKey(SyncKindInbound, "a"): testState(300)},
	}
	remote := &SyncDataSet{
		Inbounds: []*model.Inbound{testInbound("a", 1001, "remote-v2")},
		State:    map[string]SyncItemState{SyncStateKey(SyncKindInbound, "a"): testState(200)},
	}
	merged := mergeSyncData(base, local, remote, "panel-a", "panel-b")
	if len(merged.Data.Inbounds) != 1 || merged.Data.Inbounds[0].Remark != "local-v2" {
		t.Fatalf("expected later local change to win, got %+v", merged.Data.Inbounds)
	}
	// Same result from the other side.
	other := mergeSyncData(base, remote, local, "panel-b", "panel-a")
	if len(other.Data.Inbounds) != 1 || other.Data.Inbounds[0].Remark != "local-v2" {
		t.Fatalf("expected symmetric result, got %+v", other.Data.Inbounds)
	}
}

func TestMergeDeletePropagates(t *testing.T) {
	base := &SyncDataSet{
		Inbounds: []*model.Inbound{testInbound("a", 1001, "v1")},
		State:    map[string]SyncItemState{SyncStateKey(SyncKindInbound, "a"): testState(100)},
	}
	local := &SyncDataSet{
		State: map[string]SyncItemState{SyncStateKey(SyncKindInbound, "a"): testTombstone(200)},
	}
	remote := &SyncDataSet{
		Inbounds: []*model.Inbound{testInbound("a", 1001, "v1")},
		State:    map[string]SyncItemState{SyncStateKey(SyncKindInbound, "a"): testState(100)},
	}
	merged := mergeSyncData(base, local, remote, "panel-a", "panel-b")
	if len(merged.Data.Inbounds) != 0 {
		t.Fatalf("expected delete to propagate, got %+v", merged.Data.Inbounds)
	}
	state := merged.Data.State[SyncStateKey(SyncKindInbound, "a")]
	if !state.Deleted {
		t.Fatalf("expected tombstone state, got %+v", state)
	}
}

func TestMergeDeleteVersusEdit(t *testing.T) {
	base := &SyncDataSet{
		Inbounds: []*model.Inbound{testInbound("a", 1001, "v1")},
		State:    map[string]SyncItemState{SyncStateKey(SyncKindInbound, "a"): testState(100)},
	}
	localDeleted := &SyncDataSet{
		State: map[string]SyncItemState{SyncStateKey(SyncKindInbound, "a"): testTombstone(500)},
	}
	remoteEdited := &SyncDataSet{
		Inbounds: []*model.Inbound{testInbound("a", 1001, "v2")},
		State:    map[string]SyncItemState{SyncStateKey(SyncKindInbound, "a"): testState(300)},
	}
	merged := mergeSyncData(base, localDeleted, remoteEdited, "panel-a", "panel-b")
	if len(merged.Data.Inbounds) != 0 {
		t.Fatalf("later delete should win, got %+v", merged.Data.Inbounds)
	}

	mergedOther := mergeSyncData(base, remoteEdited, localDeleted, "panel-b", "panel-a")
	if len(mergedOther.Data.Inbounds) != 0 {
		t.Fatalf("later delete should win symmetrically, got %+v", mergedOther.Data.Inbounds)
	}
}

func TestMergeTieBreakByPanelId(t *testing.T) {
	base := &SyncDataSet{
		Inbounds: []*model.Inbound{testInbound("a", 1001, "v1")},
		State:    map[string]SyncItemState{SyncStateKey(SyncKindInbound, "a"): testState(100)},
	}
	local := &SyncDataSet{
		Inbounds: []*model.Inbound{testInbound("a", 1001, "local")},
		State:    map[string]SyncItemState{SyncStateKey(SyncKindInbound, "a"): testState(200)},
	}
	remote := &SyncDataSet{
		Inbounds: []*model.Inbound{testInbound("a", 1001, "remote")},
		State:    map[string]SyncItemState{SyncStateKey(SyncKindInbound, "a"): testState(200)},
	}
	left := mergeSyncData(base, local, remote, "panel-a", "panel-b")
	right := mergeSyncData(base, remote, local, "panel-b", "panel-a")
	if left.Data.Inbounds[0].Remark != right.Data.Inbounds[0].Remark {
		t.Fatalf("tie break must be symmetric: %s vs %s",
			left.Data.Inbounds[0].Remark, right.Data.Inbounds[0].Remark)
	}
}

func TestMergePortConflictKeepsNewer(t *testing.T) {
	local := &SyncDataSet{
		Inbounds: []*model.Inbound{testInbound("old", 8443, "old")},
		State:    map[string]SyncItemState{SyncStateKey(SyncKindInbound, "old"): testState(100)},
	}
	remote := &SyncDataSet{
		Inbounds: []*model.Inbound{testInbound("new", 8443, "new")},
		State:    map[string]SyncItemState{SyncStateKey(SyncKindInbound, "new"): testState(200)},
	}
	merged := mergeSyncData(&SyncDataSet{}, local, remote, "panel-a", "panel-b")
	if len(merged.Data.Inbounds) != 1 || merged.Data.Inbounds[0].Tag != "new" {
		t.Fatalf("expected newer inbound to keep the port, got %+v", merged.Data.Inbounds)
	}
	if len(merged.Summary.Warnings) == 0 {
		t.Fatalf("expected a port conflict warning")
	}
}

func TestMergeRuleOrdering(t *testing.T) {
	base := &SyncDataSet{
		RoutingRules: []*model.RoutingRule{{Tag: "r1", Sort: 0, RawJson: `{"domain":["a.com"]}`}},
		State: map[string]SyncItemState{
			SyncStateKey(SyncKindRule, "r1"): testState(100),
		},
	}
	local := &SyncDataSet{
		RoutingRules: []*model.RoutingRule{
			{Tag: "r1", Sort: 0, RawJson: `{"domain":["a.com"]}`},
			{Tag: "r2", Sort: 1, RawJson: `{"domain":["b.com"]}`},
		},
		State: map[string]SyncItemState{
			SyncStateKey(SyncKindRule, "r1"): testState(100),
			SyncStateKey(SyncKindRule, "r2"): testState(200),
		},
	}
	remote := &SyncDataSet{
		RoutingRules: []*model.RoutingRule{
			{Tag: "r1", Sort: 0, RawJson: `{"domain":["a.com"]}`},
			{Tag: "r3", Sort: 1, RawJson: `{"domain":["c.com"]}`},
		},
		State: map[string]SyncItemState{
			SyncStateKey(SyncKindRule, "r1"): testState(100),
			SyncStateKey(SyncKindRule, "r3"): testState(200),
		},
	}
	merged := mergeSyncData(base, local, remote, "panel-a", "panel-b")
	if len(merged.Data.RoutingRules) != 3 {
		t.Fatalf("expected three rules, got %+v", merged.Data.RoutingRules)
	}
	expected := []string{"r1", "r2", "r3"}
	for index, rule := range merged.Data.RoutingRules {
		if rule.Tag != expected[index] || rule.Sort != index {
			t.Fatalf("unexpected rule order at %d: %+v", index, rule)
		}
	}
}

func TestVerifyPeerMeta(t *testing.T) {
	local := panelTimeMeta{Setting: "Asia/Hong_Kong", Zone: "Asia/Hong_Kong", Offset: 28800, Now: 1000000}
	base := &SyncPanelInfo{
		App:        SyncAppID,
		Version:    "26.15",
		PanelId:    "peer",
		Timezone:   "Asia/Hong_Kong",
		UtcOffset:  28800,
		ServerTime: 1000000,
	}
	if kind, err := verifyPeerMeta(base, local); err != nil {
		t.Fatalf("expected pass, got %s %v", kind, err)
	}

	timezone := *base
	timezone.UtcOffset = 32400
	if kind, err := verifyPeerMeta(&timezone, local); err == nil || kind != SyncCheckTimezone {
		t.Fatalf("expected timezone mismatch, got %s %v", kind, err)
	}

	skewed := *base
	skewed.ServerTime = base.ServerTime + 120000
	if kind, err := verifyPeerMeta(&skewed, local); err == nil || kind != SyncCheckClock {
		t.Fatalf("expected clock skew error, got %s %v", kind, err)
	}

	old := *base
	old.Version = "26.14"
	if kind, err := verifyPeerMeta(&old, local); err == nil || kind != SyncCheckVersion {
		t.Fatalf("expected version error, got %s %v", kind, err)
	}
}

func TestMergeRateLimitChange(t *testing.T) {
	baseItem := testInbound("a", 1001, "v1")
	baseItem.SpeedDown = 100000
	base := &SyncDataSet{
		Inbounds: []*model.Inbound{baseItem},
		State:    map[string]SyncItemState{SyncStateKey(SyncKindInbound, "a"): testState(100)},
	}
	localItem := testInbound("a", 1001, "v1")
	localItem.SpeedDown = 100000
	local := &SyncDataSet{
		Inbounds: []*model.Inbound{localItem},
		State:    map[string]SyncItemState{SyncStateKey(SyncKindInbound, "a"): testState(100)},
	}
	remoteItem := testInbound("a", 1001, "v1")
	remoteItem.SpeedUp = 50000
	remoteItem.SpeedDown = 250000
	remote := &SyncDataSet{
		Inbounds: []*model.Inbound{remoteItem},
		State:    map[string]SyncItemState{SyncStateKey(SyncKindInbound, "a"): testState(300)},
	}
	if datasetContentEqual(local, remote) {
		t.Fatalf("rate limits must affect content equality")
	}
	merged := mergeSyncData(base, local, remote, "panel-a", "panel-b")
	if len(merged.Data.Inbounds) != 1 {
		t.Fatalf("expected one inbound, got %d", len(merged.Data.Inbounds))
	}
	got := merged.Data.Inbounds[0]
	if got.SpeedUp != 50000 || got.SpeedDown != 250000 {
		t.Fatalf("rate limit update did not win the merge: %+v", got)
	}
}

func TestMergeIgnoresTrafficCounters(t *testing.T) {
	base := &SyncDataSet{
		Inbounds: []*model.Inbound{testInbound("a", 1001, "v1")},
		State:    map[string]SyncItemState{SyncStateKey(SyncKindInbound, "a"): testState(100)},
	}
	localItem := testInbound("a", 1001, "v1")
	localItem.Up = 12345
	localItem.Down = 67890
	local := &SyncDataSet{
		Inbounds: []*model.Inbound{localItem},
		State:    map[string]SyncItemState{SyncStateKey(SyncKindInbound, "a"): testState(100)},
	}
	remote := &SyncDataSet{
		Inbounds: []*model.Inbound{testInbound("a", 1001, "v1")},
		State:    map[string]SyncItemState{SyncStateKey(SyncKindInbound, "a"): testState(100)},
	}
	if !datasetContentEqual(local, remote) {
		t.Fatalf("traffic counters must not affect content equality")
	}
	merged := mergeSyncData(base, local, remote, "panel-a", "panel-b")
	if !datasetContentEqual(local, merged.Data) {
		t.Fatalf("merge must not report changes when only counters differ")
	}
}
