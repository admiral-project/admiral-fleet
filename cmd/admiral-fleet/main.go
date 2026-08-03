// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	defer stop()
	fleetAgent, err := agent.New(cfg.NodeID, cfg.APIURL, cfg.FleetToken, cfg.APICACertFile, cfg.CallbackOutbox, cfg.StorageCheckInterval, cfg.StorageExceededAction, cfg.RootlessUser, cfg.QuadletDir, cfg.TaskPublicKey, exec)
	if err != nil {
		slog.Error("agent configuration error", "error", err)
		os.Exit(1)
	}

	slog.Info("admiral-fleet started", "node_id", cfg.NodeID, "executor", cfg.Executor)
	httpServer := agent.StartHTTPServerWithAllowedAdmin(cfg.HTTPAddr, cfg.NodeID, cfg.Executor, cfg.PublicHost, cfg.PublicPort, os.Getenv("ADMIRAL_FLEET_ADMIN_WIREGUARD_IP"))
	go fleetAgent.StartHealthChecker(ctx)
	go fleetAgent.StartHeartbeatSender(ctx)
	go fleetAgent.StartStorageChecker(ctx)
	go fleetAgent.StartOutboxFlusher(ctx, 30*time.Second)
	go fleetAgent.StartBackupStorageWarner(ctx)
	go fleetAgent.StartImagePuller(ctx)

	// Reconcile before consuming commands so the control plane has the
	// current local instance view after worker restart.
	fleetAgent.Reconcile(ctx)

	// Start periodic reconciler (every 1h) to ensure eventual consistency
	// regardless of transient network failures in health callbacks.
	go fleetAgent.StartReconciler(ctx, time.Hour)

	// Task claim loop: claim tasks from admirald via HTTP and execute them.
	// This replaces the previous PostgreSQL-backed consumer.
claimLoop:
	for {
		if ctx.Err() != nil {
			break
		}
		task, commandID, err := fleetAgent.ClaimTaskContext(ctx)
		if err != nil {
			if errors.Is(err, agent.ErrNoTaskAvailable) {
				select {
				case <-ctx.Done():
					break claimLoop
				case <-time.After(2 * time.Second):
				}
				continue
			}
			if ctx.Err() != nil {
				break
			}
			slog.Error("task claim failed", "error", err)
			select {
			case <-ctx.Done():
				break claimLoop
			case <-time.After(2 * time.Second):
			}
			continue
		}

		slog.Info("claimed task", "command_id", commandID, "task_id", task.TaskID, "action", task.Action)

		if err := fleetAgent.ReportRunning(commandID); err != nil {
			slog.Error("failed to report task running", "command_id", commandID, "error", err)
			continue
		}

		stopRenew := fleetAgent.StartLeaseRenewer(commandID)
		if err := fleetAgent.HandleTaskContext(ctx, *task); err != nil {
			slog.Error("failed to send callback", "task_id", task.TaskID, "error", err)
		}
		stopRenew()
	}
	if httpServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			slog.Warn("fleet HTTP shutdown failed", "error", err)
		}
	}
	if err := fleetAgent.FlushOutbox(); err != nil {
		slog.Warn("fleet outbox flush during shutdown failed", "error", err)
	}
}

func buildExecutor(cfg *config.Config) executor.Executor {
	switch cfg.Executor {
	case "systemd-podman":
		exec := executor.NewSystemdPodman(nil, nil, cfg.QuadletDir, cfg.DataDir, cfg.RootlessUser)
		// Data-plane backup/restore runs in admiral-fleet-backup as the
		// rootless user so artifacts are never chowned between root and the
		// workload user.
		exec.DelegateBackup = true
		exec.DelegateRestore = true
		exec.RemotePodman = true
		return exec
	default:
		return executor.NewSimulated()
	}
}
