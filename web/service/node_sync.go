package service

import (
	"bytes"
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
	"mime/multipart"
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
	SyncRoleSlave = "slave"
	SyncAppID     = "xui-go"

	SyncCheckAddress = "address"
	SyncCheckAuth    = "auth"
	SyncCheckPanel   = "panel"
	SyncCheckRole    = "role"

	syncRequestTimeout = 30 * time.Second
	syncInitTimeout    = 180 * time.Second
)

var (
	syncOperationMu   sync.Mutex
	autoSyncMu        sync.Mutex
	autoSyncAttemptMu sync.Mutex
	autoSyncAttemptAt = map[int]int64{}

	autoSyncIntervals = map[int]bool{5: true, 10: true, 30: true, 60: true, 1440: true}
)

var syncAccessSettingKeys = []string{
	"webListen",
	"webDomain",
	"webPort",
	"webCertFile",
	"webKeyFile",
	"webBasePath",
	"secret",
}

type SyncRoleInfo struct {
	App        string `json:"app"`
	Role       string `json:"role"`
	MasterName string `json:"masterName"`
	NodeCount  int64  `json:"nodeCount"`
	Version    string `json:"version"`
	Title      string `json:"title"`
}

type SyncNodeView struct {
	Id        int    `json:"id"`
	Remark    string `json:"remark"`
	Address   string `json:"address"`
	Username  string `json:"username"`
	Ready     bool   `json:"ready"`
	LastSync  int64  `json:"lastSync"`
	CreatedAt int64  `json:"createdAt"`
}

type SyncNodeCheck struct {
	Online  bool   `json:"online"`
	Version string `json:"version"`
	Role    string `json:"role"`
	Kind    string `json:"kind"`
	Error   string `json:"error"`
}

type SyncDataSet struct {
	Inbounds     []*model.Inbound     `json:"inbounds"`
	Outbounds    []*model.Outbound    `json:"outbounds"`
	RoutingRules []*model.RoutingRule `json:"routingRules"`
}

type SyncApplyResult struct {
	Inbounds     int      `json:"inbounds"`
	Outbounds    int      `json:"outbounds"`
	RoutingRules int      `json:"routingRules"`
	HotReload    bool     `json:"hotReload"`
	Warnings     []string `json:"warnings"`
}

type SyncMergeResult struct {
	Inbounds  int      `json:"inbounds"`
	Outbounds int      `json:"outbounds"`
	Rules     int      `json:"rules"`
	Skipped   int      `json:"skipped"`
	Warnings  []string `json:"warnings"`
}

type NodeSyncService struct {
	settingService     SettingService
	inboundService     InboundService
	outboundService    OutboundService
	routingRuleService RoutingRuleService
}

func (s *NodeSyncService) RoleInfo() (*SyncRoleInfo, error) {
	role, err := s.settingService.GetSyncRole()
	if err != nil {
		return nil, err
	}
	masterName, err := s.settingService.GetSyncMasterName()
	if err != nil {
		return nil, err
	}
	var count int64
	if err := database.GetDB().Model(&model.SyncNode{}).Count(&count).Error; err != nil {
		return nil, err
	}
	title, _ := s.settingService.GetPanelTitle()
	return &SyncRoleInfo{
		App:        SyncAppID,
		Role:       role,
		MasterName: masterName,
		NodeCount:  count,
		Version:    config.GetVersion(),
		Title:      title,
	}, nil
}

func (s *NodeSyncService) IsSlave() (bool, error) {
	role, err := s.settingService.GetSyncRole()
	if err != nil {
		return false, err
	}
	return role == SyncRoleSlave, nil
}

func (s *NodeSyncService) ListNodes() ([]*SyncNodeView, error) {
	nodes := make([]*model.SyncNode, 0, 8)
	if err := database.GetDB().Order("id asc").Find(&nodes).Error; err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}
	views := make([]*SyncNodeView, 0, len(nodes))
	for _, node := range nodes {
		views = append(views, &SyncNodeView{
			Id:        node.Id,
			Remark:    node.Remark,
			Address:   node.Address,
			Username:  node.Username,
			Ready:     node.Ready,
			LastSync:  node.LastSync,
			CreatedAt: node.CreatedAt,
		})
	}
	return views, nil
}

