//go:build integration

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"depin-backend/internal/config"
	"depin-backend/internal/repository"
	"depin-backend/pkg/database"
	"depin-backend/pkg/pb"
	"depin-backend/pkg/queue"
)

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func TestJobPublisher_RealRabbitMQ(t *testing.T) {
	cfg := &config.Config{
		RabbitMQURL: envOrDefault("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"),
	}

	conn, err := queue.NewRabbitMQConnection(cfg)
	if err != nil {
		t.Fatalf("Failed to connect to RabbitMQ: %v", err)
	}
	defer conn.Close()
	t.Log("[OK] RabbitMQ connection established")

	// Setup queues
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("Failed to open channel: %v", err)
	}
	if err := queue.SetupQueues(ch); err != nil {
		t.Fatalf("Failed to setup queues: %v", err)
	}
	// Purge queue for clean test
	ch.QueuePurge("pending_simulations", false)
	ch.Close()

	// Publish a job
	publisher := NewJobPublisher(conn)
	jobID := fmt.Sprintf("test_job_%d", time.Now().UnixNano())
	smiles := "CC(=O)OC1=CC=CC=C1C(=O)O"
	pdbURL := "https://files.rcsb.org/download/1CRN.pdb"

	err = publisher.PublishJob(context.Background(), jobID, smiles, pdbURL, 8)
	if err != nil {
		t.Fatalf("PublishJob failed: %v", err)
	}
	t.Logf("[OK] Job %s published to RabbitMQ", jobID)

	// Consume the job to verify it arrived
	ch2, err := conn.Channel()
	if err != nil {
		t.Fatalf("Failed to open consumer channel: %v", err)
	}
	defer ch2.Close()

	msgs, err := ch2.Consume("pending_simulations", "", false, false, false, false, nil)
	if err != nil {
		t.Fatalf("Failed to consume: %v", err)
	}

	select {
	case msg := <-msgs:
		var assignment pb.JobAssignment
		if err := json.Unmarshal(msg.Body, &assignment); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		if assignment.JobId != jobID {
			t.Errorf("Expected job_id %s, got %s", jobID, assignment.JobId)
		}
		if assignment.SmilesString != smiles {
			t.Errorf("Expected smiles %s, got %s", smiles, assignment.SmilesString)
		}
		if assignment.TargetPdbUrl != pdbURL {
			t.Errorf("Expected pdb_url %s, got %s", pdbURL, assignment.TargetPdbUrl)
		}
		if assignment.MaxExhaustiveness != 8 {
			t.Errorf("Expected exhaustiveness 8, got %d", assignment.MaxExhaustiveness)
		}
		msg.Ack(false)
		t.Log("[OK] Job correctly consumed from RabbitMQ queue")
		t.Logf("  job_id=%s, smiles=%s, pdb=%s, exhaustiveness=%d",
			assignment.JobId, assignment.SmilesString, assignment.TargetPdbUrl, assignment.MaxExhaustiveness)

	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for message from RabbitMQ")
	}
}

