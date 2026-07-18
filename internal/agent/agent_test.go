// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

func TestNewAgentInvalidURL(t *testing.T) {
	_, err := New("node_1", "http://insecure", "token", "", "/tmp/outbox", "", "", "", "", "", nil)
	if err == nil {
		t.Fatal("expected error for non-https URL")
	}
}

func TestNewAgentInvalidURLPlainHTTP(t *testing.T) {
	_, err := New("node_1", "http://example.com/api", "token", "", "/tmp/outbox", "", "", "", "", "", nil)
	if err == nil {
		t.Fatal("expected error for http URL")
	}
}

func TestHandleTaskSendsResult(t *testing.T) {
	var sent admiral.TaskResult
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var result admiral.TaskResult
		if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		sent = result
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ag, err := New("node_1", server.URL, "test-token", "", t.TempDir(), "", "", "", "", "", executorWrapper{})
	if err != nil {
		t.Fatal(err)
	}
	ag.http = server.Client()

	task := admiral.FleetTask{
		TaskID:      "task_1",
		OperationID: "op_1",
		NodeID:      "node_1",
		Action:      admiral.ActionInspectApp,
		InstanceID:  "inst_1",
	}

	if err := ag.HandleTask(task); err != nil {
		t.Fatalf("HandleTask: %v", err)
	}

	if sent.TaskID != "task_1" {
		t.Fatalf("expected task_1, got %q", sent.TaskID)
	}
}

func TestReconcileDoesNotPanic(t *testing.T) {
	ag := &Agent{
		NodeID: "node_1",
		APIURL: "https://example.com",
	}
	ctx := context.Background()
	ag.Reconcile(ctx)
}

func TestStartOutboxFlusherExitsOnCancel(t *testing.T) {
	ag := &Agent{
		outbox: &outbox{dir: t.TempDir()},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ag.StartOutboxFlusher(ctx, time.Millisecond)
}

func TestSendCallback(t *testing.T) {
	var gotMethod, gotPath string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ag, err := New("node_1", server.URL, "token", "", t.TempDir(), "", "", "", "", "", executorWrapper{})
	if err != nil {
		t.Fatal(err)
	}
	ag.http = server.Client()

	result := admiral.TaskResult{
		TaskID:      "t1",
		OperationID: "op1",
		NodeID:      "node_1",
		Success:     true,
	}
	if err := ag.send(result); err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotMethod != "POST" {
		t.Fatalf("expected POST, got %s", gotMethod)
	}
	if gotPath != "/api/v1/fleet/callback" {
		t.Fatalf("expected /api/v1/fleet/callback, got %s", gotPath)
	}
}

func TestPostStorage(t *testing.T) {
	var got admiral.StorageReport
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ag := &Agent{
		NodeID:     "node_1",
		APIURL:     server.URL,
		FleetToken: "tok",
		http:       server.Client(),
	}

	report := admiral.StorageReport{
		InstanceID: "inst_1",
		NodeID:     "node_1",
	}
	if err := ag.postStorage(report); err != nil {
		t.Fatalf("postStorage: %v", err)
	}
	if got.InstanceID != "inst_1" {
		t.Fatalf("expected inst_1, got %q", got.InstanceID)
	}
}

func TestSendCallbackHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ag := &Agent{
		APIURL: server.URL,
		http:   server.Client(),
	}
	err := ag.send(admiral.TaskResult{})
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
}

// executorWrapper wraps the real executor so HandleTask does not panic on nil.
type executorWrapper struct{}

func (executorWrapper) Execute(ctx context.Context, task admiral.FleetTask, nodeID string) admiral.TaskResult {
	return admiral.TaskResult{
		TaskID:      task.TaskID,
		OperationID: task.OperationID,
		NodeID:      nodeID,
		Success:     true,
	}
}

func TestOutboxDefaultDir(t *testing.T) {
	o := newOutbox("")
	if o.dir != "/var/lib/admiral/outbox" {
		t.Fatalf("expected default outbox dir, got %q", o.dir)
	}
}

