package distro

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type Info struct {
	ID            string
	VersionID     string
	PrettyName    string
	PackageFamily string
	Systemd       bool
	Podman        bool
	Quadlet       bool
}

type Detector struct {
	OSReleasePath string
	LookPath      func(string) (string, error)
	Stat          func(string) (os.FileInfo, error)
}

func NewDetector() *Detector {
	return &Detector{
		OSReleasePath: "/etc/os-release",
		LookPath:      exec.LookPath,
		Stat:          os.Stat,
	}
}

func (d *Detector) Detect() (*Info, error) {
	values, err := parseOSRelease(defaultString(d.OSReleasePath, "/etc/os-release"))
	if err != nil {
		return nil, err
	}

	lookPath := d.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	stat := d.Stat
	if stat == nil {
		stat = os.Stat
	}

	info := &Info{
		ID:         values["ID"],
		VersionID:  values["VERSION_ID"],
		PrettyName: values["PRETTY_NAME"],
		Systemd:    commandExists(lookPath, "systemctl"),
		Podman:     commandExists(lookPath, "podman"),
		Quadlet:    pathExists(stat, "/usr/libexec/podman/quadlet") || pathExists(stat, "/usr/lib/systemd/system-generators/podman-system-generator"),
	}
	info.PackageFamily = packageFamily(info.ID)
	return info, nil
}

func parseOSRelease(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read os release: %w", err)
	}
	defer file.Close()

	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		values[parts[0]] = strings.Trim(parts[1], `"`)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan os release: %w", err)
	}
	return values, nil
}

func packageFamily(id string) string {
	switch id {
	case "fedora", "rhel", "centos", "rocky", "almalinux", "ol":
		return "rpm"
	case "ubuntu", "debian":
		return "deb"
	default:
		return "unknown"
	}
}

func commandExists(lookPath func(string) (string, error), name string) bool {
	_, err := lookPath(name)
	return err == nil
}

func pathExists(stat func(string) (os.FileInfo, error), path string) bool {
	_, err := stat(path)
	return err == nil
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
