package main

import (
	"context"
	"log"
	"time"

	"github.com/admiral-project/admiral/admiral-fleet/internal/agent"
	"github.com/admiral-project/admiral/admiral-fleet/internal/config"
	"github.com/admiral-project/admiral/admiral-fleet/internal/executor"
	"github.com/admiral-project/admiral/admiral-fleet/internal/queue"
	"github.com/admiral-project/admiral/admirald/pkg/admiral/tlsconfig"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	rabbitMQTLSConfig, err := tlsconfig.NewClientConfig(cfg.RabbitMQCAFile)
	if err != nil {
		log.Fatalf("rabbitmq TLS configuration error: %v", err)
	}
	consumer, err := queue.NewConsumer(cfg.RabbitMQURL, rabbitMQTLSConfig)
	if err != nil {
		log.Fatalf("queue error: %v", err)
	}
	defer consumer.Close()

	exec := buildExecutor(cfg)
	fleetAgent, err := agent.New(cfg.NodeID, cfg.APIURL, cfg.SharedToken, cfg.APICACertFile, cfg.CallbackOutbox, cfg.StorageCheckInterval, cfg.StorageExceededAction, exec)
	if err != nil {
		log.Fatalf("agent configuration error: %v", err)
	}
	log.Printf("admiral-fleet started for node %s with executor %s", cfg.NodeID, cfg.Executor)
	agent.StartHTTPServer(cfg.HTTPAddr, cfg.NodeID, cfg.Executor, cfg.PublicHost, cfg.PublicPort)
	go fleetAgent.StartHealthChecker(context.Background())
	go fleetAgent.StartStorageChecker(context.Background())

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			_ = fleetAgent.HandleTask
		}
	}()

	if err := consumer.Consume(fleetAgent.HandleTask); err != nil {
		log.Fatalf("consumer stopped: %v", err)
	}
}

func buildExecutor(cfg *config.Config) executor.Executor {
	switch cfg.Executor {
	case "systemd-podman":
		return executor.NewSystemdPodman(nil, nil, cfg.QuadletDir, cfg.DataDir)
	default:
		return executor.NewSimulated()
	}
}
