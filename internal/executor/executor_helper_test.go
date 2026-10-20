// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

type recordingFS struct {
	mkdirs map[string]os.FileMode
	chowns map[string][2]int
	chmods map[string]os.FileMode
	// lchowns records paths handed over without following symlinks.
	lchowns map[string][2]int
	// tree holds child paths returned by Walk for recursive chown checks.
	tree []string
	// statModes represents package-managed directory modes returned by Stat.
	statModes map[string]os.FileMode
	// symlinks holds tree paths reported to Walk as symlinks.
	symlinks map[string]bool
}

func newRecordingFS() *recordingFS {
	return &recordingFS{
		mkdirs:    map[string]os.FileMode{},
		chowns:    map[string][2]int{},
		chmods:    map[string]os.FileMode{},
		lchowns:   map[string][2]int{},
		symlinks:  map[string]bool{},
		statModes: map[string]os.FileMode{},
	}
}

func (f *recordingFS) MkdirAll(path string, perm os.FileMode) error {
	f.mkdirs[path] = perm
	return nil
}
func (f *recordingFS) Chmod(name string, mode os.FileMode) error { f.chmods[name] = mode; return nil }
func (f *recordingFS) Chown(name string, uid, gid int) error {
	f.chowns[name] = [2]int{uid, gid}
	return nil
}
func (f *recordingFS) Lchown(name string, uid, gid int) error {
	f.lchowns[name] = [2]int{uid, gid}
	return nil
}
func (f *recordingFS) RemoveAll(string) error { return nil }
func (f *recordingFS) Remove(string) error    { return nil }
func (f *recordingFS) Stat(path string) (os.FileInfo, error) {
	mode, ok := f.statModes[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return fakeFileInfo{name: path, mode: mode}, nil
}
func (f *recordingFS) Create(string) (*os.File, error) { return nil, os.ErrInvalid }
func (f *recordingFS) OpenFile(string, int, os.FileMode) (*os.File, error) {
	return nil, os.ErrInvalid
}
func (f *recordingFS) Open(string) (*os.File, error) { return nil, os.ErrInvalid }
func (f *recordingFS) ReadFile(string) ([]byte, error) {
	return nil, os.ErrInvalid
}
func (f *recordingFS) WriteFile(string, []byte, os.FileMode) error { return os.ErrInvalid }
func (f *recordingFS) Walk(root string, walkFn filepath.WalkFunc) error {
	for _, p := range f.tree {
		if p != root && !strings.HasPrefix(p, root+string(filepath.Separator)) {
			continue
		}
		mode := os.FileMode(0)
		if f.symlinks[p] {
			mode = os.ModeSymlink
		} else if statMode, ok := f.statModes[p]; ok {
			mode = statMode
		}
		if err := walkFn(p, fakeFileInfo{name: p, mode: mode}, nil); err != nil {
			return err
		}
	}
	return nil
}

// fakeFileInfo is the minimal FileInfo used by recordingFS to report tree
// entries, including symlinks, back to Walk callbacks.
type fakeFileInfo struct {
	name string
	mode os.FileMode
}

func (f fakeFileInfo) Name() string      { return f.name }
func (f fakeFileInfo) Size() int64       { return 0 }
func (f fakeFileInfo) Mode() os.FileMode { return f.mode }
func (f fakeFileInfo) ModTime() time.Time {
	return time.Time{}
}
func (f fakeFileInfo) IsDir() bool      { return f.mode.IsDir() }
func (f fakeFileInfo) Sys() interface{} { return nil }

func TestPrepareStorageRootsChownsToRootlessUser(t *testing.T) {
	fs := newRecordingFS()
	for path, mode := range map[string]os.FileMode{
		"/var/lib/admiral/backups": 0750,
		"/var/lib/admiral/restore": 0750,
		"/var/lib/admiral/tmp":     0700,
	} {
		fs.statModes[path] = os.ModeDir | mode
		fs.tree = append(fs.tree, path)
	}
	fs.tree = []string{
		"/var/lib/admiral/backups",
		"/var/lib/admiral/backups/inst_old/old-mariadb-db.tar.gz",
		"/var/lib/admiral/restore",
		"/var/lib/admiral/restore/artifact.bin",
		"/var/lib/admiral/tmp",
	}
	exec := &SystemdPodmanExecutor{
		FS:           fs,
		UserLookup:   fakeUserLookup{},
		DataDir:      "/var/lib/admiral",
		RootlessUser: "admiral-apps",
	}
	if err := exec.prepareStorageRoots(); err != nil {
		t.Fatalf("prepareStorageRoots: %v", err)
	}
	for _, root := range []string{
		"/var/lib/admiral/backups",
		"/var/lib/admiral/restore",
		"/var/lib/admiral/tmp",
	} {
		if _, ok := fs.lchowns[root]; ok {
			t.Fatalf("package-managed root %q must not be chowned by Fleet", root)
		}
		if _, ok := fs.chmods[root]; ok {
			t.Fatalf("package-managed root %q must not be chmodded by Fleet", root)
		}
		if _, ok := fs.mkdirs[root]; ok {
			t.Fatalf("package-managed root %q must not be created by Fleet", root)
		}
		if _, followed := fs.chowns[root]; followed {
			t.Fatalf("storage root %q must not be chowned through a dereferencing path", root)
		}
	}
	for _, artifact := range []string{
		"/var/lib/admiral/backups/inst_old/old-mariadb-db.tar.gz",
		"/var/lib/admiral/restore/artifact.bin",
	} {
		if fs.lchowns[artifact] != [2]int{1000, 1000} {
			t.Fatalf("expected pre-existing artifact %q to be handed to rootless via lchown, got %v", artifact, fs.lchowns[artifact])
		}
	}
}

func TestPrepareStorageRootsSkipsSymlinks(t *testing.T) {
	fs := newRecordingFS()
	for path, mode := range map[string]os.FileMode{
		"/var/lib/admiral/backups": 0750,
		"/var/lib/admiral/restore": 0750,
		"/var/lib/admiral/tmp":     0700,
	} {
		fs.statModes[path] = os.ModeDir | mode
	}
	fs.tree = []string{
		"/var/lib/admiral/backups",
		"/var/lib/admiral/backups/inst_old/legit-db.tar.gz",
		"/var/lib/admiral/backups/inst_old/planted-link",
		"/var/lib/admiral/restore",
		"/var/lib/admiral/tmp",
	}
	fs.symlinks["/var/lib/admiral/backups/inst_old/planted-link"] = true
	exec := &SystemdPodmanExecutor{
		FS:           fs,
		UserLookup:   fakeUserLookup{},
		DataDir:      "/var/lib/admiral",
		RootlessUser: "admiral-apps",
	}
	if err := exec.prepareStorageRoots(); err != nil {
		t.Fatalf("prepareStorageRoots: %v", err)
	}
	if fs.lchowns["/var/lib/admiral/backups/inst_old/legit-db.tar.gz"] != [2]int{1000, 1000} {
		t.Fatalf("expected regular artifact to be lchowned, got %v", fs.lchowns["/var/lib/admiral/backups/inst_old/legit-db.tar.gz"])
	}
	if _, chowned := fs.lchowns["/var/lib/admiral/backups/inst_old/planted-link"]; chowned {
		t.Fatalf("symlink must be skipped, got lchown %v", fs.lchowns["/var/lib/admiral/backups/inst_old/planted-link"])
	}
	if _, chowned := fs.chowns["/var/lib/admiral/backups/inst_old/planted-link"]; chowned {
		t.Fatal("symlink must never be dereferenced by Chown")
	}
}

func TestPrepareStorageRootsRejectsMissingPackageManagedRoot(t *testing.T) {
	fs := newRecordingFS()
	exec := &SystemdPodmanExecutor{
		FS:           fs,
		UserLookup:   fakeUserLookup{},
		DataDir:      "/var/lib/admiral",
		RootlessUser: "admiral-apps",
	}
	if err := exec.prepareStorageRoots(); err == nil || !strings.Contains(err.Error(), "admiral-common") {
		t.Fatalf("expected actionable missing-package-directory error, got %v", err)
	}
	if len(fs.mkdirs) != 0 || len(fs.chmods) != 0 || len(fs.lchowns) != 0 {
		t.Fatalf("Fleet must not create or change package-managed roots: mkdirs=%v chmods=%v lchowns=%v", fs.mkdirs, fs.chmods, fs.lchowns)
	}
}

func TestChownInstanceDataDoesNotChangePackageManagedRoots(t *testing.T) {
	fs := newRecordingFS()
	dataDir := "/var/lib/admiral"
	instancesDir := filepath.Join(dataDir, "instances")
	instanceDir := filepath.Join(instancesDir, "inst_test")
	envDir := filepath.Join(instanceDir, "env")
	fs.statModes[dataDir] = os.ModeDir | 0751
	fs.statModes[instancesDir] = os.ModeDir | 0751
	fs.statModes[instanceDir] = os.ModeDir | 0751
	fs.statModes[envDir] = os.ModeDir | 0751
	fs.tree = []string{instanceDir, envDir}
	exec := &SystemdPodmanExecutor{
		FS:           fs,
		UserLookup:   fakeUserLookup{},
		DataDir:      dataDir,
		RootlessUser: "admiral-apps",
	}
	if err := exec.chownInstanceData("inst_test"); err != nil {
		t.Fatalf("chownInstanceData: %v", err)
	}
	for _, root := range []string{dataDir, instancesDir} {
		if _, ok := fs.chmods[root]; ok {
			t.Fatalf("Fleet changed package-managed directory mode %q", root)
		}
		if _, ok := fs.mkdirs[root]; ok {
			t.Fatalf("Fleet created package-managed directory %q", root)
		}
	}
	if fs.chmods[instanceDir] != 0751 || fs.chmods[envDir] != 0751 {
		t.Fatalf("expected only dynamic instance directories to receive rootless traversal mode, got %v", fs.chmods)
	}
}

func TestPrepareStorageRootsSkipsWithoutRootlessUser(t *testing.T) {
	fs := newRecordingFS()
	exec := &SystemdPodmanExecutor{FS: fs, DataDir: "/var/lib/admiral"}
	if err := exec.prepareStorageRoots(); err != nil {
		t.Fatalf("prepareStorageRoots: %v", err)
	}
	if len(fs.chowns) != 0 {
		t.Fatalf("expected no chowns, got %v", fs.chowns)
	}
}

func TestHelperCommandArgs(t *testing.T) {
	exec := &SystemdPodmanExecutor{
		UserLookup:        fakeUserLookup{},
		RootlessUser:      "admiral-apps",
		DataDir:           "/var/lib/admiral",
		RestoreCACertFile: "/etc/admiral/tls/ca.pem",
	}
	args, err := exec.helperCommandArgs("991", helperActionBackup)
	if err != nil {
		t.Fatalf("helperCommandArgs: %v", err)
	}
	want := []string{
		"-u", "admiral-apps", "--",
		"env",
		"HOME=/var/lib/admiral-apps",
		"XDG_RUNTIME_DIR=/run/user/991",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/991/bus",
		"ADMIRAL_FLEET_DATA_DIR=/var/lib/admiral",
		"ADMIRAL_FLEET_ROOTLESS_USER=admiral-apps",
	}
	got := args[:len(want)]
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected argv: got %v want %v", got, want)
	}
	if len(args) != len(want)+2 {
		t.Fatalf("unexpected argv length: got %d want %d", len(args), len(want)+2)
	}
	if !strings.HasSuffix(args[len(want)], defaultHelperBinary) {
		t.Fatalf("expected helper binary before action, got %v", args)
	}
	if args[len(want)+1] != helperActionBackup {
		t.Fatalf("expected action %q at the end, got %v", helperActionBackup, args)
	}
}

