// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"log/slog"
	"os"
	"time"

	"github.com/admiral-project/admiral/admiral-fleet/internal/agent"
	"github.com/admiral-project/admiral/admiral-fleet/internal/config"
	"github.com/admiral-project/admiral/admiral-fleet/internal/executor"
	"github.com/admiral-project/admiral/admiral-fleet/internal/queue"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}

	var pubKey ed25519.PublicKey
	if cfg.TaskPublicKey != "" {
		raw, err := hex.DecodeString(cfg.TaskPublicKey)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			slog.Error("invalid ADMIRAL_TASK_PUBLIC_KEY", "error", "must be 64 hex chars (32 bytes)")
			os.Exit(1)
		}
		pubKey = ed25519.PublicKey(raw)
	}

	consumer, err := queue.NewConsumer(cfg.QueueDatabaseURL, cfg.NodeID, pubKey)
	if err != nil {
		slog.Error("queue error", "error", err)
		os.Exit(1)
	}
	defer consumer.Close()

	exec := buildExecutor(cfg)
	fleetAgent, err := agent.New(cfg.NodeID, cfg.APIURL, cfg.FleetToken, cfg.APICACertFile, cfg.CallbackOutbox, cfg.StorageCheckInterval, cfg.StorageExceededAction, cfg.RootlessUser, cfg.QuadletDir, exec)
	if err != nil {
		slog.Error("agent configuration error", "error", err)
		os.Exit(1)
	}
	slog.Info("admiral-fleet started", "node_id", cfg.NodeID, "executor", cfg.Executor)
	agent.StartHTTPServer(cfg.HTTPAddr, cfg.NodeID, cfg.Executor, cfg.PublicHost, cfg.PublicPort)
	go fleetAgent.StartHealthChecker(context.Background())
	go fleetAgent.StartHeartbeatSender(context.Background())
	go fleetAgent.StartStorageChecker(context.Background())
	go fleetAgent.StartOutboxFlusher(context.Background(), 30*time.Second)

	// Reconcile before consuming commands so the control plane has the
	// current local instance view after worker restart.
	fleetAgent.Reconcile(context.Background())

	consumer.ConsumeLoop(fleetAgent.HandleTask)
}

func buildExecutor(cfg *config.Config) executor.Executor {
	switch cfg.Executor {
	case "systemd-podman":
		return executor.NewSystemdPodman(nil, nil, cfg.QuadletDir, cfg.DataDir, cfg.RootlessUser)
	default:
		return executor.NewSimulated()
	}
}
