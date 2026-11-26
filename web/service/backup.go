package service

import (
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alireza0/x-ui/database"
	"github.com/alireza0/x-ui/logger"
	"github.com/alireza0/x-ui/util/common"

	"github.com/jlaffaye/ftp"
)

const (
	backupFilePrefix = "x-ui-"
	backupFileSuffix = ".db"
	webdavPropfind   = `<?xml version="1.0" encoding="utf-8"?><d:propfind xmlns:d="DAV:"><d:prop><d:displayname/></d:prop></d:propfind>`
)

type AutoBackupService struct {
	settingService SettingService
}

// RunIfDue creates a database backup when the current minute matches the
// configured backup time and frequency. It is safe to call every minute.
func (s *AutoBackupService) RunIfDue(now time.Time) error {
	enabled, err := s.settingService.GetBackupEnable()
	if err != nil || !enabled {
		return err
	}
	location, err := s.settingService.GetTimeLocation()
	if err != nil {
		return err
	}
	local := now.In(location)
	backupTime, err := s.settingService.GetBackupTime()
	if err != nil {
		return err
	}
	hour, minute, ok := parseClock(backupTime)
	if !ok {
		return fmt.Errorf("invalid backup time: %s", backupTime)
	}
	frequency, err := s.settingService.GetBackupFrequency()
	if err != nil {
		return err
	}
	switch frequency {
	case "halfhourly":
		if local.Minute() != 0 && local.Minute() != 30 {
			return nil
		}
	case "hourly":
		if local.Minute() != 0 {
			return nil
		}
	case "weekly":
		if local.Hour() != hour || local.Minute() != minute || local.Weekday() != time.Monday {
			return nil
		}
	case "monthly":
		if local.Hour() != hour || local.Minute() != minute || local.Day() != 1 {
			return nil
		}
	default:
		if local.Hour() != hour || local.Minute() != minute {
			return nil
		}
	}
	runKey := local.Format("2006-01-02 15:04")
	lastRun, err := s.settingService.GetBackupLastRun()
	if err != nil {
		return err
	}
	if lastRun == runKey {
		return nil
	}
	name := backupFilePrefix + local.Format("20060102-150405") + backupFileSuffix
	if err := s.createBackup(name); err != nil {
		return err
	}
	if err := s.settingService.SetBackupLastRun(runKey); err != nil {
		logger.Warning("auto backup: save last run failed:", err)
	}
	return nil
}

func (s *AutoBackupService) createBackup(name string) error {
	backupType, err := s.settingService.GetBackupType()
	if err != nil {
		return err
	}
	types := splitBackupTypes(backupType)
	if len(types) == 0 {
		return fmt.Errorf("no backup type configured")
	}
	retention, err := s.settingService.GetBackupRetention()
	if err != nil {
		return err
	}
	if retention < 1 {
		retention = 1
	}
	tmp, cleanup, err := vacuumToTemp(name)
	if err != nil {
		return err
	}
	defer cleanup()
	errs := make([]error, 0, len(types))
	for _, targetType := range types {
		switch targetType {
		case "local":
			dir, err := s.settingService.GetBackupDir()
			if err != nil {
				errs = append(errs, err)
				continue
			}
			dir = strings.TrimSpace(dir)
			if dir == "" {
				errs = append(errs, fmt.Errorf("backup directory is empty"))
				continue
			}
			path := filepath.Join(dir, name)
			if err := copyLocalBackup(tmp, path); err != nil {
				logger.Warning("auto backup: write local backup failed:", err)
				errs = append(errs, err)
				continue
			}
			logger.Info("auto backup: created", path)
			if err := pruneLocalBackups(dir, retention); err != nil {
				logger.Warning("auto backup: prune local backups failed:", err)
			}
		case "webdav":
			if err := s.uploadWebDAV(tmp, name, retention); err != nil {
				logger.Warning("auto backup: upload WebDAV failed:", err)
				errs = append(errs, err)
			}
		case "ftp":
			if err := s.uploadFTP(tmp, name, retention); err != nil {
				logger.Warning("auto backup: upload FTP failed:", err)
				errs = append(errs, err)
			}
		case "s3":
			if err := s.uploadS3(tmp, name, retention); err != nil {
				logger.Warning("auto backup: upload S3 failed:", err)
				errs = append(errs, err)
			}
		default:
			errs = append(errs, fmt.Errorf("unknown backup type: %s", targetType))
		}
	}
	return common.Combine(errs...)
}

func splitBackupTypes(value string) []string {
	seen := map[string]bool{}
	types := make([]string, 0, 3)
	for _, raw := range strings.Split(value, ",") {
		backupType := strings.TrimSpace(raw)
		if backupType == "" || seen[backupType] {
			continue
		}
		seen[backupType] = true
		types = append(types, backupType)
	}
	return types
}

func copyLocalBackup(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

func parseClock(value string) (int, int, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	hour, err1 := strconv.Atoi(parts[0])
	minute, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, false
	}
	return hour, minute, true
}

func vacuumInto(target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("backup target already exists: %s", target)
	}
	escaped := strings.ReplaceAll(target, "'", "''")
	if err := database.GetDB().Exec(fmt.Sprintf("VACUUM INTO '%s'", escaped)).Error; err != nil {
		return err
	}
	return os.Chmod(target, 0600)
}

func vacuumToTemp(name string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "xui-backup-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	target := filepath.Join(dir, name)
	if err := vacuumInto(target); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return target, cleanup, nil
}

