package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alireza0/x-ui/config"
	"github.com/alireza0/x-ui/database"
	"github.com/alireza0/x-ui/database/model"
	"github.com/alireza0/x-ui/logger"
	"github.com/alireza0/x-ui/util/common"
	"github.com/alireza0/x-ui/xray"

	"gorm.io/gorm"
)

const (
	SyncAppID          = "xui-go"
	SyncPeerMinVersion = "26.15"

	SyncCheckAddress  = "address"
	SyncCheckAuth     = "auth"
	SyncCheckTimeout  = "timeout"
	SyncCheckTimezone = "timezone"
	SyncCheckClock    = "clock"
	SyncCheckIdentity = "identity"
	SyncCheckVersion  = "version"

	syncRequestTimeout = 60 * time.Second
	syncClockSkewLimit = 60 * time.Second
	syncLogLimit       = 100
)

var (
	syncOperationMu   sync.Mutex
	autoSyncMu        sync.Mutex
	autoSyncAttemptMu sync.Mutex
	autoSyncAttemptAt = map[int]int64{}

	autoSyncIntervals = map[int]bool{5: true, 10: true, 30: true, 60: true, 1440: true}
)

type SyncPanelInfo struct {
	App        string `json:"app"`
	Version    string `json:"version"`
	PanelId    string `json:"panelId"`
	Title      string `json:"title"`
	Timezone   string `json:"timezone"`
	ZoneName   string `json:"zoneName"`
	ServerTime int64  `json:"serverTime"`
	UtcOffset  int64  `json:"utcOffset"`
	NodeCount  int64  `json:"nodeCount"`
}

type SyncNodeView struct {
	Id           int    `json:"id"`
	Remark       string `json:"remark"`
	PeerTitle    string `json:"peerTitle"`
	Address      string `json:"address"`
	Username     string `json:"username"`
	PeerId       string `json:"peerId"`
	Sort         int    `json:"sort"`
	Passive      bool   `json:"passive"`
	Ready        bool   `json:"ready"`
	LastSync     int64  `json:"lastSync"`
	LastSyncText string `json:"lastSyncText"`
	CreatedAt    int64  `json:"createdAt"`
}

type SyncNodeCheck struct {
	Online     bool   `json:"online"`
	Version    string `json:"version"`
	PanelId    string `json:"panelId"`
	Title      string `json:"title"`
	Kind       string `json:"kind"`
	Error      string `json:"error"`
	Timezone   string `json:"timezone"`
	ZoneName   string `json:"zoneName"`
	ServerTime int64  `json:"serverTime"`
	UtcOffset  int64  `json:"utcOffset"`
	SkewMs     int64  `json:"skewMs"`
	Ready      bool   `json:"ready"`
	Passive    bool   `json:"passive"`
}

type SyncDataSet struct {
	Inbounds     []*model.Inbound         `json:"inbounds"`
	Outbounds    []*model.Outbound        `json:"outbounds"`
	RoutingRules []*model.RoutingRule     `json:"routingRules"`
	State        map[string]SyncItemState `json:"state,omitempty"`
}

type SyncPayload struct {
	App          string                   `json:"app,omitempty"`
	Version      string                   `json:"version,omitempty"`
	PanelId      string                   `json:"panelId"`
	Title        string                   `json:"title,omitempty"`
	Address      string                   `json:"address,omitempty"`
	Timezone     string                   `json:"timezone,omitempty"`
	ZoneName     string                   `json:"zoneName,omitempty"`
	UtcOffset    int64                    `json:"utcOffset"`
	ServerTime   int64                    `json:"serverTime"`
	Inbounds     []*model.Inbound         `json:"inbounds"`
	Outbounds    []*model.Outbound        `json:"outbounds"`
	RoutingRules []*model.RoutingRule     `json:"routingRules"`
	State        map[string]SyncItemState `json:"state,omitempty"`
}

type SyncApplyResult struct {
	Inbounds        int           `json:"inbounds"`
	Outbounds       int           `json:"outbounds"`
	RoutingRules    int           `json:"routingRules"`
	InboundChanges  SyncKindCount `json:"inboundChanges"`
	OutboundChanges SyncKindCount `json:"outboundChanges"`
	RuleChanges     SyncKindCount `json:"ruleChanges"`
	HotReload       bool          `json:"hotReload"`
	Warnings        []string      `json:"warnings"`
}

type SyncPeerResult struct {
	Id           int           `json:"id"`
	Remark       string        `json:"remark"`
	Success      bool          `json:"success"`
	Message      string        `json:"message,omitempty"`
	Inbounds     SyncKindCount `json:"inbounds"`
	Outbounds    SyncKindCount `json:"outbounds"`
	RoutingRules SyncKindCount `json:"routingRules"`
	Changed      bool          `json:"changed"`
	HotReload    bool          `json:"hotReload"`
	Warnings     []string      `json:"warnings"`
	LastSync     int64         `json:"lastSync"`
	LastSyncText string        `json:"lastSyncText"`
}

type SyncBatchResult struct {
	Total     int               `json:"total"`
	Succeeded int               `json:"succeeded"`
	Failed    int               `json:"failed"`
	Items     []*SyncPeerResult `json:"items"`
}

type SyncTimezoneResult struct {
	NodeId    int            `json:"nodeId"`
	Previous  string         `json:"previous"`
	Timezone  string         `json:"timezone"`
	ZoneName  string         `json:"zoneName"`
	UtcOffset int64          `json:"utcOffset"`
	Check     *SyncNodeCheck `json:"check"`
}

type SyncLogView struct {
	Id       int    `json:"id"`
	Time     int64  `json:"time"`
	TimeText string `json:"timeText"`
	Level    string `json:"level"`
	Message  string `json:"message"`
}

type NodeSyncService struct {
	settingService     SettingService
	inboundService     InboundService
	outboundService    OutboundService
	routingRuleService RoutingRuleService
}

type panelTimeMeta struct {
	Setting string
	Zone    string
	Offset  int64
	Now     int64
}

func panelTimeNow() panelTimeMeta {
	settingService := &SettingService{}
	location, err := settingService.GetTimeLocation()
	if err != nil || location == nil {
		location = time.Local
	}
	now := time.Now()
	_, offset := now.In(location).Zone()
	setting, _ := settingService.GetTimeLocationSetting()
	if setting == "" {
		setting = location.String()
	}
	return panelTimeMeta{Setting: setting, Zone: location.String(), Offset: int64(offset), Now: now.UnixMilli()}
}

func formatPanelTime(timestamp int64) string {
	return formatPanelTimeLayout(timestamp, "2006-01-02 15:04")
}

func formatPanelTimeSeconds(timestamp int64) string {
	return formatPanelTimeLayout(timestamp, "2006-01-02 15:04:05")
}

func formatPanelTimeLayout(timestamp int64, layout string) string {
	if timestamp <= 0 {
		return ""
	}
	location, err := (&SettingService{}).GetTimeLocation()
	if err != nil || location == nil {
		location = time.Local
	}
	return time.Unix(timestamp, 0).In(location).Format(layout)
}

func formatUtcOffset(offset int64) string {
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	return fmt.Sprintf("UTC%s%d:%02d", sign, offset/3600, (offset%3600)/60)
}