func TestOutboxCustomDir(t *testing.T) {
	dir := t.TempDir()
	o := newOutbox(dir)
	if o.dir != dir {
		t.Fatalf("expected %q, got %q", dir, o.dir)
	}
}

func TestOutboxEnqueue(t *testing.T) {
	dir := t.TempDir()
	o := newOutbox(dir)

	result := admiral.TaskResult{TaskID: "t1", OperationID: "op1", Success: true}
	if err := o.enqueue(result); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 outbox file, got %d", len(entries))
	}
}

func TestOutboxFlushSendsAndRemoves(t *testing.T) {
	dir := t.TempDir()
	o := newOutbox(dir)

	result := admiral.TaskResult{TaskID: "t1", OperationID: "op1", Success: true}
	if err := o.enqueue(result); err != nil {
		t.Fatal(err)
	}

	var sent int
	send := func(r admiral.TaskResult) error {
		sent++
		return nil
	}

	if err := o.flush(send); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if sent != 1 {
		t.Fatalf("expected 1 send, got %d", sent)
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("expected outbox dir to be empty after flush, got %d files", len(entries))
	}
}

func TestOutboxFlushStopsOnSendError(t *testing.T) {
	dir := t.TempDir()
	o := newOutbox(dir)

	o.enqueue(admiral.TaskResult{TaskID: "t1", OperationID: "op1"})
	o.enqueue(admiral.TaskResult{TaskID: "t2", OperationID: "op2"})

	send := func(r admiral.TaskResult) error {
		return os.ErrPermission
	}

	if err := o.flush(send); err == nil {
		t.Fatal("expected error from flush")
	}

	// Both files should remain for a later retry.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("expected both outbox files to remain, got %d", len(entries))
	}
}

func TestOutboxFlushNonExistentDir(t *testing.T) {
	o := newOutbox(filepath.Join(t.TempDir(), "nonexistent"))
	if err := o.flush(func(admiral.TaskResult) error { return nil }); err != nil {
		t.Fatalf("flush on missing dir: %v", err)
	}
}

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	data := map[string]string{"key": "value"}
	writeJSON(w, http.StatusCreated, data)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}
	if w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("expected application/json, got %s", w.Header().Get("Content-Type"))
	}

	var decoded map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["key"] != "value" {
		t.Fatalf("expected value, got %q", decoded["key"])
	}
}

