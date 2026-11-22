package service

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alireza0/x-ui/config"
	"github.com/alireza0/x-ui/database"
	"github.com/alireza0/x-ui/database/model"
	"github.com/alireza0/x-ui/logger"
	"github.com/alireza0/x-ui/util/common"
	"github.com/alireza0/x-ui/util/sys"
	"github.com/alireza0/x-ui/xray"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/sensors"
)

type ProcessState string

const (
	Running ProcessState = "Running"
	Stop    ProcessState = "Stop"
	Error   ProcessState = "Error"
)

type TempSensor struct {
	Name string  `json:"name"`
	Temp float64 `json:"temp"`
}

type Status struct {
	T        time.Time `json:"-"`
	Cpu      float64   `json:"cpu"`
	CpuCount int       `json:"cpuCount"`
	Mem      struct {
		Current   uint64 `json:"current"`
		Total     uint64 `json:"total"`
		Cached    uint64 `json:"cached"`
		Available uint64 `json:"available"`
	} `json:"mem"`
	Swap struct {
		Current uint64 `json:"current"`
		Total   uint64 `json:"total"`
	} `json:"swap"`
	Disk struct {
		Current uint64 `json:"current"`
		Total   uint64 `json:"total"`
	} `json:"disk"`
	Xray struct {
		State    ProcessState `json:"state"`
		ErrorMsg string       `json:"errorMsg"`
		Version  string       `json:"version"`
	} `json:"xray"`
	Uptime   uint64    `json:"uptime"`
	Loads    []float64 `json:"loads"`
	TcpCount int       `json:"tcpCount"`
	UdpCount int       `json:"udpCount"`
	NetIO    struct {
		Up   uint64 `json:"up"`
		Down uint64 `json:"down"`
	} `json:"netIO"`
	NetTraffic struct {
		Sent uint64 `json:"sent"`
		Recv uint64 `json:"recv"`
	} `json:"netTraffic"`
	AppStats struct {
		Threads uint32 `json:"threads"`
		Mem     uint64 `json:"mem"`
		Uptime  uint64 `json:"uptime"`
	} `json:"appStats"`
	Temps    []TempSensor `json:"temps"`
	HostInfo struct {
		HostName string `json:"hostname"`
		Ipv4     string `json:"ipv4"`
		Ipv6     string `json:"ipv6"`
	} `json:"hostInfo"`
}

type Release struct {
	TagName string `json:"tag_name"`
}

// minXrayVersion is the oldest release offered in the panel.
const minXrayVersion = "v26.4.25"

var xrayVersionPattern = regexp.MustCompile(`^v[0-9][0-9A-Za-z.\-]*$`)

func versionAtLeast(version, minimum string) bool {
	a, okA := parseVersion(version)
	b, okB := parseVersion(minimum)
	if !okA || !okB {
		return false
	}
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x > y
		}
	}
	return true
}

func parseVersion(version string) ([]int, bool) {
	if !xrayVersionPattern.MatchString(version) {
		return nil, false
	}
	core := strings.TrimPrefix(version, "v")
	if i := strings.IndexAny(core, "-+"); i >= 0 {
		core = core[:i]
	}
	parts := strings.Split(core, ".")
	nums := make([]int, 0, len(parts))
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, false
		}
		nums = append(nums, n)
	}
	return nums, true
}

type ServerService struct {
	xrayService        XrayService
	inboundService     InboundService
	outboundService    OutboundService
	routingRuleService RoutingRuleService
}