func compareSyncVersion(left, right string) int {
	parse := func(value string) []int {
		value = strings.TrimPrefix(strings.TrimSpace(value), "v")
		parts := strings.Split(value, ".")
		result := make([]int, 0, len(parts))
		for _, part := range parts {
			number, _ := strconv.Atoi(strings.TrimSpace(part))
			result = append(result, number)
		}
		return result
	}
	leftParts := parse(left)
	rightParts := parse(right)
	length := len(leftParts)
	if len(rightParts) > length {
		length = len(rightParts)
	}
	for i := 0; i < length; i++ {
		var a, b int
		if i < len(leftParts) {
			a = leftParts[i]
		}
		if i < len(rightParts) {
			b = rightParts[i]
		}
		if a != b {
			if a < b {
				return -1
			}
			return 1
		}
	}
	return 0
}

func verifyPeerMeta(info *SyncPanelInfo, local panelTimeMeta) (string, error) {
	if info.App != SyncAppID {
		return SyncCheckAddress, common.NewError("目标面板不是 XUI-GO 面板，不支持节点同步")
	}
	if info.PanelId == "" || compareSyncVersion(info.Version, SyncPeerMinVersion) < 0 {
		return SyncCheckVersion, common.NewErrorf("对端版本过低（%s），请先升级到 %s 及以上", info.Version, SyncPeerMinVersion)
	}
	if info.UtcOffset != local.Offset {
		return SyncCheckTimezone, common.NewErrorf("时区不一致：本面板 %s（%s），对端 %s（%s），请把两端面板时区改为一致",
			local.Setting, formatUtcOffset(local.Offset), info.Timezone, formatUtcOffset(info.UtcOffset))
	}
	skew := info.ServerTime - local.Now
	if skew < 0 {
		skew = -skew
	}
	if skew > syncClockSkewLimit.Milliseconds() {
		return SyncCheckClock, common.NewErrorf("与对端时间相差 %d 秒，请检查两端系统时间同步（NTP）", skew/1000)
	}
	return "", nil
}

func (s *NodeSyncService) PanelInfo() (*SyncPanelInfo, error) {
	panelId, err := s.settingService.GetPanelUUID()
	if err != nil {
		return nil, err
	}
	meta := panelTimeNow()
	title, _ := s.settingService.GetPanelTitle()
	if title == "" {
		title, _ = os.Hostname()
	}
	var count int64
	if err := database.GetDB().Model(&model.SyncNode{}).Count(&count).Error; err != nil {
		return nil, err
	}
	return &SyncPanelInfo{
		App:        SyncAppID,
		Version:    config.GetVersion(),
		PanelId:    panelId,
		Title:      title,
		Timezone:   meta.Setting,
		ZoneName:   meta.Zone,
		ServerTime: meta.Now,
		UtcOffset:  meta.Offset,
		NodeCount:  count,
	}, nil
}

func (s *NodeSyncService) ListNodes() ([]*SyncNodeView, error) {
	nodes := make([]*model.SyncNode, 0, 8)
	err := database.GetDB().
		Select("id", "remark", "peer_title", "address", "username", "peer_id", "sort", "passive", "ready", "last_sync", "created_at").
		Order("sort asc, id asc").Find(&nodes).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}
	views := make([]*SyncNodeView, 0, len(nodes))
	for _, node := range nodes {
		views = append(views, &SyncNodeView{
			Id:           node.Id,
			Remark:       node.Remark,
			PeerTitle:    node.PeerTitle,
			Address:      node.Address,
			Username:     node.Username,
			PeerId:       node.PeerId,
			Sort:         node.Sort,
			Passive:      node.Passive,
			Ready:        node.Ready,
			LastSync:     node.LastSync,
			LastSyncText: formatPanelTime(node.LastSync),
			CreatedAt:    node.CreatedAt,
		})
	}
	return views, nil
}

func (s *NodeSyncService) nextSort() int {
	var maxSort int
	database.GetDB().Model(&model.SyncNode{}).Select("COALESCE(MAX(sort), 0)").Scan(&maxSort)
	return maxSort + 1
}

func (s *NodeSyncService) AddNode(remark, address, username, password string) (*model.SyncNode, error) {
	address = strings.TrimSpace(address)
	username = strings.TrimSpace(username)
	if address == "" || username == "" || password == "" {
		return nil, common.NewError("面板地址、用户名和密码不能为空")
	}
	client, info, err := verifyRemotePanel(address, username, password)
	if err != nil {
		return nil, err
	}
	if info.PanelId == "" || compareSyncVersion(info.Version, SyncPeerMinVersion) < 0 {
		return nil, common.NewErrorf("对端版本过低（%s），请先升级到 %s 及以上", info.Version, SyncPeerMinVersion)
	}
	localPanelId, err := s.settingService.GetPanelUUID()
	if err != nil {
		return nil, err
	}
	if info.PanelId == localPanelId {
		return nil, common.NewError("不能添加本面板")
	}
	var count int64
	if err := database.GetDB().Model(&model.SyncNode{}).Where("peer_id = ?", info.PanelId).Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, common.NewError("该面板已在列表中")
	}
	encrypted, err := encryptSyncPassword(password)
	if err != nil {
		return nil, err
	}
	node := &model.SyncNode{
		Remark:    strings.TrimSpace(remark),
		PeerTitle: info.Title,
		Address:   client.base,
		Username:  username,
		Password:  encrypted,
		PeerId:    info.PanelId,
		Sort:      s.nextSort(),
		CreatedAt: time.Now().Unix(),
	}
	if err := database.GetDB().Create(node).Error; err != nil {
		return nil, err
	}
	s.addSyncLog(node.Id, "info", "已添加对端："+syncNodeName(node))
	return node, nil
}

func (s *NodeSyncService) UpdateNode(id int, remark, address, username, password string) (*model.SyncNode, error) {
	node, err := s.loadNode(id)
	if err != nil {
		return nil, err
	}
	node.Remark = strings.TrimSpace(remark)
	address = strings.TrimRight(strings.TrimSpace(address), "/")
	username = strings.TrimSpace(username)

	effectiveAddress := node.Address
	effectiveUser := node.Username
	if address != "" {
		effectiveAddress = address
	}
	if username != "" {
		effectiveUser = username
	}
	credentialsChanged := effectiveAddress != node.Address || effectiveUser != node.Username || password != ""

	if credentialsChanged {
		if node.Passive {
			return nil, common.NewError("被动对端没有登录凭据，不能编辑地址或账号")
		}
		effectivePassword, err := decryptSyncPassword(node.Password)
		if err != nil {
			return nil, common.NewError("节点密码解密失败，请重新填写密码")
		}
		if password != "" {
			effectivePassword = password
		}
		client, info, err := verifyRemotePanel(effectiveAddress, effectiveUser, effectivePassword)
		if err != nil {
			return nil, err
		}
		if info.PanelId == "" || compareSyncVersion(info.Version, SyncPeerMinVersion) < 0 {
			return nil, common.NewErrorf("对端版本过低（%s），请先升级到 %s 及以上", info.Version, SyncPeerMinVersion)
		}
		if node.PeerId != "" && node.PeerId != info.PanelId {
			node.BaseData = ""
		}
		node.Address = client.base
		node.Username = effectiveUser
		node.PeerId = info.PanelId
		node.PeerTitle = info.Title
		node.Ready = false
		if password != "" {
			encrypted, err := encryptSyncPassword(password)
			if err != nil {
				return nil, err
			}
			node.Password = encrypted
		}
	}

	if err := database.GetDB().Save(node).Error; err != nil {
		return nil, err
	}
	if credentialsChanged {
		s.addSyncLog(node.Id, "info", "对端信息已更新，需要重新检查")
	}
	return node, nil
}

func isSyncTimeout(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded")
}

