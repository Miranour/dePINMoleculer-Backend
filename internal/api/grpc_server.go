package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"depin-backend/internal/repository"
	"depin-backend/internal/service"
	"depin-backend/pkg/pb"

	amqp "github.com/rabbitmq/amqp091-go"
)

type SimulationWorkerService struct {
	pb.UnimplementedSimulationWorkerServiceServer
	redisRepo    *repository.RedisRepository
	rmqConn      *amqp.Connection
	rewardEngine *service.RewardEngine
}

func NewSimulationWorkerService(redisRepo *repository.RedisRepository, rmqConn *amqp.Connection, rewardEngine *service.RewardEngine) *SimulationWorkerService {
	return &SimulationWorkerService{
		redisRepo:    redisRepo,
		rmqConn:      rmqConn,
		rewardEngine: rewardEngine,
	}
}

// SendHeartbeat receives worker's hardware status and updates Redis.
func (s *SimulationWorkerService) SendHeartbeat(ctx context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	status := map[string]interface{}{
		"api_key":         req.ApiKey,
		"gpu_temperature": req.GpuTemperature,
		"fan_speed":       req.FanSpeed,
		"available_vram":  req.AvailableVram,
		"current_flops":   req.CurrentFlops,
		"is_busy":         req.IsBusy,
		"last_seen":       fmt.Sprintf("%v", req.WorkerId), // simplified
	}

	err := s.redisRepo.UpdateHeartbeat(ctx, req.WorkerId, status)
	if err != nil {
		log.Printf("Failed to update heartbeat for worker %s: %v", req.WorkerId, err)
		return nil, err
	}

	// For now, return a basic PROCEED command.
	return &pb.HeartbeatResponse{
		Status:         true,
		Command:        "PROCEED",
		RecentRewards:  []*pb.RewardNotification{},
	}, nil
}

// StreamJobChannel is a bidirectional stream.
// It assigns jobs from RabbitMQ to the worker, and receives progress/results.
// If the worker disconnects, RabbitMQ will automatically requeue unacknowledged jobs.
func (s *SimulationWorkerService) StreamJobChannel(stream pb.SimulationWorkerService_StreamJobChannelServer) error {
	// Receive the first message to identify the worker
	req, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("failed to receive initial worker signal: %w", err)
	}

	workerID := req.WorkerId
	log.Printf("Worker %s connected to StreamJobChannel", workerID)

	ch, err := s.rmqConn.Channel()
	if err != nil {
		return fmt.Errorf("failed to open rmq channel: %w", err)
	}
	defer ch.Close()

	// Enforce 1 job at a time per worker
	if err := ch.Qos(1, 0, false); err != nil {
		return fmt.Errorf("failed to set QoS: %w", err)
	}

	msgs, err := ch.Consume(
		"pending_simulations", // queue
		"",                    // consumer
		false,                 // auto-ack (we use manual ack to allow requeuing)
		false,                 // exclusive
		false,                 // no-local
		false,                 // no-wait
		nil,                   // args
	)
	if err != nil {
		return fmt.Errorf("failed to consume from pending_simulations: %w", err)
	}

	signals := make(chan *pb.WorkerSignal)
	errChan := make(chan error)

	// Goroutine to read from stream
	go func() {
		for {
			in, err := stream.Recv()
			if err != nil {
				errChan <- err
				return
			}
			signals <- in
		}
	}()

	for {
		select {
		case err := <-errChan:
			log.Printf("Worker %s stream closed: %v", workerID, err)
			return err

		case d := <-msgs:
			// Decode the JobAssignment from RabbitMQ message body.
			var assignment pb.JobAssignment
			if err := json.Unmarshal(d.Body, &assignment); err != nil {
				log.Printf("Failed to unmarshal job assignment: %v", err)
				d.Nack(false, false) // dead-letter or discard
				continue
			}

			// Send the job to the worker
			out := &pb.BackendSignal{
				Command: &pb.BackendSignal_JobAssignment{
					JobAssignment: &assignment,
				},
			}
			if err := stream.Send(out); err != nil {
				log.Printf("Failed to send job %s to worker %s: %v", assignment.JobId, workerID, err)
				// Requeue the message so another worker can pick it up
				d.Nack(false, true)
				return err
			}

			// Wait for the result or progress of THIS specific job
		waitResultLoop:
			for {
				select {
				case err := <-errChan:
					log.Printf("Worker %s disconnected while processing job %s: %v", workerID, assignment.JobId, err)
					d.Nack(false, true) // Worker disconnected, requeue the job
					return err

				case sig := <-signals:
					if progress := sig.GetProgress(); progress != nil {
						log.Printf("Job %s progress from worker %s: %.2f%%", progress.JobId, workerID, progress.CompletionPercentage)
						// Could save progress to Redis or broadcast via WebSocket here
					} else if result := sig.GetResult(); result != nil {
						log.Printf("Job %s result from worker %s: Code %v", result.JobId, workerID, result.ErrorCode)
						
						if result.ErrorCode == pb.WorkerErrorCode_SUCCESS {
							// Call RewardEngine to calculate and distribute reward
							// For MVP, we assume maxExhaustiveness is 8 and default user ID is generic.
							// The actual job parameters should be retrieved from a database.
							isSpotCheck := d.Priority == 10
							err := s.rewardEngine.ProcessJobResult(context.Background(), workerID, result.JobId, "system_user", 15000, "RTX 3090", 8, isSpotCheck, result.BindingEnergy, result.OutputPdbqtData)
							if err != nil {
								log.Printf("Failed to process reward for job %s: %v", result.JobId, err)
							}

							d.Ack(false)
						} else {
							// Worker failed. Requeue the job for someone else.
							s.redisRepo.RecordWorkerError(context.Background(), workerID)
							d.Nack(false, true)
						}
						break waitResultLoop
					}
				}
			}
		}
	}
}
