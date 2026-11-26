package job

import (
	"github.com/alireza0/x-ui/logger"
	"github.com/alireza0/x-ui/web/service"
)

type RateLimitJob struct {
	rateLimitService service.RateLimitService
}

func NewRateLimitJob() *RateLimitJob {
	return new(RateLimitJob)
}

func (j *RateLimitJob) Run() {
	if err := j.rateLimitService.Reconcile(); err != nil {
		logger.Warning("rate limit reconcile failed:", err)
	}
}
