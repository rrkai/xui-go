package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/alireza0/x-ui/logger"
)

const (
	s3EmptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	s3RequestTimeout   = 5 * time.Minute
)

var s3HTTPClient = &http.Client{
	Timeout: s3RequestTimeout,
	// Region redirects must surface as errors; following them would turn the
	// PUT into a GET and hide the bucket-region hint.
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

type s3BackupConfig struct {
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	Prefix    string
}

func (s *AutoBackupService) getS3Config() (s3BackupConfig, error) {
	var cfg s3BackupConfig
	var err error
	if cfg.Endpoint, err = s.settingService.GetBackupS3Endpoint(); err != nil {
		return cfg, err
	}
	if cfg.Region, err = s.settingService.GetBackupS3Region(); err != nil {
		return cfg, err
	}
	if cfg.Bucket, err = s.settingService.GetBackupS3Bucket(); err != nil {
		return cfg, err
	}
	if cfg.AccessKey, err = s.settingService.GetBackupS3AccessKey(); err != nil {
		return cfg, err
	}
	if cfg.SecretKey, err = s.settingService.GetBackupS3SecretKey(); err != nil {
		return cfg, err
	}
	if cfg.Prefix, err = s.settingService.GetBackupS3Prefix(); err != nil {
		return cfg, err
	}
	return cfg.normalize()
}

func (c s3BackupConfig) normalize() (s3BackupConfig, error) {
	c.Endpoint = strings.TrimSpace(c.Endpoint)
	c.Region = strings.TrimSpace(c.Region)
	c.Bucket = strings.TrimSpace(c.Bucket)
	c.AccessKey = strings.TrimSpace(c.AccessKey)
	c.SecretKey = strings.TrimSpace(c.SecretKey)
	c.Prefix = strings.TrimSpace(c.Prefix)
	if c.Endpoint == "" {
		return c, fmt.Errorf("S3 endpoint is empty")
	}
	base, err := url.Parse(c.Endpoint)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return c, fmt.Errorf("S3 endpoint is not valid: %s", c.Endpoint)
	}
	if strings.Trim(base.Path, "/") != "" {
		return c, fmt.Errorf("S3 endpoint must not contain a path: %s", c.Endpoint)
	}
	if c.Region == "" {
		c.Region = "us-east-1"
	}
	if c.Bucket == "" {
		return c, fmt.Errorf("S3 bucket is empty")
	}
	if c.AccessKey == "" || c.SecretKey == "" {
		return c, fmt.Errorf("S3 access key or secret key is empty")
	}
	c.Endpoint = base.Scheme + "://" + base.Host
	c.Prefix = strings.TrimLeft(c.Prefix, "/")
	if c.Prefix != "" && !strings.HasSuffix(c.Prefix, "/") {
		c.Prefix += "/"
	}
	return c, nil
}

// virtualHosted follows AWS's addressing rules: S3-compatible services
// (R2, B2, MinIO, OSS) are reached with path-style URLs, while AWS itself
// needs bucket.<host> for buckets created after path-style was deprecated.
func (c s3BackupConfig) virtualHosted() bool {
	if strings.Contains(c.Bucket, ".") {
		return false
	}
	base, err := url.Parse(c.Endpoint)
	if err != nil {
		return false
	}
	host := strings.ToLower(base.Hostname())
	if !strings.HasSuffix(host, ".amazonaws.com") && !strings.HasSuffix(host, ".amazonaws.com.cn") {
		return false
	}
	return strings.HasPrefix(host, "s3.") || strings.HasPrefix(host, "s3-")
}

func (c s3BackupConfig) objectURL(key string, query url.Values) (*url.URL, error) {
	base, err := url.Parse(c.Endpoint)
	if err != nil {
		return nil, err
	}
	host := base.Host
	objectPath := "/" + c.Bucket
	if c.virtualHosted() {
		host = c.Bucket + "." + base.Host
		objectPath = "/" + key
	} else if key != "" {
		objectPath += "/" + key
	}
	u := &url.URL{Scheme: base.Scheme, Host: host, Path: objectPath}
	if escaped := s3Escape(objectPath, false); escaped != objectPath {
		u.RawPath = escaped
	}
	if len(query) > 0 {
		u.RawQuery = s3EncodeQuery(query)
	}
	return u, nil
}

func s3Escape(value string, encodeSlash bool) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(value))
	for i := 0; i < len(value); i++ {
		ch := value[i]
		switch {
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9',
			ch == '-', ch == '_', ch == '.', ch == '~':
			b.WriteByte(ch)
		case ch == '/' && !encodeSlash:
			b.WriteByte(ch)
		default:
			b.WriteByte('%')
			b.WriteByte(hexDigits[ch>>4])
			b.WriteByte(hexDigits[ch&0x0f])
		}
	}
	return b.String()
}

func s3EncodeQuery(query url.Values) string {
	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		values := append([]string(nil), query[key]...)
		sort.Strings(values)
		for _, value := range values {
			if b.Len() > 0 {
				b.WriteByte('&')
			}
			b.WriteString(s3Escape(key, true))
			b.WriteByte('=')
			b.WriteString(s3Escape(value, true))
		}
	}
	return b.String()
}

