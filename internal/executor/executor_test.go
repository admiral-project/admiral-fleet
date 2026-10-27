// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/admiral-project/admiral/admiral-fleet/internal/healthcheck"
	"github.com/admiral-project/admiral/admiral-fleet/internal/osutil"
	"github.com/admiral-project/admiral/admiral-fleet/internal/podman"
	"github.com/admiral-project/admiral/admiral-fleet/internal/quadlet"
	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

func TestSimulatedExecutorSucceedsForKnownAction(t *testing.T) {
	exec := NewSimulated()
	res := exec.Execute(context.Background(), admiral.FleetTask{
		TaskID:      "task_1",
		OperationID: "op_1",
		NodeID:      "node_1",
		Action:      admiral.ActionProvisionApp,
		InstanceID:  "inst_1",
	}, "node_1")

	if !res.Success {
		t.Fatalf("expected success, got error %q", res.Error)
	}
	if res.OperationID != "op_1" || res.TaskID != "task_1" {
		t.Fatalf("result did not preserve identifiers: %+v", res)
	}
}

func TestWriteServiceHealthChecksPersistsOnlyCheckMetadata(t *testing.T) {
	dataDir := t.TempDir()
	instanceDir := filepath.Join(dataDir, "instances", "inst_1")
	if err := os.MkdirAll(instanceDir, 0700); err != nil {
		t.Fatal(err)
	}
	exec := &SystemdPodmanExecutor{
		DataDir:  dataDir,
		FS:       osutil.RealFileSystem{},
		Renderer: &quadlet.Renderer{PublishAddress: "10.99.0.2"},
	}
	service := admiral.ServiceInfo{
		Name:        "db",
		Env:         map[string]string{"POSTGRES_PASSWORD": "do-not-persist"},
		Secrets:     map[string]string{"DB_PASSWORD": "also-do-not-persist"},
		HealthCheck: &admiral.YAMLHealthCheck{Type: "command", Command: []string{"pg_isready"}, TimeoutSeconds: 3},
	}
	if err := exec.writeServiceHealthChecks("inst_1", []admiral.ServiceInfo{service}, map[string]int{"db": 40001}); err != nil {
		t.Fatalf("write service health checks: %v", err)
	}
	path := serviceHealthChecksPath(dataDir, "inst_1")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted health checks: %v", err)
	}
	if string(data) == "" || strings.Contains(string(data), "do-not-persist") || strings.Contains(string(data), "also-do-not-persist") {
		t.Fatalf("health check metadata contains secrets or is empty: %s", data)
	}
	var checks []healthcheck.Check
	if err := json.Unmarshal(data, &checks); err != nil {
		t.Fatalf("decode health check metadata: %v", err)
	}
	if len(checks) != 1 || checks[0].ServiceName != "db" || checks[0].HostAddress != "10.99.0.2" ||
		checks[0].HostPort != 40001 || checks[0].HealthCheck.Type != "command" {
		t.Fatalf("persisted checks = %#v", checks)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("health check metadata mode = %04o, want 0600", got)
	}
}

func TestSimulatedExecutorRejectsWrongNode(t *testing.T) {
	exec := NewSimulated()
	res := exec.Execute(context.Background(), admiral.FleetTask{NodeID: "node_2", Action: admiral.ActionProvisionApp}, "node_1")
	if res.Success {
		t.Fatal("expected wrong node to fail")
	}
}

func TestSimulatedExecutorRejectsUnknownAction(t *testing.T) {
	exec := NewSimulated()
	res := exec.Execute(context.Background(), admiral.FleetTask{NodeID: "node_1", Action: admiral.TaskAction("bad_action")}, "node_1")
	if res.Success {
		t.Fatal("expected unknown action to fail")
	}
}

func TestPublishedHealthcheckAddressUsesPublishAddress(t *testing.T) {
	exec := &SystemdPodmanExecutor{
		Renderer: &quadlet.Renderer{PublishAddress: "10.99.0.2"},
	}
	if got := exec.publishedHealthcheckAddress(40000); got != "10.99.0.2:40000" {
		t.Fatalf("published healthcheck address = %q, want %q", got, "10.99.0.2:40000")
	}
}

func TestPublishedHealthcheckAddressDefaultsToLoopback(t *testing.T) {
	exec := &SystemdPodmanExecutor{
		Renderer: &quadlet.Renderer{},
	}
	if got := exec.publishedHealthcheckAddress(40000); got != "127.0.0.1:40000" {
		t.Fatalf("published healthcheck address = %q, want %q", got, "127.0.0.1:40000")
	}
}

func TestServiceHTTPHealthCheckDoesNotFollowRedirects(t *testing.T) {
	var targetRequests int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetRequests++
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()
	port, err := strconv.Atoi(source.URL[strings.LastIndex(source.URL, ":")+1:])
	if err != nil {
		t.Fatal(err)
	}
	podmanRunner := &fakePodmanRunner{overrides: map[string]fakeOverride{
		"container inspect admiral-demo001-db": {output: []byte(`[{"Name":"admiral-demo001-db","State":{"Status":"running"}}]`)},
	}}
	service := admiral.ServiceInfo{
		Name: "db",
		HealthCheck: &admiral.YAMLHealthCheck{
			Type:           "http",
			Path:           "/health",
			ExpectedStatus: http.StatusOK,
		},
	}
	exec := &SystemdPodmanExecutor{
		Podman:   podman.NewInspector(podmanRunner),
		Renderer: &quadlet.Renderer{},
	}
	ready, err := exec.serviceReadyCheck(context.Background(), "demo001", "admiral-demo001-db", service, map[string]int{"db": port}, 5*time.Second)
	if ready || err == nil {
		t.Fatalf("redirect health check = ready %t, error %v; want failure", ready, err)
	}
	if targetRequests != 0 {
		t.Fatalf("provision health check followed redirect and contacted target %d times", targetRequests)
	}
}