func (s *ServerService) GetStatus(lastStatus *Status) *Status {
	now := time.Now()
	status := &Status{
		T: now,
	}

	percents, err := cpu.Percent(0, false)
	if err != nil {
		logger.Warning("get cpu percent failed:", err)
	} else {
		status.Cpu = percents[0]
	}

	upTime, err := host.Uptime()
	if err != nil {
		logger.Warning("get uptime failed:", err)
	} else {
		status.Uptime = upTime
	}

	memInfo, err := mem.VirtualMemory()
	if err != nil {
		logger.Warning("get virtual memory failed:", err)
	} else {
		status.Mem.Current = memInfo.Used
		status.Mem.Total = memInfo.Total
		status.Mem.Cached = memInfo.Cached
		status.Mem.Available = memInfo.Available
	}

	swapInfo, err := mem.SwapMemory()
	if err != nil {
		logger.Warning("get swap memory failed:", err)
	} else {
		status.Swap.Current = swapInfo.Used
		status.Swap.Total = swapInfo.Total
	}

	distInfo, err := disk.Usage("/")
	if err != nil {
		logger.Warning("get dist usage failed:", err)
	} else {
		status.Disk.Current = distInfo.Used
		status.Disk.Total = distInfo.Total
	}

	avgState, err := load.Avg()
	if err != nil {
		logger.Warning("get load avg failed:", err)
	} else {
		status.Loads = []float64{avgState.Load1, avgState.Load5, avgState.Load15}
	}

	ioStats, err := net.IOCounters(false)
	if err != nil {
		logger.Warning("get io counters failed:", err)
	} else if len(ioStats) > 0 {
		ioStat := ioStats[0]
		status.NetTraffic.Sent = ioStat.BytesSent
		status.NetTraffic.Recv = ioStat.BytesRecv

		if lastStatus != nil {
			duration := now.Sub(lastStatus.T)
			seconds := float64(duration) / float64(time.Second)
			up := uint64(float64(status.NetTraffic.Sent-lastStatus.NetTraffic.Sent) / seconds)
			down := uint64(float64(status.NetTraffic.Recv-lastStatus.NetTraffic.Recv) / seconds)
			status.NetIO.Up = up
			status.NetIO.Down = down
		}
	} else {
		logger.Warning("can not find io counters")
	}

	status.TcpCount, err = sys.GetTCPCount()
	if err != nil {
		logger.Warning("get tcp connections failed:", err)
	}

	status.UdpCount, err = sys.GetUDPCount()
	if err != nil {
		logger.Warning("get udp connections failed:", err)
	}

	if temps, err := sensors.SensorsTemperatures(); err == nil {
		for _, t := range temps {
			if t.Temperature <= 0 || t.Temperature >= 150 {
				continue
			}
			name := strings.TrimSpace(t.SensorKey)
			if name == "" {
				name = "sensor"
			}
			status.Temps = append(status.Temps, TempSensor{Name: name, Temp: t.Temperature})
			if len(status.Temps) == 2 {
				break
			}
		}
	}

	if s.xrayService.IsXrayRunning() {
		status.Xray.State = Running
		status.Xray.ErrorMsg = ""
	} else {
		err := s.xrayService.GetXrayErr()
		if err != nil {
			status.Xray.State = Error
		} else {
			status.Xray.State = Stop
		}
		status.Xray.ErrorMsg = s.xrayService.GetXrayResult()
	}
	status.Xray.Version = s.xrayService.GetXrayVersion()

	var rtm runtime.MemStats
	runtime.ReadMemStats(&rtm)

	status.AppStats.Mem = rtm.Sys
	status.AppStats.Threads = uint32(runtime.NumGoroutine())
	status.CpuCount = runtime.NumCPU()
	if p != nil && p.IsRunning() {
		status.AppStats.Uptime = p.GetUptime()
	} else {
		status.AppStats.Uptime = 0
	}

	status.HostInfo.HostName, _ = os.Hostname()
	var settingService SettingService
	if panelTitle, err := settingService.GetPanelTitle(); err == nil && panelTitle != "" {
		status.HostInfo.HostName = panelTitle
	}

	// get ip address
	netInterfaces, _ := net.Interfaces()
	for i := 0; i < len(netInterfaces); i++ {
		if len(netInterfaces[i].Flags) > 2 && netInterfaces[i].Flags[0] == "up" && netInterfaces[i].Flags[1] != "loopback" {
			addrs := netInterfaces[i].Addrs

			for _, address := range addrs {
				ip := strings.Split(address.Addr, "/")[0]
				if strings.Contains(ip, ".") {
					status.HostInfo.Ipv4 += ip + " "
				} else if !strings.HasPrefix(ip, "fe80:") {
					status.HostInfo.Ipv6 += ip + " "
				}
			}
		}
	}

	status.HostInfo.Ipv4 = strings.TrimSpace(status.HostInfo.Ipv4)
	status.HostInfo.Ipv6 = strings.TrimSpace(status.HostInfo.Ipv6)

	return status
}

