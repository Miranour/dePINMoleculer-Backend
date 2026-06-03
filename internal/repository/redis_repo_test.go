package repository

import (
	"context"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestAcquireJobLock_DoubleRequest(t *testing.T) {
	// Require a running redis server on localhost:6379
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	
	ctx := context.Background()
	// Check if Redis is reachable, if not, skip test
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("Redis not running on localhost:6379, skipping test: %v", err)
	}
	defer client.Close()

	repo := NewRedisRepository(client)
	jobID := "test_job_12345"

	// Ensure clean state
	client.Del(ctx, "lock:job:"+jobID)

	// First request should acquire the lock successfully
	acquired1, err := repo.AcquireJobLock(ctx, jobID)
	if err != nil {
		t.Fatalf("Failed to acquire lock on first try: %v", err)
	}
	if !acquired1 {
		t.Errorf("Expected to acquire lock on first try, got false")
	}

	// Second request immediately after should FAIL to acquire the lock (double request)
	acquired2, err := repo.AcquireJobLock(ctx, jobID)
	if err != nil {
		t.Fatalf("Failed to execute acquire lock on second try: %v", err)
	}
	if acquired2 {
		t.Errorf("Expected to fail acquiring lock on second try (double request prevented), got true")
	}

	// Wait for the lock to expire (we know it's 10s by default, but for test speed we could override or just verify it's working)
	// Since 10s is too long for a unit test, we will manually delete it and check again.
	client.Del(ctx, "lock:job:"+jobID)
	
	// Third request should succeed after manual deletion (simulating expiration/unlock)
	acquired3, err := repo.AcquireJobLock(ctx, jobID)
	if err != nil {
		t.Fatalf("Failed to execute acquire lock on third try: %v", err)
	}
	if !acquired3 {
		t.Errorf("Expected to acquire lock after deletion, got false")
	}
	
	// Cleanup
	client.Del(ctx, "lock:job:"+jobID)
}
