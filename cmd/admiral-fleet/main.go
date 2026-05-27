package main

import (
	"log"

	"github.com/admiral-project/admiral/admiral-fleet/internal/agent"
	"github.com/admiral-project/admiral/admiral-fleet/internal/config"
	"github.com/admiral-project/admiral/admiral-fleet/internal/queue"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	consumer, err := queue.NewConsumer(cfg.RabbitMQURL)
	if err != nil {
		log.Fatalf("queue error: %v", err)
	}
	defer consumer.Close()

	agent := agent.New(cfg.NodeID, cfg.APIURL, cfg.SharedToken)
	log.Printf("admiral-fleet started for node %s", cfg.NodeID)
	if err := consumer.Consume(agent.HandleTask); err != nil {
		log.Fatalf("consumer stopped: %v", err)
	}
}
