package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"depin-backend/internal/repository"
	"depin-backend/pkg/pb"

	"google.golang.org/grpc"
)

type RewardEngine struct {
	walletRepo      *repository.WalletRepository
	redisRepo       *repository.RedisRepository
	validatorClient pb.ValidatorServiceClient
	jobPublisher    *JobPublisher
}

func NewRewardEngine(walletRepo *repository.WalletRepository, redisRepo *repository.RedisRepository, validatorConn grpc.ClientConnInterface, jobPublisher *JobPublisher) *RewardEngine {
	var client pb.ValidatorServiceClient
	if validatorConn != nil {
		client = pb.NewValidatorServiceClient(validatorConn)
	}
	return &RewardEngine{
		walletRepo:      walletRepo,
		redisRepo:       redisRepo,
		validatorClient: client,
		jobPublisher:    jobPublisher,
	}
}

// CalculateReward applies the formula: reward = base_rate * compute_time_seconds * gpu_tier_multiplier * difficulty_factor
func (e *RewardEngine) CalculateReward(computeTimeMs int64, gpuModel string, maxExhaustiveness int32) (gross, platformFee, net float64) {
	baseRate := 0.0005 // USDT per second
	computeTimeSec := float64(computeTimeMs) / 1000.0

	gpuTierMultiplier := 0.5 // default
	switch gpuModel {
	case "RTX 4090":
		gpuTierMultiplier = 1.0
	case "RTX 3090":
		gpuTierMultiplier = 0.8
	case "RTX 3060":
		gpuTierMultiplier = 0.6
	}

	difficultyFactor := float64(maxExhaustiveness) / 8.0
	if difficultyFactor == 0 {
		difficultyFactor = 1.0
	}

	gross = baseRate * computeTimeSec * gpuTierMultiplier * difficultyFactor
	platformFee = gross * 0.60
	net = gross * 0.40

	return gross, platformFee, net
}

// ProcessJobResult is called when a worker finishes a job successfully.
func (e *RewardEngine) ProcessJobResult(ctx context.Context, workerID, jobID, userID string, computeTimeMs int64, gpuModel string, maxExhaustiveness int32, isSpotCheck bool, bindingEnergy float64, pdbqtData []byte) error {
	gross, platformFee, net := e.CalculateReward(computeTimeMs, gpuModel, maxExhaustiveness)

	if !isSpotCheck {
		// 1. Record earnings as PENDING
		_, err := e.walletRepo.InsertWorkerEarning(ctx, workerID, jobID, gross, platformFee, net, computeTimeMs, gpuModel, "PENDING")
		if err != nil {
			return err
		}
		
		// 2. Normal job: confirm immediately
		err = e.walletRepo.ConfirmJobReward(ctx, workerID, userID, jobID, net, gross, platformFee)
		if err != nil {
			return err
		}
		log.Printf("Job %s confirmed normally. Worker %s rewarded.", jobID, workerID)
		return nil
	}

	// --- SPOT-CHECK LOGIC ---
	_, err := e.walletRepo.InsertWorkerEarning(ctx, workerID, jobID, gross, platformFee, net, computeTimeMs, gpuModel, "SPOT_CHECK")
	if err != nil {
		return err
	}

	firstResultStr, err := e.redisRepo.GetSpotCheckResult(ctx, jobID)
	if err != nil {
		return err
	}

	if firstResultStr == "" {
		// First worker to finish the spot-check
		firstResult := repository.SpotCheckResult{
			WorkerID:      workerID,
			BindingEnergy: bindingEnergy,
			PDBQTData:     pdbqtData,
			ComputeTimeMs: computeTimeMs,
			GPUModel:      gpuModel,
		}
		data, _ := json.Marshal(firstResult)
		log.Printf("Spot-Check Job %s: First result received from worker %s. Waiting for second.", jobID, workerID)
		return e.redisRepo.SaveSpotCheckResult(ctx, jobID, string(data))
	}

	// Second worker finished. Validate both results.
	var firstResult repository.SpotCheckResult
	json.Unmarshal([]byte(firstResultStr), &firstResult)

	if e.validatorClient == nil {
		return fmt.Errorf("validator client is not connected")
	}

	valReq := &pb.ValidationRequest{
		JobId:         jobID,
		ResultAPdbqt:  firstResult.PDBQTData,
		ResultAEnergy: firstResult.BindingEnergy,
		ResultBPdbqt:  pdbqtData,
		ResultBEnergy: bindingEnergy,
	}

	valRes, err := e.validatorClient.ValidateJob(ctx, valReq)
	if err != nil {
		return fmt.Errorf("validator service failed: %w", err)
	}

	if valRes.IsValid {
		// Both are valid! Confirm BOTH rewards.
		gross1, platformFee1, net1 := e.CalculateReward(firstResult.ComputeTimeMs, firstResult.GPUModel, maxExhaustiveness)
		e.walletRepo.ConfirmJobReward(ctx, firstResult.WorkerID, userID, jobID, net1, gross1, platformFee1)
		e.walletRepo.ConfirmJobReward(ctx, workerID, userID, jobID, net, gross, platformFee)

		log.Printf("Spot-Check Job %s VALIDATED! Both workers rewarded.", jobID)
		e.redisRepo.DeleteSpotCheckResult(ctx, jobID)
	} else {
		// Fraud detected. Freeze both since we don't know who is lying without a 3rd worker.
		log.Printf("Spot-Check Job %s FAILED VALIDATION! Freezing workers...", jobID)
		
		e.walletRepo.FreezeWorker(ctx, firstResult.WorkerID, "Spot-Check Mismatch")
		e.redisRepo.SetBlacklist(ctx, firstResult.WorkerID)

		e.walletRepo.FreezeWorker(ctx, workerID, "Spot-Check Mismatch")
		e.redisRepo.SetBlacklist(ctx, workerID)

		e.redisRepo.DeleteSpotCheckResult(ctx, jobID)
		
		// TODO: Fetch original job params from DB and requeue 2 copies to different workers.
		log.Printf("TODO: Re-queue Spot-Check Job %s for 3rd verification.", jobID)
	}

	return nil
}
