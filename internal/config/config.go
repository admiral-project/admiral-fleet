package config

import (
	"fmt"
	"os"

	"github.com/admiral-project/admiral/admirald/pkg/admiral/tlsconfig"
)

type Config struct {
	NodeID         string
	RabbitMQURL    string
	RabbitMQCAFile string
	APIURL         string
	APICACertFile  string
	SharedToken    string
	Executor       string
	QuadletDir     string
	DataDir        string
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
	}

	if cfg.NodeID == "" {
		return nil, fmt.Errorf("ADMIRAL_FLEET_NODE_ID is required")
	}
	if cfg.SharedToken == "" {
		return nil, fmt.Errorf("ADMIRAL_SHARED_TOKEN is required")
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
