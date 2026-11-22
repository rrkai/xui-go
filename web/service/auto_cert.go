package service

import (
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alireza0/x-ui/config"
	"github.com/alireza0/x-ui/logger"
	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/http01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"
)

const (
	CertProviderLetsEncrypt = "letsencrypt"
	CertProviderZeroSSL     = "zerossl"

	letsEncryptDirectory = "https://acme-v02.api.letsencrypt.org/directory"
	zeroSSLDirectory     = "https://acme.zerossl.com/v2/DV90"
	port80CheckURL       = "https://ifconfig.co/port/80"

	// Renew only inside this window so frequent checks never hit the CA early.
	certRenewWindow = 12 * time.Hour
	// After a failed attempt wait this long before trying again.
	certRetryInterval = 30 * time.Minute
	// Post-expiry attempts (after a provider switch) before giving up.
	certMaxFailures = 3
)

var (
	autoCertRunning atomic.Bool
	autoCertStateMu sync.Mutex
	port80Mu        sync.Mutex
	ipv4Regex       = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	domainRegex     = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
	unitNameRegex   = regexp.MustCompile(`^[A-Za-z0-9_.@:-]+\.service$`)
	publicIPCache   struct {
		mu sync.Mutex
		ip string
		at time.Time
	}
	port80Cache struct {
		mu      sync.Mutex
		request string
		at      time.Time
		status  Port80Status
	}
)

type AutoCertState struct {
	Enabled           bool      `json:"enabled"`
	Target            string    `json:"target"`
	Provider          string    `json:"provider"`
	Email             string    `json:"email"`
	LastIssueAt       time.Time `json:"lastIssueAt"`
	LastError         string    `json:"lastError"`
	FailCount         int       `json:"failCount"`
	NextAttemptAt     time.Time `json:"nextAttemptAt"`
	ProviderSwitched  bool      `json:"providerSwitched"`
	GivenUp           bool      `json:"givenUp"`
	ReleasePort80     bool      `json:"releasePort80"`
	ReleasePort80Unit string    `json:"releasePort80Unit"`
}

type CertStatus struct {
	Enabled           bool      `json:"enabled"`
	Target            string    `json:"target"`
	Provider          string    `json:"provider"`
	CertFile          string    `json:"certFile"`
	KeyFile           string    `json:"keyFile"`
	Present           bool      `json:"present"`
	Valid             bool      `json:"valid"`
	Managed           bool      `json:"managed"`
	AutoRenew         bool      `json:"autoRenew"`
	Issuing           bool      `json:"issuing"`
	Subject           string    `json:"subject"`
	Names             []string  `json:"names"`
	Issuer            string    `json:"issuer"`
	NotBefore         time.Time `json:"notBefore"`
	NotAfter          time.Time `json:"notAfter"`
	RenewAt           time.Time `json:"renewAt"`
	DaysLeft          int       `json:"daysLeft"`
	FailCount         int       `json:"failCount"`
	NextAttemptAt     time.Time `json:"nextAttemptAt"`
	ProviderSwitched  bool      `json:"providerSwitched"`
	GivenUp           bool      `json:"givenUp"`
	ReleasePort80     bool      `json:"releasePort80"`
	ReleasePort80Unit string    `json:"releasePort80Unit"`
	LastError         string    `json:"lastError"`
}

type Port80Status struct {
	Target       string    `json:"target"`
	TargetIP     string    `json:"targetIp"`
	PublicIP     string    `json:"publicIp"`
	LocalFree    bool      `json:"localFree"`
	Reachable    bool      `json:"reachable"`
	Checked      bool      `json:"checked"`
	Blocked      bool      `json:"blocked"`
	OccupantName string    `json:"occupantName"`
	OccupantPID  int       `json:"occupantPid"`
	OccupantUnit string    `json:"occupantUnit"`
	CanStop      bool      `json:"canStop"`
	StopReason   string    `json:"stopReason"`
	Message      string    `json:"message"`
	CheckedAt    time.Time `json:"checkedAt"`
}

type eabCredentials struct {
	Kid  string `json:"kid"`
	Hmac string `json:"hmac"`
}

type acmeUser struct {
	email string
	key   crypto.PrivateKey
	reg   *registration.Resource
}

func (u *acmeUser) GetEmail() string                        { return u.email }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.key }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.reg }

type AutoCertService struct{}

func httpsBaseDir() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(filepath.Dir(config.GetDBPath()), "xui-https")
	}
	return "/XUI-GO/HTTP-SSL"
}

func acmeBaseDir() string {
	return filepath.Join(httpsBaseDir(), "acme")
}

func managedCertPath() string {
	return filepath.Join(httpsBaseDir(), "fullchain.pem")
}

func managedKeyPath() string {
	return filepath.Join(httpsBaseDir(), "privkey.pem")
}

func statePath() string {
	return filepath.Join(acmeBaseDir(), "state.json")
}