func (s *NodeSyncService) GetNodePassword(id int) (string, error) {
	node, err := s.loadNode(id)
	if err != nil {
		return "", err
	}
	password, err := decryptSyncPassword(node.Password)
	if err != nil {
		return "", common.NewError("节点密码解密失败，请重新填写密码")
	}
	return password, nil
}

func (s *NodeSyncService) DeleteNode(id int) (string, error) {
	if _, err := s.loadNode(id); err != nil {
		return "", err
	}
	if err := database.GetDB().Delete(&model.SyncNode{}, id).Error; err != nil {
		return "", err
	}
	if err := database.GetDB().Where("node_id = ?", id).Delete(&model.SyncLog{}).Error; err != nil {
		logger.Warning("node sync: delete sync logs failed: ", err)
	}
	autoSyncAttemptMu.Lock()
	delete(autoSyncAttemptAt, id)
	autoSyncAttemptMu.Unlock()
	s.normalizeSort()
	var activeCount int64
	if err := database.GetDB().Model(&model.SyncNode{}).Where("passive = ?", false).Count(&activeCount).Error; err == nil && activeCount == 0 {
		if err := s.settingService.SetSyncAutoInterval(0); err != nil {
			logger.Warning("node sync: disable auto sync failed: ", err)
		}
	}
	return "", nil
}

func (s *NodeSyncService) normalizeSort() {
	nodes := make([]*model.SyncNode, 0, 8)
	if err := database.GetDB().Order("sort asc, id asc").Find(&nodes).Error; err != nil {
		return
	}
	for index, node := range nodes {
		expected := index + 1
		if node.Sort == expected {
			continue
		}
		database.GetDB().Model(&model.SyncNode{}).Where("id = ?", node.Id).Update("sort", expected)
	}
}

func (s *NodeSyncService) MoveNode(id int, direction string) error {
	nodes := make([]*model.SyncNode, 0, 8)
	if err := database.GetDB().Order("sort asc, id asc").Find(&nodes).Error; err != nil {
		return err
	}
	current := -1
	for index, node := range nodes {
		if node.Id == id {
			current = index
			break
		}
	}
	if current < 0 {
		return common.NewError("节点不存在")
	}
	target := current - 1
	if direction == "down" {
		target = current + 1
	}
	if target < 0 || target >= len(nodes) {
		return nil
	}
	nodes[current].Sort, nodes[target].Sort = nodes[target].Sort, nodes[current].Sort
	for _, node := range []*model.SyncNode{nodes[current], nodes[target]} {
		if err := database.GetDB().Model(&model.SyncNode{}).Where("id = ?", node.Id).Update("sort", node.Sort).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *NodeSyncService) CheckNode(id int) (*SyncNodeCheck, error) {
	node, err := s.loadNode(id)
	if err != nil {
		return nil, err
	}
	wasReady := node.Ready
	result := &SyncNodeCheck{Passive: node.Passive, Ready: node.Ready}
	if node.Passive {
		result.Kind = SyncCheckAddress
		result.Error = "被动对端由对方发起同步，不能主动检查"
		return result, nil
	}
	password, err := decryptSyncPassword(node.Password)
	if err != nil {
		result.Kind = SyncCheckAuth
		result.Error = "节点密码解密失败，请重新填写密码"
		return result, nil
	}
	client, err := newPanelClient(node.Address, 10*time.Second)
	if err != nil {
		result.Kind = SyncCheckAddress
		result.Error = err.Error()
		return result, nil
	}
	if err := client.login(node.Username, password); err != nil {
		var authErr *syncPanelAuthError
		switch {
		case errors.As(err, &authErr):
			result.Kind = SyncCheckAuth
		case isSyncTimeout(err):
			result.Kind = SyncCheckTimeout
		default:
			result.Kind = SyncCheckAddress
		}
		result.Error = err.Error()
		return result, nil
	}
	info, err := client.info()
	if err != nil {
		if isSyncTimeout(err) {
			result.Kind = SyncCheckTimeout
		} else {
			result.Kind = SyncCheckAddress
		}
		result.Error = err.Error()
		return result, nil
	}
	result.Version = info.Version
	result.PanelId = info.PanelId
	result.Title = info.Title
	if info.Title != "" && info.Title != node.PeerTitle {
		node.PeerTitle = info.Title
		if err := database.GetDB().Model(node).Update("peer_title", info.Title).Error; err != nil {
			logger.Warning("node sync: update peer title failed: ", err)
		}
	}
	result.Timezone = info.Timezone
	result.ZoneName = info.ZoneName
	result.ServerTime = info.ServerTime
	result.UtcOffset = info.UtcOffset
	localMeta := panelTimeNow()
	result.SkewMs = info.ServerTime - localMeta.Now

	kind, verifyErr := verifyPeerMeta(info, localMeta)
	if verifyErr != nil {
		result.Kind = kind
		result.Error = verifyErr.Error()
		if kind == SyncCheckTimezone || kind == SyncCheckClock || kind == SyncCheckIdentity || kind == SyncCheckVersion {
			node.Ready = false
			database.GetDB().Save(node)
			result.Ready = false
			if wasReady {
				s.addSyncLog(node.Id, "warn", "检查未通过："+result.Error+"；已暂停同步")
			}
		}
		return result, nil
	}
	if node.PeerId != "" && node.PeerId != info.PanelId {
		result.Kind = SyncCheckIdentity
		result.Error = "对端面板标识与记录不一致，可能已重装，请删除后重新添加"
		node.Ready = false
		database.GetDB().Save(node)
		result.Ready = false
		if wasReady {
			s.addSyncLog(node.Id, "warn", "检查未通过："+result.Error+"；已暂停同步")
		}
		return result, nil
	}
	node.PeerId = info.PanelId
	node.Ready = true
	if node.Sort == 0 {
		node.Sort = s.nextSort()
	}
	if err := database.GetDB().Save(node).Error; err != nil {
		return nil, err
	}
	result.Online = true
	result.Ready = true
	if !wasReady {
		s.addSyncLog(node.Id, "info", "检查通过，已就绪")
	}
	return result, nil
}

func resolveSyncTimezone(name string, expectedOffset *int64) (*time.Location, int64, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return nil, 0, common.NewError("时区参数无效")
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return nil, 0, common.NewErrorf("时区 %s 无效", name)
	}
	_, offset := time.Now().In(location).Zone()
	if expectedOffset != nil && int64(offset) != *expectedOffset {
		return nil, 0, common.NewErrorf("时区 %s 当前为 %s，与请求的 %s 不一致，请检查两端系统时区数据",
			name, formatUtcOffset(int64(offset)), formatUtcOffset(*expectedOffset))
	}
	return location, int64(offset), nil
}

// ApplyTimezone is called by a peer panel that holds this panel's login
// credentials; the UTC offset guard keeps both sides on the same clock.
func (s *NodeSyncService) ApplyTimezone(name string, expectedOffset *int64) (*SyncPanelInfo, error) {
	location, _, err := resolveSyncTimezone(name, expectedOffset)
	if err != nil {
		return nil, err
	}
	name = location.String()
	previous, _ := s.settingService.GetTimeLocationSetting()
	if previous != name {
		if err := s.settingService.SetTimeLocation(name); err != nil {
			return nil, err
		}
		logger.Info("node sync: panel time zone changed from ", previous, " to ", name, " by a peer request")
	}
	return s.PanelInfo()
}

