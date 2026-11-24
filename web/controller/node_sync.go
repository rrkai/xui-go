package controller

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/alireza0/x-ui/util/common"
	"github.com/alireza0/x-ui/web/service"

	"github.com/gin-gonic/gin"
)

type NodeSyncController struct {
	BaseController

	syncService service.NodeSyncService
}

type syncNodeForm struct {
	Remark   string `json:"remark" form:"remark"`
	Address  string `json:"address" form:"address"`
	Username string `json:"username" form:"username"`
	Password string `json:"password" form:"password"`
}

func NewNodeSyncController(g *gin.RouterGroup) *NodeSyncController {
	a := &NodeSyncController{}
	a.initRouter(g)
	return a
}

func (a *NodeSyncController) initRouter(g *gin.RouterGroup) {
	g = g.Group("/node_sync")
	g.GET("/nodes", a.nodes)
	g.POST("/add", a.addNode)
	g.POST("/update/:id", a.updateNode)
	g.POST("/check/:id", a.checkNode)
	g.GET("/password/:id", a.nodePassword)
	g.POST("/delete/:id", a.deleteNode)
	g.POST("/move/:id", a.moveNode)
	g.POST("/sync", a.syncAll)
	g.POST("/sync/:id", a.syncNode)
	g.POST("/auto", a.saveAuto)

	g.GET("/info", a.remoteInfo)
	g.GET("/export", a.exportData)
	g.POST("/receive-merge", a.receiveMerge)
	g.POST("/timezone/apply", a.applyTimezone)
	g.POST("/align-time/:id", a.alignTime)
	g.GET("/logs/:id", a.nodeLogs)

	// Legacy master/slave endpoints. They stay routed so old peers get a clear
	// message instead of silently overwriting data.
	g.POST("/init/:id", a.legacyEndpoint)
	g.POST("/push", a.syncAll)
	g.POST("/push/:id", a.syncNode)
	g.POST("/merge/:id", a.legacyEndpoint)
	g.POST("/overwrite/:id", a.legacyEndpoint)
	g.POST("/receive-init", a.legacyEndpoint)
	g.POST("/receive-apply", a.legacyEndpoint)
	g.POST("/release", a.legacyEndpoint)
}

func (a *NodeSyncController) nodes(c *gin.Context) {
	info, err := a.syncService.PanelInfo()
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	nodes, err := a.syncService.ListNodes()
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	autoInterval, _ := a.syncService.AutoInterval()
	jsonObj(c, gin.H{
		"version":      info.Version,
		"panelId":      info.PanelId,
		"timezone":     info.Timezone,
		"zoneName":     info.ZoneName,
		"serverTime":   info.ServerTime,
		"autoInterval": autoInterval,
		"nodes":        nodes,
	}, nil)
}

func (a *NodeSyncController) addNode(c *gin.Context) {
	var form syncNodeForm
	if err := c.ShouldBind(&form); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodeSync.toasts.added"), err)
		return
	}
	if isSelfPanelAddress(c, form.Address) {
		jsonMsg(c, I18nWeb(c, "pages.nodeSync.toasts.added"), common.NewError("不能添加本面板"))
		return
	}
	node, err := a.syncService.AddNode(form.Remark, form.Address, form.Username, form.Password)
	jsonMsgObj(c, I18nWeb(c, "pages.nodeSync.toasts.added"), node, err)
}

func (a *NodeSyncController) updateNode(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodeSync.toasts.updated"), err)
		return
	}
	var form syncNodeForm
	if err := c.ShouldBind(&form); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodeSync.toasts.updated"), err)
		return
	}
	node, err := a.syncService.UpdateNode(id, form.Remark, form.Address, form.Username, form.Password)
	jsonMsgObj(c, I18nWeb(c, "pages.nodeSync.toasts.updated"), node, err)
}

func (a *NodeSyncController) nodePassword(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	password, err := a.syncService.GetNodePassword(id)
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	jsonObj(c, gin.H{"password": password}, nil)
}

