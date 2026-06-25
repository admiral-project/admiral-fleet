// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
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

func (r *fakeSystemdRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	if r.err != nil {
		return nil, r.err
	}
	return []byte("ok"), nil
}

type fakeOverride struct {
	output []byte
	err    error
	// match is a substring to look for instead of exact match
	contains string
}

type fakePodmanRunner struct {
	calls     [][]string
	overrides map[string]fakeOverride
}

func (r *fakePodmanRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	// The runner is called via systemd-run --machine <user>@ --user --wait
	// --collect --pipe -- podman <args> when RootlessUser is set. Unwrap
	// for simpler matching.
	if name == "systemd-run" {
		for i, a := range args {
			if a == "--" && i+1 < len(args) && args[i+1] == "podman" {
				call = args[i+1:]
				break
			}
		}
	}
	joined := strings.Join(call, " ")
	for _, o := range r.overrides {
		if o.contains != "" && strings.Contains(joined, o.contains) {
			return o.output, o.err
		}
	}
	if joined == "podman pod exists admiral-demo001" {
		return []byte{}, nil
	}
	if joined == "podman pod ps --format json" {
		return []byte(`[{"Name":"admiral-demo001","Status":"Running"}]`), nil
	}
	if joined == "podman ps --format json" {
		return []byte(`[{"Names":["admiral-demo001-app"],"Status":"Up"}]`), nil
	}
	if joined == "podman container inspect admiral-demo001-app --format json" {
		return []byte(`[{"Name":"admiral-demo001-app","State":{"Status":"running"}}]`), nil
	}
	if joined == "podman pod inspect admiral-demo001 --format {{.State}}" {
		return []byte("Running"), nil
	}
	if joined == "podman volume inspect admiral-demo001-db --format json" {
		return []byte(`[{"Name":"admiral-demo001-db","Mountpoint":"/var/lib/containers/storage/volumes/admiral-demo001-db/_data"}]`), nil
	}
	if strings.Contains(joined, "podman exec") && strings.Contains(joined, "--env-file") && strings.Contains(joined, "mariadb-dump") {
		return []byte("-- dump --"), nil
	}
	if strings.Contains(joined, "pg_isready -U user -d gitea") {
		return []byte("admiral-demo001-db:5432 - accepting connections"), nil
	}
	if strings.Contains(joined, "pg_restore --clean --if-exists --no-owner --no-privileges -Fc -U user -d gitea /tmp/admiral-restore.dump") {
		return []byte("restore ok"), nil
	}
	if strings.Contains(joined, "pg_restore") && strings.Contains(joined, "admiral-demo001-db") {
		return []byte("admiral-demo001-db:5432 - accepting connections"), nil
	}
	return []byte(`[]`), nil
}

type fakeFS struct {
	osutil.RealFileSystem
}

func (f fakeFS) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}
func (f fakeFS) Chmod(_ string, _ os.FileMode) error { return nil }
func (f fakeFS) Chown(_ string, _, _ int) error      { return nil }
func (f fakeFS) WriteFile(filename string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
		return err
	}
	return os.WriteFile(filename, data, perm)
}
func (f fakeFS) RemoveAll(path string) error { return os.RemoveAll(path) }
func (f fakeFS) Remove(name string) error    { return os.Remove(name) }
func (f fakeFS) Stat(name string) (os.FileInfo, error) {
	return os.Stat(name)
}
func (f fakeFS) Create(name string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		return nil, err
	}
	return os.Create(name)
}
func (f fakeFS) Open(name string) (*os.File, error) { return os.Open(name) }
func (f fakeFS) ReadFile(name string) ([]byte, error) {
	if strings.HasSuffix(name, "ports.json") {
		return []byte("{}"), nil
	}
	return os.ReadFile(name)
}
func (f fakeFS) Walk(root string, walkFn filepath.WalkFunc) error {
	return filepath.Walk(root, walkFn)
}

type fakeUserLookup struct{}

