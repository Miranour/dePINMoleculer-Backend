package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"time"

	"depin-backend/internal/repository"
)

type PayoutProcessor struct {
	walletRepo *repository.WalletRepository
}

func NewPayoutProcessor(walletRepo *repository.WalletRepository) *PayoutProcessor {
	return &PayoutProcessor{
		walletRepo: walletRepo,
	}
}

// Start begins the cron job loop running every 5 minutes.
func (p *PayoutProcessor) Start(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	log.Println("PayoutProcessor started. Running every 5 minutes.")
	go func() {
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				log.Println("PayoutProcessor stopped.")
				return
			case <-ticker.C:
				p.processPayouts(ctx)
			}
		}
	}()
}

func (p *PayoutProcessor) processPayouts(ctx context.Context) {
	log.Println("PayoutProcessor: starting cycle...")
	
	withdrawals, err := p.walletRepo.GetApprovedWithdrawals(ctx, 50)
	if err != nil {
		log.Printf("PayoutProcessor: failed to get approved withdrawals: %v", err)
		return
	}

	if len(withdrawals) == 0 {
		log.Println("PayoutProcessor: no approved withdrawals to process.")
		return
	}

	for _, req := range withdrawals {
		// Simulate blockchain integration (e.g., calling an external API to transfer crypto)
		log.Printf("PayoutProcessor: transferring %f %s to %s for worker %s", req.Amount, req.Currency, req.Destination, req.WorkerID)
		
		// Simulated tx_hash
		hashInput := fmt.Sprintf("%s-%d", req.ID, time.Now().UnixNano())
		txHash := fmt.Sprintf("0x%x", sha256.Sum256([]byte(hashInput)))
		
		// Finalize in DB
		err := p.walletRepo.CompleteWithdrawal(ctx, req.ID, req.WorkerID, req.Amount, txHash)
		if err != nil {
			log.Printf("PayoutProcessor: failed to complete withdrawal %s: %v", req.ID, err)
		} else {
			log.Printf("PayoutProcessor: successfully completed withdrawal %s, tx_hash: %s", req.ID, txHash)
		}
	}
}
