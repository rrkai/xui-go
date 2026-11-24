package model

type SyncNode struct {
	Id        int    `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`
	Remark    string `json:"remark" form:"remark"`
	PeerTitle string `json:"peerTitle" form:"peerTitle"`
	Address   string `json:"address" form:"address"`
	Username  string `json:"username" form:"username"`
	Password  string `json:"-"`
	PeerId    string `json:"peerId" gorm:"index"`
	Sort      int    `json:"sort"`
	Passive   bool   `json:"passive"`
	Ready     bool   `json:"ready" form:"ready"`
	LastSync  int64  `json:"lastSync" form:"lastSync"`
	CreatedAt int64  `json:"createdAt" form:"createdAt"`
	BaseData  string `json:"-" gorm:"type:text"`
}