func (s *ServerService) GetXrayVersions() ([]string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("https://api.github.com/repos/XTLS/Xray-core/releases")
	if err != nil {
		logger.Warning("fetch xray versions failed, using default:", err)
		return []string{xray.DefaultVersion}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logger.Warning("fetch xray versions failed, status:", resp.StatusCode)
		return []string{xray.DefaultVersion}, nil
	}

	releases := make([]Release, 0)
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		logger.Warning("parse xray versions failed, using default:", err)
		return []string{xray.DefaultVersion}, nil
	}

	versions := make([]string, 0, len(releases))
	for _, release := range releases {
		if versionAtLeast(release.TagName, minXrayVersion) {
			versions = append(versions, release.TagName)
		}
	}
	for _, version := range versions {
		if version == xray.DefaultVersion {
			return versions, nil
		}
	}
	return append([]string{xray.DefaultVersion}, versions...), nil
}

func (s *ServerService) StopXrayService() (string error) {
	err := s.xrayService.StopXray()
	if err != nil {
		logger.Error("stop xray failed:", err)
		return err
	}

	return nil
}

func (s *ServerService) RestartXrayService() (string error) {
	s.xrayService.StopXray()
	defer func() {
		err := s.xrayService.RestartXray(true)
		if err != nil {
			logger.Error("start xray failed:", err)
		}
	}()

	return nil
}

func (s *ServerService) downloadXRay(version string) (string, error) {
	if !xrayVersionPattern.MatchString(version) {
		return "", common.NewError("invalid xray version: " + version)
	}
	osName := runtime.GOOS
	arch := runtime.GOARCH

	switch osName {
	case "darwin":
		osName = "macos"
	case "windows":
		osName = "windows"
	}

	switch arch {
	case "amd64":
		arch = "64"
	case "arm64":
		arch = "arm64-v8a"
	case "armv7":
		arch = "arm32-v7a"
	case "armv6":
		arch = "arm32-v6"
	case "armv5":
		arch = "arm32-v5"
	case "386":
		arch = "32"
	case "s390x":
		arch = "s390x"
	}

	fileName := fmt.Sprintf("Xray-%s-%s.zip", osName, arch)
	url := fmt.Sprintf("https://github.com/XTLS/Xray-core/releases/download/%s/%s", version, fileName)
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", common.NewErrorf("download xray %s failed: HTTP %d", version, resp.StatusCode)
	}

	os.Remove(fileName)
	file, err := os.Create(fileName)
	if err != nil {
		return "", err
	}
	defer file.Close()

	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return "", err
	}

	return fileName, nil
}

func (s *ServerService) UpdateXray(version string) error {
	// 1. Stop xray before doing anything
	if err := s.StopXrayService(); err != nil {
		logger.Warning("failed to stop xray before update:", err)
	}

	restarted := false
	defer func() {
		if restarted {
			return
		}
		if err := s.xrayService.RestartXray(true); err != nil {
			logger.Error("failed to restart xray after update error:", err)
		}
	}()

	// 2. Download the zip
	zipFileName, err := s.downloadXRay(version)
	if err != nil {
		return err
	}
	defer os.Remove(zipFileName)

	zipFile, err := os.Open(zipFileName)
	if err != nil {
		return err
	}
	defer zipFile.Close()

	stat, err := zipFile.Stat()
	if err != nil {
		return err
	}
	reader, err := zip.NewReader(zipFile, stat.Size())
	if err != nil {
		return err
	}

	// 3. Helper to extract files
	copyZipFile := func(zipName string, fileName string) error {
		zipFile, err := reader.Open(zipName)
		if err != nil {
			return err
		}
		defer zipFile.Close()
		os.MkdirAll(filepath.Dir(fileName), 0755)
		os.Remove(fileName)
		file, err := os.OpenFile(fileName, os.O_CREATE|os.O_RDWR|os.O_TRUNC, fs.ModePerm)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(file, zipFile)
		return err
	}

	// 4. Extract correct binary
	if runtime.GOOS == "windows" {
		targetBinary := filepath.Join("bin", "xray-windows-amd64.exe")
		err = copyZipFile("xray.exe", targetBinary)
	} else {
		err = copyZipFile("xray", xray.GetBinaryPath())
	}
	if err != nil {
		return err
	}

	// 5. Restart xray
	err = s.xrayService.RestartXray(true)
	restarted = true
	if err != nil {
		logger.Error("start xray failed:", err)
	}

	return err
}

