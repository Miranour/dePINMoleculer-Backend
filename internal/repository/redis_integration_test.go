//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func getTestRedisClient(t *testing.T) *redis.Client {
	client := redis.NewClient(&redis.Options{
		Addr:     envOrDefault("REDIS_HOST", "localhost") + ":" + envOrDefault("REDIS_PORT", "6379"),
		Password: envOrDefault("REDIS_PASSWORD", ""),
		DB:       0,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("Redis not available: %v", err)
	}
	return client
}

func TestRedisIntegration_Heartbeat_TTL(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	repo := NewRedisRepository(client)
	ctx := context.Background()
	workerID := fmt.Sprintf("test_worker_%d", time.Now().UnixNano())

	status := map[string]interface{}{
		"gpu_temperature": 65.5,
		"fan_speed":       70.0,
		"available_vram":  uint64(8000000000),
		"current_flops":   12.5,
		"is_busy":         false,
	}

	err := repo.UpdateHeartbeat(ctx, workerID, status)
	if err != nil {
		t.Fatalf("UpdateHeartbeat failed: %v", err)
	}

	// Verify data was written
	key := fmt.Sprintf("worker:status:%s", workerID)
	vals, err := client.HGetAll(ctx, key).Result()
	if err != nil {
		t.Fatalf("HGetAll failed: %v", err)
	}
	if len(vals) == 0 {
		t.Fatal("Expected heartbeat data in Redis, got empty hash")
	}

	temp := vals["gpu_temperature"]
	if temp != "65.5" {
		t.Errorf("Expected gpu_temperature 65.5, got %s", temp)
	}
	t.Logf("[OK] Heartbeat written: %v", vals)

	// Verify TTL is set (should be ~30s)
	ttl, err := client.TTL(ctx, key).Result()
	if err != nil {
		t.Fatalf("TTL query failed: %v", err)
	}
	if ttl <= 0 || ttl > 31*time.Second {
		t.Errorf("Expected TTL ~30s, got %v", ttl)
	}
	t.Logf("[OK] Heartbeat TTL: %v", ttl)

	// Cleanup
	client.Del(ctx, key)
}

func TestRedisIntegration_DistributedLock(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	repo := NewRedisRepository(client)
	ctx := context.Background()
	jobID := fmt.Sprintf("test_lock_%d", time.Now().UnixNano())

	// First acquire should succeed
	acquired, err := repo.AcquireJobLock(ctx, jobID)
	if err != nil {
		t.Fatalf("AcquireJobLock failed: %v", err)
	}
	if !acquired {
		t.Error("Expected to acquire lock on first try")
	}
	t.Log("[OK] Lock acquired on first attempt")

	// Second acquire should fail (double request prevention)
	acquired2, err := repo.AcquireJobLock(ctx, jobID)
	if err != nil {
		t.Fatalf("AcquireJobLock second try failed: %v", err)
	}
	if acquired2 {
		t.Error("Expected lock to be rejected on second try (double request)")
	}
	t.Log("[OK] Lock correctly prevents double request")

	// Cleanup
	client.Del(ctx, fmt.Sprintf("lock:job:%s", jobID))
}

