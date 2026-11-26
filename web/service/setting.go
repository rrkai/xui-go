package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alireza0/x-ui/config"
	"github.com/alireza0/x-ui/database"
	"github.com/alireza0/x-ui/database/model"
	"github.com/alireza0/x-ui/logger"
	"github.com/alireza0/x-ui/util/common"
	"github.com/alireza0/x-ui/util/random"
	"github.com/alireza0/x-ui/util/reflect_util"
	"github.com/alireza0/x-ui/web/entity"
)

var defaultValueMap = map[string]string{
	"xrayTemplateConfig": config.GetDefaultXrayTemplate(),
	"webListen":          "",
	"webDomain":          "",
	"webPort":            "54321",
	"webCertFile":        "",
	"webKeyFile":         "",
	"secret":             random.Seq(32),
	"webBasePath":        "/",
	"syncRole":           "",
	"syncMasterName":     "",
	"syncAutoInterval":   "0",
	"panelUUID":          "",
	"panelTitle":         "",
	"sessionMaxAge":      "180",
	"pageSize":           "50",
	"expireDiff":         "0",
	"trafficDiff":        "0",
	"remarkModel":        "-ieo",
	"outboundTestUrl":    "https://www.gstatic.com/generate_204",
	"timeLocation":       "Asia/Hong_Kong",
	"tgBotEnable":        "false",
	"tgBotToken":         "",
	"tgBotChatId":        "",
	"tgRunTime":          "@daily",
	"tgBotBackup":        "false",
	"tgBotLoginNotify":   "false",
	"tgCpu":              "0",
	"tgLang":             "zh-Hans",
	"subEnable":          "false",
	"subListen":          "",
	"subPort":            "2096",
	"subPath":            "/sub/",
	"subDomain":          "",
	"subCertFile":        "",
	"subKeyFile":         "",
	"subUpdates":         "12",
	"subEncrypt":         "true",
	"subShowInfo":        "false",
	"subURI":             "",
	"subJsonPath":        "/json/",
	"subJsonURI":         "",
	"subJsonMux":         "",
	"subJsonRules":       "",
	"warp":               "",
	"ipBlockAfterRemove": "true",
	"backupEnable":       "false",
	"backupTime":         "05:00",
	"backupFrequency":    "daily",
	"backupRetention":    "3",
	"backupType":         "local",
	"backupDir":          "/XUI-GO/Backup",
	"backupWebdavUrl":    "",
	"backupWebdavUser":   "",
	"backupWebdavPass":   "",
	"backupFtpHost":      "",
	"backupFtpPort":      "21",
	"backupFtpUser":      "",
	"backupFtpPass":      "",
	"backupFtpDir":       "/",
	"backupS3Endpoint":   "",
	"backupS3Region":     "us-east-1",
	"backupS3Bucket":     "",
	"backupS3AccessKey":  "",
	"backupS3SecretKey":  "",
	"backupS3Prefix":     "",
	"backupLastRun":      "",
}

func init() {
	// New databases follow the host time zone; the settings page shows this
	// value as the "local" preset.
	if tz := GetHostTimeZone(); tz != "" {
		defaultValueMap["timeLocation"] = tz
	}
}

type SettingService struct{}

func (s *SettingService) GetAllSetting() (*entity.AllSetting, error) {
	db := database.GetDB()
	settings := make([]*model.Setting, 0)
	err := db.Model(model.Setting{}).Not("key = ?", "xrayTemplateConfig").Find(&settings).Error
	if err != nil {
		return nil, err
	}
	allSetting := &entity.AllSetting{}
	t := reflect.TypeOf(allSetting).Elem()
	v := reflect.ValueOf(allSetting).Elem()
	fields := reflect_util.GetFields(t)

	setSetting := func(key, value string) (err error) {
		defer func() {
			panicErr := recover()
			if panicErr != nil {
				err = errors.New(fmt.Sprint(panicErr))
			}
		}()

		var found bool
		var field reflect.StructField
		for _, f := range fields {
			if f.Tag.Get("json") == key {
				field = f
				found = true
				break
			}
		}

		if !found {
			// Some settings are automatically generated, no need to return to the front end to modify the user
			return nil
		}

		fieldV := v.FieldByName(field.Name)
		switch t := fieldV.Interface().(type) {
		case int:
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return err
			}
			fieldV.SetInt(n)
		case string:
			fieldV.SetString(value)
		case bool:
			fieldV.SetBool(value == "true")
		default:
			return common.NewErrorf("unknown field %v type %v", key, t)
		}
		return
	}

	keyMap := map[string]bool{}
	for _, setting := range settings {
		err := setSetting(setting.Key, setting.Value)
		if err != nil {
			return nil, err
		}
		keyMap[setting.Key] = true
	}

	for key, value := range defaultValueMap {
		if keyMap[key] {
			continue
		}
		err := setSetting(key, value)
		if err != nil {
			return nil, err
		}
	}

	return allSetting, nil
}

