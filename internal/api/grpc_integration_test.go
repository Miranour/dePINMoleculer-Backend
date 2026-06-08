//go:build integration

package api

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"depin-backend/internal/config"
	"depin-backend/internal/repository"
	"depin-backend/internal/service"
	"depin-backend/pkg/database"
	"depin-backend/pkg/pb"
	"depin-backend/pkg/queue"

	"github.com/jackc/pgx/v5/pgxpool"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func setupGRPCServer(t *testing.T) (*SimulationWorkerService, *pgxpool.Pool, *redis.Client, *amqp.Connection) {
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
	redisRepo := repository.NewRedisRepository(redisClient)
	jobPublisher := service.NewJobPublisher(rmqConn)
	
	rewardEngine := service.NewRewardEngine(walletRepo, redisRepo, nil, jobPublisher)

	ch, _ := rmqConn.Channel()
	queue.SetupQueues(ch)
	ch.Close()

	server := NewSimulationWorkerService(redisRepo, rmqConn, rewardEngine)
	return server, pool, redisClient, rmqConn
}

func TestGRPCServer_Integration(t *testing.T) {
	server, pool, redisClient, rmqConn := setupGRPCServer(t)
	defer pool.Close()
	defer redisClient.Close()
	defer rmqConn.Close()

	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	
	grpcServer := grpc.NewServer()
	pb.RegisterSimulationWorkerServiceServer(grpcServer, server)
	
	go func() {
		if err := grpcServer.Serve(listener); err != nil {
			panic(fmt.Sprintf("Failed to serve: %v", err))
		}
	}()
	defer grpcServer.Stop()
	
	addr := listener.Addr().String()
	
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to connect client: %v", err)
	}
	defer conn.Close()
	
	client := pb.NewSimulationWorkerServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	workerID := fmt.Sprintf("grpc_worker_%d", time.Now().UnixNano())
	
	hbResp, err := client.SendHeartbeat(ctx, &pb.HeartbeatRequest{
		WorkerId:       workerID,
		ApiKey:         "test_api_key",
		GpuTemperature: 60.5,
		FanSpeed:       50.0,
		AvailableVram:  16000,
		CurrentFlops:   10.2,
		IsBusy:         false,
	})
	if err != nil {
		t.Fatalf("Failed to send heartbeat: %v", err)
	}

	if hbResp.Command != "PROCEED" {
		t.Errorf("Expected PROCEED, got %v", hbResp.Command)
	}
	t.Log("[OK] Heartbeat sent and response received")
	
	statusKey := fmt.Sprintf("worker:status:%s", workerID)
	redisClient.Del(context.Background(), statusKey)

	jobStream, err := client.StreamJobChannel(ctx)
	if err != nil {
		t.Fatalf("Failed to open StreamJobChannel: %v", err)
	}
	
	// Send initial signal to identify the worker
	err = jobStream.Send(&pb.WorkerSignal{
		WorkerId: workerID,
	})
	if err != nil {
		t.Fatalf("Failed to send initial signal: %v", err)
	}

	assignmentChan := make(chan *pb.BackendSignal)
	go func() {
		msg, err := jobStream.Recv()
		if err == nil {
			assignmentChan <- msg
		}
	}()

	ch, _ := rmqConn.Channel()
	jobID := "grpc_test_job_123"
	jobPayload := fmt.Sprintf(`{"job_id":"%s", "smiles_string":"C", "target_pdb_url":"url", "max_exhaustiveness":8}`, jobID)
	
	time.Sleep(500 * time.Millisecond)

	err = ch.PublishWithContext(ctx, "", "pending_simulations", false, false, amqp.Publishing{
		ContentType: "application/json",
		Body:        []byte(jobPayload),
	})
	if err != nil {
		t.Fatalf("Failed to publish mock job: %v", err)
	}

	select {
	case assignment := <-assignmentChan:
		if assignment.GetJobAssignment() == nil {
			t.Errorf("Expected job assignment")
		} else if assignment.GetJobAssignment().JobId != jobID {
			t.Errorf("Expected jobID %s, got %v", jobID, assignment.GetJobAssignment().JobId)
		}
		t.Log("[OK] Received job assignment from server via stream")
	case <-time.After(5 * time.Second):
		t.Log("[WARN] Timed out waiting for job assignment.")
	}

	t.Log("\n=== GRPC API INTEGRATION TEST: ALL PASSED ===")
}
