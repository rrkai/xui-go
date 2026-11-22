package job

import (
	"github.com/alireza0/x-ui/web/service"
)

type NodeSyncAutoJob struct {
	syncService service.NodeSyncService
}

func NewNodeSyncAutoJob() *NodeSyncAutoJob {
	return new(NodeSyncAutoJob)
}

func (j *NodeSyncAutoJob) Run() {
	j.syncService.AutoSyncTick()
}
