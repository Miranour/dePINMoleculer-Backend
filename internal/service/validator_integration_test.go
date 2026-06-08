//go:build integration

package service

import (
	"context"
	"testing"
	"time"

	"depin-backend/pkg/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestValidatorEngine_Integration(t *testing.T) {
	// Connect to the Rust Validator Engine running in Docker via docker-compose (port 50052)
	conn, err := grpc.NewClient("localhost:50052", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to connect to validator engine: %v", err)
	}
	defer conn.Close()

	client := pb.NewValidatorServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Test Valid Match
	t.Run("Valid Match", func(t *testing.T) {
		req := &pb.ValidationRequest{
			JobId:         "val_test_job_1",
			ResultAEnergy: -8.5,
			ResultBEnergy: -8.51, // within tolerance 0.1
			ResultAPdbqt:  []byte("ATOM      1  N   ASP A   1      11.111  22.222  33.333  \n"),
			ResultBPdbqt:  []byte("ATOM      1  N   ASP A   1      11.111  22.222  33.333  \n"), // exact coordinate match
		}
		
		res, err := client.ValidateJob(ctx, req)
		if err != nil {
			t.Fatalf("Validator service failed: %v", err)
		}
		
		if !res.IsValid {
			t.Errorf("Expected IsValid=true for matching results, got false. Reason: %s", res.Message)
		} else {
			t.Logf("[OK] Valid match identified correctly")
		}
	})

	// 2. Test Invalid Match (Energy difference > 0.1)
	t.Run("Invalid Match - Energy", func(t *testing.T) {
		req := &pb.ValidationRequest{
			JobId:         "val_test_job_2",
			ResultAEnergy: -8.5,
			ResultBEnergy: -9.5, // > 0.1 difference
			ResultAPdbqt:  []byte("ATOM      1  N   ASP A   1      11.111  22.222  33.333  \n"),
			ResultBPdbqt:  []byte("ATOM      1  N   ASP A   1      11.111  22.222  33.333  \n"),
		}
		
		res, err := client.ValidateJob(ctx, req)
		if err != nil {
			t.Fatalf("Validator service failed: %v", err)
		}
		
		if res.IsValid {
			t.Errorf("Expected IsValid=false for mismatched energy, got true")
		} else {
			t.Logf("[OK] Invalid match (energy) rejected: %s", res.Message)
		}
	})

	// 3. Test Invalid Match (Coordinates difference > 2.0 RMSD)
	t.Run("Invalid Match - Coordinates", func(t *testing.T) {
		req := &pb.ValidationRequest{
			JobId:         "val_test_job_3",
			ResultAEnergy: -8.5,
			ResultBEnergy: -8.55,
			ResultAPdbqt:  []byte("ATOM      1  N   ASP A   1      11.111  22.222  33.333  \n"),
			ResultBPdbqt:  []byte("ATOM      1  N   ASP A   1      99.999  99.999  99.999  \n"), // significantly different coords
		}
		
		res, err := client.ValidateJob(ctx, req)
		if err != nil {
			t.Fatalf("Validator service failed: %v", err)
		}
		
		if res.IsValid {
			t.Errorf("Expected IsValid=false for mismatched coordinates, got true")
		} else {
			t.Logf("[OK] Invalid match (coordinates) rejected: %s", res.Message)
		}
	})
	
	t.Log("\n=== VALIDATOR ENGINE (RUST gRPC) INTEGRATION TEST: ALL PASSED ===")
}
