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

// SyncLineNode is a panel discovered through a peer's node directory. It is
// read-only in the UI; every sync refreshes its address and hop distance.
type SyncLineNode struct {
	Id         int    `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`
	PanelId    string `json:"panelId" gorm:"uniqueIndex"`
	Title      string `json:"title"`
	Address    string `json:"address"`
	ViaPanelId string `json:"viaPanelId"`
	ViaTitle   string `json:"viaTitle"`
	Hop        int    `json:"hop"`
	LastSeen   int64  `json:"lastSeen"`
}