func TestRedisIntegration_WorkerErrors_Blacklist(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	repo := NewRedisRepository(client)
	ctx := context.Background()
	workerID := fmt.Sprintf("test_err_worker_%d", time.Now().UnixNano())

	// Record 3 errors → should trigger blacklist
	for i := 0; i < 3; i++ {
		count, err := repo.RecordWorkerError(ctx, workerID)
		if err != nil {
			t.Fatalf("RecordWorkerError failed at iteration %d: %v", i, err)
		}
		t.Logf("[OK] Error count: %d", count)

		if count == 3 {
			// Manually blacklist (in production gRPC server does this)
			err := repo.SetBlacklist(ctx, workerID)
			if err != nil {
				t.Fatalf("SetBlacklist failed: %v", err)
			}
		}
	}

	// Check blacklist
	banned, err := repo.IsBlacklisted(ctx, workerID)
	if err != nil {
		t.Fatalf("IsBlacklisted failed: %v", err)
	}
	if !banned {
		t.Error("Expected worker to be blacklisted after 3 errors")
	}
	t.Log("[OK] Worker blacklisted after 3 errors")

	// Verify TTL on blacklist key (~10 minutes)
	blKey := fmt.Sprintf("worker:blacklist:%s", workerID)
	ttl, _ := client.TTL(ctx, blKey).Result()
	if ttl <= 0 || ttl > 11*time.Minute {
		t.Errorf("Expected blacklist TTL ~10min, got %v", ttl)
	}
	t.Logf("[OK] Blacklist TTL: %v", ttl)

	// Cleanup
	client.Del(ctx, fmt.Sprintf("worker:errors:%s", workerID))
	client.Del(ctx, blKey)
}

func TestRedisIntegration_SpotCheck_ResultStorage(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	repo := NewRedisRepository(client)
	ctx := context.Background()
	jobID := fmt.Sprintf("test_spot_%d", time.Now().UnixNano())

	// Initially no result
	result, err := repo.GetSpotCheckResult(ctx, jobID)
	if err != nil {
		t.Fatalf("GetSpotCheckResult failed: %v", err)
	}
	if result != "" {
		t.Errorf("Expected empty result, got: %s", result)
	}
	t.Log("[OK] No initial spot-check result")

	// Save first worker result
	testData := `{"worker_id":"w1","binding_energy":-8.5,"pdbqt_data":"QVRNTSB4IDEuMDAw","compute_time_ms":15000,"gpu_model":"RTX 3090"}`
	err = repo.SaveSpotCheckResult(ctx, jobID, testData)
	if err != nil {
		t.Fatalf("SaveSpotCheckResult failed: %v", err)
	}

	// Retrieve it
	stored, err := repo.GetSpotCheckResult(ctx, jobID)
	if err != nil {
		t.Fatalf("GetSpotCheckResult (after save) failed: %v", err)
	}
	if stored != testData {
		t.Errorf("Stored data mismatch.\nExpected: %s\nGot: %s", testData, stored)
	}
	t.Log("[OK] Spot-check result stored and retrieved")

	// Delete it
	err = repo.DeleteSpotCheckResult(ctx, jobID)
	if err != nil {
		t.Fatalf("DeleteSpotCheckResult failed: %v", err)
	}

	// Verify deletion
	afterDel, _ := repo.GetSpotCheckResult(ctx, jobID)
	if afterDel != "" {
		t.Error("Expected empty after deletion")
	}
	t.Log("[OK] Spot-check result deleted")
}

func TestRedisIntegration_ActiveJob_Tracking(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	repo := NewRedisRepository(client)
	ctx := context.Background()
	workerID := fmt.Sprintf("test_active_%d", time.Now().UnixNano())
	jobID := "job_abc123"

	// Set active job
	err := repo.SetWorkerActiveJob(ctx, workerID, jobID)
	if err != nil {
		t.Fatalf("SetWorkerActiveJob failed: %v", err)
	}

	// Get active job
	got, err := repo.GetWorkerActiveJob(ctx, workerID)
	if err != nil {
		t.Fatalf("GetWorkerActiveJob failed: %v", err)
	}
	if got != jobID {
		t.Errorf("Expected job %s, got %s", jobID, got)
	}
	t.Log("[OK] Active job set and retrieved")

	// Clear active job
	err = repo.ClearWorkerActiveJob(ctx, workerID)
	if err != nil {
		t.Fatalf("ClearWorkerActiveJob failed: %v", err)
	}

	_, err = repo.GetWorkerActiveJob(ctx, workerID)
	if err != redis.Nil {
		t.Errorf("Expected redis.Nil after clearing active job, got: %v", err)
	}
	t.Log("[OK] Active job cleared")
}
