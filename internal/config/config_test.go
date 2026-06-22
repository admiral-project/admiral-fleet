// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"testing"
)

func TestLoadRequiresTLSURLs(t *testing.T) {
	setEnv(t, "ADMIRAL_FLEET_NODE_ID", "node-1")
	setEnv(t, "ADMIRAL_FLEET_TOKEN", "token")
	setEnv(t, "ADMIRAL_FLEET_ROOTLESS_USER", "admiral-apps")
	setEnv(t, "ADMIRAL_API_URL", "http://127.0.0.1:8080")
	setEnv(t, "ADMIRAL_QUEUE_DATABASE_URL", "postgres://queue:pass@localhost:5432/admiral_queue?sslmode=require")
	setEnv(t, "ADMIRAL_TASK_PUBLIC_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for plaintext API URL")
	}
}

func TestLoadAcceptsSecureURLs(t *testing.T) {
	setEnv(t, "ADMIRAL_FLEET_NODE_ID", "node-1")
	setEnv(t, "ADMIRAL_FLEET_TOKEN", "token")
	setEnv(t, "ADMIRAL_FLEET_ROOTLESS_USER", "admiral-apps")
	setEnv(t, "ADMIRAL_FLEET_QUADLET_DIR", "/tmp/test-quadlet")
	setEnv(t, "ADMIRAL_API_URL", "https://127.0.0.1:8080")
	setEnv(t, "ADMIRAL_QUEUE_DATABASE_URL", "postgres://queue:pass@localhost:5432/admiral_queue?sslmode=require")
	setEnv(t, "ADMIRAL_TASK_PUBLIC_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}
	if cfg.APIURL != "https://127.0.0.1:8080" {
		t.Fatalf("expected secure API URL, got %q", cfg.APIURL)
	}
	if cfg.Executor != "simulated" {
		t.Fatalf("expected simulated executor by default, got %q", cfg.Executor)
	}
}

func TestLoadAcceptsSystemdPodmanExecutor(t *testing.T) {
	setEnv(t, "ADMIRAL_FLEET_NODE_ID", "node-1")
	setEnv(t, "ADMIRAL_FLEET_TOKEN", "token")
	setEnv(t, "ADMIRAL_FLEET_ROOTLESS_USER", "admiral-apps")
	setEnv(t, "ADMIRAL_API_URL", "https://127.0.0.1:8080")
	setEnv(t, "ADMIRAL_QUEUE_DATABASE_URL", "postgres://queue:pass@localhost:5432/admiral_queue?sslmode=require")
	setEnv(t, "ADMIRAL_TASK_PUBLIC_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	setEnv(t, "ADMIRAL_FLEET_EXECUTOR", "systemd-podman")
	setEnv(t, "ADMIRAL_FLEET_QUADLET_DIR", "/tmp/quadlet")
	setEnv(t, "ADMIRAL_FLEET_DATA_DIR", "/tmp/data")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}
	if cfg.Executor != "systemd-podman" {
		t.Fatalf("expected systemd-podman executor, got %q", cfg.Executor)
	}
	if cfg.QuadletDir != "/tmp/quadlet" || cfg.DataDir != "/tmp/data" {
		t.Fatalf("unexpected executor dirs: %+v", cfg)
	}
}

func TestLoadRejectsUnknownExecutor(t *testing.T) {
	setEnv(t, "ADMIRAL_FLEET_NODE_ID", "node-1")
	setEnv(t, "ADMIRAL_FLEET_TOKEN", "token")
	setEnv(t, "ADMIRAL_FLEET_ROOTLESS_USER", "admiral-apps")
	setEnv(t, "ADMIRAL_API_URL", "https://127.0.0.1:8080")
	setEnv(t, "ADMIRAL_QUEUE_DATABASE_URL", "postgres://queue:pass@localhost:5432/admiral_queue?sslmode=require")
	setEnv(t, "ADMIRAL_TASK_PUBLIC_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	setEnv(t, "ADMIRAL_FLEET_EXECUTOR", "shell")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for unknown executor")
	}
}

func TestLoadRequiresQueueDatabaseURL(t *testing.T) {
	setEnv(t, "ADMIRAL_FLEET_NODE_ID", "node-1")
	setEnv(t, "ADMIRAL_FLEET_TOKEN", "token")
	setEnv(t, "ADMIRAL_FLEET_ROOTLESS_USER", "admiral-apps")
	setEnv(t, "ADMIRAL_API_URL", "https://127.0.0.1:8080")
	setEnv(t, "ADMIRAL_QUEUE_DATABASE_URL", "")
	setEnv(t, "ADMIRAL_TASK_PUBLIC_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when queue database url is missing")
	}
}

func TestLoadRequiresTaskPublicKey(t *testing.T) {
	setEnv(t, "ADMIRAL_FLEET_NODE_ID", "node-1")
	setEnv(t, "ADMIRAL_FLEET_TOKEN", "token")
	setEnv(t, "ADMIRAL_FLEET_ROOTLESS_USER", "admiral-apps")
	setEnv(t, "ADMIRAL_API_URL", "https://127.0.0.1:8080")
	setEnv(t, "ADMIRAL_QUEUE_DATABASE_URL", "postgres://queue:pass@localhost:5432/admiral_queue?sslmode=require")
	setEnv(t, "ADMIRAL_TASK_PUBLIC_KEY", "")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when task public key is missing")
	}
}

func setEnv(t *testing.T, key, value string) {
	t.Helper()

	original, ok := os.LookupEnv(key)
	if err := os.Setenv(key, value); err != nil {
		t.Fatalf("set %s: %v", key, err)
	}

	t.Cleanup(func() {
		var err error
		if ok {
			err = os.Setenv(key, original)
		} else {
			err = os.Unsetenv(key)
		}
		if err != nil {
			t.Fatalf("restore %s: %v", key, err)
		}
	})
}
