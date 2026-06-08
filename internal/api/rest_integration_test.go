//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
	"fmt"

	"depin-backend/internal/config"
	"depin-backend/internal/repository"
	"depin-backend/internal/service"
	"depin-backend/pkg/database"
	"depin-backend/pkg/queue"

	"github.com/gin-gonic/gin"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/redis/go-redis/v9"
	"github.com/jackc/pgx/v5/pgxpool"
)

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func setupRESTServer(t *testing.T) (*RESTServer, *pgxpool.Pool, *redis.Client, *amqp.Connection) {
	if _, err := os.Stat("migrations"); os.IsNotExist(err) {
		if err := os.Chdir("../.."); err != nil {
			t.Fatalf("Failed to chdir: %v", err)
		}
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

	pool, err := database.NewPostgresPool(cfg)
	if err != nil {
		t.Fatalf("PostgreSQL connection failed: %v", err)
	}

	if err := database.RunMigrations(cfg); err != nil {
		t.Fatalf("Failed to run migrations: %v", err)
	}

	redisClient, err := database.NewRedisClient(cfg)
	if err != nil {
		t.Fatalf("Redis connection failed: %v", err)
	}

	rmqConn, err := queue.NewRabbitMQConnection(cfg)
	if err != nil {
		t.Fatalf("RabbitMQ connection failed: %v", err)
	}

	walletRepo := repository.NewWalletRepository(pool)
	jobPublisher := service.NewJobPublisher(rmqConn)
	server := NewRESTServer(walletRepo, jobPublisher, redisClient)
	
	// Create queues
	ch, _ := rmqConn.Channel()
	queue.SetupQueues(ch)
	ch.Close()

	return server, pool, redisClient, rmqConn
}

func TestRESTServer_FullFlow(t *testing.T) {
	// Setup dependencies
	server, pool, redisClient, rmqConn := setupRESTServer(t)
	defer pool.Close()
	defer redisClient.Close()
	defer rmqConn.Close()

	gin.SetMode(gin.TestMode)
	router := server.router
	
	ctx := context.Background()

	// 1. Worker Registration
	regReq := `{"gpu_model": "RTX 4090", "total_vram": 24000000000}`
	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest("POST", "/api/v1/workers/register", bytes.NewBufferString(regReq))
	req1.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w1, req1)

	if w1.Code != http.StatusCreated {
		t.Fatalf("Expected 201 Created for register, got %d: %s", w1.Code, w1.Body.String())
	}
	
	var regRes struct {
		WorkerID string `json:"worker_id"`
		APIKey   string `json:"api_key"`
	}
	json.Unmarshal(w1.Body.Bytes(), &regRes)
	
	if regRes.WorkerID == "" || regRes.APIKey == "" {
		t.Fatalf("Failed to parse register response: %s", w1.Body.String())
	}
	t.Logf("[OK] Worker registered: %s", regRes.WorkerID)

	// Manually insert worker wallet because MVP registerWorker doesn't hit DB yet
	pool.Exec(ctx, `INSERT INTO worker_wallets (worker_id, pending_balance, confirmed_balance, total_earned, total_withdrawn) VALUES ($1, 0, 0, 0, 0)`, regRes.WorkerID)

	// Seed test data for jobs (User + Balance)
	userID := fmt.Sprintf("rest_user_%d", time.Now().UnixNano())
	pool.Exec(ctx, `INSERT INTO user_wallets (user_id, active_balance, blocked_balance) VALUES ($1, 1000.00, 0.00)`, userID)

	// 2. Submit a Job
	jobReq := fmt.Sprintf(`{"smiles_string": "CCO", "target_pdb_url": "1CRN", "max_exhaustiveness": 8, "cost": 50.0, "user_id": "%s"}`, userID)
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest("POST", "/api/v1/jobs", bytes.NewBufferString(jobReq))
	req2.Header.Set("Content-Type", "application/json")
	researcherToken, _ := GenerateToken(userID, "researcher")
	req2.Header.Set("Authorization", "Bearer "+researcherToken)
	router.ServeHTTP(w2, req2)

	if w2.Code != http.StatusCreated {
		t.Fatalf("Expected 201 Created for job submit, got %d: %s", w2.Code, w2.Body.String())
	}
	
	var jobRes struct {
		JobID string `json:"job_id"`
	}
	json.Unmarshal(w2.Body.Bytes(), &jobRes)
	if jobRes.JobID == "" {
		t.Fatalf("Failed to parse job response: %s", w2.Body.String())
	}
	t.Logf("[OK] Job submitted: %s", jobRes.JobID)

	// 3. Verify Job in RabbitMQ
	ch, _ := rmqConn.Channel()
	msgs, err := ch.Consume("pending_simulations", "", true, false, false, false, nil)
	if err != nil {
		t.Fatalf("Failed to consume from rabbitmq: %v", err)
	}
	
	select {
	case msg := <-msgs:
		t.Logf("[OK] Job successfully hit RabbitMQ. Body: %s", string(msg.Body))
	case <-time.After(2 * time.Second):
		t.Fatal("Job did not reach RabbitMQ")
	}

	// 4. Wallet Info
	w3 := httptest.NewRecorder()
	req3, _ := http.NewRequest("GET", fmt.Sprintf("/api/v1/workers/%s/wallet", regRes.WorkerID), nil)
	workerToken, _ := GenerateToken(regRes.WorkerID, "worker")
	req3.Header.Set("Authorization", "Bearer "+workerToken)
	router.ServeHTTP(w3, req3)

	if w3.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK for wallet, got %d", w3.Code)
	}
	if !strings.Contains(w3.Body.String(), `"confirmed_balance"`) {
		t.Fatalf("Unexpected wallet response: %s", w3.Body.String())
	}
	t.Log("[OK] Wallet endpoint works")
	
	// 5. Create Withdrawal
	pool.Exec(ctx, "UPDATE worker_wallets SET confirmed_balance = 100 WHERE worker_id = $1", regRes.WorkerID)
	wdReq := `{"amount": 50, "currency": "USDT", "payout_method": "CRYPTO", "destination": "0x123456"}`
	w4 := httptest.NewRecorder()
	req4, _ := http.NewRequest("POST", fmt.Sprintf("/api/v1/workers/%s/withdraw", regRes.WorkerID), bytes.NewBufferString(wdReq))
	req4.Header.Set("Content-Type", "application/json")
	req4.Header.Set("Authorization", "Bearer "+workerToken)
	router.ServeHTTP(w4, req4)

	if w4.Code != http.StatusCreated {
		t.Fatalf("Expected 201 Created for withdrawal, got %d: %s", w4.Code, w4.Body.String())
	}
	t.Log("[OK] Withdrawal request created via REST")

	// 6. Admin Get Withdrawals
	w5 := httptest.NewRecorder()
	req5, _ := http.NewRequest("GET", "/api/v1/admin/withdrawals", nil)
	adminToken, _ := GenerateToken("admin_123", "admin")
	req5.Header.Set("Authorization", "Bearer "+adminToken)
	router.ServeHTTP(w5, req5)

	if w5.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK for admin withdrawals, got %d", w5.Code)
	}
	t.Log("[OK] Admin withdrawals list fetched")

	// Cleanup
	pool.Exec(ctx, "DELETE FROM withdrawal_requests WHERE worker_id = $1", regRes.WorkerID)
	pool.Exec(ctx, "DELETE FROM worker_wallets WHERE worker_id = $1", regRes.WorkerID)
	pool.Exec(ctx, "DELETE FROM workers WHERE id = $1", regRes.WorkerID)
	pool.Exec(ctx, "DELETE FROM ledger_entries WHERE job_id = $1", jobRes.JobID)
	pool.Exec(ctx, "DELETE FROM user_wallets WHERE user_id = $1", userID)
	t.Log("\n=== REST API INTEGRATION TEST: ALL PASSED ===")
}
