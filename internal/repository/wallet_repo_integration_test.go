//go:build integration

package repository

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"depin-backend/internal/config"
	"depin-backend/pkg/database"
)

// getTestConfig returns config pointing to local Docker infrastructure
func getTestConfig() *config.Config {
	return &config.Config{
		DBHost:     envOrDefault("DB_HOST", "localhost"),
		DBPort:     envOrDefault("DB_PORT", "5432"),
		DBUser:     envOrDefault("DB_USER", "postgres"),
		DBPassword: envOrDefault("DB_PASSWORD", "postgrespassword"),
		DBName:     envOrDefault("DB_NAME", "depin_db"),
		DBSSLMode:  envOrDefault("DB_SSLMODE", "disable"),
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func TestWalletRepo_FullLifecycle(t *testing.T) {
	// Change working directory to project root for migrations
	if err := os.Chdir("../.."); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}
	
	cfg := getTestConfig()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1. Connect to PostgreSQL
	pool, err := database.NewPostgresPool(cfg)
	if err != nil {
		t.Fatalf("Failed to connect to PostgreSQL: %v", err)
	}
	defer pool.Close()
	t.Log("[OK] PostgreSQL connection established")

	// 2. Run Migrations
	if err := database.RunMigrations(cfg); err != nil {
		t.Fatalf("Failed to run migrations: %v", err)
	}
	t.Log("[OK] Migrations applied")

	repo := NewWalletRepository(pool)
	testWorkerID := fmt.Sprintf("test_worker_%d", time.Now().UnixNano())
	testUserID := fmt.Sprintf("test_user_%d", time.Now().UnixNano())
	testJobID := fmt.Sprintf("test_job_%d", time.Now().UnixNano())

	// 3. Seed test data: create user_wallet and worker_wallet
	_, err = pool.Exec(ctx, `INSERT INTO user_wallets (user_id, active_balance, blocked_balance) VALUES ($1, 1000.00, 0.00) ON CONFLICT(user_id) DO UPDATE SET active_balance = 1000.00, blocked_balance = 0.00`, testUserID)
	if err != nil {
		t.Fatalf("Failed to seed user wallet: %v", err)
	}
	t.Log("[OK] User wallet seeded with 1000.00 active balance")

	_, err = pool.Exec(ctx, `INSERT INTO worker_wallets (worker_id, pending_balance, confirmed_balance, total_earned, total_withdrawn) VALUES ($1, 0.00, 0.00, 0.00, 0.00) ON CONFLICT(worker_id) DO UPDATE SET confirmed_balance = 0.00`, testWorkerID)
	if err != nil {
		t.Fatalf("Failed to seed worker wallet: %v", err)
	}
	t.Log("[OK] Worker wallet seeded")

	// 4. Test BlockUserBalance
	err = repo.BlockUserBalance(ctx, testUserID, 50.0)
	if err != nil {
		t.Fatalf("BlockUserBalance failed: %v", err)
	}

	// Verify balances
	var activeBalance, blockedBalance float64
	err = pool.QueryRow(ctx, "SELECT active_balance, blocked_balance FROM user_wallets WHERE user_id = $1", testUserID).Scan(&activeBalance, &blockedBalance)
	if err != nil {
		t.Fatalf("Failed to query user wallet: %v", err)
	}
	if activeBalance != 950.0 {
		t.Errorf("Expected active_balance 950.0, got %f", activeBalance)
	}
	if blockedBalance != 50.0 {
		t.Errorf("Expected blocked_balance 50.0, got %f", blockedBalance)
	}
	t.Log("[OK] BlockUserBalance: active=950, blocked=50")

	// 5. Test BlockUserBalance insufficient funds
	err = repo.BlockUserBalance(ctx, testUserID, 99999.0)
	if err == nil {
		t.Error("Expected error for insufficient balance, got nil")
	} else {
		t.Log("[OK] Insufficient balance correctly rejected")
	}

	// 6. Test InsertWorkerEarning
	earningID, err := repo.InsertWorkerEarning(ctx, testWorkerID, testJobID, 0.005, 0.003, 0.002, 15000, "RTX 3090", "PENDING")
	if err != nil {
		t.Fatalf("InsertWorkerEarning failed: %v", err)
	}
	if earningID == "" {
		t.Error("Expected non-empty earning ID")
	}
	t.Logf("[OK] Worker earning inserted: id=%s", earningID)

	// 7. Test ConfirmJobReward (ACID transaction)
	err = repo.ConfirmJobReward(ctx, testWorkerID, testUserID, testJobID, 0.002, 0.005, 0.003)
	if err != nil {
		t.Fatalf("ConfirmJobReward failed: %v", err)
	}

	// Verify worker wallet updated
	wallet, err := repo.GetWorkerWallet(ctx, testWorkerID)
	if err != nil {
		t.Fatalf("GetWorkerWallet failed: %v", err)
	}
	if wallet.ConfirmedBalance != 0.002 {
		t.Errorf("Expected confirmed_balance 0.002, got %f", wallet.ConfirmedBalance)
	}
	if wallet.TotalEarned != 0.002 {
		t.Errorf("Expected total_earned 0.002, got %f", wallet.TotalEarned)
	}
	t.Logf("[OK] ConfirmJobReward: confirmed_balance=%f, total_earned=%f", wallet.ConfirmedBalance, wallet.TotalEarned)

	// 8. Test Idempotency: second ConfirmJobReward should be a no-op
	err = repo.ConfirmJobReward(ctx, testWorkerID, testUserID, testJobID, 0.002, 0.005, 0.003)
	if err != nil {
		t.Fatalf("Idempotent ConfirmJobReward should not fail: %v", err)
	}
	walletAfter, _ := repo.GetWorkerWallet(ctx, testWorkerID)
	if walletAfter.ConfirmedBalance != 0.002 {
		t.Errorf("Idempotency violated: balance changed to %f after duplicate confirm", walletAfter.ConfirmedBalance)
	}
	t.Log("[OK] Idempotency: duplicate ConfirmJobReward is a no-op")

	// 9. Test GetWorkerEarnings
	earnings, err := repo.GetWorkerEarnings(ctx, testWorkerID)
	if err != nil {
		t.Fatalf("GetWorkerEarnings failed: %v", err)
	}
	if len(earnings) == 0 {
		t.Error("Expected at least 1 earning record")
	}
	t.Logf("[OK] GetWorkerEarnings: found %d records", len(earnings))

	// 10. Test Ledger entries
	var ledgerCount int
	err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM ledger_entries WHERE job_id = $1", testJobID).Scan(&ledgerCount)
	if err != nil {
		t.Fatalf("Failed to query ledger: %v", err)
	}
	if ledgerCount < 2 {
		t.Errorf("Expected at least 2 ledger entries (PLATFORM_FEE + WORKER_PAYOUT), got %d", ledgerCount)
	}
	t.Logf("[OK] Ledger entries: %d records for job %s", ledgerCount, testJobID)

	// 11. Test CreateWithdrawalRequest
	// First add some confirmed balance for withdrawal
	_, err = pool.Exec(ctx, "UPDATE worker_wallets SET confirmed_balance = 50.0 WHERE worker_id = $1", testWorkerID)
	if err != nil {
		t.Fatalf("Failed to set confirmed balance: %v", err)
	}

	err = repo.CreateWithdrawalRequest(ctx, testWorkerID, 20.0, "USDT", "CRYPTO", "0x1234567890abcdef")
	if err != nil {
		t.Fatalf("CreateWithdrawalRequest failed: %v", err)
	}
	t.Log("[OK] Withdrawal request created")

	// Verify balance moved: confirmed -20, pending +20
	walletWd, _ := repo.GetWorkerWallet(ctx, testWorkerID)
	if walletWd.ConfirmedBalance != 30.0 {
		t.Errorf("Expected confirmed_balance 30.0 after withdrawal, got %f", walletWd.ConfirmedBalance)
	}
	if walletWd.PendingBalance != 20.0 {
		t.Errorf("Expected pending_balance 20.0 after withdrawal, got %f", walletWd.PendingBalance)
	}
	t.Logf("[OK] Withdrawal balances: confirmed=%f, pending=%f", walletWd.ConfirmedBalance, walletWd.PendingBalance)

	// 12. Test GetPendingWithdrawals
	pending, err := repo.GetPendingWithdrawals(ctx)
	if err != nil {
		t.Fatalf("GetPendingWithdrawals failed: %v", err)
	}
	if len(pending) == 0 {
		t.Error("Expected at least 1 pending withdrawal")
	}
	t.Logf("[OK] Pending withdrawals: %d", len(pending))

	// 13. Test UpdateWithdrawalStatus REJECT → refund
	var wdID string
	err = pool.QueryRow(ctx, "SELECT id FROM withdrawal_requests WHERE worker_id = $1 AND status = 'PENDING' LIMIT 1", testWorkerID).Scan(&wdID)
	if err != nil {
		t.Fatalf("Failed to get withdrawal ID: %v", err)
	}

	err = repo.UpdateWithdrawalStatus(ctx, wdID, "REJECTED")
	if err != nil {
		t.Fatalf("UpdateWithdrawalStatus REJECTED failed: %v", err)
	}

	walletRefund, _ := repo.GetWorkerWallet(ctx, testWorkerID)
	if walletRefund.ConfirmedBalance != 50.0 {
		t.Errorf("Expected confirmed_balance refunded to 50.0, got %f", walletRefund.ConfirmedBalance)
	}
	if walletRefund.PendingBalance != 0.0 {
		t.Errorf("Expected pending_balance 0.0 after rejection, got %f", walletRefund.PendingBalance)
	}
	t.Log("[OK] Withdrawal rejection refund: confirmed=50, pending=0")

	// 14. Test FreezeWorker
	err = repo.FreezeWorker(ctx, testWorkerID, "Spot-Check Mismatch")
	if err != nil {
		t.Fatalf("FreezeWorker failed: %v", err)
	}

	walletFrozen, _ := repo.GetWorkerWallet(ctx, testWorkerID)
	if !walletFrozen.IsFrozen {
		t.Error("Expected worker to be frozen")
	}
	t.Log("[OK] Worker frozen successfully")

	// 15. Test withdrawal while frozen → should fail
	err = repo.CreateWithdrawalRequest(ctx, testWorkerID, 10.0, "USDT", "CRYPTO", "0x1234567890abcdef")
	if err == nil {
		t.Error("Expected error for withdrawal while frozen, got nil")
	} else {
		t.Log("[OK] Withdrawal correctly blocked for frozen worker")
	}

	// Cleanup
	pool.Exec(ctx, "DELETE FROM withdrawal_requests WHERE worker_id = $1", testWorkerID)
	pool.Exec(ctx, "DELETE FROM worker_earnings WHERE worker_id = $1", testWorkerID)
	pool.Exec(ctx, "DELETE FROM ledger_entries WHERE job_id = $1", testJobID)
	pool.Exec(ctx, "DELETE FROM worker_wallets WHERE worker_id = $1", testWorkerID)
	pool.Exec(ctx, "DELETE FROM user_wallets WHERE user_id = $1", testUserID)
	t.Log("[OK] Cleanup complete")

	t.Log("\n=== WALLET REPO INTEGRATION TEST: ALL PASSED ===")
}
