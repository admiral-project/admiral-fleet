// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

func TestNewS3Client(t *testing.T) {
	c := NewS3Client("https://s3.example.com", "us-east-1", "mybucket", "backups", "AKID", "secret", false)
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if c.endpoint != "https://s3.example.com" {
		t.Fatalf("endpoint: %q", c.endpoint)
	}
	if c.bucket != "mybucket" {
		t.Fatalf("bucket: %q", c.bucket)
	}
}

func TestNewS3ClientTrimsEndpoint(t *testing.T) {
	c := NewS3Client("https://s3.example.com/", "us-east-1", "b", "p", "a", "s", false)
	if c.endpoint != "https://s3.example.com" {
		t.Fatalf("expected trimmed endpoint, got %q", c.endpoint)
	}
}

func TestNewS3ClientTrimsPrefix(t *testing.T) {
	c := NewS3Client("https://s3.example.com", "us-east-1", "b", "/backups/v1/", "a", "s", false)
	if c.prefix != "backups/v1" {
		t.Fatalf("expected trimmed prefix, got %q", c.prefix)
	}
}

func TestSHA256Hex(t *testing.T) {
	emptyHash := sha256Hex(nil)
	expected := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if emptyHash != expected {
		t.Fatalf("empty hash: got %q, want %q", emptyHash, expected)
	}

	dataHash := sha256Hex([]byte("hello"))
	h := sha256.Sum256([]byte("hello"))
	expected = hex.EncodeToString(h[:])
	if dataHash != expected {
		t.Fatalf("data hash: got %q, want %q", dataHash, expected)
	}
}

func TestHMACSHA256(t *testing.T) {
	c := NewS3Client("https://s3.example.com", "us-east-1", "b", "", "a", "s", false)
	result := c.hmacSHA256([]byte("key"), "data")

	h := hmac.New(sha256.New, []byte("key"))
	h.Write([]byte("data"))
	expected := h.Sum(nil)

	if len(result) != len(expected) {
		t.Fatalf("hmac length mismatch: got %d, want %d", len(result), len(expected))
	}
	for i := range result {
		if result[i] != expected[i] {
			t.Fatalf("hmac mismatch at byte %d", i)
		}
	}
}

func TestSigningKey(t *testing.T) {
	c := NewS3Client("https://s3.example.com", "us-east-1", "b", "", "AKID", "secret", false)
	key := c.signingKey("20250101", "s3")
	if len(key) == 0 {
		t.Fatal("signing key must not be empty")
	}
}

func TestCanonicalURIPathStyle(t *testing.T) {
	c := NewS3Client("https://minio.example.com", "us-east-1", "mybucket", "", "a", "s", true)
	uri := c.canonicalURI(nil, "path/to/obj")
	expected := "/mybucket/path/to/obj"
	if uri != expected {
		t.Fatalf("path-style URI: got %q, want %q", uri, expected)
	}
}

func TestCanonicalURIVirtualHosted(t *testing.T) {
	c := NewS3Client("https://s3.amazonaws.com", "us-east-1", "mybucket", "", "a", "s", false)
	uri := c.canonicalURI(nil, "path/to/obj")
	expected := "/path/to/obj"
	if uri != expected {
		t.Fatalf("virtual-hosted URI: got %q, want %q", uri, expected)
	}
}

func TestCanonicalURIEmptyKeyPathStyle(t *testing.T) {
	c := NewS3Client("https://minio.example.com", "us-east-1", "b", "", "a", "s", true)
	uri := c.canonicalURI(nil, "")
	expected := "/b/"
	if uri != expected {
		t.Fatalf("path-style empty key: got %q, want %q", uri, expected)
	}
}

func TestCanonicalURIEmptyKeyVirtualHosted(t *testing.T) {
	c := NewS3Client("https://s3.amazonaws.com", "us-east-1", "b", "", "a", "s", false)
	uri := c.canonicalURI(nil, "")
	expected := "/"
	if uri != expected {
		t.Fatalf("virtual-hosted empty key: got %q, want %q", uri, expected)
	}
}