func (s *SettingService) ResetSettings() error {
	db := database.GetDB()
	return db.Where("1 = 1").Delete(model.Setting{}).Error
}

func (s *SettingService) getSetting(key string) (*model.Setting, error) {
	db := database.GetDB()
	setting := &model.Setting{}
	err := db.Model(model.Setting{}).Where("key = ?", key).First(setting).Error
	if err != nil {
		return nil, err
	}
	return setting, nil
}

func (s *SettingService) saveSetting(key string, value string) error {
	setting, err := s.getSetting(key)
	db := database.GetDB()
	if database.IsNotFound(err) {
		return db.Create(&model.Setting{
			Key:   key,
			Value: value,
		}).Error
	} else if err != nil {
		return err
	}
	setting.Key = key
	setting.Value = value
	return db.Save(setting).Error
}

func (s *SettingService) getString(key string) (string, error) {
	setting, err := s.getSetting(key)
	if database.IsNotFound(err) {
		value, ok := defaultValueMap[key]
		if !ok {
			return "", common.NewErrorf("key <%v> not in defaultValueMap", key)
		}
		return value, nil
	} else if err != nil {
		return "", err
	}
	return setting.Value, nil
}

func (s *SettingService) setString(key string, value string) error {
	return s.saveSetting(key, value)
}

func (s *SettingService) getBool(key string) (bool, error) {
	str, err := s.getString(key)
	if err != nil {
		return false, err
	}
	return strconv.ParseBool(str)
}

func (s *SettingService) setBool(key string, value bool) error {
	return s.setString(key, strconv.FormatBool(value))
}

func (s *SettingService) getInt(key string) (int, error) {
	str, err := s.getString(key)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(str)
}

func (s *SettingService) setInt(key string, value int) error {
	return s.setString(key, strconv.Itoa(value))
}

func (s *SettingService) GetXrayConfigTemplate() (string, error) {
	return s.getString("xrayTemplateConfig")
}

func (s *SettingService) GetListen() (string, error) {
	return s.getString("webListen")
}

func (s *SettingService) GetWebDomain() (string, error) {
	return s.getString("webDomain")
}

func (s *SettingService) GetTgBotToken() (string, error) {
	return s.getString("tgBotToken")
}

func (s *SettingService) SetTgBotToken(token string) error {
	return s.setString("tgBotToken", token)
}

func (s *SettingService) GetTgBotChatId() (string, error) {
	return s.getString("tgBotChatId")
}

func (s *SettingService) SetTgBotChatId(chatIds string) error {
	return s.setString("tgBotChatId", chatIds)
}

func (s *SettingService) GetTgbotenabled() (bool, error) {
	return s.getBool("tgBotEnable")
}

func (s *SettingService) SetTgbotenabled(value bool) error {
	return s.setBool("tgBotEnable", value)
}

func (s *SettingService) GetTgbotRuntime() (string, error) {
	return s.getString("tgRunTime")
}

func (s *SettingService) SetTgbotRuntime(time string) error {
	return s.setString("tgRunTime", time)
}

func (s *SettingService) GetTgBotBackup() (bool, error) {
	return s.getBool("tgBotBackup")
}

func (s *SettingService) GetTgBotLoginNotify() (bool, error) {
	return s.getBool("tgBotLoginNotify")
}

func (s *SettingService) GetTgCpu() (int, error) {
	return s.getInt("tgCpu")
}

func (s *SettingService) GetTgLang() (string, error) {
	return s.getString("tgLang")
}

func (s *SettingService) GetPort() (int, error) {
	return s.getInt("webPort")
}

func (s *SettingService) SetPort(port int) error {
	return s.setInt("webPort", port)
}