func (s *NodeSyncService) AlignNodeTimezone(id int) (*SyncTimezoneResult, error) {
	node, err := s.loadNode(id)
	if err != nil {
		return nil, err
	}
	if node.Passive {
		return nil, common.NewError("被动对端由对方发起同步，不能主动修改对端时区")
	}
	password, err := decryptSyncPassword(node.Password)
	if err != nil {
		return nil, common.NewError("节点密码解密失败，请重新填写密码")
	}
	client, err := newPanelClient(node.Address, syncRequestTimeout)
	if err != nil {
		return nil, err
	}
	if err := client.login(node.Username, password); err != nil {
		return nil, common.NewError("连接对端失败: ", err)
	}
	info, err := client.info()
	if err != nil {
		return nil, err
	}
	if node.PeerId != "" && node.PeerId != info.PanelId {
		return nil, common.NewError("对端面板标识与记录不一致，可能已重装，请删除后重新添加")
	}
	local := panelTimeNow()
	applied := &SyncPanelInfo{}
	err = client.postJSON("/xui/node_sync/timezone/apply", map[string]interface{}{
		"timezone":  local.Setting,
		"utcOffset": local.Offset,
	}, applied)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return nil, common.NewError("对端版本不支持时区对齐，请把两端面板都升级到最新版")
		}
		return nil, err
	}
	logger.Info("node sync: aligned peer ", node.Address, " to time zone ", local.Setting)
	s.addSyncLog(id, "info", fmt.Sprintf("已对齐对端时区：%s → %s（只修改对端面板时区设置）", info.Timezone, local.Setting))
	check, err := s.CheckNode(id)
	if err != nil {
		return nil, err
	}
	return &SyncTimezoneResult{
		NodeId:    id,
		Previous:  info.Timezone,
		Timezone:  local.Setting,
		ZoneName:  local.Zone,
		UtcOffset: local.Offset,
		Check:     check,
	}, nil
}

func syncNodeName(node *model.SyncNode) string {
	title := strings.TrimSpace(node.PeerTitle)
	remark := strings.TrimSpace(node.Remark)
	switch {
	case title != "" && remark != "" && remark != title:
		return title + "（" + remark + "）"
	case title != "":
		return title
	case remark != "":
		return remark
	default:
		return node.Address
	}
}

func formatSyncKindCount(count SyncKindCount) string {
	return fmt.Sprintf("+%d ~%d -%d", count.Added, count.Updated, count.Deleted)
}

func formatSyncKindSummary(inbound, outbound, rule SyncKindCount) string {
	return "入站 " + formatSyncKindCount(inbound) + "；出站 " + formatSyncKindCount(outbound) + "；路由 " + formatSyncKindCount(rule)
}

func syncChangedText(changed bool) string {
	if changed {
		return "有变更"
	}
	return "无变更"
}

func syncReloadText(hotReload bool) string {
	if hotReload {
		return "成功"
	}
	return "待重启"
}

func (s *NodeSyncService) addSyncLog(nodeId int, level, message string) {
	if nodeId <= 0 || strings.TrimSpace(message) == "" {
		return
	}
	db := database.GetDB()
	entry := &model.SyncLog{NodeId: nodeId, Level: level, Message: message, CreatedAt: time.Now().Unix()}
	if err := db.Create(entry).Error; err != nil {
		logger.Warning("node sync: write sync log failed: ", err)
		return
	}
	var cutoff int
	err := db.Model(&model.SyncLog{}).Where("node_id = ?", nodeId).
		Order("id desc").Offset(syncLogLimit-1).Limit(1).Pluck("id", &cutoff).Error
	if err != nil || cutoff <= 0 {
		return
	}
	if err := db.Where("node_id = ? AND id < ?", nodeId, cutoff).Delete(&model.SyncLog{}).Error; err != nil {
		logger.Warning("node sync: prune sync logs failed: ", err)
	}
}

func (s *NodeSyncService) ListLogs(nodeId int) ([]*SyncLogView, error) {
	if _, err := s.loadNode(nodeId); err != nil {
		return nil, err
	}
	entries := make([]*model.SyncLog, 0, 32)
	if err := database.GetDB().Where("node_id = ?", nodeId).Order("id desc").Limit(syncLogLimit).Find(&entries).Error; err != nil {
		return nil, err
	}
	views := make([]*SyncLogView, 0, len(entries))
	for _, entry := range entries {
		views = append(views, &SyncLogView{
			Id:       entry.Id,
			Time:     entry.CreatedAt,
			TimeText: formatPanelTimeSeconds(entry.CreatedAt),
			Level:    entry.Level,
			Message:  entry.Message,
		})
	}
	return views, nil
}

func (s *NodeSyncService) SyncNodes(id int) (*SyncBatchResult, error) {
	nodes := make([]*model.SyncNode, 0, 8)
	if id > 0 {
		node, err := s.loadNode(id)
		if err != nil {
			return nil, err
		}
		if node.Passive {
			return nil, common.NewError("被动对端由对方发起同步，不能主动同步")
		}
		if !node.Ready || node.PeerId == "" {
			return nil, common.NewError("请先检查对端后再同步")
		}
		nodes = append(nodes, node)
	} else if err := database.GetDB().Order("sort asc, id asc").Find(&nodes).Error; err != nil {
		return nil, err
	}
	result := &SyncBatchResult{Items: make([]*SyncPeerResult, 0, len(nodes))}
	for _, node := range nodes {
		if node.Passive || !node.Ready || node.PeerId == "" {
			continue
		}
		item := &SyncPeerResult{Id: node.Id, Remark: node.Remark}
		applied, err := s.syncPeerWithWait(node.Id)
		if err != nil {
			item.Message = err.Error()
			result.Failed++
		} else {
			item.Success = true
			item.Inbounds = applied.Inbounds
			item.Outbounds = applied.Outbounds
			item.RoutingRules = applied.RoutingRules
			item.Changed = applied.Changed
			item.HotReload = applied.HotReload
			item.Warnings = applied.Warnings
			item.LastSync = applied.LastSync
			item.LastSyncText = applied.LastSyncText
			result.Succeeded++
		}
		if fresh, err := s.loadNode(node.Id); err == nil {
			item.LastSync = fresh.LastSync
			item.LastSyncText = formatPanelTime(fresh.LastSync)
		}
		result.Total++
		result.Items = append(result.Items, item)
	}
	if result.Total == 0 {
		return nil, common.NewError("没有可同步的对端，请先添加并检查对端")
	}
	if result.Succeeded == 0 {
		return result, common.NewError(result.Items[0].Message)
	}
	return result, nil
}