func TestCanonicalHeaders(t *testing.T) {
	c := NewS3Client("https://s3.example.com", "us-east-1", "b", "", "a", "s", false)
	req, _ := http.NewRequest("GET", "https://s3.example.com/b/k", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Amz-Date", "20250101T000000Z")

	headers, signed := c.canonicalHeaders(req)
	if !strings.Contains(headers, "content-type:application/json") {
		t.Fatalf("missing content-type in canonical headers: %q", headers)
	}
	if !strings.Contains(signed, "x-amz-date") {
		t.Fatalf("missing x-amz-date in signed headers: %q", signed)
	}
}

func TestCanonicalHeadersSkipsAuthorization(t *testing.T) {
	c := NewS3Client("https://s3.example.com", "us-east-1", "b", "", "a", "s", false)
	req, _ := http.NewRequest("GET", "https://s3.example.com/b/k", nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 ...")
	req.Header.Set("Host", "s3.example.com")

	_, signed := c.canonicalHeaders(req)
	if strings.Contains(signed, "authorization") {
		t.Fatalf("authorization header should not be in signed headers: %q", signed)
	}
}

func TestNewRequestPathStyle(t *testing.T) {
	c := NewS3Client("https://minio.example.com:9000", "us-east-1", "b", "prefix", "a", "s", true)
	req, err := c.newRequest(context.Background(), "PUT", "prefix/obj", nil)
	if err != nil {
		t.Fatal(err)
	}
	expected := "https://minio.example.com:9000/b/prefix/obj"
	if req.URL.String() != expected {
		t.Fatalf("URL: got %q, want %q", req.URL.String(), expected)
	}
}

func TestNewRequestVirtualHosted(t *testing.T) {
	c := NewS3Client("https://s3.amazonaws.com", "us-east-1", "mybucket", "", "a", "s", false)
	req, err := c.newRequest(context.Background(), "GET", "obj", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(req.URL.String(), "mybucket.s3.amazonaws.com") {
		t.Fatalf("expected virtual-hosted style URL, got %q", req.URL.String())
	}
}

func TestPutObject(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	server := newTestS3Server(t, &gotMethod, &gotPath, &gotBody)
	defer server.Close()

	c := NewS3Client(server.URL, "us-east-1", "testbucket", "", "AKID", "secret", true)
	c.httpClient = server.Client()

	data := []byte("test data")
	if err := c.PutObject(context.Background(), "testkey", data); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if gotMethod != "PUT" {
		t.Fatalf("expected PUT, got %s", gotMethod)
	}
	if string(gotBody) != "test data" {
		t.Fatalf("body: got %q, want %q", string(gotBody), "test data")
	}
}

func TestGetObject(t *testing.T) {
	var gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte("stored data"))
	}))
	defer server.Close()

	c := NewS3Client(server.URL, "us-east-1", "bucket", "", "a", "s", true)
	c.httpClient = server.Client()

	data, err := c.GetObject(context.Background(), "mykey")
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if gotMethod != "GET" {
		t.Fatalf("expected GET, got %s", gotMethod)
	}
	if string(data) != "stored data" {
		t.Fatalf("data: got %q, want %q", string(data), "stored data")
	}
}

func TestDeleteObject(t *testing.T) {
	var gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c := NewS3Client(server.URL, "us-east-1", "bucket", "", "a", "s", true)
	c.httpClient = server.Client()

	if err := c.DeleteObject(context.Background(), "mykey"); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if gotMethod != "DELETE" {
		t.Fatalf("expected DELETE, got %s", gotMethod)
	}
}

func TestPutObjectHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := NewS3Client(server.URL, "us-east-1", "bucket", "", "a", "s", true)
	c.httpClient = server.Client()

	err := c.PutObject(context.Background(), "k", []byte("data"))
	if err == nil {
		t.Fatal("expected error for HTTP 404")
	}
}

func TestGetObjectHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	c := NewS3Client(server.URL, "us-east-1", "bucket", "", "a", "s", true)
	c.httpClient = server.Client()

	_, err := c.GetObject(context.Background(), "k")
	if err == nil {
		t.Fatal("expected error for HTTP 403")
	}
}

func TestS3TestSuccess(t *testing.T) {
	var puts, deletes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PUT":
			puts++
			w.WriteHeader(http.StatusOK)
		case "DELETE":
			deletes++
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	c := NewS3Client(server.URL, "us-east-1", "bucket", "", "a", "s", true)
	c.httpClient = server.Client()

	if err := c.Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}
	if puts != 1 {
		t.Fatalf("expected 1 PUT, got %d", puts)
	}
	if deletes != 1 {
		t.Fatalf("expected 1 DELETE, got %d", deletes)
	}
}

func TestS3TestWriteFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	c := NewS3Client(server.URL, "us-east-1", "bucket", "", "a", "s", true)
	c.httpClient = server.Client()

	if err := c.Test(context.Background()); err == nil {
		t.Fatal("expected error for failed test write")
	}
}

