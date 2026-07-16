// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/admiral-project/admiral/admiral-fleet/internal/agent"
	"github.com/admiral-project/admiral/admiral-fleet/internal/config"
	"github.com/admiral-project/admiral/admiral-fleet/internal/executor"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}

	exec := buildExecutor(cfg)
	fleetAgent, err := agent.New(cfg.NodeID, cfg.APIURL, cfg.FleetToken, cfg.APICACertFile, cfg.CallbackOutbox, cfg.StorageCheckInterval, cfg.StorageExceededAction, cfg.RootlessUser, cfg.QuadletDir, cfg.TaskPublicKey, exec)
	if err != nil {
		slog.Error("agent configuration error", "error", err)
		os.Exit(1)
	}

	slog.Info("admiral-fleet started", "node_id", cfg.NodeID, "executor", cfg.Executor)
	agent.StartHTTPServerWithAllowedAdmin(cfg.HTTPAddr, cfg.NodeID, cfg.Executor, cfg.PublicHost, cfg.PublicPort, os.Getenv("ADMIRAL_FLEET_ADMIN_WIREGUARD_IP"))
	go fleetAgent.StartHealthChecker(context.Background())
	go fleetAgent.StartHeartbeatSender(context.Background())
	go fleetAgent.StartStorageChecker(context.Background())
	go fleetAgent.StartOutboxFlusher(context.Background(), 30*time.Second)
	go fleetAgent.StartBackupStorageWarner(context.Background())

	// Reconcile before consuming commands so the control plane has the
	// current local instance view after worker restart.
	fleetAgent.Reconcile(context.Background())

	// Start periodic reconciler (every 1h) to ensure eventual consistency
	// regardless of transient network failures in health callbacks.
	go fleetAgent.StartReconciler(context.Background(), time.Hour)

	// Task claim loop: claim tasks from admirald via HTTP and execute them.
	// This replaces the previous PostgreSQL-backed consumer.
	for {
		task, commandID, err := fleetAgent.ClaimTask()
		if err != nil {
			if errors.Is(err, agent.ErrNoTaskAvailable) {
				time.Sleep(2 * time.Second)
				continue
			}
			slog.Error("task claim failed", "error", err)
			time.Sleep(2 * time.Second)
			continue
		}

		slog.Info("claimed task", "command_id", commandID, "task_id", task.TaskID, "action", task.Action)

		if err := fleetAgent.ReportRunning(commandID); err != nil {
			slog.Error("failed to report task running", "command_id", commandID, "error", err)
			continue
		}

		stopRenew := fleetAgent.StartLeaseRenewer(commandID)
		if err := fleetAgent.HandleTask(*task); err != nil {
			slog.Error("failed to send callback", "task_id", task.TaskID, "error", err)
		}
		stopRenew()
	}
}

func buildExecutor(cfg *config.Config) executor.Executor {
	switch cfg.Executor {
	case "systemd-podman":
		return executor.NewSystemdPodman(nil, nil, cfg.QuadletDir, cfg.DataDir, cfg.RootlessUser)
	default:
		return executor.NewSimulated()
	}
}
