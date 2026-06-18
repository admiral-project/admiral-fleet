// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

type S3Client struct {
	endpoint     string
	region       string
	bucket       string
	prefix       string
	accessKey    string
	secretKey    string
	usePathStyle bool
	httpClient   *http.Client
}

func NewS3Client(endpoint, region, bucket, prefix, accessKey, secretKey string, usePathStyle bool) *S3Client {
	return &S3Client{
		endpoint:     strings.TrimRight(endpoint, "/"),
		region:       region,
		bucket:       bucket,
		prefix:       strings.Trim(prefix, "/"),
		accessKey:    accessKey,
		secretKey:    secretKey,
		usePathStyle: usePathStyle,
		httpClient:   &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *S3Client) PutObject(ctx context.Context, key string, data []byte) error {
	body := bytes.NewReader(data)
	req, err := c.newRequest(ctx, http.MethodPut, key, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = int64(len(data))
	c.signV4(req, key, "s3", sha256Hex(data))
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("s3 put %q: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("s3 put %q: http %d: %s", key, resp.StatusCode, string(body))
	}
	return nil
}

func (c *S3Client) GetObject(ctx context.Context, key string) ([]byte, error) {
	req, err := c.newRequest(ctx, http.MethodGet, key, nil)
	if err != nil {
		return nil, err
	}
	c.signV4(req, key, "s3", "")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("s3 get %q: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("s3 get %q: http %d: %s", key, resp.StatusCode, string(body))
	}
	return io.ReadAll(resp.Body)
}

func (c *S3Client) DeleteObject(ctx context.Context, key string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, key, nil)
	if err != nil {
		return err
	}
	c.signV4(req, key, "s3", "")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("s3 delete %q: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("s3 delete %q: http %d: %s", key, resp.StatusCode, string(body))
	}
	return nil
}

func (c *S3Client) Test(ctx context.Context) error {
	healthKey := c.prefix + "/.health-check-tmp"
	if strings.HasPrefix(healthKey, "/") {
		healthKey = strings.TrimPrefix(healthKey, "/")
	}
	if err := c.PutObject(ctx, healthKey, []byte("ok")); err != nil {
		return fmt.Errorf("s3 test write: %w", err)
	}
	if err := c.DeleteObject(ctx, healthKey); err != nil {
		return fmt.Errorf("s3 test cleanup: %w", err)
	}
	return nil
}

func (c *S3Client) newRequest(ctx context.Context, method, key string, body io.Reader) (*http.Request, error) {
	var urlStr string
	if c.usePathStyle || !strings.Contains(c.endpoint, ".amazonaws.com") {
		urlStr = fmt.Sprintf("%s/%s/%s", c.endpoint, c.bucket, key)
	} else {
		urlStr = fmt.Sprintf("%s/%s", strings.Replace(c.endpoint, "://", fmt.Sprintf("://%s.", c.bucket), 1), key)
	}
	req, err := http.NewRequestWithContext(ctx, method, urlStr, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Host", req.URL.Host)
	return req, nil
}

func (c *S3Client) signV4(req *http.Request, key, service, payloadHash string) {
	if payloadHash == "" {
		payloadHash = sha256Hex(nil)
	}
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)

	credentialScope := fmt.Sprintf("%s/%s/%s/aws4_request", dateStamp, c.region, service)

	canonicalHeaders, signedHeaders := c.canonicalHeaders(req)
	canonicalURI := c.canonicalURI(req, key)
	canonicalQuery := c.canonicalQuery(req)
	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		req.Method, canonicalURI, canonicalQuery, canonicalHeaders, signedHeaders, payloadHash)

	stringToSign := fmt.Sprintf("AWS4-HMAC-SHA256\n%s\n%s\n%s",
		amzDate, credentialScope, sha256Hex([]byte(canonicalRequest)))

	signingKey := c.signingKey(dateStamp, service)
	signature := hex.EncodeToString(c.hmacSHA256(signingKey, stringToSign))

	auth := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.accessKey, credentialScope, signedHeaders, signature)
	req.Header.Set("Authorization", auth)
}

func (c *S3Client) signingKey(dateStamp, service string) []byte {
	kSecret := []byte("AWS4" + c.secretKey)
	kDate := c.hmacSHA256(kSecret, dateStamp)
	kRegion := c.hmacSHA256(kDate, c.region)
	kService := c.hmacSHA256(kRegion, service)
	return c.hmacSHA256(kService, "aws4_request")
}

func (c *S3Client) hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func sha256Hex(data []byte) string {
	if data == nil {
		return "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func NewS3FromConfig(cfg *admiral.StorageConfig) (*S3Client, error) {
	if cfg == nil {
		return nil, fmt.Errorf("storage config is nil")
	}
	accessKey := os.Getenv("ADMIRAL_S3_ACCESS_KEY_ID")
	secretKey := os.Getenv("ADMIRAL_S3_SECRET_ACCESS_KEY")
	if accessKey == "" {
		return nil, fmt.Errorf("ADMIRAL_S3_ACCESS_KEY_ID is not set")
	}
	if secretKey == "" {
		return nil, fmt.Errorf("ADMIRAL_S3_SECRET_ACCESS_KEY is not set")
	}
	return NewS3Client(cfg.Endpoint, cfg.Region, cfg.Bucket, cfg.Prefix, accessKey, secretKey, cfg.ForcePathStyle), nil
}

func (c *S3Client) canonicalURI(_ *http.Request, key string) string {
	isPathStyle := c.usePathStyle || !strings.Contains(c.endpoint, ".amazonaws.com")
	if isPathStyle {
		if key == "" {
			return "/" + c.bucket + "/"
		}
		return "/" + c.bucket + "/" + key
	}
	if key == "" {
		return "/"
	}
	return "/" + key
}

func (c *S3Client) canonicalQuery(req *http.Request) string {
	return req.URL.RawQuery
}

func (c *S3Client) canonicalHeaders(req *http.Request) (string, string) {
	hdrs := make(map[string]string)
	for k, v := range req.Header {
		lk := strings.ToLower(k)
		if lk == "authorization" {
			continue
		}
		hdrs[lk] = strings.TrimSpace(strings.Join(v, ","))
	}
	names := make([]string, 0, len(hdrs))
	for n := range hdrs {
		names = append(names, n)
	}
	sort.Strings(names)
	var buf strings.Builder
	var signed strings.Builder
	for i, n := range names {
		buf.WriteString(n)
		buf.WriteByte(':')
		buf.WriteString(hdrs[n])
		buf.WriteByte('\n')
		if i > 0 {
			signed.WriteByte(';')
		}
		signed.WriteString(n)
	}
	return buf.String(), signed.String()
}
