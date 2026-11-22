package model

type SyncNode struct {
	Id        int    `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`
	Remark    string `json:"remark" form:"remark"`
	Address   string `json:"address" form:"address"`
	Username  string `json:"username" form:"username"`
	Password  string `json:"-"`
	Ready     bool   `json:"ready" form:"ready"`
	LastSync  int64  `json:"lastSync" form:"lastSync"`
	CreatedAt int64  `json:"createdAt" form:"createdAt"`
}