func (s *ServerService) GetLogs(count string, level string, syslog string) []string {
	c, _ := strconv.Atoi(count)
	var lines []string

	if syslog == "true" {
		// Check if running on Windows - journalctl is not available
		if runtime.GOOS == "windows" {
			return []string{"Syslog is not supported on Windows. Please use application logs instead by unchecking the 'Syslog' option."}
		}

		// Validate and sanitize count parameter
		countInt, err := strconv.Atoi(count)
		if err != nil || countInt < 1 || countInt > 10000 {
			return []string{"Invalid count parameter - must be a number between 1 and 10000"}
		}

		// Validate level parameter - only allow valid syslog levels
		validLevels := map[string]bool{
			"0": true, "emerg": true,
			"1": true, "alert": true,
			"2": true, "crit": true,
			"3": true, "err": true,
			"4": true, "warning": true,
			"5": true, "notice": true,
			"6": true, "info": true,
			"7": true, "debug": true,
		}
		if !validLevels[level] {
			return []string{"Invalid level parameter - must be a valid syslog level"}
		}

		// Use hardcoded command with validated parameters
		cmd := exec.Command("journalctl", "-u", "x-ui", "--no-pager", "-n", strconv.Itoa(countInt), "-p", level)
		var out bytes.Buffer
		cmd.Stdout = &out
		err = cmd.Run()
		if err != nil {
			return []string{"Failed to run journalctl command! Make sure systemd is available and x-ui service is registered."}
		}
		lines = strings.Split(out.String(), "\n")
	} else {
		lines = logger.GetLogs(c, level)
	}

	return lines
}

func (s *ServerService) GetConfigJson() (interface{}, error) {
	config, err := s.xrayService.GetXrayConfig()
	if err != nil {
		return nil, err
	}
	contents, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, err
	}

	var jsonData interface{}
	err = json.Unmarshal(contents, &jsonData)
	if err != nil {
		return nil, err
	}

	return jsonData, nil
}

func (s *ServerService) GetDb() ([]byte, error) {
	// Export through VACUUM INTO so the download is a consistent snapshot and
	// the live file (plus its WAL) is never read directly.
	snapshotPath, cleanup, err := database.CreateSnapshot()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	return os.ReadFile(snapshotPath)
}