func TestHTTPServerHealthEndpoint(t *testing.T) {
	addr := startTestHTTPServer(t, "node_1", "simulated", "127.0.0.1", "8080")

	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestHTTPServerEndpointEndpoint(t *testing.T) {
	addr := startTestHTTPServer(t, "node_x", "systemd", "", "")

	resp, err := http.Get("http://" + addr + "/endpoint")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var info EndpointInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if info.NodeID != "node_x" {
		t.Fatalf("expected node_x, got %q", info.NodeID)
	}
	if info.Executor != "systemd" {
		t.Fatalf("expected systemd, got %q", info.Executor)
	}
}

func startTestHTTPServer(t *testing.T, nodeID, executor, targetHost, targetPort string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/endpoint":
			writeJSON(w, http.StatusOK, EndpointInfo{
				NodeID:     nodeID,
				TargetHost: targetHost,
				TargetPort: targetPort,
				Executor:   executor,
				Status:     "healthy",
				CheckedAt:  time.Now().UTC().Format(time.RFC3339),
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server.Listener.Addr().String()
}

func TestStartHTTPServerInvalidAddr(t *testing.T) {
	StartHTTPServer("", "node_1", "simulated", "", "")
}

func TestFleetVersion(t *testing.T) {
	if FleetVersion == "" {
		t.Fatal("FleetVersion must not be empty")
	}
}

func TestExtractInstanceID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"admiral prefix", "admiral-inst_001", "inst_001"},
		{"no prefix", "other-pod", ""},
		{"empty", "", ""},
		{"just admiral", "admiral-", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractInstanceID(tt.input)
			if got != tt.expected {
				t.Errorf("extractInstanceID(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSendResult_And_FlushOutbox(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ag := &Agent{
		APIURL: server.URL,
		http:   server.Client(),
		outbox: &outbox{dir: t.TempDir()},
	}

	res := admiral.TaskResult{TaskID: "t_result"}
	err := ag.SendResult(res)
	if err != nil {
		t.Fatalf("unexpected error on SendResult: %v", err)
	}

	err = ag.FlushOutbox()
	if err != nil {
		t.Fatalf("unexpected error on FlushOutbox: %v", err)
	}

	agNoOutbox := &Agent{}
	err = agNoOutbox.FlushOutbox()
	if err != nil {
		t.Fatalf("unexpected error on FlushOutbox with nil outbox: %v", err)
	}
}

func TestStartBackupStorageWarner_Exit(t *testing.T) {
	ag := &Agent{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Warner should exit immediately because context is cancelled
	ag.StartBackupStorageWarner(ctx)
}

func TestPostHeartbeat_AllCases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer bad" {
			w.WriteHeader(http.StatusUnauthorized)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ag := &Agent{
		APIURL:     server.URL,
		FleetToken: "tok",
		http:       server.Client(),
	}

	req := admiral.HeartbeatRequest{NodeID: "test-node"}
	err := ag.postHeartbeat(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	agBad := &Agent{
		APIURL:     server.URL,
		FleetToken: "bad",
		http:       server.Client(),
	}
	err = agBad.postHeartbeat(req)
	if err == nil {
		t.Fatal("expected error with unauthorized request")
	}
}

func TestStartHeartbeatSender_Cancel(t *testing.T) {
	ag := &Agent{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Should exit immediately without panic or hanging
	ag.StartHeartbeatSender(ctx)
}

func TestIPAllowed(t *testing.T) {
	tests := []struct {
		name           string
		remoteAddr     string
		allowedAdminIP string
		expected       bool
	}{
		{"loopback ipv4", "127.0.0.1:1234", "", true},
		{"loopback ipv6", "[::1]:1234", "", true},
		{"matched admin ip", "192.168.1.100:1234", "192.168.1.100", true},
		{"mismatched admin ip", "192.168.1.100:1234", "192.168.1.200", false},
		{"mismatched non-loopback ip", "192.168.1.100:1234", "", false},
		{"invalid remote addr", "invalid-ip", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ipAllowed(tt.remoteAddr, tt.allowedAdminIP)
			if got != tt.expected {
				t.Errorf("ipAllowed(%q, %q) = %v, want %v", tt.remoteAddr, tt.allowedAdminIP, got, tt.expected)
			}
		})
	}
}

func TestHTTPServer_ReadyAndAllowedAdmin(t *testing.T) {
	addr := "127.0.0.1:19091"
	srv := StartHTTPServerWithAllowedAdmin(addr, "node_test", "simulated", "host", "port", "127.0.0.1")
	if srv == nil {
		t.Fatal("expected server to start")
	}
	defer srv.Close()

	// Wait a moment for server to listen
	time.Sleep(100 * time.Millisecond)

	client := &http.Client{}

	// 1. Get health (should succeed because we request from localhost/loopback)
	resp, err := client.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("Get /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	// 2. Get ready
	resp2, err := client.Get("http://" + addr + "/ready")
	if err != nil {
		t.Fatalf("Get /ready: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp2.StatusCode)
	}
	var ready ReadyInfo
	json.NewDecoder(resp2.Body).Decode(&ready)
	if ready.Status != "ok" {
		t.Errorf("expected ready.Status ok, got %q", ready.Status)
	}

	// 3. Test allowedHandler with a mismatched IP (we can fake a custom request or handler directly if remote addr is hard to fake on actual server)
	handler := allowedHandler("192.168.10.10", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req, _ := http.NewRequest("GET", "/health", nil)
	req.RemoteAddr = "192.168.10.20:1234"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for mismatched remote IP, got %d", rr.Code)
	}
}

func TestClaimTaskContext_AllCases(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	pubHex := hex.EncodeToString(pub)

	tests := []struct {
		name         string
		statusCode   int
		respBodyFunc func() interface{}
		pubKeyHex    string
		wantErr      bool
		errContains  string
	}{
		{
			name:       "No Content (no task)",
			statusCode: http.StatusNoContent,
			respBodyFunc: func() interface{} {
				return nil
			},
			pubKeyHex:   pubHex,
			wantErr:     true,
			errContains: "no task available",
		},
		{
			name:       "Server Error",
			statusCode: http.StatusInternalServerError,
			respBodyFunc: func() interface{} {
				return nil
			},
			pubKeyHex:   pubHex,
			wantErr:     true,
			errContains: "claim task returned HTTP 500",
		},
		{
			name:       "Missing Task or CommandID",
			statusCode: http.StatusOK,
			respBodyFunc: func() interface{} {
				return map[string]interface{}{"command_id": ""}
			},
			pubKeyHex:   pubHex,
			wantErr:     true,
			errContains: "invalid claim response",
		},
		{
			name:       "Unsigned Task rejected",
			statusCode: http.StatusOK,
			respBodyFunc: func() interface{} {
				return map[string]interface{}{
					"command_id": "cmd_123",
					"task": &admiral.FleetTask{
						TaskID: "task_123",
					},
				}
			},
			pubKeyHex:   pubHex,
			wantErr:     true,
			errContains: "task signature is required but missing",
		},
		{
			name:       "Signed Task without verification key",
			statusCode: http.StatusOK,
			respBodyFunc: func() interface{} {
				return map[string]interface{}{
					"command_id": "cmd_123",
					"task": &admiral.FleetTask{
						TaskID:        "task_123",
						TaskSignature: "sig",
					},
				}
			},
			pubKeyHex:   "",
			wantErr:     true,
			errContains: "task public key is not configured",
		},
		{
			name:       "Valid signed task",
			statusCode: http.StatusOK,
			respBodyFunc: func() interface{} {
				task := &admiral.FleetTask{
					TaskID:   "task_123",
					SignedAt: time.Now().Unix(),
				}
				verifyTask := *task
				verifyTask.TaskSignature = ""
				verifyTask.SignedAt = 0
				payload, _ := json.Marshal(verifyTask)
				msg := append(payload, []byte(fmt.Sprintf("%d", task.SignedAt))...)
				sig := ed25519.Sign(priv, msg)
				task.TaskSignature = hex.EncodeToString(sig)

				return map[string]interface{}{
					"command_id": "cmd_123",
					"task":       task,
				}
			},
			pubKeyHex: pubHex,
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				if tt.statusCode == http.StatusOK {
					json.NewEncoder(w).Encode(tt.respBodyFunc())
				}
			}))
			defer server.Close()

			ag, err := New("node_1", server.URL, "token", "", t.TempDir(), "", "", "", "", tt.pubKeyHex, nil)
			if err != nil {
				t.Fatal(err)
			}
			ag.http = server.Client()

			task, cmdID, err := ag.ClaimTask()
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("expected error containing %q, got %v", tt.errContains, err)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if cmdID != "cmd_123" {
					t.Errorf("expected command_id cmd_123, got %q", cmdID)
				}
				if task.TaskID != "task_123" {
					t.Errorf("expected task_id task_123, got %q", task.TaskID)
				}
			}
		})
	}
}

func TestReportRunning_AllCases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/fleet/tasks/cmd_ok/running" {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	ag := &Agent{
		APIURL: server.URL,
		http:   server.Client(),
	}

	err := ag.ReportRunning("cmd_ok")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = ag.ReportRunning("cmd_bad")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestRenewLease_AllCases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/fleet/tasks/cmd_ok/renew-lease" {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	ag := &Agent{
		APIURL: server.URL,
		http:   server.Client(),
	}

	err := ag.RenewLease("cmd_ok")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = ag.RenewLease("cmd_bad")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestStartLeaseRenewer_Close(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ag := &Agent{
		APIURL: server.URL,
		http:   server.Client(),
	}

	stop := ag.StartLeaseRenewer("cmd_123")
	stop() // stop immediately
}

