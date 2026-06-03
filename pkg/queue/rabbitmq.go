package queue

import (
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
	"depin-backend/internal/config"
)

// NewRabbitMQConnection establishes a connection to the RabbitMQ broker.
func NewRabbitMQConnection(cfg *config.Config) (*amqp.Connection, error) {
	conn, err := amqp.Dial(cfg.RabbitMQURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to rabbitmq: %w", err)
	}

	// Double check that we can open a channel as a test of operational status
	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to open test channel on rabbitmq: %w", err)
	}
	ch.Close()

	return conn, nil
}

// SetupQueues configures the required queues for the application.
func SetupQueues(ch *amqp.Channel) error {
	args := amqp.Table{
		"x-max-priority": int32(10), // Support priority queueing up to 10
	}
	
	_, err := ch.QueueDeclare(
		"pending_simulations", // name
		true,                  // durable
		false,                 // delete when unused
		false,                 // exclusive
		false,                 // no-wait
		args,                  // arguments
	)
	if err != nil {
		return fmt.Errorf("failed to declare pending_simulations queue: %w", err)
	}
	
	return nil
}
