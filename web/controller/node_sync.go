package controller

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/alireza0/x-ui/util/common"
	"github.com/alireza0/x-ui/web/service"
	"github.com/alireza0/x-ui/web/session"

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
	g.POST("/init/:id", a.initNode)
	g.POST("/push/:id", a.pushNode)
	g.POST("/merge/:id", a.mergeNode)
	g.POST("/auto", a.saveAuto)

	g.GET("/info", a.remoteInfo)
	g.POST("/receive-init", a.receiveInit)
	g.POST("/receive-apply", a.receiveApply)
	g.GET("/export", a.exportData)
	g.POST("/release", a.release)
}

func (a *NodeSyncController) nodes(c *gin.Context) {
	info, err := a.syncService.RoleInfo()
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	nodes, err := a.syncService.ListNodes()
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	autoInterval := 0
	if info.Role != service.SyncRoleSlave {
		autoInterval, _ = a.syncService.AutoInterval()
	}
	jsonObj(c, gin.H{
		"role":         info.Role,
		"masterName":   info.MasterName,
		"version":      info.Version,
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

func (a *NodeSyncController) initNode(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodeSync.toasts.initDone"), err)
		return
	}
	node, err := a.syncService.InitNode(id)
	jsonMsgObj(c, I18nWeb(c, "pages.nodeSync.toasts.initDone"), node, err)
}

func (a *NodeSyncController) pushNode(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodeSync.toasts.pushDone"), err)
		return
	}
	result, err := a.syncService.PushNode(id)
	jsonMsgObj(c, I18nWeb(c, "pages.nodeSync.toasts.pushDone"), result, err)
}

func (a *NodeSyncController) mergeNode(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodeSync.toasts.mergeDone"), err)
		return
	}
	user := session.GetLoginUser(c)
	userId := 0
	if user != nil {
		userId = user.Id
	}
	result, err := a.syncService.MergeNode(id, userId)
	jsonMsgObj(c, I18nWeb(c, "pages.nodeSync.toasts.mergeDone"), result, err)
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
	info, err := a.syncService.RoleInfo()
	jsonObj(c, info, err)
}

func (a *NodeSyncController) receiveInit(c *gin.Context) {
	file, _, err := c.Request.FormFile("db")
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodeSync.toasts.initDone"), err)
		return
	}
	defer file.Close()
	masterName := strings.TrimSpace(c.PostForm("masterName"))
	err = a.syncService.ReceiveInit(file, masterName)
	jsonMsg(c, I18nWeb(c, "pages.nodeSync.toasts.initDone"), err)
}

func (a *NodeSyncController) receiveApply(c *gin.Context) {
	data := &service.SyncDataSet{}
	if err := c.ShouldBindJSON(data); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodeSync.toasts.pushDone"), err)
		return
	}
	result, err := a.syncService.ReceiveApply(data)
	jsonObj(c, result, err)
}

func (a *NodeSyncController) exportData(c *gin.Context) {
	data, err := a.syncService.BuildLocalDataSet()
	jsonObj(c, data, err)
}

func (a *NodeSyncController) release(c *gin.Context) {
	err := a.syncService.ReleaseSlave()
	jsonMsg(c, I18nWeb(c, "pages.nodeSync.toasts.released"), err)
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