func (s *NodeSyncService) CheckNode(id int) (*SyncNodeCheck, error) {
	node, err := s.loadNode(id)
	if err != nil {
		return nil, err
	}
	result := &SyncNodeCheck{}
	password, err := decryptSyncPassword(node.Password)
	if err != nil {
		result.Kind = SyncCheckAuth
		result.Error = "节点密码解密失败，请重新编辑节点信息"
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
		if errors.As(err, &authErr) {
			result.Kind = SyncCheckAuth
		} else {
			result.Kind = SyncCheckAddress
		}
		result.Error = err.Error()
		return result, nil
	}
	info, err := client.info()
	if err != nil {
		result.Kind = SyncCheckPanel
		result.Error = err.Error()
		return result, nil
	}
	result.Online = true
	result.Version = info.Version
	result.Role = info.Role
	if node.Ready && info.Role != SyncRoleSlave {
		result.Online = false
		result.Kind = SyncCheckRole
		result.Error = "目标面板已不是副面板，请重新执行同步初始化"
	}
	return result, nil
}

func (s *NodeSyncService) AddNode(remark, address, username, password string) (*model.SyncNode, error) {
	if slave, err := s.IsSlave(); err != nil {
		return nil, err
	} else if slave {
		return nil, common.NewError("本面板是副面板，不能添加其他面板")
	}
	address = strings.TrimSpace(address)
	username = strings.TrimSpace(username)
	if address == "" || username == "" || password == "" {
		return nil, common.NewError("面板地址、用户名和密码不能为空")
	}
	client, info, err := verifyRemotePanel(address, username, password)
	if err != nil {
		return nil, err
	}
	if info.Role == SyncRoleSlave {
		return nil, common.NewError("目标面板已经是副面板，不能重复添加")
	}
	if info.NodeCount > 0 {
		return nil, common.NewError("目标面板已经是主面板，不能作为副面板")
	}
	encrypted, err := encryptSyncPassword(password)
	if err != nil {
		return nil, err
	}
	node := &model.SyncNode{
		Remark:    strings.TrimSpace(remark),
		Address:   client.base,
		Username:  username,
		Password:  encrypted,
		CreatedAt: time.Now().Unix(),
	}
	if err := database.GetDB().Create(node).Error; err != nil {
		return nil, err
	}
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
	addressChanged := effectiveAddress != node.Address
	credentialsChanged := addressChanged || effectiveUser != node.Username || password != ""

	if credentialsChanged {
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
		if info.NodeCount > 0 {
			return nil, common.NewError("目标面板已经是主面板，不能作为副面板")
		}
		node.Address = client.base
		node.Username = effectiveUser
		if password != "" {
			encrypted, err := encryptSyncPassword(password)
			if err != nil {
				return nil, err
			}
			node.Password = encrypted
		}
		if addressChanged {
			node.Ready = false
			node.LastSync = 0
		}
	}

	if err := database.GetDB().Save(node).Error; err != nil {
		return nil, err
	}
	return node, nil
}

func (s *NodeSyncService) DeleteNode(id int) (string, error) {
	node, err := s.loadNode(id)
	if err != nil {
		return "", err
	}
	warning := ""
	if password, err := decryptSyncPassword(node.Password); err == nil {
		if client, err := newPanelClient(node.Address, syncRequestTimeout); err == nil {
			if err := client.login(node.Username, password); err == nil {
				if err := client.release(); err != nil {
					warning = "已删除节点，但通知副面板解除副属性失败: " + err.Error()
				}
			} else {
				warning = "已删除节点，但无法登录副面板解除副属性: " + err.Error()
			}
		}
	}
	if err := database.GetDB().Delete(&model.SyncNode{}, id).Error; err != nil {
		return "", err
	}
	autoSyncAttemptMu.Lock()
	delete(autoSyncAttemptAt, id)
	autoSyncAttemptMu.Unlock()
	var count int64
	if err := database.GetDB().Model(&model.SyncNode{}).Count(&count).Error; err == nil && count == 0 {
		if err := s.settingService.SetSyncAutoInterval(0); err != nil {
			logger.Warning("node sync: disable auto sync failed: ", err)
		}
	}
	return warning, nil
}

func (s *NodeSyncService) InitNode(id int) (*model.SyncNode, error) {
	if slave, err := s.IsSlave(); err != nil {
		return nil, err
	} else if slave {
		return nil, common.NewError("本面板是副面板，不能同步其他面板")
	}
	if !syncOperationMu.TryLock() {
		return nil, common.NewError("正在同步中，请稍后再试")
	}
	defer syncOperationMu.Unlock()
	node, err := s.loadNode(id)
	if err != nil {
		return nil, err
	}
	password, err := decryptSyncPassword(node.Password)
	if err != nil {
		return nil, common.NewError("节点密码解密失败，请重新编辑节点信息")
	}
	client, err := newPanelClient(node.Address, syncInitTimeout)
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
	if info.NodeCount > 0 {
		return nil, common.NewError("目标面板已经是主面板，不能作为副面板")
	}

	snapshotPath, cleanup, err := database.CreateSnapshot()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	masterName, _ := s.settingService.GetPanelTitle()
	if masterName == "" {
		masterName, _ = os.Hostname()
	}
	if err := client.uploadInit(snapshotPath, masterName); err != nil {
		return nil, err
	}
	node.Ready = true
	node.LastSync = time.Now().Unix()
	if err := database.GetDB().Save(node).Error; err != nil {
		return nil, err
	}
	return node, nil
}

