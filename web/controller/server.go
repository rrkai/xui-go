package controller

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alireza0/x-ui/util/common"
	"github.com/alireza0/x-ui/web/global"
	"github.com/alireza0/x-ui/web/network"
	"github.com/alireza0/x-ui/web/service"

	"github.com/gin-gonic/gin"
)

var filenameRegex = regexp.MustCompile(`^[a-zA-Z0-9_\-.]+$`)

type ServerController struct {
	BaseController

	serverService service.ServerService

	lastStatus        *service.Status
	lastGetStatusTime time.Time

	lastVersions        []string
	lastGetVersionsTime time.Time
}

func NewServerController(g *gin.RouterGroup) *ServerController {
	a := &ServerController{
		lastGetStatusTime: time.Now(),
	}
	a.initRouter(g)
	a.startTask()
	return a
}

func (a *ServerController) initRouter(g *gin.RouterGroup) {
	g = g.Group("/server")

	g.Use(a.checkLogin)
	g.GET("/status", a.status)
	g.GET("/trafficRank", a.trafficRank)
	g.GET("/clientExpiry", a.clientExpiry)
	g.GET("/remainingTraffic", a.remainingTraffic)
	g.GET("/dashboard", a.dashboard)
	g.GET("/checkUpdate", a.checkUpdate)
	g.GET("/panelUpdateStatus", a.panelUpdateStatus)
	g.POST("/panelUpdate", a.panelUpdate)
	g.GET("/certStatus", a.certStatus)
	g.GET("/certPortCheck", a.certPortCheck)
	g.POST("/certIssue", a.certIssue)
	g.POST("/certOptions", a.certOptions)
	g.GET("/getDb", a.getDb)
	g.GET("/getConfigJson", a.getConfigJson)
	g.GET("/getNewmldsa65", a.getNewmldsa65)
	g.GET("/getNewVlessEnc", a.getNewVlessEnc)
	g.GET("/getXrayVersion", a.getXrayVersion)
	g.GET("/getNewX25519Cert", a.getNewX25519Cert)

	g.POST("/getNewEchCert", a.getNewEchCert)
	g.POST("/getNewSelfSignedCert", a.getNewSelfSignedCert)
	g.POST("/getCertHash", a.getCertHash)
	g.POST("/getTlsPing", a.getTlsPing)
	g.POST("/stopXrayService", a.stopXrayService)
	g.POST("/restartXrayService", a.restartXrayService)
	g.POST("/installXray/:version", a.installXray)
	g.POST("/logs/:count", a.getLogs)
	g.POST("/importDB", a.importDB)
	g.POST("/panelTitle", a.setPanelTitle)
}

func (a *ServerController) certStatus(c *gin.Context) {
	status, err := (&service.AutoCertService{}).Status()
	jsonObj(c, status, err)
}

func (a *ServerController) certPortCheck(c *gin.Context) {
	target := c.Query("target")
	force := c.Query("force") == "1"
	status := (&service.AutoCertService{}).CheckPort80(target, force)
	jsonObj(c, status, nil)
}

func (a *ServerController) certOptions(c *gin.Context) {
	var form struct {
		ReleasePort80 bool   `json:"releasePort80" form:"releasePort80"`
		Unit          string `json:"unit" form:"unit"`
	}
	if err := c.ShouldBind(&form); err != nil {
		jsonObj(c, nil, err)
		return
	}
	err := (&service.AutoCertService{}).SetReleasePort80(form.ReleasePort80, form.Unit)
	jsonObj(c, nil, err)
}

func (a *ServerController) certIssue(c *gin.Context) {
	var form struct {
		Target   string `json:"target" form:"target"`
		Provider string `json:"provider" form:"provider"`
		EabKid   string `json:"eabKid" form:"eabKid"`
		EabHmac  string `json:"eabHmac" form:"eabHmac"`
		Email    string `json:"email" form:"email"`
	}
	if err := c.ShouldBind(&form); err != nil {
		jsonObj(c, nil, err)
		return
	}
	err := (&service.AutoCertService{}).Issue(form.Target, form.Provider, form.EabKid, form.EabHmac, form.Email)
	jsonObj(c, nil, err)
}

func (a *ServerController) refreshStatus() {
	a.lastStatus = a.serverService.GetStatus(a.lastStatus)
}

func (a *ServerController) startTask() {
	webServer := global.GetWebServer()
	c := webServer.GetCron()
	c.AddFunc("@every 2s", func() {
		now := time.Now()
		if now.Sub(a.lastGetStatusTime) > time.Minute*3 {
			return
		}
		a.refreshStatus()
	})
}

func (a *ServerController) status(c *gin.Context) {
	a.lastGetStatusTime = time.Now()

	jsonObj(c, a.lastStatus, nil)
}

func (a *ServerController) trafficRank(c *gin.Context) {
	rank, err := a.serverService.GetTrafficRank(10)
	jsonObj(c, rank, err)
}

func (a *ServerController) clientExpiry(c *gin.Context) {
	items, err := a.serverService.GetClientExpiry(10)
	jsonObj(c, items, err)
}

func (a *ServerController) remainingTraffic(c *gin.Context) {
	items, err := a.serverService.GetRemainingTraffic(10)
	jsonObj(c, items, err)
}

func (a *ServerController) dashboard(c *gin.Context) {
	widgets, err := a.serverService.GetDashboardWidgets(10)
	jsonObj(c, widgets, err)
}

func (a *ServerController) checkUpdate(c *gin.Context) {
	info, err := a.serverService.CheckUpdate()
	jsonObj(c, info, err)
}

func (a *ServerController) panelUpdateStatus(c *gin.Context) {
	jsonObj(c, a.serverService.GetPanelUpdateStatus(), nil)
}

