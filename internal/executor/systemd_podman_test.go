package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/admiral-project/admiral/admiral-fleet/internal/systemd"
	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

type fakeSystemdRunner struct {
	calls [][]string
	err   error
}

func (r *fakeSystemdRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	if r.err != nil {
		return nil, r.err
	}
	return []byte("ok"), nil
}

func TestSystemdPodmanExecutorStartsAppUnit(t *testing.T) {
	runner := &fakeSystemdRunner{}
	manager := systemd.NewManager(runner)
	manager.Timeout = time.Second
	exec := NewSystemdPodman(manager, nil, "", "")

	res := exec.Execute(context.Background(), admiral.FleetTask{
		TaskID:      "task_1",
		OperationID: "op_1",
		NodeID:      "node_1",
		Action:      admiral.ActionStartApp,
		InstanceID:  "demo001",
	}, "node_1")

	if !res.Success {
		t.Fatalf("expected success, got %q", res.Error)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected one systemd call, got %d", len(runner.calls))
	}
	got := runner.calls[0]
	want := []string{"systemctl", "start", "admiral-demo001-app.service"}
	if len(got) != len(want) {
		t.Fatalf("unexpected call length: got %#v want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected call: got %#v want %#v", got, want)
		}
	}
}

func TestSystemdPodmanExecutorReturnsSystemdError(t *testing.T) {
	runner := &fakeSystemdRunner{err: errors.New("unit not found")}
	manager := systemd.NewManager(runner)
	manager.Timeout = time.Second
	exec := NewSystemdPodman(manager, nil, "", "")

	res := exec.Execute(context.Background(), admiral.FleetTask{
		TaskID:      "task_1",
		OperationID: "op_1",
		NodeID:      "node_1",
		Action:      admiral.ActionStopApp,
		InstanceID:  "demo001",
	}, "node_1")

	if res.Success {
		t.Fatal("expected stop to fail")
	}
	if res.Error == "" {
		t.Fatal("expected actionable error")
	}
}

func TestSystemdPodmanExecutorRejectsInvalidProvision(t *testing.T) {
	exec := NewSystemdPodman(nil, nil, "", "")
	res := exec.Execute(context.Background(), admiral.FleetTask{
		NodeID:     "node_1",
		Action:     admiral.ActionProvisionApp,
		InstanceID: "demo001",
	}, "node_1")

	if res.Success {
		t.Fatal("expected invalid provision to fail clearly")
	}
}
