package service

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	awsTestAccessKey = "AKIAIOSFODNN7EXAMPLE"
	awsTestSecretKey = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
)

func TestS3SignatureAWSGetObjectVector(t *testing.T) {
	headers := map[string]string{
		"host":                 "examplebucket.s3.amazonaws.com",
		"range":                "bytes=0-9",
		"x-amz-content-sha256": s3EmptyPayloadHash,
		"x-amz-date":           "20130524T000000Z",
	}
	signed := []string{"host", "range", "x-amz-content-sha256", "x-amz-date"}
	canonical := buildS3CanonicalRequest(http.MethodGet, "/test.txt", "", headers, signed, s3EmptyPayloadHash)
	signature := s3Signature(canonical, "us-east-1", "20130524", "20130524T000000Z", awsTestSecretKey)
	const expected = "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if signature != expected {
		t.Fatalf("signature mismatch:\n got %s\nwant %s", signature, expected)
	}
}

func TestS3SignatureAWSPutObjectVector(t *testing.T) {
	payloadHash := "44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072"
	headers := map[string]string{
		"date":                 "Fri, 24 May 2013 00:00:00 GMT",
		"host":                 "examplebucket.s3.amazonaws.com",
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           "20130524T000000Z",
		"x-amz-storage-class":  "REDUCED_REDUNDANCY",
	}
	signed := []string{"date", "host", "x-amz-content-sha256", "x-amz-date", "x-amz-storage-class"}
	canonical := buildS3CanonicalRequest(http.MethodPut, "/test%24file.text", "", headers, signed, payloadHash)
	signature := s3Signature(canonical, "us-east-1", "20130524", "20130524T000000Z", awsTestSecretKey)
	const expected = "98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd"
	if signature != expected {
		t.Fatalf("signature mismatch:\n got %s\nwant %s", signature, expected)
	}
}

func TestS3EscapePath(t *testing.T) {
	cases := map[string]string{
		"/x-ui-20260101-000000.db": "/x-ui-20260101-000000.db",
		"/test$file.text":          "/test%24file.text",
		"/backups/中文 备份.db":        "/backups/%E4%B8%AD%E6%96%87%20%E5%A4%87%E4%BB%BD.db",
	}
	for input, expected := range cases {
		if got := s3Escape(input, false); got != expected {
			t.Errorf("escape %q: got %q, want %q", input, got, expected)
		}
	}
}

func TestS3EncodeQuery(t *testing.T) {
	query := url.Values{}
	query.Set("prefix", "backups/x-ui-")
	query.Set("list-type", "2")
	query.Set("continuation-token", "a+b/c=")
	expected := "continuation-token=a%2Bb%2Fc%3D&list-type=2&prefix=backups%2Fx-ui-"
	if got := s3EncodeQuery(query); got != expected {
		t.Fatalf("query mismatch:\n got %s\nwant %s", got, expected)
	}
}

func TestS3ObjectURLAddressing(t *testing.T) {
	r2, err := (s3BackupConfig{
		Endpoint: "https://abc123.r2.cloudflarestorage.com",
		Region:   "auto",
		Bucket:   "xui-backups",
		AccessKey: awsTestAccessKey,
		SecretKey: awsTestSecretKey,
		Prefix:   "/nested/",
	}).normalize()
	if err != nil {
		t.Fatal(err)
	}
	if r2.Prefix != "nested/" {
		t.Fatalf("prefix not normalized: %q", r2.Prefix)
	}
	u, err := r2.objectURL("nested/x-ui-20260101-000000.db", nil)
	if err != nil {
		t.Fatal(err)
	}
	if u.String() != "https://abc123.r2.cloudflarestorage.com/xui-backups/nested/x-ui-20260101-000000.db" {
		t.Fatalf("unexpected R2 URL: %s", u.String())
	}

	aws, err := (s3BackupConfig{
		Endpoint: "https://s3.us-east-1.amazonaws.com",
		Region:   "us-east-1",
		Bucket:   "xui-backups",
		AccessKey: awsTestAccessKey,
		SecretKey: awsTestSecretKey,
	}).normalize()
	if err != nil {
		t.Fatal(err)
	}
	u, err = aws.objectURL("x-ui-20260101-000000.db", nil)
	if err != nil {
		t.Fatal(err)
	}
	if u.String() != "https://xui-backups.s3.us-east-1.amazonaws.com/x-ui-20260101-000000.db" {
		t.Fatalf("unexpected AWS URL: %s", u.String())
	}

	minio, err := (s3BackupConfig{
		Endpoint: "http://127.0.0.1:9000",
		Region:   "us-east-1",
		Bucket:   "xui",
		AccessKey: awsTestAccessKey,
		SecretKey: awsTestSecretKey,
	}).normalize()
	if err != nil {
		t.Fatal(err)
	}
	u, err = minio.objectURL("x-ui-20260101-000000.db", nil)
	if err != nil {
		t.Fatal(err)
	}
	if u.String() != "http://127.0.0.1:9000/xui/x-ui-20260101-000000.db" {
		t.Fatalf("unexpected MinIO URL: %s", u.String())
	}
}

func TestS3NormalizeRejectsBadEndpoint(t *testing.T) {
	_, err := (s3BackupConfig{
		Endpoint: "https://s3.example.com/some/path",
		Region:   "us-east-1",
		Bucket:   "xui",
		AccessKey: awsTestAccessKey,
		SecretKey: awsTestSecretKey,
	}).normalize()
	if err == nil {
		t.Fatal("expected endpoint with a path to be rejected")
	}
}

// TestS3LiveProvider exercises PUT, ListObjectsV2 and DELETE against a real
// S3-compatible endpoint (MinIO during development). It is skipped unless
// XUI_S3_TEST_ENDPOINT is set.
func TestS3LiveProvider(t *testing.T) {
	endpoint := os.Getenv("XUI_S3_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("set XUI_S3_TEST_ENDPOINT/BUCKET/ACCESS_KEY/SECRET_KEY to run")
	}
	cfg, err := (s3BackupConfig{
		Endpoint:  endpoint,
		Region:    os.Getenv("XUI_S3_TEST_REGION"),
		Bucket:    os.Getenv("XUI_S3_TEST_BUCKET"),
		AccessKey: os.Getenv("XUI_S3_TEST_ACCESS_KEY"),
		SecretKey: os.Getenv("XUI_S3_TEST_SECRET_KEY"),
		Prefix:    fmt.Sprintf("itest-%d/", time.Now().UnixNano()),
	}).normalize()
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	older := "x-ui-20260101-000000.db"
	newer := "x-ui-20260101-000001.db"
	for name, content := range map[string]string{older: "older", newer: "newer"} {
		file := filepath.Join(dir, name)
		if err := os.WriteFile(file, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if err := cfg.putObject(cfg.Prefix+name, file); err != nil {
			t.Fatalf("upload %s: %v", name, err)
		}
	}
	defer cfg.deleteObject(cfg.Prefix + newer)

	keys, err := cfg.listObjects(cfg.Prefix + backupFilePrefix)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %v", keys)
	}
	if err := cfg.prune(1); err != nil {
		t.Fatalf("prune: %v", err)
	}
	keys, err = cfg.listObjects(cfg.Prefix + backupFilePrefix)
	if err != nil {
		t.Fatalf("list after prune: %v", err)
	}
	if len(keys) != 1 || keys[0] != cfg.Prefix+newer {
		t.Fatalf("prune kept unexpected keys: %v", keys)
	}
}