package model

type SyncLog struct {
	Id        int    `json:"id" gorm:"primaryKey;autoIncrement"`
	NodeId    int    `json:"nodeId" gorm:"index"`
	Level     string `json:"level"`
	Message   string `json:"message" gorm:"type:text"`
	CreatedAt int64  `json:"createdAt" gorm:"index"`
}

func (SyncLog) TableName() string {
	return "sync_logs"
}
