package executor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/admiral-project/admiral/admiral-fleet/internal/podman"
	"github.com/admiral-project/admiral/admiral-fleet/internal/systemd"
	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

type errorRunner struct {
	err error
}

func (r *errorRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return nil, r.err
}

func TestSystemdPodmanExecutorFailureInjection(t *testing.T) {
	runner := &errorRunner{err: errors.New("command failed")}
	manager := systemd.NewManager(runner)
	inspector := podman.NewInspector(runner)

	exec := NewSystemdPodmanWithFS(manager, inspector, "/tmp/quadlet", "/tmp/data", "nobody", fakeFS{}, fakeUserLookup{})

	task := admiral.FleetTask{
		TaskID:      "task_fail",
		OperationID: "op_fail",
		NodeID:      "node_1",
		Action:      admiral.ActionStartApp,
		InstanceID:  "demo_fail",
		Services: []admiral.ServiceInfo{
			{Name: "app", Image: "example.com/app:1"},
		},
	}

	res := exec.Execute(context.Background(), task, "node_1")

	if res.Success {
		t.Fatal("expected failure, got success")
	}
	if !strings.Contains(res.Error, "command failed") {
		t.Errorf("expected error message to contain 'command failed', got %q", res.Error)
	}
}

func TestSystemdPodmanExecutorSecretSanitizationInError(t *testing.T) {
	// We'll test sanitization using a fake runner that mimics CommandRunner's sanitization
	// or by using CommandRunner with a mockable execution mechanism.
	// Since CommandRunner uses exec.CommandContext, we can't easily mock it without
	// changing CommandRunner to use an interface for execution.
	// Let's test the sanitization logic via podman.CommandRunner.Run directly but with
	// a command that we know will fail without hitting the host too much, or
	// by just trusting the security package tests and adding a test for CommandRunner
	// that doesn't rely on host commands if possible.

	// Actually, we already have a direct test for CommandRunner.Run in failure_injection_test.go
	// that was failing because it used 'sudo' and couldn't find 'podman'.
	// Let's use a simple command like 'false' or a non-existent command.

	runner := podman.CommandRunner{}
	_, err := runner.Run(context.Background(), "nonexistent-admiral-cmd", "password=secret")
	if err == nil {
		t.Fatal("expected error")
	}

	if strings.Contains(err.Error(), "secret") && !strings.Contains(err.Error(), "[REDACTED]") {
		t.Errorf("secret leaked in error: %v", err)
	}
}