func (c s3BackupConfig) sign(req *http.Request, payloadHash string, now time.Time) {
	amzDate := now.UTC().Format("20060102T150405Z")
	dateStamp := now.UTC().Format("20060102")
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)

	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	headers := map[string]string{
		"host":                 host,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           amzDate,
	}
	signedHeaders := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalRequest := buildS3CanonicalRequest(req.Method, canonicalURI, req.URL.RawQuery, headers, signedHeaders, payloadHash)
	signature := s3Signature(canonicalRequest, c.Region, dateStamp, amzDate, c.SecretKey)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.AccessKey+"/"+dateStamp+"/"+c.Region+
		"/s3/aws4_request, SignedHeaders="+strings.Join(signedHeaders, ";")+", Signature="+signature)
}

func buildS3CanonicalRequest(method, canonicalURI, canonicalQuery string, headers map[string]string, signedHeaders []string, payloadHash string) string {
	names := append([]string(nil), signedHeaders...)
	sort.Strings(names)
	var b strings.Builder
	b.WriteString(method)
	b.WriteByte('\n')
	b.WriteString(canonicalURI)
	b.WriteByte('\n')
	b.WriteString(canonicalQuery)
	b.WriteByte('\n')
	for _, name := range names {
		b.WriteString(name)
		b.WriteByte(':')
		b.WriteString(strings.Join(strings.Fields(headers[name]), " "))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(strings.Join(names, ";"))
	b.WriteByte('\n')
	b.WriteString(payloadHash)
	return b.String()
}

func s3Signature(canonicalRequest, region, dateStamp, amzDate, secretKey string) string {
	requestHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + dateStamp + "/" + region + "/s3/aws4_request\n" +
		hex.EncodeToString(requestHash[:])
	key := hmacSHA256([]byte("AWS4"+secretKey), dateStamp)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, "s3")
	key = hmacSHA256(key, "aws4_request")
	return hex.EncodeToString(hmacSHA256(key, stringToSign))
}

func hmacSHA256(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(value))
	return mac.Sum(nil)
}

func s3ResponseError(action string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	detail := strings.Join(strings.Fields(string(body)), " ")
	message := action + " failed: " + resp.Status
	if detail != "" {
		message += ": " + detail
	}
	if region := strings.TrimSpace(resp.Header.Get("x-amz-bucket-region")); region != "" {
		message += " (bucket region: " + region + ")"
	}
	return fmt.Errorf("%s", message)
}

func (c s3BackupConfig) putObject(key, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	payloadHash := hex.EncodeToString(hasher.Sum(nil))
	target, err := c.objectURL(key, nil)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPut, target.String(), file)
	if err != nil {
		return err
	}
	req.ContentLength = stat.Size()
	req.Header.Set("Content-Type", "application/octet-stream")
	c.sign(req, payloadHash, time.Now())
	resp, err := s3HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return s3ResponseError("S3 upload", resp)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	return nil
}

type s3ListBucketResult struct {
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
	Contents              []struct {
		Key string `xml:"Key"`
	} `xml:"Contents"`
}

func (c s3BackupConfig) listObjects(prefix string) ([]string, error) {
	keys := make([]string, 0, 16)
	token := ""
	for page := 0; page < 20; page++ {
		query := url.Values{}
		query.Set("list-type", "2")
		query.Set("prefix", prefix)
		if token != "" {
			query.Set("continuation-token", token)
		}
		target, err := c.objectURL("", query)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequest(http.MethodGet, target.String(), nil)
		if err != nil {
			return nil, err
		}
		c.sign(req, s3EmptyPayloadHash, time.Now())
		resp, err := s3HTTPClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			err := s3ResponseError("S3 list", resp)
			resp.Body.Close()
			return nil, err
		}
		var result s3ListBucketResult
		decodeErr := xml.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&result)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, decodeErr
		}
		for _, item := range result.Contents {
			keys = append(keys, item.Key)
		}
		if !result.IsTruncated || result.NextContinuationToken == "" {
			return keys, nil
		}
		token = result.NextContinuationToken
	}
	return keys, nil
}

func (c s3BackupConfig) deleteObject(key string) error {
	target, err := c.objectURL(key, nil)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodDelete, target.String(), nil)
	if err != nil {
		return err
	}
	c.sign(req, s3EmptyPayloadHash, time.Now())
	resp, err := s3HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return s3ResponseError("S3 delete", resp)
	}
	return nil
}

func (c s3BackupConfig) prune(retention int) error {
	keys, err := c.listObjects(c.Prefix + backupFilePrefix)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(keys))
	for _, key := range keys {
		if isBackupFileName(path.Base(key)) {
			names = append(names, key)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if len(names) <= retention {
		return nil
	}
	for _, key := range names[retention:] {
		if err := c.deleteObject(key); err != nil {
			logger.Warning("auto backup: delete S3 backup failed:", key, err)
		}
	}
	return nil
}

func (s *AutoBackupService) uploadS3(filePath, name string, retention int) error {
	cfg, err := s.getS3Config()
	if err != nil {
		return err
	}
	key := cfg.Prefix + name
	if err := cfg.putObject(key, filePath); err != nil {
		return err
	}
	logger.Info("auto backup: uploaded to S3:", key)
	if err := cfg.prune(retention); err != nil {
		logger.Warning("auto backup: prune S3 backups failed:", err)
	}
	return nil
}