func (s *NodeSyncService) PushNode(id int) (*SyncApplyResult, error) {
	if !syncOperationMu.TryLock() {
		return nil, common.NewError("正在同步中，请稍后再试")
	}
	defer syncOperationMu.Unlock()
	node, err := s.loadNode(id)
	if err != nil {
		return nil, err
	}
	if !node.Ready {
		return nil, common.NewError("请先完成同步初始化")
	}
	password, err := decryptSyncPassword(node.Password)
	if err != nil {
		return nil, common.NewError("节点密码解密失败，请重新编辑节点信息")
	}
	client, err := newPanelClient(node.Address, syncInitTimeout)
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
	if info.Role != SyncRoleSlave {
		return nil, common.NewError("目标面板不是副面板，请先执行同步初始化")
	}
	data, err := s.BuildLocalDataSet()
	if err != nil {
		return nil, err
	}
	result := &SyncApplyResult{}
	if err := client.postJSON("/xui/node_sync/receive-apply", data, result); err != nil {
		return nil, err
	}
	node.LastSync = time.Now().Unix()
	database.GetDB().Save(node)
	autoSyncAttemptMu.Lock()
	autoSyncAttemptAt[node.Id] = node.LastSync
	autoSyncAttemptMu.Unlock()
	return result, nil
}

func (s *NodeSyncService) MergeNode(id int, userId int) (*SyncMergeResult, error) {
	if !syncOperationMu.TryLock() {
		return nil, common.NewError("正在同步中，请稍后再试")
	}
	defer syncOperationMu.Unlock()
	node, err := s.loadNode(id)
	if err != nil {
		return nil, err
	}
	password, err := decryptSyncPassword(node.Password)
	if err != nil {
		return nil, common.NewError("节点密码解密失败，请重新编辑节点信息")
	}
	client, err := newPanelClient(node.Address, syncInitTimeout)
	if err != nil {
		return nil, err
	}
	if err := client.login(node.Username, password); err != nil {
		return nil, err
	}
	remote := &SyncDataSet{}
	if err := client.getJSON("/xui/node_sync/export", remote); err != nil {
		return nil, err
	}

	result := &SyncMergeResult{}
	needRestart := false
	inbounds, err := s.inboundService.GetAllInbounds()
	if err != nil {
		return nil, err
	}
	usedPorts := make(map[int]bool, len(inbounds))
	for _, inbound := range inbounds {
		usedPorts[inbound.Port] = true
	}
	for _, inbound := range remote.Inbounds {
		if inbound == nil || inbound.Port <= 0 {
			continue
		}
		if usedPorts[inbound.Port] {
			result.Skipped++
			continue
		}
		merged := *inbound
		merged.Id = 0
		merged.UserId = userId
		merged.ClientStats = nil
		if merged.Tag == "" {
			merged.Tag = fmt.Sprintf("inbound-%d", merged.Port)
		}
		_, restart, err := s.inboundService.AddInbound(&merged)
		if err != nil {
			result.Skipped++
			result.Warnings = append(result.Warnings, fmt.Sprintf("入站 %s: %v", merged.Remark, err))
			continue
		}
		needRestart = needRestart || restart
		usedPorts[merged.Port] = true
		result.Inbounds++
	}

	outbounds, err := s.outboundService.GetAllOutbounds()
	if err != nil {
		return nil, err
	}
	usedTags := make(map[string]bool, len(outbounds))
	for _, outbound := range outbounds {
		usedTags[outbound.Tag] = true
	}
	for _, outbound := range remote.Outbounds {
		if outbound == nil || outbound.Tag == "" {
			continue
		}
		if usedTags[outbound.Tag] {
			result.Skipped++
			continue
		}
		merged := *outbound
		merged.Id = 0
		_, restart, err := s.outboundService.AddOutbound(&merged)
		if err != nil {
			result.Skipped++
			result.Warnings = append(result.Warnings, fmt.Sprintf("出站 %s: %v", merged.Tag, err))
			continue
		}
		needRestart = needRestart || restart
		usedTags[merged.Tag] = true
		result.Outbounds++
	}

	rules, err := s.routingRuleService.GetAllRules()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(rules))
	ruleTags := make(map[string]bool, len(rules))
	for _, rule := range rules {
		seen[canonicalRule(rule.RawJson)] = true
		ruleTags[rule.Tag] = true
	}
	mergedRules := make([]*model.RoutingRule, 0, len(rules)+len(remote.RoutingRules))
	mergedRules = append(mergedRules, rules...)
	for _, rule := range remote.RoutingRules {
		if rule == nil {
			continue
		}
		key := canonicalRule(rule.RawJson)
		if seen[key] {
			result.Skipped++
			continue
		}
		tag := rule.Tag
		if tag == "" || ruleTags[tag] {
			for i := 1; ; i++ {
				candidate := fmt.Sprintf("sync-%d", i)
				if !ruleTags[candidate] {
					tag = candidate
					break
				}
			}
		}
		mergedRules = append(mergedRules, &model.RoutingRule{Tag: tag, RawJson: rule.RawJson})
		seen[key] = true
		ruleTags[tag] = true
		result.Rules++
	}
	if result.Rules > 0 {
		restart, err := s.routingRuleService.SaveAllRules(mergedRules)
		needRestart = needRestart || restart
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("路由规则合并失败: %v", err))
		}
	}
	if needRestart {
		(&XrayService{}).SetToNeedRestart()
	}

	node.LastSync = time.Now().Unix()
	database.GetDB().Save(node)
	return result, nil
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
	return &SyncDataSet{Inbounds: inbounds, Outbounds: outbounds, RoutingRules: rules}, nil
}