func TestRewardEngine_RealDependencies(t *testing.T) {
	// Change working directory to project root for migrations
	if err := os.Chdir("../.."); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}

	cfg := &config.Config{
		DBHost:      envOrDefault("DB_HOST", "localhost"),
		DBPort:      envOrDefault("DB_PORT", "5432"),
		DBUser:      envOrDefault("DB_USER", "postgres"),
		DBPassword:  envOrDefault("DB_PASSWORD", "postgrespassword"),
		DBName:      envOrDefault("DB_NAME", "depin_db"),
		DBSSLMode:   envOrDefault("DB_SSLMODE", "disable"),
		RedisHost:   envOrDefault("REDIS_HOST", "localhost"),
		RedisPort:   envOrDefault("REDIS_PORT", "6379"),
		RabbitMQURL: envOrDefault("RABBITMQ_URL", "amqp://guest:guest@localhost:5672/"),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Setup real dependencies
	pool, err := database.NewPostgresPool(cfg)
	if err != nil {
		t.Fatalf("PostgreSQL connection failed: %v", err)
	}
	defer pool.Close()

	if err := database.RunMigrations(cfg); err != nil {
		t.Fatalf("Migrations failed: %v", err)
	}

	redisClient, err := database.NewRedisClient(cfg)
	if err != nil {
		t.Fatalf("Redis connection failed: %v", err)
	}
	defer redisClient.Close()

	rmqConn, err := queue.NewRabbitMQConnection(cfg)
	if err != nil {
		t.Fatalf("RabbitMQ connection failed: %v", err)
	}
	defer rmqConn.Close()

	t.Log("[OK] All 3 infrastructure dependencies connected")

	walletRepo := repository.NewWalletRepository(pool)
	redisRepo := repository.NewRedisRepository(redisClient)
	jobPublisher := NewJobPublisher(rmqConn)

	// Create engine WITHOUT validator (for non-spot-check tests)
	engine := NewRewardEngine(walletRepo, redisRepo, nil, jobPublisher)

	// Seed test data
	workerID := fmt.Sprintf("reward_test_worker_%d", time.Now().UnixNano())
	userID := fmt.Sprintf("reward_test_user_%d", time.Now().UnixNano())
	jobID := fmt.Sprintf("reward_test_job_%d", time.Now().UnixNano())

	pool.Exec(ctx, `INSERT INTO user_wallets (user_id, active_balance, blocked_balance) VALUES ($1, 500.00, 50.00)`, userID)
	pool.Exec(ctx, `INSERT INTO worker_wallets (worker_id, pending_balance, confirmed_balance, total_earned, total_withdrawn) VALUES ($1, 0, 0, 0, 0)`, workerID)

	// Process a normal (non-spot-check) job result
	err = engine.ProcessJobResult(ctx, workerID, jobID, userID, 15000, "RTX 3090", 8, false, -8.5, []byte("ATOM test data"))
	if err != nil {
		t.Fatalf("ProcessJobResult failed: %v", err)
	}
	t.Log("[OK] ProcessJobResult succeeded")

	// Verify worker wallet was updated
	wallet, err := walletRepo.GetWorkerWallet(ctx, workerID)
	if err != nil {
		t.Fatalf("GetWorkerWallet failed: %v", err)
	}
	if wallet.ConfirmedBalance <= 0 {
		t.Errorf("Expected positive confirmed balance, got %f", wallet.ConfirmedBalance)
	}
	if wallet.TotalEarned <= 0 {
		t.Errorf("Expected positive total earned, got %f", wallet.TotalEarned)
	}
	t.Logf("[OK] Worker wallet: confirmed=%f, total_earned=%f", wallet.ConfirmedBalance, wallet.TotalEarned)

	// Verify earnings record
	earnings, err := walletRepo.GetWorkerEarnings(ctx, workerID)
	if err != nil {
		t.Fatalf("GetWorkerEarnings failed: %v", err)
	}
	if len(earnings) != 1 {
		t.Errorf("Expected 1 earning record, got %d", len(earnings))
	}
	if earnings[0].Status != "CONFIRMED" {
		t.Errorf("Expected CONFIRMED status, got %s", earnings[0].Status)
	}
	t.Logf("[OK] Earning record: job=%s, status=%s, net=%f", earnings[0].JobID, earnings[0].Status, earnings[0].NetAmount)

	// Verify ledger
	var ledgerCount int
	pool.QueryRow(ctx, "SELECT COUNT(*) FROM ledger_entries WHERE job_id = $1", jobID).Scan(&ledgerCount)
	if ledgerCount < 2 {
		t.Errorf("Expected >=2 ledger entries, got %d", ledgerCount)
	}
	t.Logf("[OK] Ledger: %d entries for job %s", ledgerCount, jobID)

	// Cleanup
	pool.Exec(ctx, "DELETE FROM withdrawal_requests WHERE worker_id = $1", workerID)
	pool.Exec(ctx, "DELETE FROM worker_earnings WHERE worker_id = $1", workerID)
	pool.Exec(ctx, "DELETE FROM ledger_entries WHERE job_id = $1", jobID)
	pool.Exec(ctx, "DELETE FROM worker_wallets WHERE worker_id = $1", workerID)
	pool.Exec(ctx, "DELETE FROM user_wallets WHERE user_id = $1", userID)

	t.Log("\n=== REWARD ENGINE INTEGRATION TEST: ALL PASSED ===")
}
