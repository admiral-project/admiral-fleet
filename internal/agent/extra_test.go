// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

func TestStartReconciler(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Skip TLS verification for test
	a, err := New("node1", server.URL, "token", "", t.TempDir(), "1h", "none", "root", "", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.http.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify = true

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	a.StartReconciler(ctx, 10*time.Millisecond)
}

func TestWarnIfNoBackupStorage(t *testing.T) {
	a := &Agent{}
	// This test mainly covers the logic branches
	os.Setenv("ADMIRAL_S3_ACCESS_KEY_ID", "")
	os.Setenv("ADMIRAL_S3_SECRET_ACCESS_KEY", "")
	a.warnIfNoBackupStorage()

	os.Setenv("ADMIRAL_S3_ACCESS_KEY_ID", "key")
	os.Setenv("ADMIRAL_S3_SECRET_ACCESS_KEY", "secret")
	a.warnIfNoBackupStorage()
}

func TestAgentCoreHelpers(t *testing.T) {
	// sanitizeInstanceID removes .. and /
	if got := sanitizeInstanceID("demo-001"); got != "demo-001" {
		t.Errorf("sanitizeInstanceID: %s", got)
	}
	if got := extractInstanceID("admiral-inst1"); got != "inst1" {
		t.Errorf("extractInstanceID: %s", got)
	}
	if got := extractInstanceID("other-pod"); got != "" {
		t.Errorf("extractInstanceID: %s", got)
	}

	// classifyStorageState
	tests := []struct {
		pct  float64
		want admiral.StorageState
	}{
		{10.0, admiral.StorageOK},
		{65.0, admiral.StorageWarning},
		{85.0, admiral.StorageCritical},
		{105.0, admiral.StorageOverQuota},
	}
	for _, tc := range tests {
		state, _ := classifyStorageState(tc.pct)
		if state != tc.want {
			t.Errorf("classifyStorageState(%v) = %v, want %v", tc.pct, state, tc.want)
		}
	}

	// parseStorageLimitBytes
	if got := parseStorageLimitBytes("1G"); got != 1024*1024*1024 {
		t.Errorf("parseStorageLimitBytes(1G): %d", got)
	}
}
