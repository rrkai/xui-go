package controller

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"strings"

	"github.com/alireza0/x-ui/database/model"
	"github.com/alireza0/x-ui/util/common"
	"github.com/alireza0/x-ui/web/network"
	"github.com/alireza0/x-ui/web/service"
	"github.com/alireza0/x-ui/web/session"

	"github.com/gin-gonic/gin"
)

const (
	quickPortMin       = 10000
	quickPortMax       = 20000
	quickSniffing       = `{"enabled":false,"destOverride":["http","tls","quic","fakedns"]}`
	quickHttpTlsSniffing = `{"enabled":true,"destOverride":["http","tls"],"metadataOnly":false,"routeOnly":false}`
	quickCredentialLen  = 8
	quickSS2022Method   = "2022-blake3-aes-256-gcm"
)

var quickRealityPresets = []string{
	"dl-cdn.alpinelinux.org",
	"download-cdn.jetbrains.com",
	"fastcdn.hoyoverse.com",
}

type quickPortForwardForm struct {
	LocalPort     int    `json:"localPort" form:"localPort"`
	TargetAddress string `json:"targetAddress" form:"targetAddress"`
	TargetPort    int    `json:"targetPort" form:"targetPort"`
}

func (a *InboundController) freePort(c *gin.Context) {
	port, err := a.inboundService.GetFreePort(quickPortMin, quickPortMax)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.inbounds.noFreePort"), err)
		return
	}
	jsonObj(c, port, nil)
}

func (a *InboundController) quickSocks5(c *gin.Context) {
	port, err := a.inboundService.GetFreePort(quickPortMin, quickPortMax)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.inbounds.noFreePort"), err)
		return
	}
	username, err := randomAlphaNum(quickCredentialLen)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.inbounds.quickAdd"), err)
		return
	}
	password, err := randomAlphaNum(quickCredentialLen)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.inbounds.quickAdd"), err)
		return
	}
	settings, err := json.Marshal(map[string]interface{}{
		"auth":     "password",
		"accounts": []map[string]string{{"user": username, "pass": password}},
		"udp":      false,
		"ip":       "127.0.0.1",
	})
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.inbounds.quickAdd"), err)
		return
	}
	a.addQuickInbound(c, &model.Inbound{
		Remark:   fmt.Sprintf("SK5-%s-%d", quickRemarkIP(c), port),
		Enable:   true,
		Port:     port,
		Protocol: "socks",
		Settings: string(settings),
		Sniffing: quickSniffing,
	})
}

