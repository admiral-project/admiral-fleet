package quadlet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

func TestRendererWritesQuadletFiles(t *testing.T) {
	quadletDir := t.TempDir()
	dataDir := t.TempDir()
	renderer := NewRenderer(quadletDir, dataDir)

	task := admiral.FleetTask{
		InstanceID: "demo001",
		Services: []admiral.ServiceInfo{
			{
				Name:  "app",
				Image: "docker.io/traefik/whoami:v1.10",
				Port:  80,
				Env:   map[string]string{"DATABASE_HOST": "db"},
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
		"admiral-demo001.network",
		"admiral-demo001-app.container",
		"admiral-demo001-db.container",
		"admiral-demo001-db.volume",
	}
	for _, name := range expectedFiles {
		if _, err := os.Stat(filepath.Join(quadletDir, name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}

	envPath := filepath.Join(dataDir, "instances", "demo001", "env", "db.env")
	envData, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read db env: %v", err)
	}
	if string(envData) != "POSTGRES_DB=whoami\nPOSTGRES_PASSWORD=secret\nPOSTGRES_USER=user\n" {
		t.Fatalf("unexpected env file: %q", string(envData))
	}

	appData, err := os.ReadFile(filepath.Join(quadletDir, "admiral-demo001-app.container"))
	if err != nil {
		t.Fatalf("read app container: %v", err)
	}
	got := string(appData)
	for _, want := range []string{"Network=admiral-demo001.network", "PodmanArgs=--network-alias=app", "PublishPort=80"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in container file, got %q", want, got)
		}
	}
}

func TestSafeName(t *testing.T) {
	if got := SafeName("demo.001/example"); got != "demo-001-example" {
		t.Fatalf("unexpected safe name %q", got)
	}
}
