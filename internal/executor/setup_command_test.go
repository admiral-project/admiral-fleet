// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/admiral-project/admiral/admiral-fleet/internal/podman"
	"github.com/admiral-project/admiral/admiral-fleet/internal/systemd"
	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

type setupRacePodmanRunner struct {
	calls       [][]string
	existsCalls int
}

func (r *setupRacePodmanRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	joined := strings.Join(call, " ")

	switch {
	case joined == "podman pod exists admiral-racedemo":
		return nil, os.ErrNotExist
	case joined == "podman container exists admiral-racedemo-backend":
		r.existsCalls++
		if r.existsCalls < 3 {
			return nil, errors.New("no such container")
		}
		return []byte{}, nil
	case joined == "podman container inspect admiral-racedemo-backend --format json":
		return []byte(`[{"State":{"Status":"running"}}]`), nil
	case joined == "podman exec admiral-racedemo-db healthcheck":
		return []byte("ok"), nil
	case joined == "podman exec admiral-racedemo-backend app healthcheck":
		return []byte("ok"), nil
	case strings.Contains(joined, "podman exec admiral-racedemo-backend sh -c app bootstrap"):
		return []byte("ok"), nil
	case joined == "podman port admiral-racedemo-infra 8000/tcp":
		return []byte("127.0.0.1:40013"), nil
	case joined == "podman port admiral-racedemo-infra 5432/tcp":
		return []byte("127.0.0.1:40014"), nil
	default:
		return []byte(`[]`), nil
	}
}

// TestTaskHasSetup detects when a task declares a setup_command.
func TestTaskHasSetup(t *testing.T) {
	tests := []struct {
		name string
		task admiral.FleetTask
		want bool
	}{
		{
			name: "no setup command",
			task: admiral.FleetTask{
				Services: []admiral.ServiceInfo{
					{Name: "web", Image: "nginx:1"},
				},
			},
			want: false,
		},
		{
			name: "one service with setup",
			task: admiral.FleetTask{
				Services: []admiral.ServiceInfo{
					{Name: "web", Image: "nginx:1"},
					{Name: "backend", Image: "app:1", SetupCommand: "init-db"},
				},
			},
			want: true,
		},
		{
			name: "setup command whitespace only",
			task: admiral.FleetTask{
				Services: []admiral.ServiceInfo{
					{Name: "web", Image: "nginx:1", SetupCommand: "   "},
				},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := taskHasSetup(tt.task); got != tt.want {
				t.Fatalf("taskHasSetup() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSetupMarkerRoundTrip verifies the local setup_done marker file is
// created and detected correctly.
func TestSetupMarkerRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	exec := NewSystemdPodmanWithFS(systemd.NewManager(nil), nil, "/tmp/quadlet", tmpDir, "", fakeFS{}, fakeUserLookup{})

	if exec.setupMarkerExists("inst_001") {
		t.Fatal("expected marker to not exist initially")
	}
	exec.writeSetupMarker("inst_001")
	if !exec.setupMarkerExists("inst_001") {
		t.Fatal("expected marker to exist after writeSetupMarker")
	}
	data, _ := os.ReadFile(setupMarkerPath(tmpDir, "inst_001"))
	if string(data) != "done" {
		t.Fatalf("expected marker content 'done', got %q", string(data))
	}
}

// TestSetupMarkerAbsentOnFreshNode ensures the marker does not exist on
// a fresh data directory (e.g. after a cross-node migration).
func TestSetupMarkerAbsentOnFreshNode(t *testing.T) {
	tmpDir := t.TempDir()
	exec := NewSystemdPodmanWithFS(systemd.NewManager(nil), nil, "/tmp/quadlet", tmpDir, "", fakeFS{}, fakeUserLookup{})
	if exec.setupMarkerExists("never_seen") {
		t.Fatal("expected marker to not exist on fresh data dir")
	}
}

// TestProvisionSetupCommandSkippedWhenSetupCompleted verifies that when
// admirald sends SetupCompleted=true, the executor does NOT attempt to
// run setup_command even though services declare it. The provision
// should succeed with has_setup:true in metadata without invoking
// podman exec.
func TestProvisionSetupCommandSkippedWhenSetupCompleted(t *testing.T) {
	tmpDir := t.TempDir()
	quadletDir := filepath.Join(tmpDir, "quadlet")
	dataDir := filepath.Join(tmpDir, "data")
	if err := os.MkdirAll(quadletDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0751); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "instances"), 0751); err != nil {
		t.Fatal(err)
	}

	systemdRunner := &fakeSystemdRunner{}
	// Make pod exists fail so provision proceeds past idempotency
	podmanRunner := &fakePodmanRunner{
		overrides: map[string]fakeOverride{
			"podman pod exists admiral-skipdemo": {
				err: os.ErrNotExist,
			},
		},
	}
	exec := NewSystemdPodmanWithFS(
		systemd.NewManager(systemdRunner),
		podman.NewInspector(podmanRunner),
		quadletDir,
		dataDir,
		"nobody",
		fakeFS{},
		fakeUserLookup{},
	)

	task := admiral.FleetTask{
		TaskID:         "task_setup_skip",
		OperationID:    "op_setup_skip",
		NodeID:         "node_1",
		Action:         admiral.ActionProvisionApp,
		InstanceID:     "skipdemo",
		SetupCompleted: true,
		Tier: admiral.TierInfo{
			Name:    "dev",
			CPU:     1,
			Memory:  "512MiB",
			Storage: "1GiB",
		},
		Services: []admiral.ServiceInfo{
			{
				Name:         "backend",
				Image:        "example.com/app:1",
				SetupCommand: "app bootstrap",
			},
		},
	}

	res := exec.Execute(context.Background(), task, "node_1")
	if !res.Success {
		t.Fatalf("expected success, got error %q", res.Error)
	}
	if !strings.Contains(res.Metadata, "\"has_setup\":true") {
		t.Fatalf("expected has_setup:true in metadata, got %s", res.Metadata)
	}
	// Confirm no podman exec sh -c was invoked
	for _, call := range podmanRunner.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "podman exec") && strings.Contains(joined, "sh -c") {
			t.Fatalf("setup_command should have been skipped, but podman exec was called: %s", joined)
		}
	}
}

// TestProvisionSetupCommandSkippedByMarker verifies that when the local
// setup_done marker exists (lost-callback scenario), the executor does
// NOT re-run setup_command even if SetupCompleted is false.
func TestProvisionSetupCommandSkippedByMarker(t *testing.T) {
	tmpDir := t.TempDir()
	quadletDir := filepath.Join(tmpDir, "quadlet")
	dataDir := filepath.Join(tmpDir, "data")
	if err := os.MkdirAll(quadletDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "instances"), 0751); err != nil {
		t.Fatal(err)
	}

	systemdRunner := &fakeSystemdRunner{}
	podmanRunner := &fakePodmanRunner{
		overrides: map[string]fakeOverride{
			"podman pod exists admiral-markerdemo": {
				err: os.ErrNotExist,
			},
		},
	}
	exec := NewSystemdPodmanWithFS(
		systemd.NewManager(systemdRunner),
		podman.NewInspector(podmanRunner),
		quadletDir,
		dataDir,
		"nobody",
		fakeFS{},
		fakeUserLookup{},
	)

	// Pre-create the marker so setup is skipped on this node
	exec.writeSetupMarker("markerdemo")

	task := admiral.FleetTask{
		TaskID:         "task_marker_skip",
		OperationID:    "op_marker_skip",
		NodeID:         "node_1",
		Action:         admiral.ActionProvisionApp,
		InstanceID:     "markerdemo",
		SetupCompleted: false,
		Tier: admiral.TierInfo{
			Name:    "dev",
			CPU:     1,
			Memory:  "512MiB",
			Storage: "1GiB",
		},
		Services: []admiral.ServiceInfo{
			{
				Name:         "backend",
				Image:        "example.com/app:1",
				SetupCommand: "app bootstrap",
			},
		},
	}

	res := exec.Execute(context.Background(), task, "node_1")
	if !res.Success {
		t.Fatalf("expected success, got error %q", res.Error)
	}
	if !strings.Contains(res.Metadata, "\"has_setup\":true") {
		t.Fatalf("expected has_setup:true in metadata, got %s", res.Metadata)
	}
	for _, call := range podmanRunner.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "podman exec") && strings.Contains(joined, "sh -c") {
			t.Fatalf("setup_command should have been skipped by marker, but podman exec was called: %s", joined)
		}
	}
}

