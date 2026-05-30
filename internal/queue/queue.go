package queue

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/admiral-project/admiral/admirald/pkg/admiral"
	"github.com/streadway/amqp"
)

const queueName = "fleet_tasks"

type Consumer struct {
	url       string
	tlsConfig *tls.Config
	conn      *amqp.Connection
	ch        *amqp.Channel
}

func NewConsumer(url string, tlsConfig *tls.Config) (*Consumer, error) {
	c := &Consumer{url: url, tlsConfig: tlsConfig}
	if err := c.connect(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Consumer) connect() error {
	conn, err := amqp.DialTLS(c.url, c.tlsConfig)
	if err != nil {
		return fmt.Errorf("connect rabbitmq: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("open rabbitmq channel: %w", err)
	}
	_, err = ch.QueueDeclare(
		queueName,
		true,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return fmt.Errorf("declare %s queue: %w", queueName, err)
	}
	if c.ch != nil {
		_ = c.ch.Close()
	}
	if c.conn != nil {
		_ = c.conn.Close()
	}
	c.conn = conn
	c.ch = ch
	return nil
}

func (c *Consumer) ConsumeLoop(handler func(admiral.FleetTask) error) {
	backoff := 1 * time.Second
	maxBackoff := 30 * time.Second
	const factor = 2

	for {
		if err := c.consumeOnce(handler); err != nil {
			log.Printf("consumer error: %v (reconnecting in %v)", err, backoff)
		} else {
			log.Printf("consumer disconnected (reconnecting in %v)", backoff)
		}

		time.Sleep(backoff)
		backoff *= factor
		if backoff > maxBackoff {
			backoff = maxBackoff
		}

		if err := c.connect(); err != nil {
			log.Printf("reconnect failed: %v", err)
			continue
		}
		log.Printf("reconnected to rabbitmq")
		backoff = 1 * time.Second
	}
}

func (c *Consumer) consumeOnce(handler func(admiral.FleetTask) error) error {
	deliveries, err := c.ch.Consume(
		queueName,
		"",
		false,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		return fmt.Errorf("consume %s: %w", queueName, err)
	}

	closeCh := c.conn.NotifyClose(make(chan *amqp.Error, 1))

	for {
		select {
		case msg, ok := <-deliveries:
			if !ok {
				return nil
			}
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

		case err := <-closeCh:
			if err != nil {
				return fmt.Errorf("connection closed: %w", err)
			}
			return nil
		}
	}
}

func (c *Consumer) Close() {
	if c.ch != nil {
		_ = c.ch.Close()
	}
	if c.conn != nil {
		_ = c.conn.Close()
	}
}
