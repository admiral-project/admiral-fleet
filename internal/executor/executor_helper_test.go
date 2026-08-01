// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package executor

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type recordingFS struct {
	mkdirs map[string]os.FileMode
	chowns map[string][2]int
	chmods map[string]os.FileMode
	// lchowns records paths handed over without following symlinks.
	lchowns map[string][2]int
	// tree holds child paths returned by Walk for recursive chown checks.
	tree []string
	// symlinks holds tree paths reported to Walk as symlinks.
	symlinks map[string]bool
}

func newRecordingFS() *recordingFS {
	return &recordingFS{
		mkdirs:   map[string]os.FileMode{},
		chowns:   map[string][2]int{},
		chmods:   map[string]os.FileMode{},
		lchowns:  map[string][2]int{},
		symlinks: map[string]bool{},
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
func (f *recordingFS) RemoveAll(string) error           { return nil }
func (f *recordingFS) Remove(string) error              { return nil }
func (f *recordingFS) Stat(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
func (f *recordingFS) Create(string) (*os.File, error)  { return nil, os.ErrInvalid }
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
		mode := os.FileMode(0)
		if f.symlinks[p] {
			mode = os.ModeSymlink
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
	fs.tree = []string{
		"/var/lib/admiral/backups/inst_old/old-mariadb-db.tar.gz",
		"/var/lib/admiral/restore/artifact.bin",
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
		if _, ok := fs.lchowns[root]; !ok {
			t.Fatalf("expected %q to be handed to rootless via lchown", root)
		}
		if fs.lchowns[root] != [2]int{1000, 1000} {
			t.Fatalf("unexpected lchown for %q: %v", root, fs.lchowns[root])
		}
		if fs.chmods[root] != 0751 {
			t.Fatalf("unexpected chmod for %q: %v", root, fs.chmods[root])
		}
		if _, followed := fs.chowns[root]; followed {
			t.Fatalf("storage root %q must not be chowned through a dereferencing path", root)
		}
	}
	for _, artifact := range fs.tree {
		if fs.lchowns[artifact] != [2]int{1000, 1000} {
			t.Fatalf("expected pre-existing artifact %q to be handed to rootless via lchown, got %v", artifact, fs.lchowns[artifact])
		}
	}
}

func TestPrepareStorageRootsSkipsSymlinks(t *testing.T) {
	fs := newRecordingFS()
	fs.tree = []string{
		"/var/lib/admiral/backups/inst_old/legit-db.tar.gz",
		"/var/lib/admiral/backups/inst_old/planted-link",
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
		RootlessUser: "admiral-apps",
		DataDir:      "/var/lib/admiral",
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

func TestHelperBinaryPathFallsBackToUsrBin(t *testing.T) {
	exec := &SystemdPodmanExecutor{}
	got := exec.helperBinaryPath()
	want := filepath.Join("/usr/bin", defaultHelperBinary)
	if got != want {
		t.Fatalf("unexpected helper path: got %q want %q", got, want)
	}
}
