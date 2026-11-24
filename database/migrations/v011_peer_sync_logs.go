package migrations

import (
	"github.com/alireza0/x-ui/database/model"
	"github.com/alireza0/x-ui/logger"

	"gorm.io/gorm"
)

func migrateV011PeerSyncLogs(db *gorm.DB) error {
	if err := db.AutoMigrate(&model.SyncNode{}, &model.SyncLog{}); err != nil {
		return err
	}
	logger.Info("Peer sync logs migration (v11)")
	return nil
}
