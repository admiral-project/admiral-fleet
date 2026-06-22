// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package osutil

import (
	"os"
	"os/user"
	"path/filepath"
	"testing"
)

func TestRealFileSystemRoundTrip(t *testing.T) {
	fs := RealFileSystem{}
	root := t.TempDir()
	dir := filepath.Join(root, "nested")
	file := filepath.Join(dir, "config.txt")

	if err := fs.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir all: %v", err)
	}
	if err := fs.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	data, err := fs.ReadFile(file)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if got := string(data); got != "hello" {
		t.Fatalf("unexpected file contents %q", got)
	}

	info, err := fs.Stat(file)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if info.IsDir() {
		t.Fatal("expected regular file, got directory")
	}

	f, err := fs.Open(file)
	if err != nil {
		t.Fatalf("open file: %v", err)
	}
	_ = f.Close()

	if err := fs.Chmod(file, 0o644); err != nil {
		t.Fatalf("chmod file: %v", err)
	}
	if err := fs.Remove(file); err != nil {
		t.Fatalf("remove file: %v", err)
	}
	if _, err := fs.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("expected removed file to be missing, got %v", err)
	}
}

func TestRealFileSystemCreateWalkAndRemoveAll(t *testing.T) {
	fs := RealFileSystem{}
	root := t.TempDir()
	file := filepath.Join(root, "walk.txt")

	f, err := fs.Create(file)
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	if _, err := f.WriteString("walk"); err != nil {
		t.Fatalf("write created file: %v", err)
	}
	_ = f.Close()

	var seen []string
	if err := fs.Walk(root, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		seen = append(seen, filepath.Base(path))
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(seen) < 2 {
		t.Fatalf("expected root and file during walk, saw %v", seen)
	}

	if err := fs.RemoveAll(root); err != nil {
		t.Fatalf("remove all: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("expected removed tree to be missing, got %v", err)
	}
}

func TestRealUserLookupMatchesStdlibLookup(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Fatalf("current user: %v", err)
	}

	got, err := RealUserLookup{}.Lookup(current.Username)
	if err != nil {
		t.Fatalf("lookup user: %v", err)
	}
	if got.Username != current.Username {
		t.Fatalf("unexpected username %q", got.Username)
	}
}