func TestProvisionSetupCommandWaitsForDependenciesToBeReady(t *testing.T) {
	tmpDir := t.TempDir()
	quadletDir := filepath.Join(tmpDir, "quadlet")
	dataDir := filepath.Join(tmpDir, "data")
	if err := os.MkdirAll(quadletDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "instances"), 0751); err != nil {
		t.Fatal(err)
	}

	systemdRunner := &fakeSystemdRunner{}
	podmanRunner := &setupRacePodmanRunner{}
	exec := NewSystemdPodmanWithFS(
		systemd.NewManager(systemdRunner),
		podman.NewInspector(podmanRunner),
		quadletDir,
		dataDir,
		"nobody",
		fakeFS{},
		fakeUserLookup{},
	)

	task := admiral.FleetTask{
		TaskID:      "task_race_wait",
		OperationID: "op_race_wait",
		NodeID:      "node_1",
		Action:      admiral.ActionProvisionApp,
		InstanceID:  "racedemo",
		Tier: admiral.TierInfo{
			Name:    "dev",
			CPU:     1,
			Memory:  "512MiB",
			Storage: "1GiB",
		},
		Services: []admiral.ServiceInfo{
			{
				Name:  "db",
				Image: "example.com/db:1",
				Port:  5432,
				HealthCheck: &admiral.YAMLHealthCheck{
					Type:    "command",
					Command: []string{"healthcheck"},
				},
			},
			{
				Name:         "backend",
				Image:        "example.com/app:1",
				Port:         8000,
				DependsOn:    []string{"db"},
				SetupCommand: "app bootstrap",
				HealthCheck: &admiral.YAMLHealthCheck{
					Type:    "command",
					Command: []string{"app", "healthcheck"},
				},
			},
		},
	}

	res := exec.Execute(context.Background(), task, "node_1")
	if !res.Success {
		t.Fatalf("expected success, got error %q", res.Error)
	}
	if podmanRunner.existsCalls < 3 {
		t.Fatalf("expected repeated container existence checks, got %d", podmanRunner.existsCalls)
	}
	foundExec := false
	for _, call := range podmanRunner.calls {
		if strings.Contains(strings.Join(call, " "), "podman exec admiral-racedemo-db healthcheck") {
			foundExec = true
		}
		if strings.Contains(strings.Join(call, " "), "podman exec admiral-racedemo-backend sh -c app bootstrap") {
			foundExec = true
		}
	}
	if !foundExec {
		t.Fatalf("expected dependency readiness checks and setup_command exec, calls: %#v", podmanRunner.calls)
	}
}