func (s *SettingService) SetCertFile(webCertFile string) error {
	return s.setString("webCertFile", webCertFile)
}

func (s *SettingService) GetCertFile() (string, error) {
	return s.getString("webCertFile")
}

func (s *SettingService) SetKeyFile(webKeyFile string) error {
	return s.setString("webKeyFile", webKeyFile)
}

func (s *SettingService) GetKeyFile() (string, error) {
	return s.getString("webKeyFile")
}

func (s *SettingService) GetExpireDiff() (int, error) {
	return s.getInt("expireDiff")
}

func (s *SettingService) GetTrafficDiff() (int, error) {
	return s.getInt("trafficDiff")
}

func (s *SettingService) GetSessionMaxAge() (int, error) {
	return s.getInt("sessionMaxAge")
}

func (s *SettingService) GetRemarkModel() (string, error) {
	return s.getString("remarkModel")
}

func (s *SettingService) GetOutboundTestUrl() (string, error) {
	return s.getString("outboundTestUrl")
}

var panelUUIDMu sync.Mutex

// GetPanelUUID returns the stable identity of this panel, generating it on
// first use. Node sync peers exchange it to detect reinstalled or unrelated
// panels and to break timestamp ties deterministically.
func (s *SettingService) GetPanelUUID() (string, error) {
	panelUUIDMu.Lock()
	defer panelUUIDMu.Unlock()
	value, err := s.getString("panelUUID")
	if err != nil {
		return "", err
	}
	if value != "" {
		return value, nil
	}
	value = random.Seq(32)
	if err := s.setString("panelUUID", value); err != nil {
		return "", err
	}
	return value, nil
}

func (s *SettingService) GetTimeLocationSetting() (string, error) {
	return s.getString("timeLocation")
}

func (s *SettingService) SetTimeLocation(value string) error {
	return s.setString("timeLocation", value)
}

func (s *SettingService) GetSecret() ([]byte, error) {
	secret, err := s.getString("secret")
	if secret == defaultValueMap["secret"] {
		err := s.saveSetting("secret", secret)
		if err != nil {
			logger.Warning("save secret failed:", err)
		}
	}
	return []byte(secret), err
}

func (s *SettingService) SetBasePath(basePath string) error {
	if !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	if !strings.HasSuffix(basePath, "/") {
		basePath += "/"
	}
	return s.setString("webBasePath", basePath)
}

func (s *SettingService) GetBasePath() (string, error) {
	basePath, err := s.getString("webBasePath")
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	if !strings.HasSuffix(basePath, "/") {
		basePath += "/"
	}
	return basePath, nil
}

func (s *SettingService) GetPanelTitle() (string, error) {
	return s.getString("panelTitle")
}

func (s *SettingService) SetPanelTitle(title string) error {
	return s.setString("panelTitle", title)
}

func (s *SettingService) GetSyncRole() (string, error) {
	return s.getString("syncRole")
}

func (s *SettingService) SetSyncRole(role string) error {
	return s.setString("syncRole", role)
}

func (s *SettingService) GetSyncMasterName() (string, error) {
	return s.getString("syncMasterName")
}

func (s *SettingService) SetSyncMasterName(name string) error {
	return s.setString("syncMasterName", name)
}

func (s *SettingService) GetSyncAutoInterval() (int, error) {
	return s.getInt("syncAutoInterval")
}

func (s *SettingService) SetSyncAutoInterval(minutes int) error {
	return s.setInt("syncAutoInterval", minutes)
}

// GetHostTimeZone reports the host operating system time zone name (IANA),
// independent from the panel's own timeLocation setting.
func GetHostTimeZone() string {
	if tz := strings.TrimSpace(os.Getenv("TZ")); tz != "" {
		if _, err := time.LoadLocation(tz); err == nil {
			return tz
		}
	}
	if data, err := os.ReadFile("/etc/timezone"); err == nil {
		tz := strings.TrimSpace(string(data))
		if tz != "" {
			if _, err := time.LoadLocation(tz); err == nil {
				return tz
			}
		}
	}
	if link, err := os.Readlink("/etc/localtime"); err == nil {
		const marker = "zoneinfo/"
		if idx := strings.LastIndex(link, marker); idx >= 0 {
			tz := link[idx+len(marker):]
			if _, err := time.LoadLocation(tz); err == nil {
				return tz
			}
		}
	}
	return ""
}