func (s *ServerService) ImportDB(file multipart.File) error {
	// Check if the file is a SQLite database
	isValidDb, err := database.IsSQLiteDB(file)
	if err != nil {
		return common.NewErrorf("Error checking db file format: %v", err)
	}
	if !isValidDb {
		return common.NewError("Invalid db file format")
	}

	// Reset the file reader to the beginning
	_, err = file.Seek(0, 0)
	if err != nil {
		return common.NewErrorf("Error resetting file reader: %v", err)
	}

	// Save the file as temporary file
	tempPath := fmt.Sprintf("%s.temp", config.GetDBPath())
	// Remove the existing fallback file (if any) before creating one
	_, err = os.Stat(tempPath)
	if err == nil {
		errRemove := os.Remove(tempPath)
		if errRemove != nil {
			return common.NewErrorf("Error removing existing temporary db file: %v", errRemove)
		}
	}
	// Create the temporary file
	tempFile, err := os.Create(tempPath)
	if err != nil {
		return common.NewErrorf("Error creating temporary db file: %v", err)
	}
	defer tempFile.Close()

	// Remove temp file before returning
	defer os.Remove(tempPath)

	// Save uploaded file to temporary file
	_, err = io.Copy(tempFile, file)
	if err != nil {
		return common.NewErrorf("Error saving db: %v", err)
	}

	// Close temp file before opening via sqlite
	if err = tempFile.Close(); err != nil {
		return common.NewErrorf("Error closing temporary db file: %v", err)
	}
	tempFile = nil

	// Validate integrity (no migrations / side effects)
	if err = database.ValidateSQLiteDB(tempPath); err != nil {
		return common.NewErrorf("Invalid or corrupt db file: %v", err)
	}

	// Stop Xray (ignore error but log)
	if errStop := s.StopXrayService(); errStop != nil {
		logger.Warningf("Failed to stop Xray before DB import: %v", errStop)
	}

	// Close existing DB to release file locks (especially on Windows)
	if errClose := database.CloseDB(); errClose != nil {
		logger.Warningf("Failed to close existing DB before replacement: %v", errClose)
	}

	// Remove stale WAL sidecar files so they cannot be applied to the imported DB
	for _, suffix := range []string{"-wal", "-shm"} {
		sidecar := config.GetDBPath() + suffix
		if errRemove := os.Remove(sidecar); errRemove != nil && !os.IsNotExist(errRemove) {
			logger.Warningf("Failed to remove %s: %v", sidecar, errRemove)
		}
	}

	// Backup the current database for fallback
	fallbackPath := fmt.Sprintf("%s.backup", config.GetDBPath())
	// Remove the existing fallback file (if any)
	_, err = os.Stat(fallbackPath)
	if err == nil {
		errRemove := os.Remove(fallbackPath)
		if errRemove != nil {
			return common.NewErrorf("Error removing existing fallback db file: %v", errRemove)
		}
	}
	// Move the current database to the fallback location
	err = os.Rename(config.GetDBPath(), fallbackPath)
	if err != nil {
		return common.NewErrorf("Error backing up temporary db file: %v", err)
	}

	// Remove the temporary file before returning
	defer os.Remove(fallbackPath)

	// Move temp to DB path
	err = os.Rename(tempPath, config.GetDBPath())
	if err != nil {
		errRename := os.Rename(fallbackPath, config.GetDBPath())
		if errRename != nil {
			return common.NewErrorf("Error moving db file and restoring fallback: %v", errRename)
		}
		return common.NewErrorf("Error moving db file: %v", err)
	}

	// Migrate DB
	err = database.InitDB(config.GetDBPath())
	if err != nil {
		errRename := os.Rename(fallbackPath, config.GetDBPath())
		if errRename != nil {
			return common.NewErrorf("Error migrating db and restoring fallback: %v", errRename)
		}
		return common.NewErrorf("Error migrating db: %v", err)
	}
	// Start Xray
	err = s.RestartXrayService()
	if err != nil {
		return common.NewErrorf("Imported DB but Failed to start Xray: %v", err)
	}

	return nil
}

func (s *ServerService) GetNewX25519Cert() (interface{}, error) {
	// Run the command
	cmd := exec.Command(xray.GetBinaryPath(), "x25519")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return nil, err
	}

	lines := strings.Split(out.String(), "\n")

	privateKeyLine := strings.Split(lines[0], ":")
	publicKeyLine := strings.Split(lines[1], ":")

	privateKey := strings.TrimSpace(privateKeyLine[1])
	publicKey := strings.TrimSpace(publicKeyLine[1])

	keyPair := map[string]interface{}{
		"privateKey": privateKey,
		"publicKey":  publicKey,
	}

	return keyPair, nil
}

