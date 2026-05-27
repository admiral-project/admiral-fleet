package config

import (
	"fmt"
	"os"
)

type Config struct {
	NodeID      string
	RabbitMQURL string
	APIURL      string
	SharedToken string
}

func Load() (*Config, error) {
	cfg := &Config{
		NodeID:      os.Getenv("ADMIRAL_FLEET_NODE_ID"),
		RabbitMQURL: getEnv("ADMIRAL_RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"),
		APIURL:      getEnv("ADMIRAL_API_URL", "http://127.0.0.1:8080"),
		SharedToken: os.Getenv("ADMIRAL_SHARED_TOKEN"),
	}

	if cfg.NodeID == "" {
		return nil, fmt.Errorf("ADMIRAL_FLEET_NODE_ID is required")
	}
	if cfg.SharedToken == "" {
		return nil, fmt.Errorf("ADMIRAL_SHARED_TOKEN is required")
	}
	return cfg, nil
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
