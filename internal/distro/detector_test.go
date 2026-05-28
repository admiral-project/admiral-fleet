package distro

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDetectUbuntuDevelopmentTier(t *testing.T) {
	dir := t.TempDir()
	osRelease := filepath.Join(dir, "os-release")
	if err := os.WriteFile(osRelease, []byte("ID=ubuntu\nVERSION_ID=\"25.04\"\nPRETTY_NAME=\"Ubuntu 25.04\"\n"), 0644); err != nil {
		t.Fatalf("write os-release: %v", err)
	}

	detector := &Detector{
		OSReleasePath: osRelease,
		LookPath: func(name string) (string, error) {
			if name == "systemctl" || name == "podman" {
				return "/usr/bin/" + name, nil
			}
			return "", os.ErrNotExist
		},
		Stat: func(path string) (os.FileInfo, error) {
			if path == "/usr/libexec/podman/quadlet" {
				return fakeFileInfo{}, nil
			}
			return nil, os.ErrNotExist
		},
	}

	info, err := detector.Detect()
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if info.PackageFamily != "deb" {
		t.Fatalf("expected deb family, got %q", info.PackageFamily)
	}
	if !info.Systemd || !info.Podman || !info.Quadlet {
		t.Fatalf("expected systemd/podman/quadlet detected: %+v", info)
	}
}

func TestPackageFamilyRPM(t *testing.T) {
	for _, id := range []string{"fedora", "rhel", "rocky", "almalinux"} {
		if got := packageFamily(id); got != "rpm" {
			t.Fatalf("expected rpm for %s, got %q", id, got)
		}
	}
}

type fakeFileInfo struct{}

func (fakeFileInfo) Name() string       { return "quadlet" }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) Mode() os.FileMode  { return 0755 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return false }
func (fakeFileInfo) Sys() interface{}   { return nil }