func isBackupFileName(name string) bool {
	return strings.HasPrefix(name, backupFilePrefix) && strings.HasSuffix(name, backupFileSuffix)
}

func pruneLocalBackups(dir string, retention int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && isBackupFileName(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if len(names) <= retention {
		return nil
	}
	for _, name := range names[retention:] {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			logger.Warning("auto backup: remove old backup failed:", name, err)
		}
	}
	return nil
}

func (s *AutoBackupService) uploadWebDAV(filePath, name string, retention int) error {
	base, err := s.settingService.GetBackupWebdavUrl()
	if err != nil {
		return err
	}
	user, err := s.settingService.GetBackupWebdavUser()
	if err != nil {
		return err
	}
	pass, err := s.settingService.GetBackupWebdavPass()
	if err != nil {
		return err
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return fmt.Errorf("WebDAV URL is empty")
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	target, err := url.JoinPath(base, name)
	if err != nil {
		return err
	}
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPut, target, file)
	if err != nil {
		return err
	}
	req.ContentLength = stat.Size()
	req.SetBasicAuth(user, pass)
	req.Header.Set("Content-Type", "application/octet-stream")
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("WebDAV upload failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	logger.Info("auto backup: uploaded to WebDAV:", target)
	if err := pruneWebDAV(base, user, pass, retention); err != nil {
		logger.Warning("auto backup: prune WebDAV backups failed:", err)
	}
	return nil
}

type davMultiStatus struct {
	Responses []struct {
		Href string `xml:"href"`
	} `xml:"response"`
}

func pruneWebDAV(base, user, pass string, retention int) error {
	req, err := http.NewRequest("PROPFIND", base, strings.NewReader(webdavPropfind))
	if err != nil {
		return err
	}
	req.SetBasicAuth(user, pass)
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	client := &http.Client{Timeout: time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("WebDAV PROPFIND failed: %s", resp.Status)
	}
	var status davMultiStatus
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&status); err != nil {
		return err
	}
	type entry struct {
		name string
		href string
	}
	entries := make([]entry, 0, len(status.Responses))
	for _, response := range status.Responses {
		href := strings.TrimSpace(response.Href)
		name := path.Base(href)
		if decoded, err := url.PathUnescape(name); err == nil {
			name = decoded
		}
		if isBackupFileName(name) {
			entries = append(entries, entry{name: name, href: href})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name > entries[j].name })
	baseURL, err := url.Parse(base)
	if err != nil {
		return err
	}
	for _, item := range entries[min(retention, len(entries)):] {
		deleteURL, err := resolveHref(baseURL, item.href)
		if err != nil {
			logger.Warning("auto backup: resolve WebDAV href failed:", item.href, err)
			continue
		}
		if err := deleteWebDAV(deleteURL, user, pass); err != nil {
			logger.Warning("auto backup: delete WebDAV backup failed:", deleteURL, err)
		}
	}
	return nil
}

func resolveHref(base *url.URL, href string) (string, error) {
	parsed, err := url.Parse(href)
	if err != nil {
		return "", err
	}
	if parsed.IsAbs() {
		return parsed.String(), nil
	}
	return base.ResolveReference(parsed).String(), nil
}

func deleteWebDAV(target, user, pass string) error {
	req, err := http.NewRequest(http.MethodDelete, target, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(user, pass)
	client := &http.Client{Timeout: time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("DELETE failed: %s", resp.Status)
	}
	return nil
}

func (s *AutoBackupService) uploadFTP(filePath, name string, retention int) error {
	host, err := s.settingService.GetBackupFtpHost()
	if err != nil {
		return err
	}
	port, err := s.settingService.GetBackupFtpPort()
	if err != nil {
		return err
	}
	user, err := s.settingService.GetBackupFtpUser()
	if err != nil {
		return err
	}
	pass, err := s.settingService.GetBackupFtpPass()
	if err != nil {
		return err
	}
	dir, err := s.settingService.GetBackupFtpDir()
	if err != nil {
		return err
	}
	if port <= 0 || port > 65535 {
		port = 21
	}
	conn, err := ftp.Dial(net.JoinHostPort(strings.TrimSpace(host), strconv.Itoa(port)),
		ftp.DialWithTimeout(15*time.Second))
	if err != nil {
		return err
	}
	defer conn.Quit()
	if user == "" {
		user = "anonymous"
		if pass == "" {
			pass = "anonymous@"
		}
	}
	if err := conn.Login(user, pass); err != nil {
		return err
	}
	if dir = strings.TrimSpace(dir); dir != "" && dir != "/" {
		if err := conn.ChangeDir(dir); err != nil {
			return err
		}
	}
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := conn.Stor(name, file); err != nil {
		return err
	}
	logger.Info("auto backup: uploaded to FTP:", name)
	if err := pruneFTP(conn, retention); err != nil {
		logger.Warning("auto backup: prune FTP backups failed:", err)
	}
	return nil
}

func pruneFTP(conn *ftp.ServerConn, retention int) error {
	entries, err := conn.List(".")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type == ftp.EntryTypeFile && isBackupFileName(entry.Name) {
			names = append(names, entry.Name)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if len(names) <= retention {
		return nil
	}
	for _, name := range names[retention:] {
		if err := conn.Delete(name); err != nil {
			logger.Warning("auto backup: delete FTP backup failed:", name, err)
		}
	}
	return nil
}