func (a *InboundController) quickVless(c *gin.Context) {
	quickMsg := I18nWeb(c, "pages.inbounds.quickAdd")
	port, err := a.inboundService.GetFreePort(quickPortMin, quickPortMax)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.inbounds.noFreePort"), err)
		return
	}
	preset, err := randomRealityPreset()
	if err != nil {
		jsonMsg(c, quickMsg, err)
		return
	}
	clientName, err := a.randomUniqueClientName()
	if err != nil {
		jsonMsg(c, quickMsg, err)
		return
	}
	clientID, err := randomUUID()
	if err != nil {
		jsonMsg(c, quickMsg, err)
		return
	}
	keyPairRaw, err := (&service.ServerService{}).GetNewX25519Cert()
	if err != nil {
		jsonMsg(c, quickMsg, err)
		return
	}
	keyPair, ok := keyPairRaw.(map[string]interface{})
	if !ok {
		jsonMsg(c, quickMsg, common.NewError("invalid key pair"))
		return
	}
	privateKey, _ := keyPair["privateKey"].(string)
	publicKey, _ := keyPair["publicKey"].(string)
	if privateKey == "" || publicKey == "" {
		jsonMsg(c, quickMsg, common.NewError("invalid key pair"))
		return
	}
	shortIDs, err := randomShortIDs(5)
	if err != nil {
		jsonMsg(c, quickMsg, err)
		return
	}
	settings, err := json.Marshal(map[string]interface{}{
		"clients": []map[string]interface{}{{
			"email":      clientName,
			"enable":     true,
			"expiryTime": 0,
			"flow":       "xtls-rprx-vision",
			"id":         clientID,
			"limitIp":    0,
			"reset":      0,
			"subId":      "",
			"tgId":       "",
			"totalGB":    0,
		}},
		"decryption": "none",
		"encryption": "none",
		"testseed":   []int{900, 500, 900, 256},
	})
	if err != nil {
		jsonMsg(c, quickMsg, err)
		return
	}
	streamSettings, err := json.Marshal(map[string]interface{}{
		"network":       "tcp",
		"security":      "reality",
		"externalProxy": []interface{}{},
		"tcpSettings": map[string]interface{}{
			"acceptProxyProtocol": false,
			"header":              map[string]interface{}{"type": "none"},
		},
		"realitySettings": map[string]interface{}{
			"show":         false,
			"xver":         0,
			"dest":         preset + ":443",
			"serverNames":  []string{preset},
			"privateKey":   privateKey,
			"minClientVer": "",
			"maxClientVer": "",
			"maxTimeDiff":  0,
			"shortIds":     shortIDs,
			"mldsa65Seed":  "",
			"settings": map[string]interface{}{
				"publicKey":     publicKey,
				"fingerprint":   "chrome",
				"serverName":    "",
				"spiderX":       "/",
				"mldsa65Verify": "",
			},
		},
	})
	if err != nil {
		jsonMsg(c, quickMsg, err)
		return
	}
	a.addQuickInbound(c, &model.Inbound{
		Remark:         fmt.Sprintf("VLESS_%d", port),
		Enable:         true,
		Port:           port,
		Protocol:       "vless",
		Settings:       string(settings),
		StreamSettings: string(streamSettings),
		Sniffing:       quickHttpTlsSniffing,
	})
}

func (a *InboundController) quickVmess(c *gin.Context) {
	quickMsg := I18nWeb(c, "pages.inbounds.quickAdd")
	port, err := a.inboundService.GetFreePort(quickPortMin, quickPortMax)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.inbounds.noFreePort"), err)
		return
	}
	clientName, err := a.randomUniqueClientName()
	if err != nil {
		jsonMsg(c, quickMsg, err)
		return
	}
	clientID, err := randomUUID()
	if err != nil {
		jsonMsg(c, quickMsg, err)
		return
	}
	subID, err := randomLowerNum(16)
	if err != nil {
		jsonMsg(c, quickMsg, err)
		return
	}
	streamSettings, err := quickTLSStream(quickRemarkIP(c))
	if err != nil {
		jsonMsg(c, quickMsg, err)
		return
	}
	settings, err := json.Marshal(map[string]interface{}{
		"clients": []map[string]interface{}{{
			"id":         clientID,
			"security":   "auto",
			"email":      clientName,
			"totalGB":    0,
			"expiryTime": 0,
			"enable":     true,
			"tgId":       "",
			"subId":      subID,
			"reset":      0,
			"limitIp":    0,
		}},
	})
	if err != nil {
		jsonMsg(c, quickMsg, err)
		return
	}
	a.addQuickInbound(c, &model.Inbound{
		Remark:         fmt.Sprintf("VMess_%d", port),
		Enable:         true,
		Port:           port,
		Protocol:       "vmess",
		Settings:       string(settings),
		StreamSettings: streamSettings,
		Sniffing:       quickHttpTlsSniffing,
	})
}