func (a *ServerController) panelUpdate(c *gin.Context) {
	err := a.serverService.StartPanelUpdate()
	jsonMsg(c, I18nWeb(c, "pages.index.updateTitle"), err)
}

func (a *ServerController) setPanelTitle(c *gin.Context) {
	title := strings.TrimSpace(c.PostForm("title"))
	if utf8.RuneCountInString(title) > 64 {
		jsonMsg(c, "set panel title", common.NewError("panel title is too long"))
		return
	}
	var settingService service.SettingService
	if err := settingService.SetPanelTitle(title); err != nil {
		jsonMsg(c, "set panel title", err)
		return
	}
	a.lastGetStatusTime = time.Now()
	a.refreshStatus()
	jsonMsg(c, "set panel title", nil)
}

func (a *ServerController) getXrayVersion(c *gin.Context) {
	now := time.Now()
	if now.Sub(a.lastGetVersionsTime) <= time.Minute {
		jsonObj(c, a.lastVersions, nil)
		return
	}

	versions, err := a.serverService.GetXrayVersions()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "getVersion"), err)
		return
	}

	a.lastVersions = versions
	a.lastGetVersionsTime = time.Now()

	jsonObj(c, versions, nil)
}

func (a *ServerController) installXray(c *gin.Context) {
	version := c.Param("version")
	err := a.serverService.UpdateXray(version)
	jsonMsg(c, I18nWeb(c, "install")+" xray", err)
}

func (a *ServerController) stopXrayService(c *gin.Context) {
	a.lastGetStatusTime = time.Now()
	err := a.serverService.StopXrayService()
	if err != nil {
		jsonMsg(c, "", err)
		return
	}
	jsonMsg(c, "Xray stopped", err)
}

func (a *ServerController) restartXrayService(c *gin.Context) {
	err := a.serverService.RestartXrayService()
	if err != nil {
		jsonMsg(c, "", err)
		return
	}
	jsonMsg(c, "Xray restarted", err)
}

func (a *ServerController) getLogs(c *gin.Context) {
	count := c.Param("count")
	level := c.PostForm("level")
	syslog := c.PostForm("syslog")
	logs := a.serverService.GetLogs(count, level, syslog)
	jsonObj(c, logs, nil)
}

func (a *ServerController) getConfigJson(c *gin.Context) {
	configJson, err := a.serverService.GetConfigJson()
	if err != nil {
		jsonMsg(c, "get config.json", err)
		return
	}
	jsonObj(c, configJson, nil)
}

func (a *ServerController) getDb(c *gin.Context) {
	db, err := a.serverService.GetDb()
	if err != nil {
		jsonMsg(c, "get Database", err)
		return
	}

	filename := "x-ui.db"

	if !filenameRegex.MatchString(filename) {
		c.AbortWithError(http.StatusBadRequest, fmt.Errorf("invalid filename"))
		return
	}

	// Set the headers for the response
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", "attachment; filename="+filename)

	// Write the file contents to the response
	c.Writer.Write(db)
}

func (a *ServerController) importDB(c *gin.Context) {
	// Get the file from the request body
	file, _, err := c.Request.FormFile("db")
	if err != nil {
		jsonMsg(c, "Error reading db file", err)
		return
	}
	defer file.Close()
	// Always restart Xray before return
	defer a.serverService.RestartXrayService()
	defer func() {
		a.lastGetStatusTime = time.Now()
	}()
	// Import it
	err = a.serverService.ImportDB(file)
	if err != nil {
		jsonMsg(c, "", err)
		return
	}
	jsonObj(c, "Import DB", nil)
}

func (a *ServerController) getNewX25519Cert(c *gin.Context) {
	cert, err := a.serverService.GetNewX25519Cert()
	if err != nil {
		jsonMsg(c, "get x25519 certificate", err)
		return
	}
	jsonObj(c, cert, nil)
}

func (a *ServerController) getNewmldsa65(c *gin.Context) {
	cert, err := a.serverService.GetNewmldsa65()
	if err != nil {
		jsonMsg(c, "get mldsa65 certificate", err)
		return
	}
	jsonObj(c, cert, nil)
}

func (a *ServerController) getNewEchCert(c *gin.Context) {
	sni := c.PostForm("sni")
	cert, err := a.serverService.GetNewEchCert(sni)
	if err != nil {
		jsonMsg(c, "get ech certificate", err)
		return
	}
	jsonObj(c, cert, nil)
}

func (a *ServerController) getNewSelfSignedCert(c *gin.Context) {
	sni := c.PostForm("sni")
	cert, err := network.GenerateSelfSignedCert(sni)
	if err != nil {
		jsonMsg(c, "generate self-signed certificate", err)
		return
	}
	jsonObj(c, cert, nil)
}

func (a *ServerController) getCertHash(c *gin.Context) {
	certFile := c.PostForm("certFile")
	certContent := c.PostForm("certContent")
	hashes, err := network.GetCertHash(certFile, certContent)
	if err != nil {
		jsonMsg(c, "get certificate hash", err)
		return
	}
	jsonObj(c, hashes, nil)
}

func (a *ServerController) getTlsPing(c *gin.Context) {
	domain := c.PostForm("domain")
	port := c.PostForm("port")
	tlsPing, err := network.GetTlsPing(domain, port)
	if err != nil {
		jsonMsg(c, "get tls ping", err)
		return
	}
	jsonObj(c, tlsPing, nil)
}

func (a *ServerController) getNewVlessEnc(c *gin.Context) {
	out, err := a.serverService.GetNewVlessEnc()
	if err != nil {
		jsonMsg(c, "Failed to generate vless encryption config", err)
		return
	}
	jsonObj(c, out, nil)
}
