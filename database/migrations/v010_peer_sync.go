package migrations

import (
	"fmt"

	"github.com/alireza0/x-ui/database/model"
	"github.com/alireza0/x-ui/logger"

	"gorm.io/gorm"
)

func migrateV010PeerSync(db *gorm.DB) error {
	if err := db.AutoMigrate(&model.SyncNode{}, &model.SyncItemState{}); err != nil {
		return err
	}
	if err := db.Exec("UPDATE sync_nodes SET sort = id WHERE sort IS NULL OR sort = 0").Error; err != nil {
		return err
	}
	tables := map[string]string{
		"inbound":  "inbounds",
		"outbound": "outbounds",
		"rule":     "routing_rules",
	}
	for kind, table := range tables {
		stmt := fmt.Sprintf(`INSERT OR IGNORE INTO sync_item_state(kind, key, changed_at, deleted)
			SELECT ?, tag, 0, 0 FROM %s WHERE tag IS NOT NULL AND tag <> ''`, table)
		if err := db.Exec(stmt, kind).Error; err != nil {
			return err
		}
	}
	if err := db.Exec("UPDATE settings SET value = '' WHERE key IN ('syncRole', 'syncMasterName')").Error; err != nil {
		return err
	}
	logger.Info("Peer sync migration (v10)")
	return nil
}
