package entity

import (
	"crypto/tls"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/alireza0/x-ui/util/common"
	"github.com/alireza0/x-ui/util/cronspec"
)

type Msg struct {
	Success bool        `json:"success"`
	Msg     string      `json:"msg"`
	Obj     interface{} `json:"obj"`
}

type AllSetting struct {
	WebListen          string `json:"webListen" form:"webListen"`
	WebDomain          string `json:"webDomain" form:"webDomain"`
	WebPort            int    `json:"webPort" form:"webPort"`
	WebCertFile        string `json:"webCertFile" form:"webCertFile"`
	WebKeyFile         string `json:"webKeyFile" form:"webKeyFile"`
	WebBasePath        string `json:"webBasePath" form:"webBasePath"`
	SessionMaxAge      int    `json:"sessionMaxAge" form:"sessionMaxAge"`
	PageSize           int    `json:"pageSize" form:"pageSize"`
	ExpireDiff         int    `json:"expireDiff" form:"expireDiff"`
	TrafficDiff        int    `json:"trafficDiff" form:"trafficDiff"`
	RemarkModel        string `json:"remarkModel" form:"remarkModel"`
	OutboundTestUrl    string `json:"outboundTestUrl" form:"outboundTestUrl"`
	TgBotEnable        bool   `json:"tgBotEnable" form:"tgBotEnable"`
	TgBotToken         string `json:"tgBotToken" form:"tgBotToken"`
	TgBotChatId        string `json:"tgBotChatId" form:"tgBotChatId"`
	TgRunTime          string `json:"tgRunTime" form:"tgRunTime"`
	TgBotBackup        bool   `json:"tgBotBackup" form:"tgBotBackup"`
	TgBotLoginNotify   bool   `json:"tgBotLoginNotify" form:"tgBotLoginNotify"`
	TgCpu              int    `json:"tgCpu" form:"tgCpu"`
	TgLang             string `json:"tgLang" form:"tgLang"`
	TimeLocation       string `json:"timeLocation" form:"timeLocation"`
	SubEnable          bool   `json:"subEnable" form:"subEnable"`
	SubListen          string `json:"subListen" form:"subListen"`
	SubPort            int    `json:"subPort" form:"subPort"`
	SubPath            string `json:"subPath" form:"subPath"`
	SubDomain          string `json:"subDomain" form:"subDomain"`
	SubCertFile        string `json:"subCertFile" form:"subCertFile"`
	SubKeyFile         string `json:"subKeyFile" form:"subKeyFile"`
	SubUpdates         int    `json:"subUpdates" form:"subUpdates"`
	SubEncrypt         bool   `json:"subEncrypt" form:"subEncrypt"`
	SubShowInfo        bool   `json:"subShowInfo" form:"subShowInfo"`
	SubURI             string `json:"subURI" form:"subURI"`
	SubJsonPath        string `json:"subJsonPath" form:"subJsonPath"`
	SubJsonURI         string `json:"subJsonURI" form:"subJsonURI"`
	SubJsonMux         string `json:"subJsonMux" form:"subJsonMux"`
	SubJsonRules       string `json:"subJsonRules" form:"subJsonRules"`
	IpBlockAfterRemove bool   `json:"ipBlockAfterRemove" form:"ipBlockAfterRemove"`
	BackupEnable       bool   `json:"backupEnable" form:"backupEnable"`
	BackupTime         string `json:"backupTime" form:"backupTime"`
	BackupFrequency    string `json:"backupFrequency" form:"backupFrequency"`
	BackupRetention    int    `json:"backupRetention" form:"backupRetention"`
	BackupType         string `json:"backupType" form:"backupType"`
	BackupDir          string `json:"backupDir" form:"backupDir"`
	BackupWebdavUrl    string `json:"backupWebdavUrl" form:"backupWebdavUrl"`
	BackupWebdavUser   string `json:"backupWebdavUser" form:"backupWebdavUser"`
	BackupWebdavPass   string `json:"backupWebdavPass" form:"backupWebdavPass"`
	BackupFtpHost      string `json:"backupFtpHost" form:"backupFtpHost"`
	BackupFtpPort      int    `json:"backupFtpPort" form:"backupFtpPort"`
	BackupFtpUser      string `json:"backupFtpUser" form:"backupFtpUser"`
	BackupFtpPass      string `json:"backupFtpPass" form:"backupFtpPass"`
	BackupFtpDir       string `json:"backupFtpDir" form:"backupFtpDir"`
	BackupS3Endpoint   string `json:"backupS3Endpoint" form:"backupS3Endpoint"`
	BackupS3Region     string `json:"backupS3Region" form:"backupS3Region"`
	BackupS3Bucket     string `json:"backupS3Bucket" form:"backupS3Bucket"`
	BackupS3AccessKey  string `json:"backupS3AccessKey" form:"backupS3AccessKey"`
	BackupS3SecretKey  string `json:"backupS3SecretKey" form:"backupS3SecretKey"`
	BackupS3Prefix     string `json:"backupS3Prefix" form:"backupS3Prefix"`
}