// ReceiveInit replaces the local database with the master snapshot. Panel
// access settings and the admin account are kept so the panel stays reachable
// and the master can keep logging in for later syncs.
func (s *NodeSyncService) ReceiveInit(file multipart.File, masterName string) error {
	if !syncOperationMu.TryLock() {
		return common.NewError("正在同步中，请稍后再试")
	}
	defer syncOperationMu.Unlock()
	var count int64
	if err := database.GetDB().Model(&model.SyncNode{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return common.NewError("本面板已有副面板，不能作为副面板被接管")
	}

	db := database.GetDB()
	users := make([]model.User, 0, 4)
	if err := db.Find(&users).Error; err != nil {
		return err
	}
	preserved := make(map[string]string, len(syncAccessSettingKeys))
	preserved["webListen"], _ = s.settingService.GetListen()
	preserved["webDomain"], _ = s.settingService.GetWebDomain()
	if port, err := s.settingService.GetPort(); err == nil {
		preserved["webPort"] = strconv.Itoa(port)
	}
	preserved["webCertFile"], _ = s.settingService.GetCertFile()
	preserved["webKeyFile"], _ = s.settingService.GetKeyFile()
	preserved["webBasePath"], _ = s.settingService.GetBasePath()
	if secret, err := s.settingService.GetSecret(); err == nil {
		preserved["secret"] = string(secret)
	}

	if err := (&ServerService{}).ImportDB(file); err != nil {
		return err
	}

	db = database.GetDB()
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("1 = 1").Delete(&model.SyncNode{}).Error; err != nil {
			return err
		}
		if err := tx.Where("1 = 1").Delete(&model.User{}).Error; err != nil {
			return err
		}
		for i := range users {
			users[i].Id = 0
			if err := tx.Create(&users[i]).Error; err != nil {
				return err
			}
		}
		if len(users) > 0 {
			if err := tx.Model(&model.Inbound{}).Where("1 = 1").Update("user_id", users[0].Id).Error; err != nil {
				return err
			}
		}
		for key, value := range preserved {
			if err := upsertSyncSetting(tx, key, value); err != nil {
				return err
			}
		}
		if err := upsertSyncSetting(tx, "syncRole", SyncRoleSlave); err != nil {
			return err
		}
		if err := upsertSyncSetting(tx, "syncMasterName", masterName); err != nil {
			return err
		}
		if err := upsertSyncSetting(tx, "syncAutoInterval", "0"); err != nil {
			return err
		}
		return nil
	})
}