func (s *SettingService) GetTimeLocation() (*time.Location, error) {
	l, err := s.getString("timeLocation")
	if err != nil {
		return nil, err
	}
	location, err := time.LoadLocation(l)
	if err != nil {
		defaultLocation := defaultValueMap["timeLocation"]
		logger.Errorf("location <%v> not exist, using default location: %v", l, defaultLocation)
		location, err = time.LoadLocation(defaultLocation)
		if err != nil {
			logger.Errorf("failed to load default location, using UTC: %v", err)
			return time.UTC, nil
		}
		return location, nil
	}
	return location, nil
}

func (s *SettingService) GetSubEnable() (bool, error) {
	return s.getBool("subEnable")
}

func (s *SettingService) GetIpBlockAfterRemove() (bool, error) {
	return s.getBool("ipBlockAfterRemove")
}

func (s *SettingService) SetIpBlockAfterRemove(value bool) error {
	return s.setBool("ipBlockAfterRemove", value)
}

func (s *SettingService) GetBackupEnable() (bool, error) {
	return s.getBool("backupEnable")
}

func (s *SettingService) GetBackupTime() (string, error) {
	return s.getString("backupTime")
}

func (s *SettingService) GetBackupFrequency() (string, error) {
	return s.getString("backupFrequency")
}

func (s *SettingService) GetBackupRetention() (int, error) {
	return s.getInt("backupRetention")
}

func (s *SettingService) GetBackupType() (string, error) {
	return s.getString("backupType")
}

func (s *SettingService) GetBackupDir() (string, error) {
	return s.getString("backupDir")
}

func (s *SettingService) GetBackupWebdavUrl() (string, error) {
	return s.getString("backupWebdavUrl")
}

func (s *SettingService) GetBackupWebdavUser() (string, error) {
	return s.getString("backupWebdavUser")
}

func (s *SettingService) GetBackupWebdavPass() (string, error) {
	return s.getString("backupWebdavPass")
}

func (s *SettingService) GetBackupFtpHost() (string, error) {
	return s.getString("backupFtpHost")
}

func (s *SettingService) GetBackupFtpPort() (int, error) {
	return s.getInt("backupFtpPort")
}

func (s *SettingService) GetBackupFtpUser() (string, error) {
	return s.getString("backupFtpUser")
}

func (s *SettingService) GetBackupFtpPass() (string, error) {
	return s.getString("backupFtpPass")
}

func (s *SettingService) GetBackupFtpDir() (string, error) {
	return s.getString("backupFtpDir")
}

func (s *SettingService) GetBackupS3Endpoint() (string, error) {
	return s.getString("backupS3Endpoint")
}

func (s *SettingService) GetBackupS3Region() (string, error) {
	return s.getString("backupS3Region")
}

func (s *SettingService) GetBackupS3Bucket() (string, error) {
	return s.getString("backupS3Bucket")
}

func (s *SettingService) GetBackupS3AccessKey() (string, error) {
	return s.getString("backupS3AccessKey")
}

func (s *SettingService) GetBackupS3SecretKey() (string, error) {
	return s.getString("backupS3SecretKey")
}

func (s *SettingService) GetBackupS3Prefix() (string, error) {
	return s.getString("backupS3Prefix")
}

func (s *SettingService) GetBackupLastRun() (string, error) {
	return s.getString("backupLastRun")
}

func (s *SettingService) SetBackupLastRun(value string) error {
	return s.setString("backupLastRun", value)
}

func (s *SettingService) GetSubListen() (string, error) {
	return s.getString("subListen")
}

func (s *SettingService) GetSubPort() (int, error) {
	return s.getInt("subPort")
}

func (s *SettingService) GetSubPath() (string, error) {
	return s.getString("subPath")
}

func (s *SettingService) GetSubJsonPath() (string, error) {
	return s.getString("subJsonPath")
}

func (s *SettingService) GetSubDomain() (string, error) {
	return s.getString("subDomain")
}

func (s *SettingService) GetSubCertFile() (string, error) {
	return s.getString("subCertFile")
}

func (s *SettingService) GetSubKeyFile() (string, error) {
	return s.getString("subKeyFile")
}

func (s *SettingService) GetSubUpdates() (string, error) {
	return s.getString("subUpdates")
}