func eabPath() string {
	return filepath.Join(acmeBaseDir(), "zerossl-eab.json")
}

func accountKeyPath(provider string) string {
	return filepath.Join(acmeBaseDir(), "accounts", provider+".key")
}

func loadState() (AutoCertState, error) {
	autoCertStateMu.Lock()
	defer autoCertStateMu.Unlock()
	return loadStateLocked()
}

func loadStateLocked() (AutoCertState, error) {
	var state AutoCertState
	b, err := os.ReadFile(statePath())
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return state, err
	}
	if err := json.Unmarshal(b, &state); err != nil {
		return state, err
	}
	return state, nil
}

func saveState(state AutoCertState) error {
	autoCertStateMu.Lock()
	defer autoCertStateMu.Unlock()
	return saveStateLocked(state)
}

func saveStateLocked(state AutoCertState) error {
	if err := os.MkdirAll(acmeBaseDir(), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(statePath(), b, 0600)
}

func updateLastError(msg string) {
	state, err := loadState()
	if err != nil {
		return
	}
	state.LastError = msg
	_ = saveState(state)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp := path + ".tmp-" + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func loadAccountKey(provider string) (crypto.PrivateKey, error) {
	path := accountKeyPath(provider)
	if b, err := os.ReadFile(path); err == nil {
		return certcrypto.ParsePEMPrivateKey(b)
	}
	key, err := certcrypto.GeneratePrivateKey(certcrypto.EC256)
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(path, certcrypto.PEMEncode(key), 0600); err != nil {
		return nil, err
	}
	return key, nil
}

func loadEAB() (eabCredentials, error) {
	var eab eabCredentials
	b, err := os.ReadFile(eabPath())
	if err != nil {
		return eab, err
	}
	err = json.Unmarshal(b, &eab)
	return eab, err
}

func saveEAB(eab eabCredentials) error {
	b, err := json.MarshalIndent(eab, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(eabPath(), b, 0600)
}

func (s *AutoCertService) PublicIP() (string, error) {
	publicIPCache.mu.Lock()
	if publicIPCache.ip != "" && time.Since(publicIPCache.at) < 10*time.Minute {
		ip := publicIPCache.ip
		publicIPCache.mu.Unlock()
		return ip, nil
	}
	publicIPCache.mu.Unlock()

	endpoints := []string{
		"https://api.ipify.org",
		"https://ip.3322.net",
		"https://ifconfig.me/ip",
		"https://myip.ipip.net",
	}
	client := &http.Client{Timeout: 4 * time.Second}
	var lastErr error
	for _, endpoint := range endpoints {
		resp, err := client.Get(endpoint)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := readLimited(resp.Body, 256)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		for _, candidate := range ipv4Regex.FindAllString(body, -1) {
			ip := net.ParseIP(candidate)
			if ip == nil || ip.To4() == nil || !isPublicIPv4(ip) {
				continue
			}
			ipStr := ip.String()
			publicIPCache.mu.Lock()
			publicIPCache.ip = ipStr
			publicIPCache.at = time.Now()
			publicIPCache.mu.Unlock()
			return ipStr, nil
		}
		lastErr = fmt.Errorf("no public IPv4 in response from %s", endpoint)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("unable to detect public IPv4")
	}
	return "", lastErr
}

func readLimited(r interface{ Read([]byte) (int, error) }, limit int) (string, error) {
	buf := make([]byte, limit)
	n, err := r.Read(buf)
	if err != nil && n == 0 {
		return "", err
	}
	return string(buf[:n]), nil
}

func isPublicIPv4(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	// 100.64.0.0/10 carrier-grade NAT
	if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
		return false
	}
	return true
}

func normalizeTarget(target string) (string, bool, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		ip, err := (&AutoCertService{}).PublicIP()
		if err != nil {
			return "", false, fmt.Errorf("无法自动检测公网 IP，请手动填写：%v", err)
		}
		target = ip
	}
	if ip := net.ParseIP(strings.Trim(target, "[]")); ip != nil {
		if ip.To4() == nil {
			return "", false, fmt.Errorf("当前仅支持 IPv4 证书")
		}
		return ip.String(), true, nil
	}
	target = strings.ToLower(strings.TrimSuffix(target, "."))
	if strings.Contains(target, "*") {
		return "", false, fmt.Errorf("HTTP 文件验证无法签发通配符证书，请填写具体域名")
	}
	if !domainRegex.MatchString(target) {
		return "", false, fmt.Errorf("目标不是有效的公网 IP 或域名")
	}
	return target, false, nil
}

func isIPTarget(target string) bool {
	return net.ParseIP(strings.Trim(target, "[]")) != nil
}

func resolveTargetIPv4(target string) (string, error) {
	if ip := net.ParseIP(strings.Trim(target, "[]")); ip != nil {
		if ip.To4() == nil {
			return "", fmt.Errorf("当前仅支持 IPv4 证书")
		}
		return ip.String(), nil
	}
	ips, err := net.LookupIP(target)
	if err != nil {
		return "", fmt.Errorf("域名解析失败：%v", err)
	}
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil {
			return ip4.String(), nil
		}
	}
	return "", fmt.Errorf("域名未解析到 IPv4 地址")
}