func TestHelperCommandArgsRequiresAction(t *testing.T) {
	exec := &SystemdPodmanExecutor{RootlessUser: "admiral-apps"}
	if _, err := exec.helperCommandArgs("991", ""); err == nil {
		t.Fatal("expected error for empty action")
	}
}

func TestHelperTaskPayloadPassesAdmiralCAOnlyForTrustedHarborRestore(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	caPath := filepath.Join(t.TempDir(), "admiral-ca.pem")
	if err := os.WriteFile(caPath, caPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if !x509.NewCertPool().AppendCertsFromPEM(caPEM) {
		t.Fatal("test certificate is not valid PEM")
	}

	task := admiral.FleetTask{
		TaskID:  "task_test",
		Restore: &admiral.RestoreInfo{TrustedSourceOrigin: "https://localhost:5001"},
	}
	exec := &SystemdPodmanExecutor{RestoreCACertFile: caPath}

	encoded, err := exec.marshalHelperTaskPayload(task, helperActionRestore)
	if err != nil {
		t.Fatalf("marshal trusted Harbor restore payload: %v", err)
	}
	var decoded HelperTaskPayload
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal helper payload: %v", err)
	}
	if decoded.Task.TaskID != task.TaskID {
		t.Fatalf("task id changed in helper payload: %q", decoded.Task.TaskID)
	}
	if string(decoded.RestoreCACertPEM) != string(caPEM) {
		t.Fatal("helper payload did not carry the validated Admiral CA PEM")
	}

	for _, action := range []string{helperActionBackup, helperActionRestore} {
		untrusted := task
		untrusted.Restore = &admiral.RestoreInfo{}
		encoded, err := exec.marshalHelperTaskPayload(untrusted, action)
		if err != nil {
			t.Fatalf("marshal %s payload without Harbor capability: %v", action, err)
		}
		decoded = HelperTaskPayload{}
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("unmarshal %s payload: %v", action, err)
		}
		if len(decoded.RestoreCACertPEM) != 0 {
			t.Fatalf("%s payload unexpectedly contains the Admiral CA", action)
		}
	}
}

