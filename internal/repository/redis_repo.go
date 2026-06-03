package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisRepository struct {
	client *redis.Client
}

func NewRedisRepository(client *redis.Client) *RedisRepository {
	return &RedisRepository{client: client}
}

// UpdateHeartbeat sets the worker status hash and updates its TTL to 30s
func (r *RedisRepository) UpdateHeartbeat(ctx context.Context, workerID string, status map[string]interface{}) error {
	key := fmt.Sprintf("worker:status:%s", workerID)
	
	pipe := r.client.TxPipeline()
	pipe.HSet(ctx, key, status)
	pipe.Expire(ctx, key, 30*time.Second)
	
	_, err := pipe.Exec(ctx)
	return err
}

// SetWorkerActiveJob saves the job ID this worker is currently processing.
// This is kept WITHOUT a TTL so the Watchdog can read it when the worker's status expires.
func (r *RedisRepository) SetWorkerActiveJob(ctx context.Context, workerID, jobID string) error {
	key := fmt.Sprintf("worker:active_job:%s", workerID)
	return r.client.Set(ctx, key, jobID, 0).Err()
}

// ClearWorkerActiveJob removes the active job from the worker once it completes or aborts.
func (r *RedisRepository) ClearWorkerActiveJob(ctx context.Context, workerID string) error {
	key := fmt.Sprintf("worker:active_job:%s", workerID)
	return r.client.Del(ctx, key).Err()
}

// GetWorkerActiveJob gets the job the worker was working on. Used by Watchdog.
func (r *RedisRepository) GetWorkerActiveJob(ctx context.Context, workerID string) (string, error) {
	key := fmt.Sprintf("worker:active_job:%s", workerID)
	return r.client.Get(ctx, key).Result()
}

// AcquireJobLock creates a distributed lock for a job to ensure idempotency.
func (r *RedisRepository) AcquireJobLock(ctx context.Context, jobID string) (bool, error) {
	key := fmt.Sprintf("lock:job:%s", jobID)
	// Lock for 10 seconds. Enough to process DB transaction.
	return r.client.SetNX(ctx, key, "1", 10*time.Second).Result()
}

// RecordWorkerError increments the error counter. Returns new count.
func (r *RedisRepository) RecordWorkerError(ctx context.Context, workerID string) (int64, error) {
	key := fmt.Sprintf("worker:errors:%s", workerID)
	count, err := r.client.Incr(ctx, key).Result()
	if err != nil {
		return 0, err
	}
	// Set/Reset TTL to 1 hour on every error
	r.client.Expire(ctx, key, time.Hour)
	return count, nil
}

// SetBlacklist bans a worker for 10 minutes.
func (r *RedisRepository) SetBlacklist(ctx context.Context, workerID string) error {
	key := fmt.Sprintf("worker:blacklist:%s", workerID)
	return r.client.Set(ctx, key, "1", 10*time.Minute).Err()
}

// IsBlacklisted checks if the worker is currently banned.
func (r *RedisRepository) IsBlacklisted(ctx context.Context, workerID string) (bool, error) {
	key := fmt.Sprintf("worker:blacklist:%s", workerID)
	res, err := r.client.Exists(ctx, key).Result()
	if err != nil {
		return false, err
	}
	return res > 0, nil
}

// SpotCheckResult represents the intermediate result of a spot-check job.
type SpotCheckResult struct {
	WorkerID      string  `json:"worker_id"`
	BindingEnergy float64 `json:"binding_energy"`
	PDBQTData     []byte  `json:"pdbqt_data"`
	ComputeTimeMs int64   `json:"compute_time_ms"`
	GPUModel      string  `json:"gpu_model"`
}

// SaveSpotCheckResult saves the first worker's result for a spot-check job.
func (r *RedisRepository) SaveSpotCheckResult(ctx context.Context, jobID string, data string) error {
	key := fmt.Sprintf("spot_check:result:%s", jobID)
	// Keep for 1 hour to wait for the second worker
	return r.client.Set(ctx, key, data, time.Hour).Err()
}

// GetSpotCheckResult retrieves the first worker's result. Returns empty string if not found.
func (r *RedisRepository) GetSpotCheckResult(ctx context.Context, jobID string) (string, error) {
	key := fmt.Sprintf("spot_check:result:%s", jobID)
	res, err := r.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil // Not found
	}
	return res, err
}

// DeleteSpotCheckResult removes the temporary spot-check result.
func (r *RedisRepository) DeleteSpotCheckResult(ctx context.Context, jobID string) error {
	key := fmt.Sprintf("spot_check:result:%s", jobID)
	return r.client.Del(ctx, key).Err()
}
