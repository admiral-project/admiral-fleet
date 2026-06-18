// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

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
	QueueDatabaseURL      string
	APIURL                string
	APICACertFile         string
	FleetToken            string
	TaskPublicKey         string
	TaskEncryptionKey     string
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
		NodeID:                os.Getenv("ADMIRAL_FLEET_NODE_ID"),
		QueueDatabaseURL:      os.Getenv("ADMIRAL_QUEUE_DATABASE_URL"),
		APIURL:                getEnv("ADMIRAL_API_URL", "https://127.0.0.1:8080"),
		APICACertFile:         os.Getenv("ADMIRAL_API_CA_FILE"),
		FleetToken:            os.Getenv("ADMIRAL_FLEET_TOKEN"),
		TaskPublicKey:         os.Getenv("ADMIRAL_TASK_PUBLIC_KEY"),
		TaskEncryptionKey:     os.Getenv("ADMIRAL_TASK_ENCRYPTION_KEY"),
		Executor:              getEnv("ADMIRAL_FLEET_EXECUTOR", "simulated"),
		QuadletDir:            getEnv("ADMIRAL_FLEET_QUADLET_DIR", "/etc/containers/systemd/admiral"),
		DataDir:               getEnv("ADMIRAL_FLEET_DATA_DIR", "/var/lib/admiral"),
		CallbackOutbox:        getEnv("ADMIRAL_FLEET_CALLBACK_OUTBOX", "/var/lib/admiral/outbox"),
		HTTPAddr:              getEnv("ADMIRAL_FLEET_HTTP_ADDR", "127.0.0.1:9099"),
		PublicHost:            os.Getenv("ADMIRAL_FLEET_PUBLIC_HOST"),
		PublicPort:            os.Getenv("ADMIRAL_FLEET_PUBLIC_PORT"),
		StorageCheckInterval:  getEnv("ADMIRAL_FLEET_STORAGE_CHECK_INTERVAL", "60s"),
		StorageExceededAction: getEnv("ADMIRAL_FLEET_STORAGE_EXCEEDED_ACTION", "report_only"),
		RootlessUser:          os.Getenv("ADMIRAL_FLEET_ROOTLESS_USER"),
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
	if cfg.FleetToken == "" {
		return nil, fmt.Errorf("ADMIRAL_FLEET_TOKEN is required")
	}
	if cfg.RootlessUser == "" {
		return nil, fmt.Errorf("ADMIRAL_FLEET_ROOTLESS_USER is required: Admiral only supports rootless workloads")
	}
	if cfg.QueueDatabaseURL == "" {
		return nil, fmt.Errorf("ADMIRAL_QUEUE_DATABASE_URL is required")
	}
	if err := tlsconfig.ValidateURLScheme(cfg.APIURL, "https"); err != nil {
		return nil, fmt.Errorf("invalid ADMIRAL_API_URL: %w", err)
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