func (s *ServerService) GetNewmldsa65() (any, error) {
	// Run the command
	cmd := exec.Command(xray.GetBinaryPath(), "mldsa65")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return nil, err
	}

	lines := strings.Split(out.String(), "\n")

	SeedLine := strings.Split(lines[0], ":")
	VerifyLine := strings.Split(lines[1], ":")

	seed := strings.TrimSpace(SeedLine[1])
	verify := strings.TrimSpace(VerifyLine[1])

	keyPair := map[string]any{
		"seed":   seed,
		"verify": verify,
	}

	return keyPair, nil
}

func (s *ServerService) GetNewEchCert(sni string) (interface{}, error) {
	// Run the command
	cmd := exec.Command(xray.GetBinaryPath(), "tls", "ech", "--serverName", sni)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return nil, err
	}

	lines := strings.Split(out.String(), "\n")
	if len(lines) < 4 {
		return nil, common.NewError("invalid ech cert")
	}

	configList := lines[1]
	serverKeys := lines[3]

	return map[string]interface{}{
		"echServerKeys": serverKeys,
		"echConfigList": configList,
	}, nil
}

func (s *ServerService) GetNewVlessEnc() (any, error) {
	cmd := exec.Command(xray.GetBinaryPath(), "vlessenc")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	lines := strings.Split(out.String(), "\n")
	var auths []map[string]string
	var current map[string]string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Authentication:") {
			if current != nil {
				auths = append(auths, current)
			}
			current = map[string]string{
				"label": strings.TrimSpace(strings.TrimPrefix(line, "Authentication:")),
			}
		} else if strings.HasPrefix(line, `"decryption"`) || strings.HasPrefix(line, `"encryption"`) {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 && current != nil {
				key := strings.Trim(parts[0], `" `)
				val := strings.Trim(parts[1], `" `)
				current[key] = val
			}
		}
	}

	if current != nil {
		auths = append(auths, current)
	}

	return map[string]any{
		"auths": auths,
	}, nil
}

type TrafficRankItem struct {
	Email string `json:"email"`
	Up    int64  `json:"up"`
	Down  int64  `json:"down"`
	Total int64  `json:"total"`
}

func (s *ServerService) GetTrafficRank(limit int) (map[string][]TrafficRankItem, error) {
	db := database.GetDB()
	now := time.Now()
	result := make(map[string][]TrafficRankItem, 3)
	for key, day := range map[string]string{
		"today":           now.Format("2006-01-02"),
		"yesterday":       now.AddDate(0, 0, -1).Format("2006-01-02"),
		"beforeYesterday": now.AddDate(0, 0, -2).Format("2006-01-02"),
	} {
		items := make([]TrafficRankItem, 0, limit)
		err := db.Model(&model.ClientTrafficDaily{}).
			Select("email, SUM(up) AS up, SUM(down) AS down").
			Where("day = ?", day).
			Group("email").
			Order("SUM(up) + SUM(down) DESC").
			Limit(limit).
			Scan(&items).Error
		if err != nil {
			return nil, err
		}
		for i := range items {
			items[i].Total = items[i].Up + items[i].Down
		}
		result[key] = items
	}
	return result, nil
}

type ClientExpiryItem struct {
	Email      string `json:"email"`
	ExpiryTime int64  `json:"expiryTime"`
	DaysLeft   int    `json:"daysLeft"`
}