func TestStartReconciler_Cancel(t *testing.T) {
	ag := &Agent{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Reconciler should exit immediately because context is cancelled
	ag.StartReconciler(ctx, time.Millisecond)
}

func TestWarnIfNoBackupStorage(t *testing.T) {
	ag := &Agent{}
	// This should not panic
	ag.warnIfNoBackupStorage()
}

func TestBuildHeartbeat(t *testing.T) {
	ag := &Agent{
		NodeID: "node-1",
	}
	hb := ag.buildHeartbeat(context.Background())
	if hb.NodeID != "node-1" {
		t.Errorf("expected node-1, got %q", hb.NodeID)
	}
	if hb.FleetVersion != FleetVersion {
		t.Errorf("expected %q, got %q", FleetVersion, hb.FleetVersion)
	}
}

func TestNewOutboxDefaultDir(t *testing.T) {
	o := newOutbox("")
	if !strings.HasSuffix(o.dir, "outbox") {
		t.Errorf("unexpected default outbox dir: %q", o.dir)
	}
}

func TestSanitizeInstanceID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple", "inst_001", "inst_001"},
		{"with dots", "inst..001", "inst001"},
		{"with slashes", "inst/001", "inst001"},
		{"path traversal", "../etc", "etc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeInstanceID(tt.input)
			if got != tt.expected {
				t.Errorf("sanitizeInstanceID(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestParseStorageLimitBytes(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int64
	}{
		{"empty", "", 0},
		{"gib", "1GiB", 1073741824},
		{"gb", "2GB", 2000000000},
		{"g", "1G", 1073741824},
		{"mib", "512MiB", 536870912},
		{"mb", "100MB", 100000000},
		{"kib", "1KiB", 1024},
		{"tib", "1TiB", 1099511627776},
		{"lowercase gb", "1gb", 1000000000},
		{"invalid", "xyz", 0},
		{"negative", "-1G", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseStorageLimitBytes(tt.input)
			if got != tt.expected {
				t.Errorf("parseStorageLimitBytes(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}

func TestClassifyStorageState(t *testing.T) {
	tests := []struct {
		name      string
		pct       float64
		wantState admiral.StorageState
	}{
		{"ok", 50, admiral.StorageOK},
		{"warning", 60, admiral.StorageWarning},
		{"critical", 80, admiral.StorageCritical},
		{"over quota", 100, admiral.StorageOverQuota},
		{"over quota above", 150, admiral.StorageOverQuota},
		{"just below warning", 59.9, admiral.StorageOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, _ := classifyStorageState(tt.pct)
			if state != tt.wantState {
				t.Errorf("classifyStorageState(%v) = %q, want %q", tt.pct, state, tt.wantState)
			}
		})
	}
}

func TestPostHealth(t *testing.T) {
	var got healthReport
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ag := &Agent{
		APIURL:     server.URL,
		FleetToken: "tok",
		http:       server.Client(),
	}

	report := healthReport{
		InstanceID:   "inst_1",
		NodeID:       "node_1",
		HealthStatus: "healthy",
		CheckedAt:    "now",
	}
	if err := ag.postHealth(report); err != nil {
		t.Fatalf("postHealth: %v", err)
	}
	if got.InstanceID != "inst_1" {
		t.Fatalf("expected inst_1, got %q", got.InstanceID)
	}
}

func TestPostHealthHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	ag := &Agent{
		APIURL: server.URL,
		http:   server.Client(),
	}
	err := ag.postHealth(healthReport{})
	if err == nil {
		t.Fatal("expected error for HTTP 503")
	}
}

func TestParseStorageLimitBytesEdgeCases(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
	}{
		{"  1G  ", 1073741824},
		{"1.5G", 1610612736},
		{"0.5G", 536870912},
		{"100", 0},
		{"100 kb", 102400},
		{"100KB", 102400},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseStorageLimitBytes(tt.input)
			if got != tt.expected {
				t.Errorf("parseStorageLimitBytes(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}