func (f fakeUserLookup) Lookup(_ string) (*user.User, error) {
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
	// Pods are mandatory; start always uses the pod unit
	if len(runner.calls) != 2 {
		t.Fatalf("expected two systemd calls (daemon-reload + start), got %d", len(runner.calls))
	}
	got := runner.calls[1]
	want := []string{"systemctl", "start", "admiral-demo001-pod.service"}
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

func TestSystemdPodmanExecutorBackupUsesPodInfraContainer(t *testing.T) {
	podmanRunner := &fakePodmanRunner{}
	systemdRunner := &fakeSystemdRunner{}
	exec := NewSystemdPodmanWithFS(systemd.NewManager(systemdRunner), podman.NewInspector(podmanRunner), "/tmp/quadlet", "/tmp/data", "nobody", fakeFS{}, fakeUserLookup{})

	res := exec.Execute(context.Background(), admiral.FleetTask{
		TaskID:      "task_3",
		OperationID: "op_3",
		NodeID:      "node_1",
		Action:      admiral.ActionBackupDatabase,
		InstanceID:  "demo001",
		Tier: admiral.TierInfo{
			CPU:    1,
			Memory: "512MiB",
		},
		Services: []admiral.ServiceInfo{
			{Name: "web", Image: "docker.io/library/wordpress:6", Port: 80},
			{
				Name:   "db",
				Image:  "docker.io/library/mariadb:10",
				Volume: "db_data",
				Env: map[string]string{
					"MARIADB_DATABASE": "wordpress",
				},
				Secrets: map[string]string{
					"MARIADB_USER":     "user",
					"MARIADB_PASSWORD": "secret",
				},
			},
		},
		Backup: &admiral.BackupInfo{
			Service:      "db",
			DatabaseType: "mysql",
			DatabaseEnv:  "MARIADB_DATABASE",
			UsernameEnv:  "MARIADB_USER",
			PasswordEnv:  "MARIADB_PASSWORD",
		},
	}, "node_1")

	if !res.Success {
		t.Fatalf("expected backup to succeed, got %q", res.Error)
	}
	found := false
	for _, call := range podmanRunner.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "podman exec") && strings.Contains(joined, "--env-file") && strings.Contains(joined, "mariadb-dump") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected backup to exec against db service container, calls: %#v", podmanRunner.calls)
	}
}

func TestSystemdPodmanExecutorRestorePostgresUsesCleanRestore(t *testing.T) {
	podmanRunner := &fakePodmanRunner{}
	systemdRunner := &fakeSystemdRunner{}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "backups", "demo001"), 0755); err != nil {
		t.Fatalf("create backup dir: %v", err)
	}
	dumpPath := filepath.Join(dir, "backups", "demo001", "restore.dump")
	f, err := os.Create(dumpPath)
	if err != nil {
		t.Fatalf("create dump: %v", err)
	}
	gw := gzip.NewWriter(f)
	if _, err := gw.Write([]byte("dummy dump")); err != nil {
		t.Fatalf("write gzipped dump: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close dump: %v", err)
	}
	exec := NewSystemdPodmanWithFS(systemd.NewManager(systemdRunner), podman.NewInspector(podmanRunner), "/tmp/quadlet", dir, "nobody", fakeFS{}, fakeUserLookup{})

	res := exec.Execute(context.Background(), admiral.FleetTask{
		TaskID:      "task_5",
		OperationID: "op_5",
		NodeID:      "node_1",
		Action:      admiral.ActionRestoreBackup,
		InstanceID:  "demo001",
		Services: []admiral.ServiceInfo{
			{Name: "web", Image: "example.com/app:1"},
			{
				Name:   "db",
				Image:  "docker.io/library/postgres:16",
				Volume: "db_data",
				Env: map[string]string{
					"POSTGRES_DB": "gitea",
				},
				Secrets: map[string]string{
					"POSTGRES_USER":     "user",
					"POSTGRES_PASSWORD": "secret",
				},
			},
		},
		Backup: &admiral.BackupInfo{
			Service:      "db",
			DatabaseType: "postgresql",
			DatabaseEnv:  "POSTGRES_DB",
			UsernameEnv:  "POSTGRES_USER",
			PasswordEnv:  "POSTGRES_PASSWORD",
		},
		Restore: &admiral.RestoreInfo{
			BackupID:       "bk_demo",
			BackupType:     "database",
			DatabaseType:   "postgresql",
			Service:        "db",
			StorageKey:     dumpPath,
			StorageBackend: "local",
			ChecksumSHA256: "",
		},
	}, "node_1")

	if !res.Success {
		t.Fatalf("expected restore to succeed, got %q", res.Error)
	}

	found := false
	for _, call := range podmanRunner.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "podman exec") && strings.Contains(joined, "--env-file") && strings.Contains(joined, "pg_restore") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected clean pg_restore call, calls: %#v", podmanRunner.calls)
	}
}

