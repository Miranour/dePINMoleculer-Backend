package service

import (
	"context"
	"log"
	"strings"

	"github.com/redis/go-redis/v9"
	"depin-backend/internal/repository"
)

type WatchdogService struct {
	redisClient *redis.Client
	redisRepo   *repository.RedisRepository
	requeueCh   chan<- string // Channel to send job IDs back to the RabbitMQ publisher
}

func NewWatchdogService(client *redis.Client, repo *repository.RedisRepository, requeueCh chan<- string) *WatchdogService {
	return &WatchdogService{
		redisClient: client,
		redisRepo:   repo,
		requeueCh:   requeueCh,
	}
}

// Start begins listening for expired keyspace events from Redis
func (s *WatchdogService) Start(ctx context.Context) {
	// Ensure we're subscribed to expired events on database 0
	pubsub := s.redisClient.Subscribe(ctx, "__keyevent@0__:expired")
	defer pubsub.Close()

	log.Println("[Watchdog] Started listening for worker timeouts...")

	ch := pubsub.Channel()

	for {
		select {
		case <-ctx.Done():
			log.Println("[Watchdog] Shutting down...")
			return
		case msg := <-ch:
			key := msg.Payload
			
			// We only care about worker:status expiration
			if strings.HasPrefix(key, "worker:status:") {
				workerID := strings.TrimPrefix(key, "worker:status:")
				log.Printf("[Watchdog] Worker %s heartbeat expired. Checking for active jobs...", workerID)
				
				s.handleExpiredWorker(ctx, workerID)
			}
		}
	}
}

func (s *WatchdogService) handleExpiredWorker(ctx context.Context, workerID string) {
	// Fetch the job the worker was working on (stored without TTL)
	jobID, err := s.redisRepo.GetWorkerActiveJob(ctx, workerID)
	if err == redis.Nil {
		// No active job for this worker, nothing to recover
		return
	} else if err != nil {
		log.Printf("[Watchdog] Error getting active job for worker %s: %v", workerID, err)
		return
	}

	log.Printf("[Watchdog] Worker %s dropped while processing Job %s! Re-queueing job...", workerID, jobID)
	
	// Send to re-queue channel so the orchestrator can push it back to RabbitMQ with High Priority
	select {
	case s.requeueCh <- jobID:
		// Clear it from the active job list so it's not processed twice
		_ = s.redisRepo.ClearWorkerActiveJob(ctx, workerID)
	default:
		// In a production system, if the channel is full, we should probably 
		// fallback to a DB loop, or block. For now, log the issue.
		log.Printf("[Watchdog] [CRITICAL] Re-queue channel is full. Failed to requeue job %s", jobID)
	}
}