func TestHelperTaskPayloadRequiresReadableValidCAForTrustedRestore(t *testing.T) {
	task := admiral.FleetTask{
		TaskID:  "task_test",
		Restore: &admiral.RestoreInfo{TrustedSourceOrigin: "https://localhost:5001"},
	}
	if _, err := (&SystemdPodmanExecutor{}).marshalHelperTaskPayload(task, helperActionRestore); err == nil || !strings.Contains(err.Error(), "requires a configured Admiral CA") {
		t.Fatalf("expected missing CA to fail clearly, got %v", err)
	}
	if _, err := (&SystemdPodmanExecutor{RestoreCACertFile: filepath.Join(t.TempDir(), "missing.pem")}).marshalHelperTaskPayload(task, helperActionRestore); err == nil || !strings.Contains(err.Error(), "read Admiral CA certificate") {
		t.Fatalf("expected unreadable CA to fail clearly, got %v", err)
	}
	path := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(path, []byte("not a certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&SystemdPodmanExecutor{RestoreCACertFile: path}).marshalHelperTaskPayload(task, helperActionRestore); err == nil || !strings.Contains(err.Error(), "contains no valid certificates") {
		t.Fatalf("expected invalid CA to fail closed, got %v", err)
	}
}

func TestHelperBinaryPathFallsBackToUsrBin(t *testing.T) {
	exec := &SystemdPodmanExecutor{}
	got := exec.helperBinaryPath()
	want := filepath.Join("/usr/bin", defaultHelperBinary)
	if got != want {
		t.Fatalf("unexpected helper path: got %q want %q", got, want)
	}
}