func (s *AllSetting) CheckValid() error {
	if s.WebListen != "" {
		ip := net.ParseIP(s.WebListen)
		if ip == nil {
			return common.NewError("web listen is not valid ip:", s.WebListen)
		}
	}

	if s.SubListen != "" {
		ip := net.ParseIP(s.SubListen)
		if ip == nil {
			return common.NewError("Sub listen is not valid ip:", s.SubListen)
		}
	}

	if s.WebPort <= 0 || s.WebPort > 65535 {
		return common.NewError("web port is not a valid port:", s.WebPort)
	}

	if s.SubPort <= 0 || s.SubPort > 65535 {
		return common.NewError("Sub port is not a valid port:", s.SubPort)
	}

	if s.SubPort == s.WebPort {
		return common.NewError("Sub and Web could not use same port:", s.SubPort)
	}

	if s.WebCertFile != "" || s.WebKeyFile != "" {
		_, err := tls.LoadX509KeyPair(s.WebCertFile, s.WebKeyFile)
		if err != nil {
			return common.NewErrorf("cert file <%v> or key file <%v> invalid: %v", s.WebCertFile, s.WebKeyFile, err)
		}
	}

	if s.SubCertFile != "" || s.SubKeyFile != "" {
		_, err := tls.LoadX509KeyPair(s.SubCertFile, s.SubKeyFile)
		if err != nil {
			return common.NewErrorf("cert file <%v> or key file <%v> invalid: %v", s.SubCertFile, s.SubKeyFile, err)
		}
	}

	if !strings.HasPrefix(s.WebBasePath, "/") {
		s.WebBasePath = "/" + s.WebBasePath
	}
	if !strings.HasSuffix(s.WebBasePath, "/") {
		s.WebBasePath += "/"
	}

	if !strings.HasPrefix(s.SubPath, "/") {
		s.SubPath = "/" + s.SubPath
	}
	if !strings.HasSuffix(s.SubPath, "/") {
		s.SubPath += "/"
	}

	if !strings.HasPrefix(s.SubJsonPath, "/") {
		s.SubJsonPath = "/" + s.SubJsonPath
	}
	if !strings.HasSuffix(s.SubJsonPath, "/") {
		s.SubJsonPath += "/"
	}

	// Older clients do not send these fields; keep their previous behavior by
	// falling back to the documented defaults instead of rejecting the request.
	if s.BackupTime == "" {
		s.BackupTime = "05:00"
	}
	if s.BackupFrequency == "" {
		s.BackupFrequency = "daily"
	}
	if s.BackupRetention == 0 {
		s.BackupRetention = 3
	}
	if s.BackupType == "" {
		s.BackupType = "local"
	}
	if s.BackupDir == "" {
		s.BackupDir = "/XUI-GO/Backup"
	}
	if s.BackupFtpPort == 0 {
		s.BackupFtpPort = 21
	}
	if s.BackupFtpDir == "" {
		s.BackupFtpDir = "/"
	}
	if s.BackupS3Region == "" {
		s.BackupS3Region = "us-east-1"
	}

	if _, err := time.Parse("15:04", s.BackupTime); err != nil {
		return common.NewError("backup time is not valid, expected HH:MM:", s.BackupTime)
	}

	switch s.BackupFrequency {
	case "daily", "weekly", "monthly", "halfhourly", "hourly":
	default:
		return common.NewError("backup frequency is not valid:", s.BackupFrequency)
	}

	if s.BackupRetention < 1 || s.BackupRetention > 365 {
		return common.NewError("backup retention must be between 1 and 365:", s.BackupRetention)
	}

	backupTypes := make([]string, 0, 3)
	seenTypes := map[string]bool{}
	for _, raw := range strings.Split(s.BackupType, ",") {
		backupType := strings.TrimSpace(raw)
		if backupType == "" || seenTypes[backupType] {
			continue
		}
		switch backupType {
		case "local":
			if strings.TrimSpace(s.BackupDir) == "" {
				return common.NewError("backup directory is required")
			}
		case "webdav":
			u, err := url.Parse(strings.TrimSpace(s.BackupWebdavUrl))
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return common.NewError("backup WebDAV URL is not valid:", s.BackupWebdavUrl)
			}
		case "ftp":
			if strings.TrimSpace(s.BackupFtpHost) == "" {
				return common.NewError("backup FTP host is required")
			}
			if s.BackupFtpPort <= 0 || s.BackupFtpPort > 65535 {
				return common.NewError("backup FTP port is not valid:", s.BackupFtpPort)
			}
		case "s3":
			u, err := url.Parse(strings.TrimSpace(s.BackupS3Endpoint))
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return common.NewError("backup S3 endpoint is not valid:", s.BackupS3Endpoint)
			}
			if strings.Trim(u.Path, "/") != "" {
				return common.NewError("backup S3 endpoint must not contain a path:", s.BackupS3Endpoint)
			}
			if strings.TrimSpace(s.BackupS3Bucket) == "" {
				return common.NewError("backup S3 bucket is required")
			}
			if strings.TrimSpace(s.BackupS3AccessKey) == "" || strings.TrimSpace(s.BackupS3SecretKey) == "" {
				return common.NewError("backup S3 access key and secret key are required")
			}
		default:
			return common.NewError("backup type is not valid:", backupType)
		}
		seenTypes[backupType] = true
		backupTypes = append(backupTypes, backupType)
	}
	if len(backupTypes) == 0 {
		return common.NewError("at least one backup type is required")
	}
	s.BackupType = strings.Join(backupTypes, ",")

	if _, err := cronspec.Parse(s.TgRunTime); err != nil {
		return err
	}

	_, err := time.LoadLocation(s.TimeLocation)
	if err != nil {
		return common.NewError("time location not exist:", s.TimeLocation)
	}

	return nil
}