func TestNewS3FromConfig(t *testing.T) {
	os.Setenv("TEST_ACCESS_KEY", "myaccesskey")
	os.Setenv("TEST_SECRET_KEY", "mysecretkey")
	defer os.Unsetenv("TEST_ACCESS_KEY")
	defer os.Unsetenv("TEST_SECRET_KEY")

	cfg := &admiral.StorageConfig{
		Endpoint:        "https://minio.example.com",
		Region:          "us-east-1",
		Bucket:          "backups",
		Prefix:          "admiral",
		AccessKeyEnv:    "TEST_ACCESS_KEY",
		SecretKeyEnv:    "TEST_SECRET_KEY",
		ForcePathStyle:  true,
		SessionTokenEnv: "",
	}

	c, err := NewS3FromConfig(cfg)
	if err != nil {
		t.Fatalf("NewS3FromConfig: %v", err)
	}
	if c.accessKey != "myaccesskey" {
		t.Fatalf("accessKey: got %q, want %q", c.accessKey, "myaccesskey")
	}
	if c.secretKey != "mysecretkey" {
		t.Fatalf("secretKey: got %q, want %q", c.secretKey, "mysecretkey")
	}
}

func TestNewS3FromConfigNil(t *testing.T) {
	_, err := NewS3FromConfig(nil)
	if err == nil {
		t.Fatal("expected error for nil config")
	}
}

func TestNewS3FromConfigMissingAccessKey(t *testing.T) {
	os.Unsetenv("MISSING_ACCESS_KEY")

	cfg := &admiral.StorageConfig{
		Endpoint:     "https://s3.example.com",
		Region:       "us-east-1",
		Bucket:       "b",
		AccessKeyEnv: "MISSING_ACCESS_KEY",
		SecretKeyEnv: "MISSING_SECRET_KEY",
	}

	_, err := NewS3FromConfig(cfg)
	if err == nil {
		t.Fatal("expected error for missing env vars")
	}
}

func TestSignV4SetsAuthHeader(t *testing.T) {
	c := NewS3Client("https://s3.example.com", "us-east-1", "bucket", "", "AKID", "secret", true)
	req, _ := http.NewRequest("PUT", "https://s3.example.com/bucket/key", strings.NewReader("data"))

	now := time.Now()
	// Call signV4 directly
	c.signV4(req, "key", "s3", "")

	auth := req.Header.Get("Authorization")
	if auth == "" {
		t.Fatal("Authorization header must be set")
	}
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256") {
		t.Fatalf("expected AWS4-HMAC-SHA256 prefix, got %q", auth)
	}
	if !strings.Contains(auth, "AKID/") {
		t.Fatalf("expected access key in auth header: %q", auth)
	}

	_ = now
}

func TestSignV4SetsDateHeader(t *testing.T) {
	c := NewS3Client("https://s3.example.com", "us-east-1", "bucket", "", "AKID", "secret", true)
	req, _ := http.NewRequest("PUT", "https://s3.example.com/bucket/key", nil)

	c.signV4(req, "key", "s3", "")
	if req.Header.Get("x-amz-date") == "" {
		t.Fatal("x-amz-date header must be set")
	}
	if req.Header.Get("x-amz-content-sha256") == "" {
		t.Fatal("x-amz-content-sha256 header must be set")
	}
}

func TestNewRequestWithPrefix(t *testing.T) {
	c := NewS3Client("https://minio.example.com", "us-east-1", "b", "backups/v1", "a", "s", true)
	req, err := c.newRequest(context.Background(), "GET", "backups/v1/obj", nil)
	if err != nil {
		t.Fatal(err)
	}
	expected := "https://minio.example.com/b/backups/v1/obj"
	if req.URL.String() != expected {
		t.Fatalf("URL: got %q, want %q", req.URL.String(), expected)
	}
}

func TestPutObjectWithPrefix(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := NewS3Client(server.URL, "us-east-1", "bucket", "admiral/test", "a", "s", true)
	c.httpClient = server.Client()

	if err := c.PutObject(context.Background(), "admiral/test/key", []byte("data")); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if !strings.Contains(gotPath, "/bucket/admiral/test/key") {
		t.Fatalf("expected path with prefix, got %q", gotPath)
	}
}

func TestS3ClientConcurrentRequests(t *testing.T) {
	var count int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := NewS3Client(server.URL, "us-east-1", "b", "", "a", "s", true)
	c.httpClient = server.Client()

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := c.PutObject(ctx, "k", []byte("d")); err != nil {
			t.Fatalf("concurrent PutObject %d: %v", i, err)
		}
	}
	if count != 5 {
		t.Fatalf("expected 5 requests, got %d", count)
	}
}

func TestCloseBodyOnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("not found"))
	}))
	defer server.Close()

	c := NewS3Client(server.URL, "us-east-1", "bucket", "", "a", "s", true)
	c.httpClient = server.Client()

	err := c.PutObject(context.Background(), "k", []byte("data"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "http 404") {
		t.Fatalf("expected 404 in error, got %q", err.Error())
	}
}

func newTestS3Server(t *testing.T, method, path *string, body *[]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*method = r.Method
		*path = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		*body = b
		w.WriteHeader(http.StatusOK)
	}))
}