func (s *NodeSyncService) syncPeerWithWait(id int) (*SyncPeerResult, error) {
	for attempt := 0; ; attempt++ {
		result, err := s.syncPeerOnce(id)
		if err == nil || attempt >= 20 || !strings.Contains(err.Error(), "正在同步中") {
			return result, err
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (s *NodeSyncService) syncPeerOnce(id int) (*SyncPeerResult, error) {
	if !syncOperationMu.TryLock() {
		return nil, common.NewError("正在同步中，请稍后再试")
	}
	defer syncOperationMu.Unlock()

	node, err := s.loadNode(id)
	if err != nil {
		return nil, err
	}
	if node.Passive {
		return nil, common.NewError("被动对端不能主动同步")
	}
	if !node.Ready || node.PeerId == "" {
		return nil, common.NewError("请先检查对端后再同步")
	}
	start := time.Now()
	result, err := s.syncPeerRound(node)
	if err != nil {
		s.addSyncLog(node.Id, "error", fmt.Sprintf("同步失败（对端：%s）：%v", syncNodeName(node), err))
		return nil, err
	}
	level := "info"
	lines := []string{
		fmt.Sprintf("同步完成（对端：%s）", syncNodeName(node)),
		"变更：" + formatSyncKindSummary(result.Inbounds, result.Outbounds, result.RoutingRules),
		fmt.Sprintf("本端应用：%s；热加载：%s", syncChangedText(result.Changed), syncReloadText(result.HotReload)),
	}
	if len(result.Warnings) > 0 {
		level = "warn"
		lines = append(lines, "警告："+strings.Join(result.Warnings, "；"))
	}
	lines = append(lines, "耗时："+time.Since(start).Round(10*time.Millisecond).String())
	s.addSyncLog(node.Id, level, strings.Join(lines, "\n"))
	return result, nil
}

func (s *NodeSyncService) syncPeerRound(node *model.SyncNode) (*SyncPeerResult, error) {
	password, err := decryptSyncPassword(node.Password)
	if err != nil {
		return nil, common.NewError("节点密码解密失败，请重新编辑节点信息")
	}
	client, err := newPanelClient(node.Address, syncRequestTimeout)
	if err != nil {
		return nil, err
	}
	if err := client.login(node.Username, password); err != nil {
		return nil, err
	}
	info, err := client.info()
	if err != nil {
		return nil, err
	}
	localMeta := panelTimeNow()
	if kind, verifyErr := verifyPeerMeta(info, localMeta); verifyErr != nil {
		if kind == SyncCheckTimezone || kind == SyncCheckClock || kind == SyncCheckIdentity || kind == SyncCheckVersion {
			node.Ready = false
			database.GetDB().Save(node)
		}
		return nil, verifyErr
	}
	if info.PanelId != node.PeerId {
		node.Ready = false
		database.GetDB().Save(node)
		return nil, common.NewError("对端面板标识与记录不一致，请删除后重新添加")
	}

	remotePayload := &SyncPayload{}
	if err := client.getJSON("/xui/node_sync/export", remotePayload); err != nil {
		return nil, err
	}
	if remotePayload.PanelId == "" || remotePayload.PanelId != node.PeerId {
		return nil, common.NewError("对端返回的面板标识不一致")
	}
	if title := strings.TrimSpace(remotePayload.Title); title != "" {
		node.PeerTitle = title
	}
	remoteData := remotePayload.dataset()
	localData, err := s.BuildLocalDataSet()
	if err != nil {
		return nil, err
	}
	localPanelId, err := s.settingService.GetPanelUUID()
	if err != nil {
		return nil, err
	}
	baseData := parseSyncDataSet(node.BaseData)
	merged := mergeSyncData(baseData, localData, remoteData, localPanelId, node.PeerId)

	result := &SyncPeerResult{
		Id:           node.Id,
		Remark:       node.Remark,
		Success:      true,
		Inbounds:     merged.Summary.Inbounds,
		Outbounds:    merged.Summary.Outbounds,
		RoutingRules: merged.Summary.RoutingRules,
		Warnings:     merged.Summary.Warnings,
	}
	changed := !datasetContentEqual(localData, merged.Data)
	result.Changed = changed
	if changed {
		applied, err := s.applyDataSet(merged.Data, firstUserId())
		if err != nil {
			return nil, err
		}
		result.HotReload = applied.HotReload
		result.Warnings = append(result.Warnings, applied.Warnings...)
	} else {
		result.HotReload = true
	}
	if err := (&SyncStateService{}).ApplyMerged(merged.Data.State); err != nil {
		logger.Warning("node sync: persist merged state failed: ", err)
	}

	pushPayload := s.buildSyncPayload(merged.Data, localPanelId)
	remoteResult := &SyncApplyResult{}
	if err := client.postJSON("/xui/node_sync/receive-merge", pushPayload, remoteResult); err != nil {
		return nil, err
	}
	// The peer may have had the only pending change (e.g. a delete that this
	// side already applied locally); surface it in the result.
	if !result.Changed {
		peerChanges := remoteResult.InboundChanges.Added + remoteResult.InboundChanges.Updated + remoteResult.InboundChanges.Deleted +
			remoteResult.OutboundChanges.Added + remoteResult.OutboundChanges.Updated + remoteResult.OutboundChanges.Deleted +
			remoteResult.RuleChanges.Added + remoteResult.RuleChanges.Updated + remoteResult.RuleChanges.Deleted
		if peerChanges > 0 {
			result.Changed = true
			result.Inbounds = remoteResult.InboundChanges
			result.Outbounds = remoteResult.OutboundChanges
			result.RoutingRules = remoteResult.RuleChanges
		}
	}

	node.BaseData = marshalSyncDataSet(merged.Data)
	node.LastSync = time.Now().Unix()
	if err := database.GetDB().Save(node).Error; err != nil {
		return nil, err
	}
	result.LastSync = node.LastSync
	result.LastSyncText = formatPanelTime(node.LastSync)
	autoSyncAttemptMu.Lock()
	autoSyncAttemptAt[node.Id] = node.LastSync
	autoSyncAttemptMu.Unlock()
	return result, nil
}

// ReceiveMerge handles a push from a peer: it merges the incoming merged view
// against its own base and local data, applies the result and records the peer
// as a passive node when it is seen for the first time.
func (s *NodeSyncService) ReceiveMerge(payload *SyncPayload) (*SyncApplyResult, error) {
	if payload == nil {
		return nil, common.NewError("同步数据为空")
	}
	if !syncOperationMu.TryLock() {
		return nil, common.NewError("正在同步中，请稍后再试")
	}
	defer syncOperationMu.Unlock()

	localPanelId, err := s.settingService.GetPanelUUID()
	if err != nil {
		return nil, err
	}
	if payload.PanelId == "" {
		return nil, common.NewError("对端缺少面板标识，请升级对端")
	}
	if payload.PanelId == localPanelId {
		return nil, common.NewError("不能与自身同步")
	}
	peerInfo := &SyncPanelInfo{
		App:        payload.App,
		Version:    payload.Version,
		PanelId:    payload.PanelId,
		Timezone:   payload.Timezone,
		ZoneName:   payload.ZoneName,
		ServerTime: payload.ServerTime,
		UtcOffset:  payload.UtcOffset,
	}
	if _, verifyErr := verifyPeerMeta(peerInfo, panelTimeNow()); verifyErr != nil {
		return nil, verifyErr
	}

	node := &model.SyncNode{}
	err = database.GetDB().Where("peer_id = ?", payload.PanelId).First(node).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		node = &model.SyncNode{
			Remark:    syncPassiveRemark(payload),
			PeerTitle: strings.TrimSpace(payload.Title),
			Address:   sanitizeSyncAddress(payload.Address),
			PeerId:    payload.PanelId,
			Sort:      s.nextSort(),
			Passive:   true,
			Ready:     true,
			CreatedAt: time.Now().Unix(),
		}
		if err := database.GetDB().Create(node).Error; err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else if title := strings.TrimSpace(payload.Title); title != "" {
		node.PeerTitle = title
	}

	localData, err := s.BuildLocalDataSet()
	if err != nil {
		return nil, err
	}
	baseData := parseSyncDataSet(node.BaseData)
	merged := mergeSyncData(baseData, localData, payload.dataset(), localPanelId, payload.PanelId)

	result := &SyncApplyResult{
		Inbounds:        merged.Summary.Inbounds.Added + merged.Summary.Inbounds.Updated + merged.Summary.Inbounds.Deleted,
		Outbounds:       merged.Summary.Outbounds.Added + merged.Summary.Outbounds.Updated + merged.Summary.Outbounds.Deleted,
		RoutingRules:    merged.Summary.RoutingRules.Added + merged.Summary.RoutingRules.Updated + merged.Summary.RoutingRules.Deleted,
		InboundChanges:  merged.Summary.Inbounds,
		OutboundChanges: merged.Summary.Outbounds,
		RuleChanges:     merged.Summary.RoutingRules,
		Warnings:        merged.Summary.Warnings,
	}
	changed := !datasetContentEqual(localData, merged.Data)
	if changed {
		applied, err := s.applyDataSet(merged.Data, firstUserId())
		if err != nil {
			return nil, err
		}
		result.HotReload = applied.HotReload
		result.Warnings = append(result.Warnings, applied.Warnings...)
	} else {
		result.HotReload = true
	}
	if err := (&SyncStateService{}).ApplyMerged(merged.Data.State); err != nil {
		logger.Warning("node sync: persist merged state failed: ", err)
	}
	node.BaseData = marshalSyncDataSet(merged.Data)
	node.LastSync = time.Now().Unix()
	if err := database.GetDB().Save(node).Error; err != nil {
		return nil, err
	}
	level := "info"
	lines := []string{
		fmt.Sprintf("收到对端推送（%s）", syncNodeName(node)),
		"变更：" + formatSyncKindSummary(result.InboundChanges, result.OutboundChanges, result.RuleChanges),
		fmt.Sprintf("本端应用：%s；热加载：%s", syncChangedText(changed), syncReloadText(result.HotReload)),
	}
	if len(result.Warnings) > 0 {
		level = "warn"
		lines = append(lines, "警告："+strings.Join(result.Warnings, "；"))
	}
	s.addSyncLog(node.Id, level, strings.Join(lines, "\n"))
	return result, nil
}

func syncPassiveRemark(payload *SyncPayload) string {
	title := strings.TrimSpace(payload.Title)
	if title != "" {
		return title
	}
	return "被动对端"
}

func sanitizeSyncAddress(address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return ""
	}
	parsed, err := url.Parse(address)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ""
	}
	return strings.TrimRight(address, "/")
}

func (p *SyncPayload) dataset() *SyncDataSet {
	return &SyncDataSet{
		Inbounds:     p.Inbounds,
		Outbounds:    p.Outbounds,
		RoutingRules: p.RoutingRules,
		State:        p.State,
	}
}

func (s *NodeSyncService) BuildExportPayload() (*SyncPayload, error) {
	data, err := s.BuildLocalDataSet()
	if err != nil {
		return nil, err
	}
	panelId, err := s.settingService.GetPanelUUID()
	if err != nil {
		return nil, err
	}
	return s.buildSyncPayload(data, panelId), nil
}

func (s *NodeSyncService) buildSyncPayload(data *SyncDataSet, panelId string) *SyncPayload {
	meta := panelTimeNow()
	title, _ := s.settingService.GetPanelTitle()
	if title == "" {
		title, _ = os.Hostname()
	}
	return &SyncPayload{
		App:          SyncAppID,
		Version:      config.GetVersion(),
		PanelId:      panelId,
		Title:        title,
		Timezone:     meta.Setting,
		ZoneName:     meta.Zone,
		UtcOffset:    meta.Offset,
		ServerTime:   meta.Now,
		Inbounds:     data.Inbounds,
		Outbounds:    data.Outbounds,
		RoutingRules: data.RoutingRules,
		State:        data.State,
	}
}

func (s *NodeSyncService) BuildLocalDataSet() (*SyncDataSet, error) {
	inbounds, err := s.inboundService.GetAllInbounds()
	if err != nil {
		return nil, err
	}
	for _, inbound := range inbounds {
		inbound.ClientStats = nil
	}
	outbounds, err := s.outboundService.GetAllOutbounds()
	if err != nil {
		return nil, err
	}
	rules, err := s.routingRuleService.GetAllRules()
	if err != nil {
		return nil, err
	}
	states, err := (&SyncStateService{}).LoadAll()
	if err != nil {
		return nil, err
	}
	for _, inbound := range inbounds {
		tag := inboundEntryTag(inbound)
		key := SyncStateKey(SyncKindInbound, tag)
		if _, ok := states[key]; !ok {
			states[key] = SyncItemState{}
		}
	}
	for _, outbound := range outbounds {
		key := SyncStateKey(SyncKindOutbound, outboundEntryTag(outbound))
		if _, ok := states[key]; !ok {
			states[key] = SyncItemState{}
		}
	}
	for _, rule := range rules {
		key := SyncStateKey(SyncKindRule, ruleEntryTag(rule))
		if _, ok := states[key]; !ok {
			states[key] = SyncItemState{}
		}
	}
	return &SyncDataSet{Inbounds: inbounds, Outbounds: outbounds, RoutingRules: rules, State: states}, nil
}

func parseSyncDataSet(raw string) *SyncDataSet {
	if strings.TrimSpace(raw) == "" {
		return &SyncDataSet{}
	}
	data := &SyncDataSet{}
	if err := json.Unmarshal([]byte(raw), data); err != nil {
		logger.Warning("node sync: parse stored base failed: ", err)
		return &SyncDataSet{}
	}
	return data
}

func marshalSyncDataSet(data *SyncDataSet) string {
	payload, err := json.Marshal(data)
	if err != nil {
		logger.Warning("node sync: encode base failed: ", err)
		return ""
	}
	return string(payload)
}

func firstUserId() int {
	user := &model.User{}
	if err := database.GetDB().Order("id asc").First(user).Error; err != nil {
		return 0
	}
	return user.Id
}

func (s *NodeSyncService) applyDataSet(data *SyncDataSet, userId int) (*SyncApplyResult, error) {
	db := database.GetDB()

	oldInbounds, err := s.inboundService.GetAllInbounds()
	if err != nil {
		return nil, err
	}
	oldOutbounds, err := s.outboundService.GetAllOutbounds()
	if err != nil {
		return nil, err
	}
	oldInboundByTag := make(map[string]*model.Inbound, len(oldInbounds))
	for _, inbound := range oldInbounds {
		if inbound.Tag != "" {
			oldInboundByTag[inbound.Tag] = inbound
		}
	}
	oldOutboundByTag := make(map[string]*model.Outbound, len(oldOutbounds))
	for _, outbound := range oldOutbounds {
		if outbound.Tag != "" {
			oldOutboundByTag[outbound.Tag] = outbound
		}
	}
	oldRules, err := s.routingRuleService.GetAllRules()
	if err != nil {
		return nil, err
	}

	oldTraffic := make([]*xray.ClientTraffic, 0, 256)
	if err := db.Find(&oldTraffic).Error; err != nil {
		return nil, err
	}
	trafficByEmail := make(map[string]*xray.ClientTraffic, len(oldTraffic))
	for _, traffic := range oldTraffic {
		trafficByEmail[traffic.Email] = traffic
	}

	newInbounds := make([]*model.Inbound, 0, len(data.Inbounds))
	for _, inbound := range data.Inbounds {
		if inbound == nil {
			continue
		}
		cloned := *inbound
		cloned.Id = 0
		cloned.UserId = userId
		cloned.ClientStats = nil
		if cloned.Tag == "" {
			if cloned.Listen == "" || cloned.Listen == "0.0.0.0" || cloned.Listen == "::" || cloned.Listen == "::0" {
				cloned.Tag = fmt.Sprintf("inbound-%v", cloned.Port)
			} else {
				cloned.Tag = fmt.Sprintf("inbound-%v:%v", cloned.Listen, cloned.Port)
			}
		}
		newInbounds = append(newInbounds, &cloned)
	}
	newInboundByTag := make(map[string]*model.Inbound, len(newInbounds))
	for _, inbound := range newInbounds {
		newInboundByTag[inbound.Tag] = inbound
	}

	newOutbounds := make([]*model.Outbound, 0, len(data.Outbounds))
	for i, outbound := range data.Outbounds {
		if outbound == nil {
			continue
		}
		cloned := *outbound
		cloned.Id = 0
		cloned.Sort = i
		newOutbounds = append(newOutbounds, &cloned)
	}
	newOutboundByTag := make(map[string]*model.Outbound, len(newOutbounds))
	for _, outbound := range newOutbounds {
		newOutboundByTag[outbound.Tag] = outbound
	}

	result := &SyncApplyResult{}
	seenEmails := make(map[string]bool)
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("1 = 1").Delete(&xray.ClientTraffic{}).Error; err != nil {
			return err
		}
		if err := tx.Where("1 = 1").Delete(&model.Inbound{}).Error; err != nil {
			return err
		}
		for _, inbound := range newInbounds {
			if err := tx.Create(inbound).Error; err != nil {
				return err
			}
			result.Inbounds++
			clients, _ := s.inboundService.GetClients(inbound)
			for i := range clients {
				client := &clients[i]
				if client.Email == "" || seenEmails[client.Email] {
					continue
				}
				seenEmails[client.Email] = true
				traffic := trafficByEmail[client.Email]
				if traffic == nil {
					traffic = &xray.ClientTraffic{}
				} else {
					traffic.Id = 0
				}
				traffic.InboundId = inbound.Id
				traffic.Email = client.Email
				traffic.Enable = client.Enable
				traffic.Total = client.TotalGB
				traffic.ExpiryTime = client.ExpiryTime
				traffic.Reset = client.Reset
				if err := tx.Create(traffic).Error; err != nil {
					return err
				}
			}
		}

		if err := tx.Where("1 = 1").Delete(&model.Outbound{}).Error; err != nil {
			return err
		}
		for _, outbound := range newOutbounds {
			if err := tx.Create(outbound).Error; err != nil {
				return err
			}
			result.Outbounds++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	rules := make([]*model.RoutingRule, 0, len(data.RoutingRules))
	for _, rule := range data.RoutingRules {
		if rule == nil {
			continue
		}
		cloned := *rule
		rules = append(rules, &cloned)
	}

	needRestart := false
	if err := s.routingRuleService.ReplaceRulesToDB(rules); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("路由规则同步失败: %v", err))
		needRestart = true
	} else {
		result.RoutingRules = len(rules)
	}

	running := p != nil && p.IsRunning()
	api := &xray.XrayAPI{}
	if running {
		if err := api.Init(p.GetAPIAddr()); err != nil {
			result.Warnings = append(result.Warnings, "连接 Xray API 失败: "+err.Error())
			needRestart = true
			running = false
		}
	}
	if running {
		for _, outbound := range newOutbounds {
			old, exists := oldOutboundByTag[outbound.Tag]
			if exists && outboundSyncSignature(old) == outboundSyncSignature(outbound) {
				continue
			}
			if exists {
				if err := api.DelOutbound(old.Tag); err != nil {
					logger.Debug("node sync: delete outbound by api failed:", err)
				}
			}
			payload, err := json.MarshalIndent(outbound.GenXrayOutboundConfig(), "", "  ")
			if err != nil {
				needRestart = true
				result.Warnings = append(result.Warnings, fmt.Sprintf("出站 %s 序列化失败: %v", outbound.Tag, err))
				continue
			}
			if err := api.AddOutbound(payload); err != nil {
				logger.Debug("node sync: add outbound by api failed:", err)
				needRestart = true
				result.Warnings = append(result.Warnings, fmt.Sprintf("出站 %s 热加载失败: %v", outbound.Tag, err))
			}
		}
		for _, outbound := range oldOutbounds {
			if _, ok := newOutboundByTag[outbound.Tag]; ok {
				continue
			}
			if err := api.DelOutbound(outbound.Tag); err != nil {
				logger.Debug("node sync: delete outbound by api failed:", err)
				needRestart = true
			}
		}

		for tag, old := range oldInboundByTag {
			current, ok := newInboundByTag[tag]
			if ok && current.Enable && inboundSyncSignature(old) == inboundSyncSignature(current) {
				continue
			}
			if old.Enable {
				if err := api.DelInbound(tag); err != nil {
					logger.Debug("node sync: delete inbound by api failed:", err)
					if !strings.Contains(strings.ToLower(err.Error()), "not found") {
						needRestart = true
					}
				}
			}
		}
		for _, inbound := range newInbounds {
			if !inbound.Enable {
				continue
			}
			old, exists := oldInboundByTag[inbound.Tag]
			if exists && inboundSyncSignature(old) == inboundSyncSignature(inbound) {
				continue
			}
			payload, err := json.MarshalIndent(inbound.GenXrayInboundConfig(), "", "  ")
			if err != nil {
				needRestart = true
				result.Warnings = append(result.Warnings, fmt.Sprintf("入站 %s 序列化失败: %v", inbound.Tag, err))
				continue
			}
			if err := api.AddInbound(payload); err != nil {
				logger.Debug("node sync: add inbound by api failed:", err)
				needRestart = true
				result.Warnings = append(result.Warnings, fmt.Sprintf("入站 %s 热加载失败: %v", inbound.Tag, err))
			}
		}
		for _, rule := range oldRules {
			tag := rule.Tag
			if tag == "" {
				tag = fmt.Sprintf("rule-%d", rule.Id)
			}
			if err := api.DelRule(tag); err != nil {
				logger.Debug("node sync: delete routing rule by api failed:", err)
				if !strings.Contains(strings.ToLower(err.Error()), "not found") {
					needRestart = true
				}
			}
		}
		for _, rule := range rules {
			payload, err := rule.RuleJSON()
			if err != nil {
				needRestart = true
				result.Warnings = append(result.Warnings, fmt.Sprintf("路由规则 %s 序列化失败: %v", rule.Tag, err))
				continue
			}
			if err := api.AddRule(payload, true); err != nil {
				logger.Debug("node sync: add routing rule by api failed:", err)
				needRestart = true
				result.Warnings = append(result.Warnings, fmt.Sprintf("路由规则 %s 热加载失败: %v", rule.Tag, err))
			}
		}
		api.Close()
	}

	s.syncIpLimitStoreForApply(oldInbounds, newInbounds)
	if needRestart {
		(&XrayService{}).SetToNeedRestart()
	}
	result.HotReload = running && !needRestart
	return result, nil
}

