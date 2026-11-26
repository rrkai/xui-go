package migrations

import (
	"github.com/alireza0/x-ui/database/model"
	"github.com/alireza0/x-ui/logger"

	"gorm.io/gorm"
)

func migrateV012InboundSpeed(db *gorm.DB) error {
	if err := db.AutoMigrate(&model.Inbound{}); err != nil {
		return err
	}
	logger.Info("Inbound rate limit migration (v12)")
	return nil
}
