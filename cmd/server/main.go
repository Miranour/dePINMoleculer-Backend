package main

import (
	"context"
	"log"
	"net"

	"depin-backend/internal/api"
	"depin-backend/internal/config"
	"depin-backend/internal/repository"
	"depin-backend/internal/service"
	"depin-backend/pkg/database"
	"depin-backend/pkg/pb"
	"depin-backend/pkg/queue"
	
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	log.Println("Starting DePIN Molecular Backend infrastructure verification...")

	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	log.Printf("Configuration loaded successfully. Environment: %s", cfg.Env)

	// Check dependencies connections. Note: These might fail if docker containers 
	// are not started yet, but this file is prepared for checking them.
	log.Println("Checking PostgreSQL connection...")
	dbPool, dbErr := database.NewPostgresPool(cfg)
	if dbErr != nil {
		log.Printf("[WARNING] PostgreSQL check failed: %v", dbErr)
	} else {
		log.Println("[OK] PostgreSQL pool initialized and pinged.")
		defer dbPool.Close()
		
		if err := database.RunMigrations(cfg); err != nil {
			log.Fatalf("Failed to run migrations: %v", err)
		}
	}

	log.Println("Checking Redis connection...")
	rdbClient, redisErr := database.NewRedisClient(cfg)
	if redisErr != nil {
		log.Printf("[WARNING] Redis check failed: %v", redisErr)
	} else {
		log.Println("[OK] Redis client connected and pinged.")
		defer rdbClient.Close()
	}

	log.Println("Checking RabbitMQ connection...")
	rmqConn, rmqErr := queue.NewRabbitMQConnection(cfg)
	if rmqErr != nil {
		log.Printf("[WARNING] RabbitMQ check failed: %v", rmqErr)
	} else {
		log.Println("[OK] RabbitMQ connection established.")
		defer rmqConn.Close()

		// Set up RabbitMQ queues
		ch, err := rmqConn.Channel()
		if err == nil {
			if err := queue.SetupQueues(ch); err != nil {
				log.Fatalf("Failed to setup queues: %v", err)
			}
			ch.Close()
		}
	}

	// Initialize repositories
	userRepo := repository.NewUserRepository(dbPool)
	redisRepo := repository.NewRedisRepository(rdbClient)
	walletRepo := repository.NewWalletRepository(dbPool)
	jobRepo := repository.NewJobRepository(dbPool)
	
	// Initialize Services
	authService := service.NewAuthService(userRepo, walletRepo)

	// Connect to Validator Service (Rust)
	validatorConn, err := grpc.NewClient("localhost:50052", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("[WARNING] Failed to connect to Validator Service: %v", err)
	} else {
		log.Println("[OK] Validator Service connected.")
		defer validatorConn.Close()
	}

	// Initialize JobPublisher and RewardEngine
	jobPublisher := service.NewJobPublisher(rmqConn)
	rewardEngine := service.NewRewardEngine(walletRepo, redisRepo, validatorConn, jobPublisher)

	// Start Payout Processor
	payoutProcessor := service.NewPayoutProcessor(walletRepo)
	payoutProcessor.Start(context.Background())

	// Start gRPC Server
	grpcServer := grpc.NewServer()
	workerSvc := api.NewSimulationWorkerService(redisRepo, rmqConn, rewardEngine)
	pb.RegisterSimulationWorkerServiceServer(grpcServer, workerSvc)

	go func() {
		lis, err := net.Listen("tcp", ":50051") // Default gRPC port for backend
		if err != nil {
			log.Fatalf("failed to listen for grpc: %v", err)
		}
		log.Println("Starting gRPC server on :50051")
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatalf("failed to serve grpc: %v", err)
		}
	}()

	log.Printf("Starting REST API Gateway on port %s...", cfg.Port)
	restServer := api.NewRESTServer(walletRepo, jobRepo, jobPublisher, rdbClient, authService)
	
	// Expose a raw health check for Docker, or we can just let it run.
	if err := restServer.Run(":" + cfg.Port); err != nil {
		log.Fatalf("REST API Gateway failed to start: %v", err)
	}
}