func (a *InboundController) quickSS2022(c *gin.Context) {
	quickMsg := I18nWeb(c, "pages.inbounds.quickAdd")
	port, err := a.inboundService.GetFreePort(quickPortMin, quickPortMax)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.inbounds.noFreePort"), err)
		return
	}
	clientName, err := a.randomUniqueClientName()
	if err != nil {
		jsonMsg(c, quickMsg, err)
		return
	}
	subID, err := randomLowerNum(16)
	if err != nil {
		jsonMsg(c, quickMsg, err)
		return
	}
	serverPassword, err := randomBase64Key(32)
	if err != nil {
		jsonMsg(c, quickMsg, err)
		return
	}
	clientPassword, err := randomBase64Key(32)
	if err != nil {
		jsonMsg(c, quickMsg, err)
		return
	}
	streamSettings, err := quickTLSStream(quickRemarkIP(c))
	if err != nil {
		jsonMsg(c, quickMsg, err)
		return
	}
	settings, err := json.Marshal(map[string]interface{}{
		"method":   quickSS2022Method,
		"password": serverPassword,
		"network":  "tcp,udp",
		"clients": []map[string]interface{}{{
			"method":     "",
			"password":   clientPassword,
			"email":      clientName,
			"totalGB":    0,
			"expiryTime": 0,
			"enable":     true,
			"tgId":       "",
			"subId":      subID,
			"reset":      0,
			"limitIp":    0,
		}},
		"ivCheck": false,
	})
	if err != nil {
		jsonMsg(c, quickMsg, err)
		return
	}
	a.addQuickInbound(c, &model.Inbound{
		Remark:         fmt.Sprintf("SS2022_%d", port),
		Enable:         true,
		Port:           port,
		Protocol:       "shadowsocks",
		Settings:       string(settings),
		StreamSettings: streamSettings,
		Sniffing:       quickHttpTlsSniffing,
	})
}

func (a *InboundController) quickPortForward(c *gin.Context) {
	var form quickPortForwardForm
	if err := c.ShouldBind(&form); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.inbounds.invalidTarget"), err)
		return
	}
	address := strings.TrimSpace(form.TargetAddress)
	quickMsg := I18nWeb(c, "pages.inbounds.quickAdd")
	if form.LocalPort < 1 || form.LocalPort > 65535 {
		jsonMsg(c, quickMsg, common.NewError(I18nWeb(c, "pages.inbounds.invalidLocalPort")))
		return
	}
	if form.TargetPort < 1 || form.TargetPort > 65535 || address == "" ||
		strings.ContainsAny(address, " \t\r\n/") {
		jsonMsg(c, quickMsg, common.NewError(I18nWeb(c, "pages.inbounds.invalidTarget")))
		return
	}
	if !service.PortAvailable(form.LocalPort) {
		jsonMsg(c, quickMsg, common.NewError(I18nWeb(c, "pages.inbounds.localPortInUse")))
		return
	}
	settings, err := json.Marshal(map[string]interface{}{
		"address":        address,
		"port":           form.TargetPort,
		"network":        "tcp,udp",
		"followRedirect": false,
	})
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.inbounds.quickAdd"), err)
		return
	}
	a.addQuickInbound(c, &model.Inbound{
		Remark:   fmt.Sprintf("%d-转-%s:%d", form.LocalPort, address, form.TargetPort),
		Enable:   true,
		Port:     form.LocalPort,
		Protocol: "dokodemo-door",
		Settings: string(settings),
		Sniffing: quickSniffing,
	})
}

func (a *InboundController) addQuickInbound(c *gin.Context, inbound *model.Inbound) {
	user := session.GetLoginUser(c)
	inbound.UserId = user.Id
	inbound.Tag = fmt.Sprintf("inbound-%v", inbound.Port)
	inbound, needRestart, err := a.inboundService.AddInbound(inbound)
	jsonMsgObj(c, I18nWeb(c, "pages.inbounds.quickAddSuccess"), inbound, err)
	if err == nil && needRestart {
		a.xrayService.SetToNeedRestart()
	}
}

// quickRemarkIP prefers the address the panel is being accessed by and falls
// back to the detected public IPv4 so the remark matches the connect address.
func quickRemarkIP(c *gin.Context) string {
	host := c.Request.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() && !ip.IsUnspecified() {
		return ip.String()
	}
	if ip, err := (&service.AutoCertService{}).PublicIP(); err == nil && ip != "" {
		return ip
	}
	return host
}