func TestSystemdPodmanExecutorRestoreUnpausesPausedPod(t *testing.T) {
	podmanRunner := &fakePodmanRunner{
		overrides: map[string]fakeOverride{
			"pod_inspect_paused": {
				output:   []byte("Paused"),
				err:      nil,
				contains: "podman pod inspect admiral-demo001 --format {{.State}}",
			},
		},
	}
	systemdRunner := &fakeSystemdRunner{}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "backups", "demo001"), 0755); err != nil {
		t.Fatalf("create backup dir: %v", err)
	}
	dumpPath := filepath.Join(dir, "backups", "demo001", "restore.dump")
	f, err := os.Create(dumpPath)
	if err != nil {
		t.Fatalf("create dump: %v", err)
	}
	gw := gzip.NewWriter(f)
	if _, err := gw.Write([]byte("dummy dump")); err != nil {
		t.Fatalf("write gzipped dump: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close dump: %v", err)
	}
	exec := NewSystemdPodmanWithFS(systemd.NewManager(systemdRunner), podman.NewInspector(podmanRunner), "/tmp/quadlet", dir, "nobody", fakeFS{}, fakeUserLookup{})

	res := exec.Execute(context.Background(), admiral.FleetTask{
		TaskID:      "task_6",
		OperationID: "op_6",
		NodeID:      "node_1",
		Action:      admiral.ActionRestoreBackup,
		InstanceID:  "demo001",
		Services: []admiral.ServiceInfo{
			{Name: "web", Image: "example.com/app:1"},
			{
				Name:   "db",
				Image:  "docker.io/library/postgres:16",
				Volume: "db_data",
				Env: map[string]string{
					"POSTGRES_DB": "gitea",
				},
				Secrets: map[string]string{
					"POSTGRES_USER":     "user",
					"POSTGRES_PASSWORD": "secret",
				},
			},
		},
		Backup: &admiral.BackupInfo{
			Service:      "db",
			DatabaseType: "postgresql",
			DatabaseEnv:  "POSTGRES_DB",
			UsernameEnv:  "POSTGRES_USER",
			PasswordEnv:  "POSTGRES_PASSWORD",
		},
		Restore: &admiral.RestoreInfo{
			BackupID:       "bk_demo",
			BackupType:     "database",
			DatabaseType:   "postgresql",
			Service:        "db",
			StorageKey:     dumpPath,
			StorageBackend: "local",
			ChecksumSHA256: "",
		},
	}, "node_1")

	if !res.Success {
		t.Fatalf("expected restore from paused pod to succeed, got %q", res.Error)
	}

	foundUnpause := false
	foundRestore := false
	for _, call := range podmanRunner.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "podman pod unpause admiral-demo001") {
			foundUnpause = true
		}
		if strings.Contains(joined, "pg_restore") {
			foundRestore = true
		}
	}
	if !foundUnpause {
		t.Fatalf("expected podman pod unpause before restore on paused pod, calls: %#v", podmanRunner.calls)
	}
	if !foundRestore {
		t.Fatalf("expected pg_restore after unpause, calls: %#v", podmanRunner.calls)
	}
}

