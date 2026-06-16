// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package quadlet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

func TestRendererWritesQuadletPodFiles(t *testing.T) {
	quadletDir := t.TempDir()
	dataDir := t.TempDir()
	renderer := NewRenderer(quadletDir, dataDir)

	task := admiral.FleetTask{
		InstanceID: "demo001",
		Tier: admiral.TierInfo{
			CPU:    1.5,
			Memory: "1536MiB",
		},
		Services: []admiral.ServiceInfo{
			{
				Name:  "app",
				Image: "docker.io/traefik/whoami:v1.10",
				Port:  80,
				Env:   map[string]string{"DATABASE_HOST": "localhost"},
			},
			{
				Name:    "db",
				Image:   "docker.io/library/postgres:16",
				Volume:  "db_data",
				Env:     map[string]string{"POSTGRES_DB": "whoami"},
				Secrets: map[string]string{"POSTGRES_PASSWORD": "secret", "POSTGRES_USER": "user"},
			},
		},
	}

	if err := renderer.Render(task); err != nil {
		t.Fatalf("render quadlet: %v", err)
	}

	expectedFiles := []string{
		"admiral-demo001.pod",
		"admiral-demo001-app.container",
		"admiral-demo001-db.container",
		"admiral-demo001-db.volume",
	}
	for _, name := range expectedFiles {
		if _, err := os.Stat(filepath.Join(quadletDir, name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}

	// Verify no .network file is created when using pods
	if _, err := os.Stat(filepath.Join(quadletDir, "admiral-demo001.network")); err == nil {
		t.Fatal("unexpected .network file when pod is used")
	}

	envPath := filepath.Join(dataDir, "instances", "demo001", "env", "db.env")
	envData, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read db env: %v", err)
	}
	if string(envData) != "POSTGRES_DB=whoami\nPOSTGRES_PASSWORD=secret\nPOSTGRES_USER=user\n" {
		t.Fatalf("unexpected env file: %q", string(envData))
	}
	envInfo, err := os.Stat(envPath)
	if err != nil {
		t.Fatalf("stat db env: %v", err)
	}
	if envInfo.Mode().Perm() != 0600 {
		t.Fatalf("expected env file mode 0600, got %o", envInfo.Mode().Perm())
	}
	envDirInfo, err := os.Stat(filepath.Dir(envPath))
	if err != nil {
		t.Fatalf("stat env dir: %v", err)
	}
	if envDirInfo.Mode().Perm() != 0700 {
		t.Fatalf("expected env dir mode 0700, got %o", envDirInfo.Mode().Perm())
	}

	// Verify pod file
	podData, err := os.ReadFile(filepath.Join(quadletDir, "admiral-demo001.pod"))
	if err != nil {
		t.Fatalf("read pod file: %v", err)
	}
	gotPod := string(podData)
	if !strings.Contains(gotPod, "PodName=admiral-demo001") {
		t.Fatalf("expected PodName in pod file, got %q", gotPod)
	}
	if !strings.Contains(gotPod, "PodmanArgs=--cpus=1.5") {
		t.Fatalf("expected CPU limit in pod file, got %q", gotPod)
	}
	if !strings.Contains(gotPod, "PodmanArgs=--memory=1536m") {
		t.Fatalf("expected memory limit in pod file, got %q", gotPod)
	}
	if !strings.Contains(gotPod, "PublishPort=80") {
		t.Fatalf("expected PublishPort in pod file, got %q", gotPod)
	}

	// Verify container files reference the pod instead of network
	appData, err := os.ReadFile(filepath.Join(quadletDir, "admiral-demo001-app.container"))
	if err != nil {
		t.Fatalf("read app container: %v", err)
	}
	got := string(appData)
	if !strings.Contains(got, "Pod=admiral-demo001.pod") {
		t.Fatalf("expected Pod= in container file, got %q", got)
	}
	if strings.Contains(got, "Network=") {
		t.Fatal("unexpected Network= in container file when pod is used")
	}
	if strings.Contains(got, "--network-alias") {
		t.Fatal("unexpected --network-alias in container file when pod is used")
	}
	if !strings.Contains(got, "CgroupsMode=no-conmon") {
		t.Fatalf("expected cgroups mode in container file, got %q", got)
	}
}

func TestRendererMakesQuadletDirTraversableForRootlessUser(t *testing.T) {
	parent := t.TempDir()
	quadletDir := filepath.Join(parent, "admiral")
	dataDir := t.TempDir()
	renderer := NewRenderer(quadletDir, dataDir)

	task := admiral.FleetTask{
		InstanceID: "demo002",
		Tier:       admiral.TierInfo{CPU: 1},
		Services: []admiral.ServiceInfo{
			{Name: "app", Image: "docker.io/traefik/whoami:v1.10"},
		},
	}

	if err := renderer.Render(task); err != nil {
		t.Fatalf("render quadlet: %v", err)
	}

	info, err := os.Stat(quadletDir)
	if err != nil {
		t.Fatalf("stat quadlet dir: %v", err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("expected quadlet dir mode 0755, got %o", info.Mode().Perm())
	}
}

func TestRendererSingleServiceWithTierLimitsCreatesPod(t *testing.T) {
	quadletDir := t.TempDir()
	dataDir := t.TempDir()
	renderer := NewRenderer(quadletDir, dataDir)

	task := admiral.FleetTask{
		InstanceID: "single001",
		Tier: admiral.TierInfo{
			CPU:    1,
			Memory: "512MiB",
		},
		Services: []admiral.ServiceInfo{
			{
				Name:  "web",
				Image: "docker.io/traefik/whoami:v1.10",
				Port:  80,
			},
		},
	}

	if err := renderer.Render(task); err != nil {
		t.Fatalf("render quadlet: %v", err)
	}

	// Verify pod file exists when tier limits are set
	if _, err := os.Stat(filepath.Join(quadletDir, "admiral-single001.pod")); err != nil {
		t.Fatalf("expected pod file for single service tier limits: %v", err)
	}

	// Verify no network file either
	if _, err := os.Stat(filepath.Join(quadletDir, "admiral-single001.network")); err == nil {
		t.Fatal("unexpected .network file for single service")
	}

	// Verify container file exists and joins the pod
	appData, err := os.ReadFile(filepath.Join(quadletDir, "admiral-single001-web.container"))
	if err != nil {
		t.Fatalf("read container file: %v", err)
	}
	got := string(appData)
	if !strings.Contains(got, "Pod=admiral-single001.pod") {
		t.Fatalf("expected Pod= in container file, got %q", got)
	}
	if !strings.Contains(got, "CgroupsMode=no-conmon") {
		t.Fatalf("expected cgroups mode in container file, got %q", got)
	}
	podData, err := os.ReadFile(filepath.Join(quadletDir, "admiral-single001.pod"))
	if err != nil {
		t.Fatalf("read pod file: %v", err)
	}
	if !strings.Contains(string(podData), "PublishPort=80") {
		t.Fatalf("expected PublishPort in pod file, got %q", string(podData))
	}
}

func TestFormatCPULimitEdgeCases(t *testing.T) {
	tests := []struct {
		cpu  float64
		want string
	}{
		{0, ""},
		{-1, ""},
		{1, "1"},
		{1.0, "1"},
		{0.5, "0.5"},
		{1.5, "1.5"},
		{2.25, "2.25"},
	}
	for _, tc := range tests {
		got := formatCPULimit(tc.cpu)
		if got != tc.want {
			t.Errorf("formatCPULimit(%v) = %q; want %q", tc.cpu, got, tc.want)
		}
	}
}

func TestFormatMemoryLimitEdgeCases(t *testing.T) {
	tests := []struct {
		val  string
		want string
	}{
		{"", ""},
		{"  ", ""},
		{"1536MiB", "1536m"},
		{"512Mi", "512m"},
		{"1GiB", "1g"},
		{"2Gi", "2g"},
		{"1024MB", "1024m"},
		{"1GB", "1g"},
		{"1G", "1G"},
		{"512M", "512M"},
		{"unknown", "unknown"},
	}
	for _, tc := range tests {
		got := formatMemoryLimit(tc.val)
		if got != tc.want {
			t.Errorf("formatMemoryLimit(%q) = %q; want %q", tc.val, got, tc.want)
		}
	}
}

func TestRendererNoLimitsSkipsCPUAndMemory(t *testing.T) {
	quadletDir := t.TempDir()
	dataDir := t.TempDir()
	renderer := NewRenderer(quadletDir, dataDir)

	task := admiral.FleetTask{
		InstanceID: "nolimits",
		Tier: admiral.TierInfo{
			CPU:    0,
			Memory: "",
		},
		Services: []admiral.ServiceInfo{
			{Name: "web", Image: "docker.io/traefik/whoami:v1.10"},
			{Name: "db", Image: "docker.io/library/postgres:16", Volume: "db_data"},
		},
	}

	if err := renderer.Render(task); err != nil {
		t.Fatalf("render quadlet: %v", err)
	}

	if _, err := os.Stat(filepath.Join(quadletDir, "admiral-nolimits.pod")); err != nil {
		t.Fatalf("expected pod file even without limits: %v", err)
	}

	podData, err := os.ReadFile(filepath.Join(quadletDir, "admiral-nolimits.pod"))
	if err != nil {
		t.Fatalf("read pod file: %v", err)
	}
	got := string(podData)
	if strings.Contains(got, "PodmanArgs=--cpus") {
		t.Fatal("unexpected --cpus in pod file when CPU=0")
	}
	if strings.Contains(got, "PodmanArgs=--memory") {
		t.Fatal("unexpected --memory in pod file when Memory=\"\"")
	}
}

func TestRenderVolume(t *testing.T) {
	got := renderVolume("inst001", "db", "multi-user.target")
	if !strings.Contains(got, "VolumeName=admiral-inst001-db") {
		t.Fatalf("expected VolumeName in volume file:\n%s", got)
	}
	if strings.Contains(got, "Size=") {
		t.Fatal("unexpected Size= in volume file (storage enforcement is done via soft monitoring, not Quadlet)")
	}
}

func TestSafeName(t *testing.T) {
	if got := SafeName("demo.001/example"); got != "demo-001-example" {
		t.Fatalf("unexpected safe name %q", got)
	}
}