func inboundSyncSignature(inbound *model.Inbound) string {
	return fmt.Sprintf("%t\x00%s\x00%d\x00%s\x00%s\x00%s\x00%s",
		inbound.Enable, inbound.Listen, inbound.Port, inbound.Protocol,
		inbound.Settings, inbound.StreamSettings, inbound.Sniffing)
}

func outboundSyncSignature(outbound *model.Outbound) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s", outbound.Tag,
		outbound.SendThrough, outbound.Protocol, outbound.Settings,
		outbound.StreamSettings, outbound.ProxySettings, outbound.Mux, outbound.TargetStrategy)
}

func (s *NodeSyncService) syncIpLimitStoreForApply(oldInbounds, newInbounds []*model.Inbound) {
	oldEmails := make(map[string]bool)
	for _, inbound := range oldInbounds {
		clients, _ := s.inboundService.GetClients(inbound)
		for _, client := range clients {
			if client.Email != "" {
				oldEmails[client.Email] = true
			}
		}
	}
	updates := make([]IpLimitClientUpdate, 0)
	newEmails := make(map[string]bool)
	for _, inbound := range newInbounds {
		clients, _ := s.inboundService.GetClients(inbound)
		for _, client := range clients {
			if client.Email != "" {
				newEmails[client.Email] = true
			}
		}
		updates = append(updates, ipLimitUpdatesFromClients(inbound, clients)...)
	}
	removed := make([]string, 0)
	for email := range oldEmails {
		if !newEmails[email] {
			removed = append(removed, email)
		}
	}
	s.inboundService.syncIpLimitStore(updates, removed)
}