func TestSystemdPodmanExecutorDeprovisionRemovesPodAndVolumeUnits(t *testing.T) {
	podmanRunner := &fakePodmanRunner{}
	systemdRunner := &fakeSystemdRunner{}
	dir := t.TempDir()
	exec := NewSystemdPodmanWithFS(systemd.NewManager(systemdRunner), podman.NewInspector(podmanRunner), dir, dir, "nobody", fakeFS{}, fakeUserLookup{})

	res := exec.Execute(context.Background(), admiral.FleetTask{
		TaskID:      "task_4",
		OperationID: "op_4",
		NodeID:      "node_1",
		Action:      admiral.ActionDeprovisionApp,
		InstanceID:  "demo001",
		Tier: admiral.TierInfo{
			CPU:    1,
			Memory: "512MiB",
		},
		Services: []admiral.ServiceInfo{
			{Name: "web", Image: "docker.io/library/wordpress:6", Port: 80},
			{Name: "db", Image: "docker.io/library/mariadb:10", Volume: "db_data"},
		},
	}, "node_1")

	if !res.Success {
		t.Fatalf("expected deprovision to succeed, got %q", res.Error)
	}

	foundDisableVolume := false
	for _, call := range systemdRunner.calls {
		if strings.Contains(strings.Join(call, " "), "disable admiral-demo001-db-volume.service") {
			foundDisableVolume = true
			break
		}
	}
	if !foundDisableVolume {
		t.Fatalf("expected volume unit disable, calls: %#v", systemdRunner.calls)
	}

	foundRemovePod := false
	foundRemoveVolume := false
	for _, call := range podmanRunner.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "podman pod rm --force admiral-demo001") {
			foundRemovePod = true
		}
		if strings.Contains(joined, "podman volume rm --force admiral-demo001-db") {
			foundRemoveVolume = true
		}
	}
	if !foundRemovePod {
		t.Fatalf("expected pod removal, calls: %#v", podmanRunner.calls)
	}
	if !foundRemoveVolume {
		t.Fatalf("expected volume removal, calls: %#v", podmanRunner.calls)
	}
}

func TestSystemdPodmanExecutorVerifyRestoreChecksumAcceptsCompressedOrPayload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.tar.gz")
	payload := []byte("restore payload")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	gw := gzip.NewWriter(f)
	if _, err := gw.Write(payload); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}

	exec := NewSystemdPodmanWithFS(nil, nil, "/tmp/quadlet", dir, "nobody", fakeFS{}, fakeUserLookup{})
	compressed, err := exec.checksumArtifact(path)
	if err != nil {
		t.Fatalf("checksum artifact: %v", err)
	}
	matched, actual, err := exec.verifyRestoreChecksum(path, compressed)
	if err != nil {
		t.Fatalf("verify compressed checksum: %v", err)
	}
	if !matched || actual != compressed {
		t.Fatalf("expected compressed checksum match, got matched=%v actual=%s", matched, actual)
	}

	payloadSum := sha256.Sum256(payload)
	expectedPayload := fmt.Sprintf("sha256:%x", payloadSum[:])
	matched, actual, err = exec.verifyRestoreChecksum(path, expectedPayload)
	if err != nil {
		t.Fatalf("verify payload checksum: %v", err)
	}
	if !matched || actual != expectedPayload {
		t.Fatalf("expected payload checksum match, got matched=%v actual=%s", matched, actual)
	}

	matched, actual, err = exec.verifyRestoreChecksum(path, strings.TrimPrefix(expectedPayload, "sha256:"))
	if err != nil {
		t.Fatalf("verify bare payload checksum: %v", err)
	}
	if !matched || actual != expectedPayload {
		t.Fatalf("expected bare payload checksum match, got matched=%v actual=%s", matched, actual)
	}
}

func TestSystemdPodmanExecutorResolveLocalBackupPathConfinesToRoot(t *testing.T) {
	dir := t.TempDir()
	exec := NewSystemdPodmanWithFS(nil, nil, "/tmp/quadlet", dir, "nobody", fakeFS{}, fakeUserLookup{})

	path, err := exec.resolveLocalBackupPath("instance01/db.dump")
	if err != nil {
		t.Fatalf("resolve relative path: %v", err)
	}
	want := filepath.Join(dir, "backups", "instance01", "db.dump")
	if path != want {
		t.Fatalf("unexpected resolved path: got %q want %q", path, want)
	}

	absPath := filepath.Join(dir, "backups", "instance01", "db.dump")
	path, err = exec.resolveLocalBackupPath(absPath)
	if err != nil {
		t.Fatalf("resolve absolute path: %v", err)
	}
	if path != absPath {
		t.Fatalf("unexpected absolute path: got %q want %q", path, absPath)
	}

	if _, err := exec.resolveLocalBackupPath("../../etc/passwd"); err == nil {
		t.Fatal("expected traversal path to be rejected")
	}
	if _, err := exec.resolveLocalBackupPath("/tmp/outside.dump"); err == nil {
		t.Fatal("expected absolute path outside backup root to be rejected")
	}
}

