package migrations

import (
	"github.com/alireza0/x-ui/database/model"
	"github.com/alireza0/x-ui/logger"

	"gorm.io/gorm"
)

func migrateV013SyncLineNodes(db *gorm.DB) error {
	if err := db.AutoMigrate(&model.SyncLineNode{}); err != nil {
		return err
	}
	logger.Info("Sync line node discovery migration (v13)")
	return nil
}
