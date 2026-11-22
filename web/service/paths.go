package service

import (
	"os"
	"runtime"

	"github.com/alireza0/x-ui/logger"
)

const (
	legacyXuiGoDir = "/root/XUI-GO"
	xuiGoDir       = "/XUI-GO"
)

func MigrateLegacyXuiGoDir() {
	if runtime.GOOS == "windows" {
		return
	}
	if _, err := os.Stat(legacyXuiGoDir); err != nil {
		return
	}
	if _, err := os.Stat(xuiGoDir); err == nil {
		return
	}
	if err := os.Rename(legacyXuiGoDir, xuiGoDir); err != nil {
		logger.Warning("migrate XUI-GO directory failed: ", err)
		return
	}
	logger.Info("moved XUI-GO directory to ", xuiGoDir)
}