func (a *InboundController) randomUniqueClientName() (string, error) {
	for i := 0; i < 20; i++ {
		name, err := randomLetters(3)
		if err != nil {
			return "", err
		}
		exists, err := a.inboundService.ClientEmailExists(name)
		if err != nil {
			return "", err
		}
		if !exists {
			return name, nil
		}
	}
	return "", common.NewError("unable to generate a unique client name")
}

func quickTLSStream(serverName string) (string, error) {
	certRaw, err := network.GenerateSelfSignedCert(serverName)
	if err != nil {
		return "", err
	}
	certObj, ok := certRaw.(map[string]string)
	if !ok {
		return "", common.NewError("invalid certificate")
	}
	stream := map[string]interface{}{
		"network":       "tcp",
		"security":      "tls",
		"externalProxy": []interface{}{},
		"tcpSettings": map[string]interface{}{
			"acceptProxyProtocol": false,
			"header":              map[string]interface{}{"type": "none"},
		},
		"tlsSettings": map[string]interface{}{
			"minVersion": "1.2",
			"maxVersion": "1.3",
			"certificates": []map[string]interface{}{{
				"certificate": pemLines(certObj["cert"]),
				"key":         pemLines(certObj["key"]),
			}},
			"alpn": []string{"h3", "h2", "http/1.1"},
			"settings": map[string]interface{}{
				"allowInsecure":         true,
				"fingerprint":           "chrome",
				"echConfigList":         "",
				"pinnedPeerCertSha256":  []string{},
				"verifyPeerCertByName":  "",
			},
		},
	}
	b, err := json.Marshal(stream)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func pemLines(pem string) []string {
	return strings.Split(strings.TrimRight(pem, "\r\n"), "\n")
}

func randomLowerNum(length int) (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	result := make([]byte, length)
	for i := range result {
		ch, err := randomChar(charset)
		if err != nil {
			return "", err
		}
		result[i] = ch
	}
	return string(result), nil
}

func randomBase64Key(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func randomRealityPreset() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(quickRealityPresets))))
	if err != nil {
		return "", err
	}
	return quickRealityPresets[n.Int64()], nil
}

func randomLetters(length int) (string, error) {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	result := make([]byte, length)
	for i := range result {
		ch, err := randomChar(letters)
		if err != nil {
			return "", err
		}
		result[i] = ch
	}
	return string(result), nil
}

func randomShortIDs(count int) ([]string, error) {
	const hex = "0123456789abcdef"
	ids := make([]string, 0, count)
	seen := make(map[string]bool, count)
	for len(ids) < count {
		id := make([]byte, 8)
		for i := range id {
			ch, err := randomChar(hex)
			if err != nil {
				return nil, err
			}
			id[i] = ch
		}
		s := string(id)
		if seen[s] {
			continue
		}
		seen[s] = true
		ids = append(ids, s)
	}
	return ids, nil
}

func randomUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func randomAlphaNum(length int) (string, error) {
	const (
		digits = "0123456789"
		lowers = "abcdefghijklmnopqrstuvwxyz"
		uppers = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	)
	all := digits + lowers + uppers
	if length < 3 {
		length = 3
	}
	result := make([]byte, 0, length)
	for _, charset := range []string{digits, lowers, uppers} {
		ch, err := randomChar(charset)
		if err != nil {
			return "", err
		}
		result = append(result, ch)
	}
	for len(result) < length {
		ch, err := randomChar(all)
		if err != nil {
			return "", err
		}
		result = append(result, ch)
	}
	for i := len(result) - 1; i > 0; i-- {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return "", err
		}
		j := int(n.Int64())
		result[i], result[j] = result[j], result[i]
	}
	return string(result), nil
}

func randomChar(charset string) (byte, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
	if err != nil {
		return 0, err
	}
	return charset[int(n.Int64())], nil
}