func (s *NodeSyncService) AutoInterval() (int, error) {
	return s.settingService.GetSyncAutoInterval()
}

func (s *NodeSyncService) SetAutoSync(interval int) error {
	if interval != 0 && !autoSyncIntervals[interval] {
		return common.NewError("自动同步频率无效")
	}
	if interval > 0 {
		var count int64
		if err := database.GetDB().Model(&model.SyncNode{}).Where("passive = ?", false).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return common.NewError("请先添加并检查对端")
		}
	}
	if err := s.settingService.SetSyncAutoInterval(interval); err != nil {
		return err
	}
	autoSyncAttemptMu.Lock()
	autoSyncAttemptAt = map[int]int64{}
	autoSyncAttemptMu.Unlock()
	return nil
}

// AutoSyncTick syncs every ready peer that is due, in configured order and one
// peer at a time. Each round pulls the peer dataset, merges it locally and
// pushes the merged view back.
func (s *NodeSyncService) AutoSyncTick() {
	if !autoSyncMu.TryLock() {
		return
	}
	defer autoSyncMu.Unlock()

	interval, err := s.settingService.GetSyncAutoInterval()
	if err != nil || interval <= 0 {
		return
	}

	nodes := make([]*model.SyncNode, 0, 8)
	if err := database.GetDB().Order("sort asc, id asc").Find(&nodes).Error; err != nil {
		return
	}
	if len(nodes) == 0 {
		if err := s.settingService.SetSyncAutoInterval(0); err != nil {
			logger.Warning("node sync: disable auto sync failed: ", err)
		}
		return
	}

	now := time.Now().Unix()
	for _, node := range nodes {
		if node.Passive || !node.Ready || node.PeerId == "" {
			continue
		}
		autoSyncAttemptMu.Lock()
		last := autoSyncAttemptAt[node.Id]
		due := last == 0 || now-last >= int64(interval)*60
		if due {
			autoSyncAttemptAt[node.Id] = now
		}
		autoSyncAttemptMu.Unlock()
		if !due {
			continue
		}
		if _, err := s.syncPeerOnce(node.Id); err != nil {
			logger.Warning("node sync: auto sync peer ", node.Id, " failed: ", err)
		}
	}
}

