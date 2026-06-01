package osutil

import (
	"os"
	"os/user"
	"path/filepath"
)

type FileSystem interface {
	MkdirAll(path string, perm os.FileMode) error
	Chmod(name string, mode os.FileMode) error
	Chown(name string, uid, gid int) error
	RemoveAll(path string) error
	Remove(name string) error
	Stat(name string) (os.FileInfo, error)
	Create(name string) (*os.File, error)
	Open(name string) (*os.File, error)
	ReadFile(name string) ([]byte, error)
	WriteFile(filename string, data []byte, perm os.FileMode) error
	Walk(root string, walkFn filepath.WalkFunc) error
}

type RealFileSystem struct{}

func (RealFileSystem) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }
func (RealFileSystem) Chmod(name string, mode os.FileMode) error    { return os.Chmod(name, mode) }
func (RealFileSystem) Chown(name string, uid, gid int) error       { return os.Chown(name, uid, gid) }
func (RealFileSystem) RemoveAll(path string) error                 { return os.RemoveAll(path) }
func (RealFileSystem) Remove(name string) error                    { return os.Remove(name) }
func (RealFileSystem) Stat(name string) (os.FileInfo, error)       { return os.Stat(name) }
func (RealFileSystem) Create(name string) (*os.File, error)        { return os.Create(name) }
func (RealFileSystem) Open(name string) (*os.File, error)          { return os.Open(name) }
func (RealFileSystem) ReadFile(name string) ([]byte, error)        { return os.ReadFile(name) }
func (RealFileSystem) WriteFile(filename string, data []byte, perm os.FileMode) error {
	return os.WriteFile(filename, data, perm)
}
func (RealFileSystem) Walk(root string, walkFn filepath.WalkFunc) error {
	return filepath.Walk(root, walkFn)
}

type UserLookup interface {
	Lookup(username string) (*user.User, error)
}

type RealUserLookup struct{}

func (RealUserLookup) Lookup(username string) (*user.User, error) { return user.Lookup(username) }
