package migrations

import (
	"github.com/alireza0/x-ui/database/model"
	"github.com/alireza0/x-ui/logger"

	"gorm.io/gorm"
)

func migrateV009SyncNode(db *gorm.DB) error {
	if err := db.AutoMigrate(&model.SyncNode{}); err != nil {
		return err
	}
	logger.Info("Created node sync table (v9)")
	return nil
}