func (a *NodeSyncController) checkNode(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	check, err := a.syncService.CheckNode(id)
	jsonObj(c, check, err)
}

func (a *NodeSyncController) deleteNode(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodeSync.toasts.deleted"), err)
		return
	}
	warning, err := a.syncService.DeleteNode(id)
	jsonMsgObj(c, I18nWeb(c, "pages.nodeSync.toasts.deleted"), warning, err)
}

func (a *NodeSyncController) moveNode(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodeSync.toasts.moved"), err)
		return
	}
	var form struct {
		Direction string `json:"direction" form:"direction"`
	}
	if err := c.ShouldBind(&form); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodeSync.toasts.moved"), err)
		return
	}
	err = a.syncService.MoveNode(id, strings.TrimSpace(form.Direction))
	jsonMsg(c, I18nWeb(c, "pages.nodeSync.toasts.moved"), err)
}

func (a *NodeSyncController) syncNode(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodeSync.toasts.synced"), err)
		return
	}
	result, err := a.syncService.SyncNodes(id)
	jsonMsgObj(c, I18nWeb(c, "pages.nodeSync.toasts.synced"), result, err)
}

func (a *NodeSyncController) syncAll(c *gin.Context) {
	result, err := a.syncService.SyncNodes(0)
	jsonMsgObj(c, I18nWeb(c, "pages.nodeSync.toasts.synced"), result, err)
}

type syncAutoForm struct {
	Interval *int `json:"interval" form:"interval"`
}

func (a *NodeSyncController) saveAuto(c *gin.Context) {
	var form syncAutoForm
	if err := c.ShouldBind(&form); err != nil || form.Interval == nil {
		jsonMsg(c, I18nWeb(c, "pages.nodeSync.toasts.autoSync"), common.NewError("自动同步频率无效"))
		return
	}
	if err := a.syncService.SetAutoSync(*form.Interval); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodeSync.toasts.autoSync"), err)
		return
	}
	jsonObj(c, gin.H{"interval": *form.Interval}, nil)
}

func (a *NodeSyncController) remoteInfo(c *gin.Context) {
	info, err := a.syncService.PanelInfo()
	jsonObj(c, info, err)
}

func (a *NodeSyncController) exportData(c *gin.Context) {
	payload, err := a.syncService.BuildExportPayload()
	jsonObj(c, payload, err)
}

func (a *NodeSyncController) receiveMerge(c *gin.Context) {
	payload := &service.SyncPayload{}
	if err := c.ShouldBindJSON(payload); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodeSync.toasts.synced"), err)
		return
	}
	result, err := a.syncService.ReceiveMerge(payload)
	jsonObj(c, result, err)
}

type syncTimezoneForm struct {
	Timezone  string `json:"timezone" form:"timezone"`
	UtcOffset *int64 `json:"utcOffset" form:"utcOffset"`
}

func (a *NodeSyncController) applyTimezone(c *gin.Context) {
	var form syncTimezoneForm
	if err := c.ShouldBind(&form); err != nil {
		jsonObj(c, nil, err)
		return
	}
	info, err := a.syncService.ApplyTimezone(form.Timezone, form.UtcOffset)
	jsonObj(c, info, err)
}

func (a *NodeSyncController) alignTime(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	result, err := a.syncService.AlignNodeTimezone(id)
	jsonObj(c, result, err)
}

func (a *NodeSyncController) nodeLogs(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	logs, err := a.syncService.ListLogs(id)
	jsonObj(c, gin.H{"logs": logs}, err)
}

func (a *NodeSyncController) legacyEndpoint(c *gin.Context) {
	jsonMsg(c, I18nWeb(c, "pages.nodeSync.toasts.synced"),
		common.NewError("节点同步已改为对等双向同步，请把两端面板都升级到新版"))
}

func isSelfPanelAddress(c *gin.Context, address string) bool {
	address = strings.TrimSpace(address)
	if !strings.Contains(address, "://") {
		address = "https://" + address
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Host, c.Request.Host)
}