func (s *SettingService) GetSubEncrypt() (bool, error) {
	return s.getBool("subEncrypt")
}

func (s *SettingService) GetSubShowInfo() (bool, error) {
	return s.getBool("subShowInfo")
}

func (s *SettingService) GetPageSize() (int, error) {
	return s.getInt("pageSize")
}

func (s *SettingService) GetSubURI() (string, error) {
	return s.getString("subURI")
}

func (s *SettingService) GetSubJsonURI() (string, error) {
	return s.getString("subJsonURI")
}

func (s *SettingService) GetSubJsonMux() (string, error) {
	return s.getString("subJsonMux")
}

func (s *SettingService) GetSubJsonRules() (string, error) {
	return s.getString("subJsonRules")
}

func (s *SettingService) GetWarp() (string, error) {
	return s.getString("warp")
}

func (s *SettingService) SetWarp(data string) error {
	return s.setString("warp", data)
}

func (s *SettingService) UpdateAllSetting(allSetting *entity.AllSetting) error {
	if err := allSetting.CheckValid(); err != nil {
		return err
	}

	v := reflect.ValueOf(allSetting).Elem()
	t := reflect.TypeOf(allSetting).Elem()
	fields := reflect_util.GetFields(t)
	errs := make([]error, 0)
	for _, field := range fields {
		key := field.Tag.Get("json")
		fieldV := v.FieldByName(field.Name)
		value := fmt.Sprint(fieldV.Interface())
		err := s.saveSetting(key, value)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return common.Combine(errs...)
}

func (s *SettingService) GetDefaultXrayConfig() (interface{}, error) {
	var jsonData interface{}
	err := json.Unmarshal([]byte(config.GetDefaultXrayTemplate()), &jsonData)
	if err != nil {
		return nil, err
	}
	return jsonData, nil
}

func (s *SettingService) GetDefaultSettings(host string) (interface{}, error) {
	type settingFunc func() (interface{}, error)
	settings := map[string]settingFunc{
		"expireDiff":         func() (interface{}, error) { return s.GetExpireDiff() },
		"trafficDiff":        func() (interface{}, error) { return s.GetTrafficDiff() },
		"pageSize":           func() (interface{}, error) { return s.GetPageSize() },
		"defaultCert":        func() (interface{}, error) { return s.GetCertFile() },
		"defaultKey":         func() (interface{}, error) { return s.GetKeyFile() },
		"tgBotEnable":        func() (interface{}, error) { return s.GetTgbotenabled() },
		"subEnable":          func() (interface{}, error) { return s.GetSubEnable() },
		"subURI":             func() (interface{}, error) { return s.GetSubURI() },
		"subJsonURI":         func() (interface{}, error) { return s.GetSubJsonURI() },
		"remarkModel":        func() (interface{}, error) { return s.GetRemarkModel() },
		"ipBlockAfterRemove": func() (interface{}, error) { return s.GetIpBlockAfterRemove() },
	}

	result := make(map[string]interface{})

	for key, fn := range settings {
		value, err := fn()
		if err != nil {
			return "", err
		}
		result[key] = value
	}

	if result["subEnable"].(bool) && (result["subURI"].(string) == "" || result["subJsonURI"].(string) == "") {
		subURI := ""
		subPort, _ := s.GetSubPort()
		subPath, _ := s.GetSubPath()
		subJsonPath, _ := s.GetSubJsonPath()
		subDomain, _ := s.GetSubDomain()
		subKeyFile, _ := s.GetSubKeyFile()
		subCertFile, _ := s.GetSubCertFile()
		subTLS := false
		if subKeyFile != "" && subCertFile != "" {
			subTLS = true
		}
		if subDomain == "" {
			subDomain = strings.Split(host, ":")[0]
		}
		if subTLS {
			subURI = "https://"
		} else {
			subURI = "http://"
		}
		if (subPort == 443 && subTLS) || (subPort == 80 && !subTLS) {
			subURI += subDomain
		} else {
			subURI += fmt.Sprintf("%s:%d", subDomain, subPort)
		}
		if result["subURI"].(string) == "" {
			result["subURI"] = subURI + subPath
		}
		if result["subJsonURI"].(string) == "" {
			result["subJsonURI"] = subURI + subJsonPath
		}
	}

	return result, nil
}
