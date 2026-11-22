package job

import (
	"time"

	"github.com/alireza0/x-ui/logger"
	"github.com/alireza0/x-ui/web/service"
)

type AutoBackupJob struct {
	backupService service.AutoBackupService
}

func NewAutoBackupJob() *AutoBackupJob {
	return new(AutoBackupJob)
}

func (j *AutoBackupJob) Run() {
	if err := j.backupService.RunIfDue(time.Now()); err != nil {
		logger.Warning("auto backup failed:", err)
	}
}