func TestSystemdPodmanExecutorFetchRestoreArtifactRejectsEscapingPath(t *testing.T) {
	dir := t.TempDir()
	exec := NewSystemdPodmanWithFS(nil, nil, "/tmp/quadlet", dir, "nobody", fakeFS{}, fakeUserLookup{})

	_, err := exec.fetchRestoreArtifact(context.Background(), admiral.FleetTask{
		Restore: &admiral.RestoreInfo{
			StorageBackend: "local",
			StorageKey:     "../../etc/shadow",
		},
	})
	if err == nil {
		t.Fatal("expected escaping restore path to be rejected")
	}
}

func TestSystemdPodmanExecutorPauseUsesSystemdStop(t *testing.T) {
	podmanRunner := &fakePodmanRunner{}
	systemdRunner := &fakeSystemdRunner{}
	exec := NewSystemdPodmanWithFS(systemd.NewManager(systemdRunner), podman.NewInspector(podmanRunner), "/tmp/quadlet", "/tmp/data", "nobody", fakeFS{}, fakeUserLookup{})

	res := exec.Execute(context.Background(), admiral.FleetTask{
		TaskID:      "task_pause_1",
		OperationID: "op_pause_1",
		NodeID:      "node_1",
		Action:      admiral.ActionPauseApp,
		InstanceID:  "demo001",
		Services: []admiral.ServiceInfo{
			{Name: "app", Image: "example.com/app:1"},
		},
	}, "node_1")

	if !res.Success {
		t.Fatalf("expected pause to succeed, got %q", res.Error)
	}
	if res.Logs != "stopped instance demo001" {
		t.Fatalf("expected pause log, got %q", res.Logs)
	}
	foundStop := false
	for _, call := range systemdRunner.calls {
		if strings.Contains(strings.Join(call, " "), "stop admiral-demo001-pod.service") {
			foundStop = true
			break
		}
	}
	if !foundStop {
		t.Fatalf("expected systemctl stop call, got %#v", systemdRunner.calls)
	}
	foundPodmanPause := false
	for _, call := range podmanRunner.calls {
		if strings.Contains(strings.Join(call, " "), "podman pod pause") {
			foundPodmanPause = true
			break
		}
	}
	if foundPodmanPause {
		t.Fatal("pause must NOT call podman pod pause")
	}
}

func TestSystemdPodmanExecutorResumeUsesSystemdStart(t *testing.T) {
	podmanRunner := &fakePodmanRunner{}
	systemdRunner := &fakeSystemdRunner{}
	exec := NewSystemdPodmanWithFS(systemd.NewManager(systemdRunner), podman.NewInspector(podmanRunner), "/tmp/quadlet", "/tmp/data", "nobody", fakeFS{}, fakeUserLookup{})

	res := exec.Execute(context.Background(), admiral.FleetTask{
		TaskID:      "task_resume_1",
		OperationID: "op_resume_1",
		NodeID:      "node_1",
		Action:      admiral.ActionResumeApp,
		InstanceID:  "demo001",
		Services: []admiral.ServiceInfo{
			{Name: "app", Image: "example.com/app:1"},
		},
	}, "node_1")

	if !res.Success {
		t.Fatalf("expected resume to succeed, got %q", res.Error)
	}
	if res.Logs != "started instance demo001" {
		t.Fatalf("expected resume log, got %q", res.Logs)
	}
	foundStart := false
	for _, call := range systemdRunner.calls {
		if strings.Contains(strings.Join(call, " "), "start admiral-demo001-pod.service") {
			foundStart = true
			break
		}
	}
	if !foundStart {
		t.Fatalf("expected systemctl start call, got %#v", systemdRunner.calls)
	}
	foundPodmanUnpause := false
	for _, call := range podmanRunner.calls {
		if strings.Contains(strings.Join(call, " "), "podman pod unpause") {
			foundPodmanUnpause = true
			break
		}
	}
	if foundPodmanUnpause {
		t.Fatal("resume must NOT call podman pod unpause")
	}
}