func (s *ServerService) GetClientExpiry(limit int) ([]ClientExpiryItem, error) {
	db := database.GetDB()
	rows := make([]xray.ClientTraffic, 0, 64)
	err := db.Model(&xray.ClientTraffic{}).
		Select("email, expiry_time").
		Where("enable = ? AND expiry_time <> 0", true).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}

	nowMs := time.Now().UnixMilli()
	items := make([]ClientExpiryItem, 0, len(rows))
	for _, row := range rows {
		expiry := row.ExpiryTime
		if expiry < 0 {
			// negative values encode "this much time from now"
			expiry = nowMs - expiry
		}
		daysLeft := -1
		if expiry > nowMs {
			daysLeft = int((expiry - nowMs) / (24 * 3600 * 1000))
		}
		items = append(items, ClientExpiryItem{
			Email:      row.Email,
			ExpiryTime: expiry,
			DaysLeft:   daysLeft,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].ExpiryTime < items[j].ExpiryTime
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

type DashboardWidgets struct {
	TrafficRank      map[string][]TrafficRankItem `json:"trafficRank"`
	ClientExpiry     []ClientExpiryItem           `json:"clientExpiry"`
	RemainingTraffic []RemainingTrafficItem       `json:"remainingTraffic"`
}

// GetDashboardWidgets returns the dashboard side widgets in one response so the
// page can poll them with a single request.
func (s *ServerService) GetDashboardWidgets(limit int) (*DashboardWidgets, error) {
	rank, err := s.GetTrafficRank(limit)
	if err != nil {
		return nil, err
	}
	expiry, err := s.GetClientExpiry(limit)
	if err != nil {
		return nil, err
	}
	remaining, err := s.GetRemainingTraffic(limit)
	if err != nil {
		return nil, err
	}
	return &DashboardWidgets{
		TrafficRank:      rank,
		ClientExpiry:     expiry,
		RemainingTraffic: remaining,
	}, nil
}

type UpdateCheckInfo struct {
	Current     string `json:"current"`
	Latest      string `json:"latest"`
	HasUpdate   bool   `json:"hasUpdate"`
	URL         string `json:"url"`
	Notes       string `json:"notes"`
	PublishedAt string `json:"publishedAt"`
}

var (
	updateCheckMu    sync.Mutex
	updateCheckAt    time.Time
	updateCheckInfo  *UpdateCheckInfo
	updateCheckError error
)

// versionCode maps v26.08 / 26.08 to a comparable number. Legacy 1.11.4-x
// versions rank below the year-based scheme.
func versionCode(v string) int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return 0
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0
	}
	return major*100 + minor
}

