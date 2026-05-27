package config

import (
	"os"
	"testing"
)

func TestLoadRequiresTLSURLs(t *testing.T) {
	setEnv(t, "ADMIRAL_FLEET_NODE_ID", "node-1")
	setEnv(t, "ADMIRAL_SHARED_TOKEN", "token")
	setEnv(t, "ADMIRAL_API_URL", "http://127.0.0.1:8080")
	setEnv(t, "ADMIRAL_RABBITMQ_URL", "amqps://guest:guest@localhost:5671/")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for plaintext API URL")
	}
}

func TestLoadAcceptsSecureURLs(t *testing.T) {
	setEnv(t, "ADMIRAL_FLEET_NODE_ID", "node-1")
	setEnv(t, "ADMIRAL_SHARED_TOKEN", "token")
	setEnv(t, "ADMIRAL_API_URL", "https://127.0.0.1:8080")
	setEnv(t, "ADMIRAL_RABBITMQ_URL", "amqps://guest:guest@localhost:5671/")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}
	if cfg.APIURL != "https://127.0.0.1:8080" {
		t.Fatalf("expected secure API URL, got %q", cfg.APIURL)
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