func TestSystemdPodmanExecutorPauseIdempotentOnMissingInstance(t *testing.T) {
	podmanRunner := &fakePodmanRunner{}
	systemdRunner := &fakeSystemdRunner{}
	exec := NewSystemdPodmanWithFS(systemd.NewManager(systemdRunner), podman.NewInspector(podmanRunner), "/tmp/quadlet", "/tmp/data", "nobody", fakeFS{}, fakeUserLookup{})

	res := exec.Execute(context.Background(), admiral.FleetTask{
		TaskID:      "task_pause_missing",
		OperationID: "op_pause_missing",
		NodeID:      "node_1",
		Action:      admiral.ActionPauseApp,
		InstanceID:  "nonexistent",
		Services: []admiral.ServiceInfo{
			{Name: "app", Image: "example.com/app:1"},
		},
	}, "node_1")

	if !res.Success {
		t.Fatalf("expected pause on missing instance to succeed, got %q", res.Error)
	}
	// Must use systemd stop (not podman pod pause)
	foundSystemdStop := false
	for _, call := range systemdRunner.calls {
		if strings.Contains(strings.Join(call, " "), "stop admiral-nonexistent-pod.service") {
			foundSystemdStop = true
			break
		}
	}
	if !foundSystemdStop {
		t.Fatalf("expected systemctl stop call, got %#v", systemdRunner.calls)
	}
	foundPodmanPause := false
	for _, call := range podmanRunner.calls {
		if strings.Contains(strings.Join(call, " "), "podman pod pause") {
			foundPodmanPause = true
			break
		}
	}
	if foundPodmanPause {
		t.Fatal("pause must NOT call podman pod pause")
	}
}

func TestSystemdPodmanExecutorStopUsesSystemd(t *testing.T) {
	podmanRunner := &fakePodmanRunner{}
	systemdRunner := &fakeSystemdRunner{}
	exec := NewSystemdPodmanWithFS(systemd.NewManager(systemdRunner), podman.NewInspector(podmanRunner), "/tmp/quadlet", "/tmp/data", "nobody", fakeFS{}, fakeUserLookup{})

	res := exec.Execute(context.Background(), admiral.FleetTask{
		TaskID:      "task_stop_1",
		OperationID: "op_stop_1",
		NodeID:      "node_1",
		Action:      admiral.ActionStopApp,
		InstanceID:  "demo001",
		Services: []admiral.ServiceInfo{
			{Name: "app", Image: "example.com/app:1"},
		},
	}, "node_1")

	if !res.Success {
		t.Fatalf("expected stop to succeed, got %q", res.Error)
	}
	if res.Logs != "stopped instance demo001" {
		t.Fatalf("expected stop log, got %q", res.Logs)
	}
	foundSystemctlStop := false
	for _, call := range systemdRunner.calls {
		if strings.Contains(strings.Join(call, " "), "stop admiral-demo001-pod.service") {
			foundSystemctlStop = true
			break
		}
	}
	if !foundSystemctlStop {
		t.Fatalf("expected systemctl stop call, got %#v", systemdRunner.calls)
	}
}

func TestParsePublishedPortAcceptsPodmanHostPortOutput(t *testing.T) {
	tests := map[string]int{
		"40005":         40005,
		"0.0.0.0:40005": 40005,
		"[::]:40005":    40005,
	}

	for raw, want := range tests {
		got, err := parsePublishedPort(raw)
		if err != nil {
			t.Fatalf("parsePublishedPort(%q) returned error: %v", raw, err)
		}
		if got != want {
			t.Fatalf("parsePublishedPort(%q) = %d, want %d", raw, got, want)
		}
	}
}