type port80ProbeResponse struct {
	IP        string `json:"ip"`
	Port      int    `json:"port"`
	Reachable bool   `json:"reachable"`
}

// CheckPort80 verifies that HTTP-01 can actually work: the local port must be
// free and the public address must be reachable from the Internet. NAT hosts
// without a port-80 mapping are reported as blocked instead of burning ACME
// failed-validation quota.
func (s *AutoCertService) CheckPort80(target string, force bool) Port80Status {
	return s.checkPort80(target, force, false)
}

func (s *AutoCertService) checkPort80(target string, force, fromIssue bool) Port80Status {
	target = strings.TrimSpace(target)
	if !fromIssue && autoCertRunning.Load() {
		return Port80Status{Target: target, Message: "证书签发正在进行中，暂不检测 80 端口"}
	}

	if !force {
		port80Cache.mu.Lock()
		if target == port80Cache.request && time.Since(port80Cache.at) < 2*time.Minute {
			status := port80Cache.status
			port80Cache.mu.Unlock()
			return status
		}
		port80Cache.mu.Unlock()
	}

	status := Port80Status{Target: target, CheckedAt: time.Now()}
	cacheStatus := func() {
		port80Cache.mu.Lock()
		port80Cache.request = target
		port80Cache.at = time.Now()
		port80Cache.status = status
		port80Cache.mu.Unlock()
	}
	normalized, _, err := normalizeTarget(target)
	if err != nil {
		status.Message = err.Error()
		return status
	}
	status.Target = normalized

	targetIP, err := resolveTargetIPv4(normalized)
	if err != nil {
		status.Message = err.Error()
		return status
	}
	status.TargetIP = targetIP

	publicIP, err := s.PublicIP()
	if err != nil {
		status.Message = "无法检测本机公网 IP：" + err.Error()
		return status
	}
	status.PublicIP = publicIP
	if targetIP != publicIP {
		status.Message = fmt.Sprintf("目标 %s 不是本机公网 IP（%s），无法在本机判断 80 端口，仍可尝试申请", targetIP, publicIP)
		return status
	}

	port80Mu.Lock()
	ln, listenErr := net.Listen("tcp", ":80")
	if listenErr == nil {
		status.LocalFree = true
		go servePort80Probe(ln)
	} else if occ := detectPort80Occupant(); occ != nil {
		status.OccupantName = occ.Name
		status.OccupantPID = occ.PID
		status.OccupantUnit = occ.Unit
		status.CanStop = occ.CanStop
		status.StopReason = occ.Reason
	}
	resp, probeErr := queryPublicPort80()
	if ln != nil {
		ln.Close()
	}
	port80Mu.Unlock()

	if probeErr != nil {
		status.Message = "无法确认公网 80 端口（检测服务不可用），仍可尝试申请：" + probeErr.Error()
		cacheStatus()
		return status
	}
	if resp.IP != "" && resp.IP != publicIP {
		status.Message = fmt.Sprintf("检测出口 IP（%s）与本机公网 IP 不一致，无法确认 80 端口，仍可尝试申请", resp.IP)
		cacheStatus()
		return status
	}

	status.Checked = true
	status.Reachable = resp.Reachable
	occupant := status.OccupantUnit
	if occupant == "" {
		occupant = status.OccupantName
	}
	if occupant == "" {
		occupant = "其他进程"
	}
	switch {
	case status.LocalFree && status.Reachable:
		status.Message = "公网 80 端口可用"
	case !status.LocalFree && status.Reachable:
		status.Blocked = true
		status.Message = fmt.Sprintf("公网 80 端口可达，但本机 80 端口被 %s 占用，HTTP 验证无法进行", occupant)
	case status.LocalFree && !status.Reachable:
		status.Blocked = true
		status.Message = "未检测到公网 80 端口（可能是 NAT 未映射或被防火墙拦截），无法申请证书"
	default:
		status.Blocked = true
		status.Message = fmt.Sprintf("本机 80 端口被 %s 占用，且公网 80 端口不可达，无法申请证书", occupant)
	}

	cacheStatus()
	return status
}

func servePort80Probe(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			c.SetDeadline(time.Now().Add(5 * time.Second))
			_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"))
		}(conn)
	}
}

