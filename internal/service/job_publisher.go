package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"

	"depin-backend/pkg/pb"

	amqp "github.com/rabbitmq/amqp091-go"
)

type JobPublisher struct {
	rmqConn *amqp.Connection
}

func NewJobPublisher(rmqConn *amqp.Connection) *JobPublisher {
	return &JobPublisher{
		rmqConn: rmqConn,
	}
}

// PublishJob enqueues a new molecular simulation job.
// It applies the "Spot-Check" selection logic (5% chance).
func (p *JobPublisher) PublishJob(ctx context.Context, jobID string, smiles string, targetPDB string, maxExhaustiveness int32) error {
	ch, err := p.rmqConn.Channel()
	if err != nil {
		return fmt.Errorf("failed to open channel: %w", err)
	}
	defer ch.Close()

	assignment := &pb.JobAssignment{
		JobId:             jobID,
		SmilesString:      smiles,
		TargetPdbUrl:      targetPDB,
		MaxExhaustiveness: maxExhaustiveness,
	}

	body, err := json.Marshal(assignment)
	if err != nil {
		return fmt.Errorf("failed to marshal job assignment: %w", err)
	}

	isSpotCheck := rand.Intn(100) < 5
	priority := uint8(1)
	copies := 1

	if isSpotCheck {
		log.Printf("Job %s selected for SPOT-CHECK! Prioritizing and duplicating...", jobID)
		priority = 10
		copies = 2
	}

	for i := 0; i < copies; i++ {
		err = ch.PublishWithContext(ctx,
			"",                    // exchange
			"pending_simulations", // routing key
			false,                 // mandatory
			false,                 // immediate
			amqp.Publishing{
				DeliveryMode: amqp.Persistent,
				ContentType:  "application/json",
				Priority:     priority,
				Body:         body,
			},
		)
		if err != nil {
			return fmt.Errorf("failed to publish job to queue: %w", err)
		}
	}

	return nil
}
