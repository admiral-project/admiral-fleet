package config

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"

	"github.com/admiral-project/admiral/admirald/pkg/admiral/tlsconfig"
)

type Config struct {
	NodeID                string
	RabbitMQURL           string
	RabbitMQCAFile        string
	APIURL                string
	APICACertFile         string
	SharedToken           string
	Executor              string
	QuadletDir            string
	DataDir               string
	CallbackOutbox        string
	HTTPAddr              string
	PublicHost            string
	PublicPort            string
	StorageCheckInterval  string
	StorageExceededAction string
	RootlessUser          string // required: Admiral only supports rootless workloads
}

func Load() (*Config, error) {
	cfg := &Config{
		NodeID:         os.Getenv("ADMIRAL_FLEET_NODE_ID"),
		RabbitMQURL:    getEnv("ADMIRAL_RABBITMQ_URL", "amqps://guest:guest@localhost:5671/"),
		RabbitMQCAFile: os.Getenv("ADMIRAL_RABBITMQ_CA_FILE"),
		APIURL:         getEnv("ADMIRAL_API_URL", "https://127.0.0.1:8080"),
		APICACertFile:  os.Getenv("ADMIRAL_API_CA_FILE"),
		SharedToken:    os.Getenv("ADMIRAL_SHARED_TOKEN"),
		Executor:       getEnv("ADMIRAL_FLEET_EXECUTOR", "simulated"),
		QuadletDir:     getEnv("ADMIRAL_FLEET_QUADLET_DIR", "/etc/containers/systemd/admiral"),
		DataDir:        getEnv("ADMIRAL_FLEET_DATA_DIR", "/var/lib/admiral"),
		CallbackOutbox:       getEnv("ADMIRAL_FLEET_CALLBACK_OUTBOX", "/var/lib/admiral/outbox"),
		HTTPAddr:             getEnv("ADMIRAL_FLEET_HTTP_ADDR", "127.0.0.1:9099"),
		PublicHost:           os.Getenv("ADMIRAL_FLEET_PUBLIC_HOST"),
		PublicPort:           os.Getenv("ADMIRAL_FLEET_PUBLIC_PORT"),
		StorageCheckInterval: getEnv("ADMIRAL_FLEET_STORAGE_CHECK_INTERVAL", "60s"),
		StorageExceededAction: getEnv("ADMIRAL_FLEET_STORAGE_EXCEEDED_ACTION", "report_only"),
		RootlessUser:         os.Getenv("ADMIRAL_FLEET_ROOTLESS_USER"),
	}

	if cfg.RootlessUser != "" {
		if cfg.QuadletDir == "/etc/containers/systemd/admiral" {
			u, err := user.Lookup(cfg.RootlessUser)
			if err != nil {
				return nil, fmt.Errorf("lookup rootless user %q: %w", cfg.RootlessUser, err)
			}
			cfg.QuadletDir = filepath.Join("/etc/containers/systemd/users", u.Uid, "admiral")
		}
	}

	if cfg.NodeID == "" {
		return nil, fmt.Errorf("ADMIRAL_FLEET_NODE_ID is required")
	}
	if cfg.SharedToken == "" {
		return nil, fmt.Errorf("ADMIRAL_SHARED_TOKEN is required")
	}
	if cfg.RootlessUser == "" {
		return nil, fmt.Errorf("ADMIRAL_FLEET_ROOTLESS_USER is required: Admiral only supports rootless workloads")
	}
	if err := tlsconfig.ValidateURLScheme(cfg.APIURL, "https"); err != nil {
		return nil, fmt.Errorf("invalid ADMIRAL_API_URL: %w", err)
	}
	if err := tlsconfig.ValidateURLScheme(cfg.RabbitMQURL, "amqps"); err != nil {
		return nil, fmt.Errorf("invalid ADMIRAL_RABBITMQ_URL: %w", err)
	}
	switch cfg.Executor {
	case "simulated", "systemd-podman":
	default:
		return nil, fmt.Errorf("invalid ADMIRAL_FLEET_EXECUTOR %q", cfg.Executor)
	}
	return cfg, nil
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
