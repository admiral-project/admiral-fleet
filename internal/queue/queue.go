package queue

import (
	"crypto/tls"
	"encoding/json"
	"fmt"

	"github.com/admiral-project/admiral/admirald/pkg/admiral"
	"github.com/streadway/amqp"
)

type Consumer struct {
	conn *amqp.Connection
	ch   *amqp.Channel
}

func NewConsumer(url string, tlsConfig *tls.Config) (*Consumer, error) {
	conn, err := amqp.DialTLS(url, tlsConfig)
	if err != nil {
		return nil, fmt.Errorf("connect rabbitmq: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open rabbitmq channel: %w", err)
	}
	_, err = ch.QueueDeclare(
		"fleet_tasks",
		true,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("declare fleet_tasks queue: %w", err)
	}
	return &Consumer{conn: conn, ch: ch}, nil
}

func (c *Consumer) Consume(handler func(admiral.FleetTask) error) error {
	deliveries, err := c.ch.Consume(
		"fleet_tasks",
		"",
		false,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		return fmt.Errorf("consume fleet_tasks: %w", err)
	}

	for msg := range deliveries {
		var task admiral.FleetTask
		if err := json.Unmarshal(msg.Body, &task); err != nil {
			_ = msg.Nack(false, false)
			continue
		}
		if err := handler(task); err != nil {
			_ = msg.Nack(false, true)
			continue
		}
		_ = msg.Ack(false)
	}
	return nil
}

func (c *Consumer) Close() {
	if c.ch != nil {
		_ = c.ch.Close()
	}
	if c.conn != nil {
		_ = c.conn.Close()
	}
}
