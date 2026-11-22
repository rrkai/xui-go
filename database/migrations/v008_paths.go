package migrations

import (
	"github.com/alireza0/x-ui/logger"

	"gorm.io/gorm"
)

func migrateV008Paths(db *gorm.DB) error {
	db.Exec(`UPDATE settings SET value = '/XUI-GO/Backup' WHERE key = 'backupDir' AND value = '/root/XUI-GO/Backup'`)
	db.Exec(`UPDATE settings SET value = REPLACE(value, '/root/XUI-GO/HTTP-SSL', '/XUI-GO/HTTP-SSL') WHERE key IN ('webCertFile','webKeyFile') AND value LIKE '/root/XUI-GO/HTTP-SSL%'`)

	logger.Info("Migrated XUI-GO paths to v8")
	return nil
}