// ReceiveApply replaces inbounds, outbounds and routing rules with the master
// copies while keeping local traffic counters for matching client emails.
// Runtime changes are pushed through the Xray gRPC API; xray keeps serving and
// only diffs are touched, so an unchanged auto-sync run does not break sessions.
func (s *NodeSyncService) ReceiveApply(data *SyncDataSet) (*SyncApplyResult, error) {
	if slave, err := s.IsSlave(); err != nil {
		return nil, err
	} else if !slave {
		return nil, common.NewError("本面板不是副面板，不能接收主面板下发")
	}
	if data == nil {
		return nil, common.NewError("同步数据为空")
	}
	if !syncOperationMu.TryLock() {
		return nil, common.NewError("正在同步中，请稍后再试")
	}
	defer syncOperationMu.Unlock()

	db := database.GetDB()
	var user model.User
	if err := db.Order("id asc").First(&user).Error; err != nil {
		return nil, err
	}

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
		cloned.UserId = user.Id
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
	restart, err := s.routingRuleService.SaveAllRules(rules)
	needRestart = needRestart || restart
	if err != nil {
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
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s", outbound.Tag,
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

func (s *NodeSyncService) ReleaseSlave() error {
	if err := s.settingService.SetSyncRole(""); err != nil {
		return err
	}
	return s.settingService.SetSyncMasterName("")
}

func (s *NodeSyncService) AutoInterval() (int, error) {
	return s.settingService.GetSyncAutoInterval()
}

func (s *NodeSyncService) SetAutoSync(interval int) error {
	if slave, err := s.IsSlave(); err != nil {
		return err
	} else if slave {
		return common.NewError("本面板是副面板，不能开启自动同步")
	}
	if interval != 0 && !autoSyncIntervals[interval] {
		return common.NewError("自动同步频率无效")
	}
	if interval > 0 {
		var count int64
		if err := database.GetDB().Model(&model.SyncNode{}).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return common.NewError("请先添加副面板")
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

// AutoSyncTick pushes the master dataset to every ready slave that is due.
// It is called by a 30s cron job; the per-node attempt timestamp provides the
// configured minute interval without persisting extra state.
func (s *NodeSyncService) AutoSyncTick() {
	if !autoSyncMu.TryLock() {
		return
	}
	defer autoSyncMu.Unlock()

	if slave, err := s.IsSlave(); err != nil || slave {
		return
	}
	interval, err := s.settingService.GetSyncAutoInterval()
	if err != nil || interval <= 0 {
		return
	}

	nodes := make([]*model.SyncNode, 0, 8)
	if err := database.GetDB().Order("id asc").Find(&nodes).Error; err != nil {
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
		if !node.Ready {
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
		if _, err := s.PushNode(node.Id); err != nil {
			logger.Warning("node sync: auto sync panel ", node.Id, " failed: ", err)
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

func upsertSyncSetting(tx *gorm.DB, key, value string) error {
	var setting model.Setting
	err := tx.Where("key = ?", key).First(&setting).Error
	if err == gorm.ErrRecordNotFound {
		return tx.Create(&model.Setting{Key: key, Value: value}).Error
	}
	if err != nil {
		return err
	}
	setting.Value = value
	return tx.Save(&setting).Error
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

func verifyRemotePanel(address, username, password string) (*panelClient, *SyncRoleInfo, error) {
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

func (p *panelClient) info() (*SyncRoleInfo, error) {
	envelope := &remoteEnvelope{}
	if err := p.request(http.MethodGet, "/xui/node_sync/info", nil, envelope); err != nil {
		return nil, common.NewError("目标面板不是 XUI-GO 面板或版本过低")
	}
	if !envelope.Success {
		return nil, common.NewError("读取面板信息失败: " + envelope.Msg)
	}
	info := &SyncRoleInfo{}
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

func (p *panelClient) uploadInit(snapshotPath, masterName string) error {
	file, err := os.Open(snapshotPath)
	if err != nil {
		return err
	}
	defer file.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("masterName", masterName); err != nil {
		return err
	}
	part, err := writer.CreateFormFile("db", "x-ui.db")
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, file); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, p.base+"/xui/node_sync/receive-init", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := p.client.Do(req)
	if err != nil {
		return common.NewError("同步初始化失败: ", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	envelope := &remoteEnvelope{}
	if err := json.Unmarshal(payload, envelope); err != nil {
		return common.NewErrorf("同步初始化失败: HTTP %d", resp.StatusCode)
	}
	if !envelope.Success {
		return common.NewError("同步初始化失败: " + envelope.Msg)
	}
	return nil
}

func (p *panelClient) release() error {
	envelope := &remoteEnvelope{}
	if err := p.request(http.MethodPost, "/xui/node_sync/release", nil, envelope); err != nil {
		return err
	}
	if !envelope.Success {
		return common.NewError(envelope.Msg)
	}
	return nil
}

func (p *panelClient) request(method, path string, in interface{}, out *remoteEnvelope) error {
	var body io.Reader
	if in != nil {
		payload, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
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
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return common.NewErrorf("目标面板返回异常 (HTTP %d)", resp.StatusCode)
	}
	return nil
}