func queryPublicPort80() (port80ProbeResponse, error) {
	var resp port80ProbeResponse
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialer := &net.Dialer{Timeout: 6 * time.Second}
			return dialer.DialContext(ctx, "tcp4", addr)
		},
	}
	client := &http.Client{Timeout: 15 * time.Second, Transport: transport}
	req, err := http.NewRequest(http.MethodGet, port80CheckURL, nil)
	if err != nil {
		return resp, err
	}
	req.Header.Set("User-Agent", "x-ui-cert-check/1.0")
	httpResp, err := client.Do(req)
	if err != nil {
		return resp, err
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		return resp, fmt.Errorf("检测服务返回 HTTP %d", httpResp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(httpResp.Body, 4096))
	if err != nil {
		return resp, err
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return resp, fmt.Errorf("检测服务响应无法解析")
	}
	if resp.Port != 0 && resp.Port != 80 {
		return resp, fmt.Errorf("检测服务返回了意外的端口")
	}
	return resp, nil
}

type port80Occupant struct {
	PID     int
	Name    string
	Unit    string
	CanStop bool
	Reason  string
}

type port80Release struct {
	Unit  string
	once  sync.Once
	timer *time.Timer
	err   error
}

type port80ReleaseRecord struct {
	Unit      string    `json:"unit"`
	StartedAt time.Time `json:"startedAt"`
}

func releaseRecordPath() string {
	return filepath.Join(acmeBaseDir(), "port80-release.json")
}

func saveReleaseRecord(unit string) error {
	b, err := json.MarshalIndent(port80ReleaseRecord{Unit: unit, StartedAt: time.Now()}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(releaseRecordPath(), b, 0600)
}

func clearReleaseRecord() {
	_ = os.Remove(releaseRecordPath())
}

func systemctlAction(action, unit string) error {
	if !unitNameRegex.MatchString(unit) {
		return fmt.Errorf("非法的服务名：%s", unit)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", action, unit).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return err
		}
		return fmt.Errorf("%v: %s", err, msg)
	}
	return nil
}

func listeningInodesOnPort(port int) map[string]bool {
	inodes := make(map[string]bool)
	for _, file := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		b, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		lines := strings.Split(string(b), "\n")
		for _, line := range lines[1:] {
			fields := strings.Fields(line)
			if len(fields) < 10 || !strings.EqualFold(fields[3], "0A") {
				continue
			}
			idx := strings.LastIndex(fields[1], ":")
			if idx < 0 {
				continue
			}
			p, err := strconv.ParseUint(fields[1][idx+1:], 16, 16)
			if err != nil || int(p) != port {
				continue
			}
			inodes[fields[9]] = true
		}
	}
	return inodes
}

func pidsForSocketInodes(inodes map[string]bool) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var pids []int
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		fdDir := filepath.Join("/proc", entry.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil || !strings.HasPrefix(link, "socket:[") || !strings.HasSuffix(link, "]") {
				continue
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")
			if inodes[inode] {
				pids = append(pids, pid)
				break
			}
		}
	}
	return pids
}

func selfAndAncestors() map[int]bool {
	set := map[int]bool{os.Getpid(): true}
	pid := os.Getppid()
	for pid > 1 && !set[pid] {
		set[pid] = true
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			break
		}
		s := string(b)
		closeIdx := strings.LastIndex(s, ")")
		if closeIdx < 0 {
			break
		}
		fields := strings.Fields(s[closeIdx+1:])
		if len(fields) < 2 {
			break
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			break
		}
		pid = ppid
	}
	return set
}

func processComm(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func cgroupUnit(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		for _, seg := range strings.Split(line, "/") {
			seg = strings.TrimSpace(seg)
			if unitNameRegex.MatchString(seg) {
				return seg
			}
		}
	}
	return ""
}