func fetchLatestRelease(current string) (*UpdateCheckInfo, error) {
	client := &http.Client{Timeout: 6 * time.Second}
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/rrkai/xui-go/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "xui-go-panel")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, common.NewErrorf("release check: HTTP %d", resp.StatusCode)
	}
	var release struct {
		TagName     string `json:"tag_name"`
		HTMLURL     string `json:"html_url"`
		Body        string `json:"body"`
		PublishedAt string `json:"published_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}
	latest := strings.TrimSpace(release.TagName)
	if latest == "" {
		return nil, common.NewError("release check: empty tag")
	}
	notes := strings.TrimSpace(strings.ReplaceAll(release.Body, "\r\n", "\n"))
	if len(notes) > 20000 {
		notes = notes[:20000]
	}
	info := &UpdateCheckInfo{
		Current:     current,
		Latest:      latest,
		URL:         release.HTMLURL,
		Notes:       notes,
		PublishedAt: release.PublishedAt,
	}
	cur, lat := versionCode(current), versionCode(latest)
	if cur > 0 && lat > 0 {
		info.HasUpdate = lat > cur
	} else {
		info.HasUpdate = strings.TrimPrefix(latest, "v") != strings.TrimPrefix(current, "v")
	}
	return info, nil
}

// CheckUpdate returns the latest GitHub release. Results are cached for an
// hour (10 minutes after a failure) so dashboard loads do not hit GitHub.
func (s *ServerService) CheckUpdate() (*UpdateCheckInfo, error) {
	updateCheckMu.Lock()
	defer updateCheckMu.Unlock()
	ttl := time.Hour
	if updateCheckError != nil {
		ttl = 10 * time.Minute
	}
	if !updateCheckAt.IsZero() && time.Since(updateCheckAt) < ttl {
		return updateCheckInfo, updateCheckError
	}
	updateCheckAt = time.Now()
	updateCheckInfo, updateCheckError = fetchLatestRelease(config.GetVersion())
	return updateCheckInfo, updateCheckError
}

var panelUpdateMu sync.Mutex
var panelUpdateRunning bool

func downloadInstallScript() ([]byte, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	sources := []struct {
		url    string
		accept string
	}{
		{"https://raw.githubusercontent.com/rrkai/xui-go/main/install.sh", ""},
		{"https://api.github.com/repos/rrkai/xui-go/contents/install.sh?ref=main", "application/vnd.github.raw"},
	}
	var lastErr error
	for _, source := range sources {
		req, err := http.NewRequest(http.MethodGet, source.url, nil)
		if err != nil {
			lastErr = err
			continue
		}
		if source.accept != "" {
			req.Header.Set("Accept", source.accept)
		}
		req.Header.Set("User-Agent", "xui-go-panel")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = common.NewErrorf("install script: HTTP %d", resp.StatusCode)
			continue
		}
		if !strings.HasPrefix(string(body), "#!/bin/bash") {
			lastErr = common.NewError("install script: unexpected content")
			continue
		}
		return body, nil
	}
	if lastErr == nil {
		lastErr = common.NewError("install script: no source available")
	}
	return nil, lastErr
}

// StartPanelUpdate downloads install.sh and runs it in a transient systemd
// unit. The unit lives outside the x-ui service cgroup, so the updater
// survives the service restart and the panel comes back on the new release.
func (s *ServerService) StartPanelUpdate() error {
	panelUpdateMu.Lock()
	defer panelUpdateMu.Unlock()
	if panelUpdateRunning {
		return common.NewError("panel update is already running")
	}
	if runtime.GOOS != "linux" {
		return common.NewError("panel update is only supported on Linux")
	}
	if exe, err := os.Executable(); err != nil || filepath.Clean(exe) != "/usr/local/x-ui/x-ui" {
		return common.NewError("panel update is only available for the standard /usr/local/x-ui installation")
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return common.NewError("panel update requires systemd; run 'x-ui update' over SSH instead")
	}
	if err := exec.Command("systemctl", "is-active", "--quiet", "x-ui").Run(); err != nil {
		return common.NewError("panel update requires the x-ui systemd service to be running")
	}
	script, err := downloadInstallScript()
	if err != nil {
		return err
	}
	scriptPath := fmt.Sprintf("/tmp/xui-panel-update-%d.sh", time.Now().Unix())
	if err := os.WriteFile(scriptPath, script, 0700); err != nil {
		return err
	}
	unit := fmt.Sprintf("xui-panel-update-%d", time.Now().Unix())
	command := fmt.Sprintf("umask 077; rm -f /var/log/x-ui-update.log; bash %s </dev/null >/var/log/x-ui-update.log 2>&1; rm -f %s", scriptPath, scriptPath)
	out, err := exec.Command("systemd-run", "--unit="+unit, "--collect", "--no-block",
		"/bin/bash", "-lc", command).CombinedOutput()
	if err != nil {
		os.Remove(scriptPath)
		return common.NewErrorf("failed to start updater: %v %s", err, strings.TrimSpace(string(out)))
	}
	panelUpdateRunning = true
	go func() {
		time.Sleep(15 * time.Minute)
		panelUpdateMu.Lock()
		panelUpdateRunning = false
		panelUpdateMu.Unlock()
	}()
	logger.Info("panel update started: ", unit)
	return nil
}

type RemainingTrafficItem struct {
	Email     string  `json:"email"`
	Total     int64   `json:"total"`
	Used      int64   `json:"used"`
	Remaining int64   `json:"remaining"`
	Percent   float64 `json:"percent"`
}

func (s *ServerService) GetRemainingTraffic(limit int) ([]RemainingTrafficItem, error) {
	db := database.GetDB()
	rows := make([]xray.ClientTraffic, 0, 64)
	err := db.Model(&xray.ClientTraffic{}).
		Select("email, total, up, down").
		Where("total > 0").
		Order("(total - up - down) ASC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}

	items := make([]RemainingTrafficItem, 0, len(rows))
	for _, row := range rows {
		used := row.Up + row.Down
		remaining := row.Total - used
		percent := 0.0
		if row.Total > 0 {
			percent = float64(remaining) / float64(row.Total) * 100
		}
		items = append(items, RemainingTrafficItem{
			Email:     row.Email,
			Total:     row.Total,
			Used:      used,
			Remaining: remaining,
			Percent:   percent,
		})
	}
	return items, nil
}