func (s *NodeSyncService) loadNode(id int) (*model.SyncNode, error) {
	node := &model.SyncNode{}
	if err := database.GetDB().First(node, id).Error; err != nil {
		return nil, common.NewError("节点不存在")
	}
	return node, nil
}

func canonicalRule(raw string) string {
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return raw
	}
	delete(obj, "ruleTag")
	normalized, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return string(normalized)
}

func syncSecretKey() ([]byte, error) {
	secret, err := (&SettingService{}).GetSecret()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(secret)
	return sum[:], nil
}

func encryptSyncPassword(password string) (string, error) {
	key, err := syncSecretKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	encrypted := gcm.Seal(nonce, nonce, []byte(password), nil)
	return base64.StdEncoding.EncodeToString(encrypted), nil
}

func decryptSyncPassword(value string) (string, error) {
	key, err := syncSecretKey()
	if err != nil {
		return "", err
	}
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", common.NewError("invalid encrypted password")
	}
	plain, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

type remoteEnvelope struct {
	Success bool            `json:"success"`
	Msg     string          `json:"msg"`
	Obj     json.RawMessage `json:"obj"`
}

type syncPanelAuthError struct {
	msg string
}

func (e *syncPanelAuthError) Error() string {
	return "登录失败: " + e.msg
}

type panelClient struct {
	base   string
	client *http.Client
}

func verifyRemotePanel(address, username, password string) (*panelClient, *SyncPanelInfo, error) {
	address = strings.TrimSpace(address)
	candidates := []string{address}
	if !strings.Contains(address, "://") {
		candidates = []string{"https://" + address, "http://" + address}
	}
	var lastErr error
	for _, candidate := range candidates {
		client, err := newPanelClient(candidate, syncRequestTimeout)
		if err != nil {
			lastErr = err
			continue
		}
		if err := client.login(username, password); err != nil {
			lastErr = err
			continue
		}
		info, err := client.info()
		if err != nil {
			lastErr = err
			continue
		}
		return client, info, nil
	}
	if lastErr == nil {
		lastErr = common.NewError("无法连接目标面板")
	}
	return nil, nil, common.NewError("连接验证失败: ", lastErr)
}

func newPanelClient(base string, timeout time.Duration) (*panelClient, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, common.NewError("面板地址无效")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, common.NewError("面板地址仅支持 http/https")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{
		Jar:       jar,
		Timeout:   timeout,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	return &panelClient{base: base, client: client}, nil
}

func (p *panelClient) login(username, password string) error {
	envelope := &remoteEnvelope{}
	err := p.request(http.MethodPost, "/login", map[string]string{
		"username": username,
		"password": password,
	}, envelope)
	if err != nil {
		return err
	}
	if !envelope.Success {
		return &syncPanelAuthError{msg: envelope.Msg}
	}
	return nil
}

func (p *panelClient) info() (*SyncPanelInfo, error) {
	envelope := &remoteEnvelope{}
	if err := p.request(http.MethodGet, "/xui/node_sync/info", nil, envelope); err != nil {
		return nil, common.NewError("目标面板不是 XUI-GO 面板或版本过低")
	}
	if !envelope.Success {
		return nil, common.NewError("读取面板信息失败: " + envelope.Msg)
	}
	info := &SyncPanelInfo{}
	if err := json.Unmarshal(envelope.Obj, info); err != nil {
		return nil, common.NewError("目标面板不是 XUI-GO 面板或版本过低")
	}
	if info.App != SyncAppID {
		return nil, common.NewError("目标面板不是 XUI-GO 面板，不支持节点同步")
	}
	return info, nil
}

func (p *panelClient) getJSON(path string, out interface{}) error {
	envelope := &remoteEnvelope{}
	if err := p.request(http.MethodGet, path, nil, envelope); err != nil {
		return err
	}
	if !envelope.Success {
		return common.NewError("请求失败: " + envelope.Msg)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Obj, out)
}

func (p *panelClient) postJSON(path string, in interface{}, out interface{}) error {
	envelope := &remoteEnvelope{}
	if err := p.request(http.MethodPost, path, in, envelope); err != nil {
		return err
	}
	if !envelope.Success {
		return common.NewError("请求失败: " + envelope.Msg)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Obj, out)
}

func (p *panelClient) request(method, path string, in interface{}, out *remoteEnvelope) error {
	var body io.Reader
	if in != nil {
		payload, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(payload))
	}
	req, err := http.NewRequest(method, p.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return common.NewErrorf("目标面板返回异常 (HTTP %d)", resp.StatusCode)
	}
	return nil
}
