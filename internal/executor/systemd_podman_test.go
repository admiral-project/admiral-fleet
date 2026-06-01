package executor

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/admiral-project/admiral/admiral-fleet/internal/osutil"
	"github.com/admiral-project/admiral/admiral-fleet/internal/podman"
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

type fakePodmanRunner struct {
	calls [][]string
}

func (r *fakePodmanRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	switch strings.Join(call, " ") {
	case "podman pod exists admiral-demo001":
		return []byte{}, nil
	case "podman pod ps --format json":
		return []byte(`[{"Name":"admiral-demo001","Status":"Running"}]`), nil
	case "podman ps --format json":
		return []byte(`[{"Names":["admiral-demo001-app"],"Status":"Up"}]`), nil
	case "podman container inspect admiral-demo001-app --format json":
		return []byte(`[{"Name":"admiral-demo001-app","State":{"Status":"running"}}]`), nil
	case "podman volume inspect admiral-demo001-db --format json":
		return []byte(`[{"Name":"admiral-demo001-db","Mountpoint":"/var/lib/containers/storage/volumes/admiral-demo001-db/_data"}]`), nil
	default:
		return []byte(`[]`), nil
	}
}

type fakeFS struct {
	osutil.RealFileSystem
}

func (f fakeFS) MkdirAll(path string, perm os.FileMode) error { return nil }
func (f fakeFS) Chmod(name string, mode os.FileMode) error    { return nil }
func (f fakeFS) Chown(name string, uid, gid int) error       { return nil }
func (f fakeFS) WriteFile(filename string, data []byte, perm os.FileMode) error { return nil }
func (f fakeFS) RemoveAll(path string) error                 { return nil }
func (f fakeFS) ReadFile(name string) ([]byte, error) {
	if strings.HasSuffix(name, "ports.json") {
		return []byte("{}"), nil
	}
	return nil, os.ErrNotExist
}
func (f fakeFS) Walk(root string, walkFn filepath.WalkFunc) error {
	return nil
}

type fakeUserLookup struct{}

func (f fakeUserLookup) Lookup(username string) (*user.User, error) {
	return &user.User{Uid: "1000", Gid: "1000"}, nil
}

func TestSystemdPodmanExecutorStartsAppUnit(t *testing.T) {
	runner := &fakeSystemdRunner{}
	manager := systemd.NewManager(runner)
	manager.Timeout = time.Second
	exec := NewSystemdPodmanWithFS(manager, nil, "/tmp/quadlet", "/tmp/data", "nobody", fakeFS{}, fakeUserLookup{})

	res := exec.Execute(context.Background(), admiral.FleetTask{
		TaskID:      "task_1",
		OperationID: "op_1",
		NodeID:      "node_1",
		Action:      admiral.ActionStartApp,
		InstanceID:  "demo001",
		Services: []admiral.ServiceInfo{
			{Name: "app", Image: "example.com/app:1"},
		},
	}, "node_1")

	if !res.Success {
		t.Fatalf("expected success, got %q", res.Error)
	}
	// start calls daemon-reload then start per service unit
	if len(runner.calls) != 2 {
		t.Fatalf("expected two systemd calls (daemon-reload + start), got %d", len(runner.calls))
	}
	got := runner.calls[1]
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

func TestSystemdPodmanExecutorStartsPodUnitWithLimits(t *testing.T) {
	runner := &fakeSystemdRunner{}
	manager := systemd.NewManager(runner)
	manager.Timeout = time.Second
	exec := NewSystemdPodmanWithFS(manager, nil, "/tmp/quadlet", "/tmp/data", "nobody", fakeFS{}, fakeUserLookup{})

	res := exec.Execute(context.Background(), admiral.FleetTask{
		TaskID:      "task_2",
		OperationID: "op_2",
		NodeID:      "node_1",
		Action:      admiral.ActionStartApp,
		InstanceID:  "demo002",
		Tier: admiral.TierInfo{
			CPU:    1,
			Memory: "512MiB",
		},
		Services: []admiral.ServiceInfo{
			{Name: "app", Image: "example.com/app:1"},
			{Name: "db", Image: "docker.io/library/postgres:16", Volume: "db_data"},
		},
	}, "node_1")

	if !res.Success {
		t.Fatalf("expected success, got %q", res.Error)
	}
	// With tier limits, the pod unit should be started instead of individual container units
	if len(runner.calls) < 2 {
		t.Fatalf("expected at least 2 systemd calls, got %d", len(runner.calls))
	}
	got := runner.calls[1]
	want := []string{"systemctl", "start", "admiral-demo002-pod.service"}
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
	exec := NewSystemdPodmanWithFS(manager, nil, "/tmp/quadlet", "/tmp/data", "nobody", fakeFS{}, fakeUserLookup{})

	res := exec.Execute(context.Background(), admiral.FleetTask{
		TaskID:      "task_1",
		OperationID: "op_1",
		NodeID:      "node_1",
		Action:      admiral.ActionStopApp,
		InstanceID:  "demo001",
		Services: []admiral.ServiceInfo{
			{Name: "app", Image: "example.com/app:1"},
		},
	}, "node_1")

	if res.Success {
		t.Fatal("expected stop to fail")
	}
	if res.Error == "" {
		t.Fatal("expected actionable error")
	}
}

func TestSystemdPodmanExecutorRejectsInvalidProvision(t *testing.T) {
	exec := NewSystemdPodmanWithFS(nil, nil, "/tmp/quadlet", "/tmp/data", "nobody", fakeFS{}, fakeUserLookup{})
	res := exec.Execute(context.Background(), admiral.FleetTask{
		NodeID:     "node_1",
		Action:     admiral.ActionProvisionApp,
		InstanceID: "demo001",
	}, "node_1")

	if res.Success {
		t.Fatal("expected invalid provision to fail clearly")
	}
}

func TestSystemdPodmanExecutorInspectAppSnapshot(t *testing.T) {
	podmanRunner := &fakePodmanRunner{}
	systemdRunner := &fakeSystemdRunner{}
	exec := NewSystemdPodmanWithFS(systemd.NewManager(systemdRunner), podman.NewInspector(podmanRunner), "/tmp/quadlet", "/tmp/data", "nobody", fakeFS{}, fakeUserLookup{})

	res := exec.Execute(context.Background(), admiral.FleetTask{
		TaskID:      "task_1",
		OperationID: "op_1",
		NodeID:      "node_1",
		Action:      admiral.ActionInspectApp,
		InstanceID:  "demo001",
		Services: []admiral.ServiceInfo{
			{Name: "app", Image: "example.com/app:1"},
			{Name: "db", Image: "docker.io/library/postgres:16", Volume: "db_data"},
		},
	}, "node_1")

	if !res.Success {
		t.Fatalf("expected inspect to succeed, got %q", res.Error)
	}
	if !strings.Contains(res.Metadata, `"instance_id":"demo001"`) {
		t.Fatalf("expected instance id in metadata, got %s", res.Metadata)
	}
	if !strings.Contains(res.Metadata, `"containers"`) {
		t.Fatalf("expected containers in metadata, got %s", res.Metadata)
	}
	if len(podmanRunner.calls) == 0 {
		t.Fatal("expected podman calls")
	}
}