func protectedUnit(unit, ownUnit string) bool {
	if unit == "" {
		return true
	}
	if unit == ownUnit || unit == "x-ui.service" {
		return true
	}
	lower := strings.ToLower(unit)
	for _, prefix := range []string{"docker", "containerd", "systemd-", "dbus", "snapd"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func detectPort80Occupant() *port80Occupant {
	inodes := listeningInodesOnPort(80)
	if len(inodes) == 0 {
		return nil
	}
	pids := pidsForSocketInodes(inodes)
	if len(pids) == 0 {
		return nil
	}
	ancestors := selfAndAncestors()
	ownUnit := cgroupUnit(os.Getpid())
	var fallback *port80Occupant
	for _, pid := range pids {
		occ := &port80Occupant{PID: pid, Name: processComm(pid), Unit: cgroupUnit(pid)}
		switch {
		case ancestors[pid]:
			occ.Reason = "80 端口由面板自身或父进程占用"
		case pid == 1:
			occ.Reason = "80 端口由系统 init 占用"
		case occ.Unit == "":
			occ.Reason = "占用进程不是 systemd 服务，请手动停止"
		case protectedUnit(occ.Unit, ownUnit):
			occ.Reason = fmt.Sprintf("服务 %s 受保护，不能自动停止", occ.Unit)
		default:
			occ.CanStop = true
		}
		if fallback == nil || (occ.CanStop && !fallback.CanStop) {
			fallback = occ
		}
		if occ.CanStop {
			break
		}
	}
	return fallback
}

func canListenPort80() bool {
	port80Mu.Lock()
	defer port80Mu.Unlock()
	ln, err := net.Listen("tcp", ":80")
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

func releasePort80Local() (*port80Release, error) {
	occ := detectPort80Occupant()
	if occ == nil {
		return nil, fmt.Errorf("未能识别占用 80 端口的进程")
	}
	if !occ.CanStop {
		return nil, fmt.Errorf("无法自动停止占用 80 端口的进程：%s", occ.Reason)
	}
	if err := saveReleaseRecord(occ.Unit); err != nil {
		return nil, fmt.Errorf("记录 80 端口服务恢复信息失败：%v", err)
	}
	if err := systemctlAction("stop", occ.Unit); err != nil {
		clearReleaseRecord()
		return nil, fmt.Errorf("停止服务 %s 失败：%v", occ.Unit, err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for !canListenPort80() {
		if time.Now().After(deadline) {
			_ = systemctlAction("start", occ.Unit)
			clearReleaseRecord()
			return nil, fmt.Errorf("服务 %s 已停止但 80 端口仍被占用", occ.Unit)
		}
		time.Sleep(200 * time.Millisecond)
	}

	release := &port80Release{Unit: occ.Unit}
	release.timer = time.AfterFunc(10*time.Minute, func() {
		if err := release.Restore(); err != nil {
			logger.Error("Failed to restore service after certificate issuance:", err)
		}
	})
	logger.Infof("Temporarily stopped %s to free port 80 for certificate issuance", occ.Unit)
	return release, nil
}

func (r *port80Release) Restore() error {
	r.once.Do(func() {
		if r.timer != nil {
			r.timer.Stop()
		}
		if err := systemctlAction("start", r.Unit); err != nil {
			r.err = fmt.Errorf("服务 %s 启动失败：%v", r.Unit, err)
			logger.Error("Failed to restart ", r.Unit, " after certificate issuance: ", err)
			// Keep the recovery record so the next panel start retries.
			return
		}
		clearReleaseRecord()
		logger.Infof("Restarted %s after certificate issuance", r.Unit)
	})
	return r.err
}

// RecoverPort80Release restarts a service that was stopped for certificate
// issuance if the panel crashed before it could restore it.
func RecoverPort80Release() {
	b, err := os.ReadFile(releaseRecordPath())
	if err != nil {
		return
	}
	var record port80ReleaseRecord
	if err := json.Unmarshal(b, &record); err != nil || !unitNameRegex.MatchString(record.Unit) ||
		protectedUnit(record.Unit, cgroupUnit(os.Getpid())) {
		clearReleaseRecord()
		return
	}
	if err := systemctlAction("start", record.Unit); err != nil {
		logger.Error("Failed to restore service ", record.Unit, " after previous certificate issuance: ", err)
		return
	}
	clearReleaseRecord()
	logger.Infof("Restored %s after previous certificate issuance", record.Unit)
}

func (s *AutoCertService) Issue(target, provider, eabKid, eabHmac, email string) (retErr error) {
	if !autoCertRunning.CompareAndSwap(false, true) {
		return fmt.Errorf("证书签发正在进行中，请稍候")
	}
	defer autoCertRunning.Store(false)

	var release *port80Release
	defer func() {
		if release == nil {
			return
		}
		if rerr := release.Restore(); rerr != nil {
			msg := fmt.Sprintf("临时停止的服务 %s 启动失败：%v，请手动启动", release.Unit, rerr)
			updateLastError(msg)
			if retErr == nil {
				retErr = fmt.Errorf("%s", msg)
			} else {
				retErr = fmt.Errorf("%v；%s", retErr, msg)
			}
		}
	}()
	existing, _ := loadState()
	// New installs allow auto-renewal to temporarily free port 80 by default;
	// an existing choice is always preserved.
	if _, statErr := os.Stat(statePath()); os.IsNotExist(statErr) {
		existing.ReleasePort80 = true
	}

	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = CertProviderLetsEncrypt
	}
	if provider != CertProviderLetsEncrypt && provider != CertProviderZeroSSL {
		err := fmt.Errorf("不支持的证书提供商：%s", provider)
		updateLastError(err.Error())
		return err
	}

	target, isIP, err := normalizeTarget(target)
	if err != nil {
		updateLastError(err.Error())
		return err
	}
	if isIP && provider != CertProviderLetsEncrypt {
		err := fmt.Errorf("IP 证书仅支持 Let's Encrypt（请将提供商切换为 Let's Encrypt）")
		updateLastError(err.Error())
		return err
	}
	if provider == CertProviderZeroSSL {
		if strings.TrimSpace(eabKid) == "" || strings.TrimSpace(eabHmac) == "" {
			stored, loadErr := loadEAB()
			if loadErr != nil {
				err := fmt.Errorf("ZeroSSL 需要 EAB 凭据（EAB Key ID 与 HMAC Key）")
				updateLastError(err.Error())
				return err
			}
			eabKid, eabHmac = stored.Kid, stored.Hmac
		}
	}

	if portStatus := s.checkPort80(target, false, true); portStatus.Blocked {
		if existing.ReleasePort80 && existing.ReleasePort80Unit == "" &&
			portStatus.CanStop && !portStatus.LocalFree && portStatus.Reachable {
			existing.ReleasePort80Unit = portStatus.OccupantUnit
		}
		canRelease := portStatus.CanStop && !portStatus.LocalFree && portStatus.Reachable &&
			existing.ReleasePort80 && existing.ReleasePort80Unit == portStatus.OccupantUnit
		if !canRelease {
			msg := portStatus.Message
			if existing.ReleasePort80 && existing.ReleasePort80Unit != "" && existing.ReleasePort80Unit != portStatus.OccupantUnit {
				msg += fmt.Sprintf("；已批准的占用服务是 %s，当前为 %s，请重新确认", existing.ReleasePort80Unit, portStatus.OccupantUnit)
			} else if existing.ReleasePort80 && existing.ReleasePort80Unit == "" {
				msg += "；未批准具体占用服务，请重新检测并开启临时释放"
			}
			if portStatus.StopReason != "" {
				msg += "；" + portStatus.StopReason
			}
			err := fmt.Errorf("%s", msg)
			updateLastError(err.Error())
			return err
		}
		release, err = releasePort80Local()
		if err != nil {
			updateLastError(err.Error())
			return err
		}
	}

	// HTTP-01 always validates against port 80.
	port80Mu.Lock()
	probe, err := net.Listen("tcp", ":80")
	port80Mu.Unlock()
	if err != nil {
		err = fmt.Errorf("80 端口被占用或无法监听，HTTP 验证无法进行：%v", err)
		updateLastError(err.Error())
		return err
	}
	probe.Close()

	accountKey, err := loadAccountKey(provider)
	if err != nil {
		updateLastError(err.Error())
		return err
	}
	user := &acmeUser{email: strings.TrimSpace(email), key: accountKey}

	cfg := lego.NewConfig(user)
	cfg.Certificate.KeyType = certcrypto.EC256
	cfg.Certificate.Timeout = 90 * time.Second
	// Let's Encrypt rejects IP addresses in the CSR Common Name (badCSR);
	// IP certificates must carry the address only as a SAN.
	cfg.Certificate.DisableCommonName = isIP
	if provider == CertProviderZeroSSL {
		cfg.CADirURL = zeroSSLDirectory
	} else {
		cfg.CADirURL = letsEncryptDirectory
	}

	client, err := lego.NewClient(cfg)
	if err != nil {
		updateLastError(err.Error())
		return err
	}

	if provider == CertProviderZeroSSL {
		res, err := client.Registration.RegisterWithExternalAccountBinding(registration.RegisterEABOptions{
			TermsOfServiceAgreed: true,
			Kid:                  eabKid,
			HmacEncoded:          eabHmac,
		})
		if err != nil {
			err = fmt.Errorf("ZeroSSL 账号注册失败（检查 EAB 凭据）：%v", err)
			updateLastError(err.Error())
			return err
		}
		user.reg = res
		if err := saveEAB(eabCredentials{Kid: eabKid, Hmac: eabHmac}); err != nil {
			logger.Warning("failed to persist ZeroSSL EAB credentials:", err)
		}
	} else {
		res, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			updateLastError(err.Error())
			return err
		}
		user.reg = res
	}

	client.Challenge.SetHTTP01Provider(http01.NewProviderServer("", "80"))

	req := certificate.ObtainRequest{
		Domains: []string{target},
		Bundle:  true,
	}
	if isIP {
		req.Profile = "shortlived"
	}

	logger.Infof("Requesting %s certificate for %s via %s", map[bool]string{true: "IP", false: "domain"}[isIP], target, provider)
	res, err := client.Certificate.Obtain(req)
	if err != nil {
		err = fmt.Errorf("证书申请失败：%v", err)
		updateLastError(err.Error())
		return err
	}

	leafs, err := certcrypto.ParsePEMBundle(res.Certificate)
	if err != nil || len(leafs) == 0 {
		err = fmt.Errorf("证书解析失败：%v", err)
		updateLastError(err.Error())
		return err
	}
	leaf := leafs[0]

	certFile, keyFile := managedCertPath(), managedKeyPath()
	if err := writeFileAtomic(certFile, res.Certificate, 0644); err != nil {
		updateLastError(err.Error())
		return err
	}
	if err := writeFileAtomic(keyFile, res.PrivateKey, 0600); err != nil {
		updateLastError(err.Error())
		return err
	}

	settingService := SettingService{}
	if err := settingService.SetCertFile(certFile); err != nil {
		updateLastError(err.Error())
		return err
	}
	if err := settingService.SetKeyFile(keyFile); err != nil {
		updateLastError(err.Error())
		return err
	}

	state := AutoCertState{
		Enabled:           true,
		Target:            target,
		Provider:          provider,
		Email:             strings.TrimSpace(email),
		LastIssueAt:       time.Now(),
		ReleasePort80:     existing.ReleasePort80,
		ReleasePort80Unit: existing.ReleasePort80Unit,
	}
	if err := saveState(state); err != nil {
		logger.Warning("failed to persist auto certificate state:", err)
	}

	logger.Infof("Certificate issued for %s (%s), expires %s", target, provider, leaf.NotAfter.Format(time.RFC3339))
	return nil
}

func alternateProvider(provider string) (string, bool) {
	switch provider {
	case CertProviderLetsEncrypt:
		return CertProviderZeroSSL, true
	case CertProviderZeroSSL:
		return CertProviderLetsEncrypt, true
	default:
		return CertProviderLetsEncrypt, true
	}
}

func providerAvailable(provider string) bool {
	if provider == CertProviderZeroSSL {
		_, err := loadEAB()
		return err == nil
	}
	return true
}

func (s *AutoCertService) RenewIfNeeded() {
	if autoCertRunning.Load() {
		return
	}
	state, err := loadState()
	if err != nil || !state.Enabled || state.Target == "" || state.GivenUp {
		return
	}
	if !state.NextAttemptAt.IsZero() && time.Now().Before(state.NextAttemptAt) {
		return
	}

	settingService := SettingService{}
	certFile, err := settingService.GetCertFile()
	if err != nil {
		return
	}
	keyFile, err := settingService.GetKeyFile()
	if err != nil {
		return
	}
	if filepath.Clean(certFile) != filepath.Clean(managedCertPath()) {
		return
	}

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil || len(cert.Certificate) == 0 {
		s.handleExpired(state)
		return
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return
	}

	now := time.Now()
	if now.After(leaf.NotAfter) {
		s.handleExpired(state)
		return
	}
	remaining := time.Until(leaf.NotAfter)
	if remaining > certRenewWindow {
		return
	}

	logger.Infof("Certificate for %s expires in %s, renewing automatically", state.Target, remaining.Round(time.Minute))
	s.attemptRenewal(state, false)
}

func (s *AutoCertService) handleExpired(state AutoCertState) {
	if isIPTarget(state.Target) {
		state.GivenUp = true
		state.NextAttemptAt = time.Time{}
		state.LastError = "证书已过期，IP 证书无法切换供应商，已放弃自动续期并回退 HTTP"
		_ = saveState(state)
		logger.Warning("Certificate for IP ", state.Target, " expired; automatic renewal stopped, panel falls back to HTTP")
		return
	}

	if !state.ProviderSwitched {
		alt, ok := alternateProvider(state.Provider)
		if !ok || !providerAvailable(alt) {
			state.GivenUp = true
			state.NextAttemptAt = time.Time{}
			state.LastError = "证书已过期，且无法切换到其他证书供应商，已放弃自动续期并回退 HTTP"
			_ = saveState(state)
			return
		}
		state.Provider = alt
		state.ProviderSwitched = true
		state.FailCount = 0
		state.NextAttemptAt = time.Time{}
		state.LastError = fmt.Sprintf("证书已过期，已切换证书供应商为 %s 并尝试重新签发", alt)
		_ = saveState(state)
		logger.Infof("Certificate for %s expired, switched provider to %s", state.Target, alt)
		s.attemptRenewal(state, true)
		return
	}

	if state.FailCount >= certMaxFailures {
		state.GivenUp = true
		state.NextAttemptAt = time.Time{}
		state.LastError = fmt.Sprintf("证书已过期且续期失败超过 %d 次，已放弃自动续期并回退 HTTP", certMaxFailures)
		_ = saveState(state)
		return
	}
	s.attemptRenewal(state, true)
}

func (s *AutoCertService) attemptRenewal(state AutoCertState, expired bool) {
	portStatus := s.checkPort80(state.Target, false, false)
	if portStatus.Blocked {
		approvedUnit := state.ReleasePort80Unit
		if state.ReleasePort80 && approvedUnit == "" && portStatus.CanStop {
			approvedUnit = portStatus.OccupantUnit
		}
		canRelease := state.ReleasePort80 && approvedUnit == portStatus.OccupantUnit &&
			portStatus.CanStop && !portStatus.LocalFree && portStatus.Reachable
		if !canRelease {
			if expired {
				state.FailCount++
				if state.FailCount >= certMaxFailures {
					state.GivenUp = true
					state.NextAttemptAt = time.Time{}
					state.LastError = portStatus.Message + "；续期失败超过 3 次，已放弃自动续期并回退 HTTP"
				} else {
					state.NextAttemptAt = time.Now().Add(certRetryInterval)
					state.LastError = portStatus.Message
				}
			} else {
				state.NextAttemptAt = time.Now().Add(certRetryInterval)
				state.LastError = portStatus.Message
			}
			_ = saveState(state)
			logger.Warning("Certificate renewal skipped: ", portStatus.Message)
			return
		}
		logger.Infof("Port 80 is occupied by %s, temporarily stopping it for renewal", portStatus.OccupantUnit)
	}

	err := s.Issue(state.Target, state.Provider, "", "", state.Email)
	if err == nil {
		logger.Infof("Certificate renewed for %s", state.Target)
		return
	}
	if autoCertRunning.Load() {
		return
	}

	latest, loadErr := loadState()
	if loadErr != nil || !latest.Enabled || latest.Target != state.Target {
		return
	}
	if latest.LastIssueAt.After(state.LastIssueAt) {
		return
	}
	latest.LastError = err.Error()
	if expired {
		latest.FailCount++
		if latest.FailCount >= certMaxFailures {
			latest.GivenUp = true
			latest.NextAttemptAt = time.Time{}
			latest.LastError = fmt.Sprintf("证书已过期且续期失败超过 %d 次，已放弃自动续期并回退 HTTP", certMaxFailures)
		} else {
			latest.NextAttemptAt = time.Now().Add(certRetryInterval)
		}
	} else {
		latest.FailCount++
		latest.NextAttemptAt = time.Now().Add(certRetryInterval)
	}
	_ = saveState(latest)
	logger.Warningf("Certificate renewal for %s failed (attempt %d): %v", state.Target, latest.FailCount, err)
}

func (s *AutoCertService) Status() (CertStatus, error) {
	state, err := loadState()
	if err != nil {
		return CertStatus{}, err
	}
	if _, statErr := os.Stat(statePath()); os.IsNotExist(statErr) {
		state.ReleasePort80 = true
	}

	settingService := SettingService{}
	certFile, _ := settingService.GetCertFile()
	keyFile, _ := settingService.GetKeyFile()

	status := CertStatus{
		Enabled:           state.Enabled,
		Target:            state.Target,
		Provider:          state.Provider,
		CertFile:          certFile,
		KeyFile:           keyFile,
		Issuing:           autoCertRunning.Load(),
		FailCount:         state.FailCount,
		NextAttemptAt:     state.NextAttemptAt,
		ProviderSwitched:  state.ProviderSwitched,
		GivenUp:           state.GivenUp,
		ReleasePort80:     state.ReleasePort80,
		ReleasePort80Unit: state.ReleasePort80Unit,
		LastError:         state.LastError,
	}
	status.Managed = filepath.Clean(certFile) == filepath.Clean(managedCertPath())
	status.AutoRenew = state.Enabled && status.Managed && !state.GivenUp

	if certFile != "" && keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err == nil && len(cert.Certificate) > 0 {
			if leaf, err := x509.ParseCertificate(cert.Certificate[0]); err == nil {
				now := time.Now()
				status.Present = true
				status.Valid = now.After(leaf.NotBefore) && now.Before(leaf.NotAfter)
				status.Subject = leaf.Subject.CommonName
				status.Names = leaf.DNSNames
				status.Issuer = leaf.Issuer.CommonName
				status.NotBefore = leaf.NotBefore
				status.NotAfter = leaf.NotAfter
				status.RenewAt = leaf.NotAfter.Add(-certRenewWindow)
				status.DaysLeft = int(time.Until(leaf.NotAfter).Hours() / 24)
			}
		}
	}
	return status, nil
}

func (s *AutoCertService) SetReleasePort80(enabled bool, unit string) error {
	state, err := loadState()
	if err != nil {
		return err
	}
	if enabled && !unitNameRegex.MatchString(unit) {
		return fmt.Errorf("需要先检测到可停止的 systemd 占用服务")
	}
	if enabled && protectedUnit(unit, cgroupUnit(os.Getpid())) {
		return fmt.Errorf("服务 %s 受保护，不能自动停止", unit)
	}
	state.ReleasePort80 = enabled
	if enabled {
		state.ReleasePort80Unit = unit
	} else {
		state.ReleasePort80Unit = ""
	}
	return saveState(state)
}

// ApproveReleasePort80 approves the service currently holding port 80. It is
// used by the CLI flag so a later renewal never stops a service the operator
// did not explicitly approve.
func (s *AutoCertService) ApproveReleasePort80() error {
	occ := detectPort80Occupant()
	if occ == nil {
		return fmt.Errorf("未检测到占用 80 端口的进程")
	}
	if !occ.CanStop {
		return fmt.Errorf("%s", occ.Reason)
	}
	return s.SetReleasePort80(true, occ.Unit)
}
