package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/alireza0/x-ui/database/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newDirectoryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:dir-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.SyncNode{}, &model.SyncLineNode{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

func directoryPayload(panelId, title string, nodes ...SyncDirectoryNode) *SyncPayload {
	return &SyncPayload{
		PanelId:   panelId,
		Title:     title,
		Directory: nodes,
	}
}

func findLineNode(t *testing.T, db *gorm.DB, panelId string) *model.SyncLineNode {
	t.Helper()
	node := &model.SyncLineNode{}
	if err := db.Where("panel_id = ?", panelId).First(node).Error; err != nil {
		t.Fatalf("line node %s not found: %v", panelId, err)
	}
	return node
}

func TestMergeSyncDirectoryStoresIndirectNodes(t *testing.T) {
	db := newDirectoryTestDB(t)
	payload := directoryPayload("panel-b", "Node B",
		SyncDirectoryNode{PanelId: "panel-b", Title: "Node B", Hop: 0},
		SyncDirectoryNode{PanelId: "panel-c", Title: "Node C", Address: "https://c.example:54321/abc/", Hop: 1},
		SyncDirectoryNode{PanelId: "panel-d", Title: "Node D", Hop: 2},
	)
	if err := mergeSyncDirectory(db, "panel-a", payload); err != nil {
		t.Fatalf("merge directory: %v", err)
	}
	c := findLineNode(t, db, "panel-c")
	if c.Hop != 2 || c.ViaPanelId != "panel-b" || c.ViaTitle != "Node B" {
		t.Fatalf("unexpected indirect node: %+v", c)
	}
	if c.Address != "https://c.example:54321/abc" || c.Title != "Node C" {
		t.Fatalf("unexpected indirect node data: %+v", c)
	}
	d := findLineNode(t, db, "panel-d")
	if d.Hop != 3 {
		t.Fatalf("expected hop 3 for panel-d, got %d", d.Hop)
	}
}

func TestMergeSyncDirectorySkipsDirectAndLocal(t *testing.T) {
	db := newDirectoryTestDB(t)
	if err := db.Create(&model.SyncNode{PeerTitle: "Node B", PeerId: "panel-b", Sort: 1}).Error; err != nil {
		t.Fatalf("create direct node: %v", err)
	}
	payload := directoryPayload("panel-b", "Node B",
		SyncDirectoryNode{PanelId: "panel-a", Title: "Local", Hop: 0},
		SyncDirectoryNode{PanelId: "panel-b", Title: "Node B", Hop: 0},
		SyncDirectoryNode{PanelId: "panel-c", Title: "Node C", Hop: 1},
	)
	if err := mergeSyncDirectory(db, "panel-a", payload); err != nil {
		t.Fatalf("merge directory: %v", err)
	}
	var count int64
	db.Model(&model.SyncLineNode{}).Where("panel_id IN ?", []string{"panel-a", "panel-b"}).Count(&count)
	if count != 0 {
		t.Fatalf("local/direct panel must not be stored as line node")
	}
	findLineNode(t, db, "panel-c")

	// A direct peer that was previously discovered must be removed.
	if err := db.Create(&model.SyncLineNode{PanelId: "panel-d", Hop: 2, LastSeen: time.Now().Unix()}).Error; err != nil {
		t.Fatalf("seed line node: %v", err)
	}
	if err := db.Create(&model.SyncNode{PeerTitle: "Node D", PeerId: "panel-d", Sort: 2}).Error; err != nil {
		t.Fatalf("create direct node: %v", err)
	}
	if err := mergeSyncDirectory(db, "panel-a", payload); err != nil {
		t.Fatalf("merge directory again: %v", err)
	}
	var lineCount int64
	db.Model(&model.SyncLineNode{}).Where("panel_id = ?", "panel-d").Count(&lineCount)
	if lineCount != 0 {
		t.Fatalf("direct peer panel-d must be removed from line nodes")
	}
}

func TestMergeSyncDirectoryKeepsShortestHop(t *testing.T) {
	db := newDirectoryTestDB(t)
	first := directoryPayload("panel-b", "Node B",
		SyncDirectoryNode{PanelId: "panel-c", Title: "Node C", Hop: 2},
	)
	if err := mergeSyncDirectory(db, "panel-a", first); err != nil {
		t.Fatalf("merge first directory: %v", err)
	}
	if node := findLineNode(t, db, "panel-c"); node.Hop != 3 {
		t.Fatalf("expected hop 3, got %d", node.Hop)
	}
	second := directoryPayload("panel-d", "Node D",
		SyncDirectoryNode{PanelId: "panel-c", Title: "Node C", Address: "https://c.example/", Hop: 1},
	)
	if err := mergeSyncDirectory(db, "panel-a", second); err != nil {
		t.Fatalf("merge second directory: %v", err)
	}
	node := findLineNode(t, db, "panel-c")
	if node.Hop != 2 || node.ViaPanelId != "panel-d" {
		t.Fatalf("expected shorter hop via panel-d, got %+v", node)
	}
	if node.Address != "https://c.example" {
		t.Fatalf("expected address refresh, got %q", node.Address)
	}
}

func TestBuildSyncDirectoryOrder(t *testing.T) {
	db := newDirectoryTestDB(t)
	if err := db.Create(&model.SyncNode{PeerTitle: "Node B", Address: "https://b.example:54321/", PeerId: "panel-b", Sort: 1}).Error; err != nil {
		t.Fatalf("create direct node: %v", err)
	}
	if err := db.Create(&model.SyncLineNode{PanelId: "panel-c", Title: "Node C", Hop: 2, LastSeen: time.Now().Unix()}).Error; err != nil {
		t.Fatalf("create line node: %v", err)
	}
	directory := buildSyncDirectory(db, "panel-a", "Panel A", "https://a.example:54321/")
	if len(directory) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(directory))
	}
	if directory[0].PanelId != "panel-a" || directory[0].Hop != 0 || directory[0].Address != "https://a.example:54321" {
		t.Fatalf("local panel must be first with hop 0 and its address, got %+v", directory[0])
	}
	if directory[1].PanelId != "panel-b" || directory[1].Hop != 1 || directory[1].Address != "https://b.example:54321" {
		t.Fatalf("direct peer must follow, got %+v", directory[1])
	}
	if directory[2].PanelId != "panel-c" || directory[2].Hop != 2 {
		t.Fatalf("indirect node must be last, got %+v", directory[2])
	}
}
