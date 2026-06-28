package agent

import (
	"context"
	"testing"
)

func TestDetectLocalIP(t *testing.T) {
	ip := detectLocalIP()
	// This might be empty in some CI environments, but shouldn't panic
	t.Logf("Detected IP: %s", ip)
}

func TestDetectPodmanVersion(t *testing.T) {
	v := detectPodmanVersion(context.Background())
	// In sandbox it might fail if podman is not installed, but it should handle it
	t.Logf("Podman version: %s", v)
}

func TestDetectDiskUsage(t *testing.T) {
	total, used := detectDiskUsage(context.Background())
	t.Logf("Disk: %d/%d", used, total)
}

func TestDetectRAMUsage(t *testing.T) {
	total, used, avail := detectRAMUsage(context.Background())
	t.Logf("RAM: %d/%d (avail %d)", used, total, avail)
}
