package service

import (
	"strings"
	"time"

	"github.com/alireza0/x-ui/database"
	"gorm.io/gorm"
)

const (
	SyncKindInbound  = "inbound"
	SyncKindOutbound = "outbound"
	SyncKindRule     = "rule"
)

// SyncItemState is the per-item merge metadata exchanged between peers.
type SyncItemState struct {
	ChangedAt int64 `json:"changedAt"`
	Deleted   bool  `json:"deleted"`
}

type SyncStateService struct{}

func SyncStateKey(kind, key string) string {
	return kind + ":" + key
}

func splitSyncStateKey(full string) (string, string) {
	if idx := strings.Index(full, ":"); idx >= 0 {
		return full[:idx], full[idx+1:]
	}
	return full, ""
}

func (s *SyncStateService) Touch(kind, key string, deleted bool) {
	if key == "" {
		return
	}
	_ = s.touch(database.GetDB(), kind, key, deleted)
}

// TouchTx stamps items inside an existing transaction, used by background jobs
// that already hold one.
func (s *SyncStateService) TouchTx(tx *gorm.DB, kind, key string, deleted bool) {
	if key == "" {
		return
	}
	_ = s.touch(tx, kind, key, deleted)
}

func (s *SyncStateService) touch(db *gorm.DB, kind, key string, deleted bool) error {
	if db == nil {
		db = database.GetDB()
	}
	return db.Exec(`INSERT INTO sync_item_state(kind, key, changed_at, deleted) VALUES(?, ?, ?, ?)
		ON CONFLICT(kind, key) DO UPDATE SET changed_at = excluded.changed_at, deleted = excluded.deleted`,
		kind, key, time.Now().UnixMilli(), deleted).Error
}

func (s *SyncStateService) LoadAll() (map[string]SyncItemState, error) {
	type row struct {
		Kind      string
		Key       string
		ChangedAt int64
		Deleted   bool
	}
	rows := make([]row, 0, 64)
	if err := database.GetDB().Table("sync_item_state").Find(&rows).Error; err != nil {
		return nil, err
	}
	states := make(map[string]SyncItemState, len(rows))
	for _, r := range rows {
		states[SyncStateKey(r.Kind, r.Key)] = SyncItemState{ChangedAt: r.ChangedAt, Deleted: r.Deleted}
	}
	return states, nil
}

// ApplyMerged persists the merged merge metadata. A concurrent local edit with
// a newer timestamp is never rolled back by an older merge result.
func (s *SyncStateService) ApplyMerged(states map[string]SyncItemState) error {
	if len(states) == 0 {
		return nil
	}
	tx := database.GetDB().Begin()
	if tx.Error != nil {
		return tx.Error
	}
	for key, state := range states {
		kind, itemKey := splitSyncStateKey(key)
		if itemKey == "" {
			continue
		}
		err := tx.Exec(`INSERT INTO sync_item_state(kind, key, changed_at, deleted) VALUES(?, ?, ?, ?)
			ON CONFLICT(kind, key) DO UPDATE SET changed_at = excluded.changed_at, deleted = excluded.deleted
			WHERE excluded.changed_at >= sync_item_state.changed_at`,
			kind, itemKey, state.ChangedAt, state.Deleted).Error
		if err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit().Error
}
