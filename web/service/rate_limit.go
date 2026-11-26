package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"

	"github.com/alireza0/x-ui/database"
	"github.com/alireza0/x-ui/database/model"
	"github.com/alireza0/x-ui/logger"
	"github.com/alireza0/x-ui/ratelimit"
)

type RateLimitService struct {
	settingService SettingService
}

var rateLimitState struct {
	mu   sync.Mutex
	spec string
}

// Reconcile makes the kernel rules match the inbound rate limits. The ruleset
// lives in a panel-specific nftables table, so Xray and its connections are
// never touched by a limit change.
func (s *RateLimitService) Reconcile() error {
	if !ratelimit.Available() {
		return nil
	}
	inbounds := make([]model.Inbound, 0, 16)
	err := database.GetDB().Model(model.Inbound{}).
		Select("port", "speed_up", "speed_down").
		Where("enable = ?", true).
		Order("port").
		Find(&inbounds).Error
	if err != nil {
		return err
	}
	rules := make([]ratelimit.Rule, 0, len(inbounds))
	for _, inbound := range inbounds {
		if inbound.Port <= 0 || (inbound.SpeedUp <= 0 && inbound.SpeedDown <= 0) {
			continue
		}
		rules = append(rules, ratelimit.Rule{
			Port:     inbound.Port,
			UpKbps:   inbound.SpeedUp,
			DownKbps: inbound.SpeedDown,
		})
	}
	payload, err := json.Marshal(rules)
	if err != nil {
		return err
	}

	panelUUID, err := s.settingService.GetPanelUUID()
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(panelUUID))
	tableName := "xui_rl_" + hex.EncodeToString(sum[:4])

	rateLimitState.mu.Lock()
	defer rateLimitState.mu.Unlock()
	if string(payload) == rateLimitState.spec {
		return nil
	}
	if err := ratelimit.Apply(tableName, rules); err != nil {
		return err
	}
	rateLimitState.spec = string(payload)
	return nil
}

func reconcileRateLimit() {
	if err := (&RateLimitService{}).Reconcile(); err != nil {
		logger.Warning("rate limit reconcile failed:", err)
	}
}
