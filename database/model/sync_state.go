package model

type SyncItemState struct {
	Kind      string `json:"kind" gorm:"primaryKey"`
	Key       string `json:"key" gorm:"primaryKey"`
	ChangedAt int64  `json:"changedAt"`
	Deleted   bool   `json:"deleted"`
}

func (SyncItemState) TableName() string {
	return "sync_item_state"
}
