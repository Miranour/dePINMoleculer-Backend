package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type WalletRepository struct {
	pool *pgxpool.Pool
}

func NewWalletRepository(pool *pgxpool.Pool) *WalletRepository {
	return &WalletRepository{pool: pool}
}

// ConfirmJobReward handles the atomic update of the worker's confirmed balance,
// the client's blocked balance, and creating ledger entries.
// It uses SELECT FOR UPDATE to prevent double-spending and ensure atomicity.
func (r *WalletRepository) ConfirmJobReward(
	ctx context.Context,
	workerID string,
	userID string,
	jobID string,
	netAmount float64,
	jobCost float64,
	platformFee float64,
) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	// Defer rollback, if the transaction is already committed this is a no-op
	defer tx.Rollback(ctx)

	// 1. Lock the worker_earnings row for the given job_id and worker_id to prevent duplicate confirmations
	// Use SELECT FOR UPDATE to ensure no other thread can process this concurrent row.
	var currentStatus string
	err = tx.QueryRow(ctx, "SELECT status FROM worker_earnings WHERE job_id = $1 AND worker_id = $2 FOR UPDATE", jobID, workerID).Scan(&currentStatus)
	if err != nil {
		if err == pgx.ErrNoRows {
			return fmt.Errorf("worker_earnings record not found for job %s and worker %s", jobID, workerID)
		}
		return fmt.Errorf("failed to lock worker_earnings: %w", err)
	}

	if currentStatus == "CONFIRMED" || currentStatus == "PAID" {
		// Idempotency: Duplicate request, just return success without modifying anything
		return nil
	}

	// Update worker_earnings to CONFIRMED
	_, err = tx.Exec(ctx, "UPDATE worker_earnings SET status = 'CONFIRMED', confirmed_at = NOW() WHERE job_id = $1 AND worker_id = $2", jobID, workerID)
	if err != nil {
		return fmt.Errorf("failed to update worker_earnings: %w", err)
	}

	// 2. Lock and Update Worker Wallet
	_, err = tx.Exec(ctx, "UPDATE worker_wallets SET confirmed_balance = confirmed_balance + $1, total_earned = total_earned + $1, updated_at = NOW() WHERE worker_id = $2", netAmount, workerID)
	if err != nil {
		return fmt.Errorf("failed to update worker wallet: %w", err)
	}

	// 3. Lock and Update User Wallet
	// Active balance and blocked balance are decreased based on the actual job completion.
	_, err = tx.Exec(ctx, "UPDATE user_wallets SET blocked_balance = blocked_balance - $1, active_balance = active_balance - $1, updated_at = NOW() WHERE user_id = $2", jobCost, userID)
	if err != nil {
		return fmt.Errorf("failed to update user wallet: %w", err)
	}

	// 4. Insert Ledger Entries
	_, err = tx.Exec(ctx, "INSERT INTO ledger_entries (job_id, amount, type) VALUES ($1, $2, 'PLATFORM_FEE')", jobID, platformFee)
	if err != nil {
		return fmt.Errorf("failed to insert platform fee ledger: %w", err)
	}

	_, err = tx.Exec(ctx, "INSERT INTO ledger_entries (job_id, amount, type) VALUES ($1, $2, 'WORKER_PAYOUT')", jobID, netAmount)
	if err != nil {
		return fmt.Errorf("failed to insert worker payout ledger: %w", err)
	}

	// Commit the transaction
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// InsertWorkerEarning inserts a new record into worker_earnings.
func (r *WalletRepository) InsertWorkerEarning(
	ctx context.Context,
	workerID string,
	jobID string,
	grossAmount float64,
	platformFee float64,
	netAmount float64,
	computeTimeMs int64,
	gpuModel string,
	status string,
) (string, error) {
	var id string
	err := r.pool.QueryRow(ctx,
		`INSERT INTO worker_earnings (worker_id, job_id, gross_amount, platform_fee, net_amount, compute_time_ms, gpu_model, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		workerID, jobID, grossAmount, platformFee, netAmount, computeTimeMs, gpuModel, status,
	).Scan(&id)
	
	if err != nil {
		return "", fmt.Errorf("failed to insert worker earning: %w", err)
	}
	return id, nil
}

// FreezeWorker freezes a worker's wallet due to fraud or rules violation.
func (r *WalletRepository) FreezeWorker(ctx context.Context, workerID string, reason string) error {
	_, err := r.pool.Exec(ctx,
		"UPDATE worker_wallets SET is_frozen = TRUE, frozen_reason = $1, updated_at = NOW() WHERE worker_id = $2",
		reason, workerID,
	)
	if err != nil {
		return fmt.Errorf("failed to freeze worker: %w", err)
	}
	return nil
}

// WithdrawalRequest represents a row from withdrawal_requests
type WithdrawalRequest struct {
	ID          string
	WorkerID    string
	Amount      float64
	Currency    string
	Destination string
}

// GetApprovedWithdrawals fetches withdrawal requests that are in APPROVED status.
func (r *WalletRepository) GetApprovedWithdrawals(ctx context.Context, limit int) ([]WithdrawalRequest, error) {
	rows, err := r.pool.Query(ctx, 
		"SELECT id, worker_id, amount, currency, destination FROM withdrawal_requests WHERE status = 'APPROVED' LIMIT $1", 
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query approved withdrawals: %w", err)
	}
	defer rows.Close()

	var requests []WithdrawalRequest
	for rows.Next() {
		var req WithdrawalRequest
		if err := rows.Scan(&req.ID, &req.WorkerID, &req.Amount, &req.Currency, &req.Destination); err != nil {
			return nil, err
		}
		requests = append(requests, req)
	}
	return requests, nil
}

// CompleteWithdrawal updates a withdrawal request to COMPLETED and records the tx_hash.
// It also updates the worker's total_withdrawn balance.
func (r *WalletRepository) CompleteWithdrawal(ctx context.Context, id string, workerID string, amount float64, txHash string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Update withdrawal status
	_, err = tx.Exec(ctx,
		"UPDATE withdrawal_requests SET status = 'COMPLETED', tx_hash = $1, completed_at = NOW() WHERE id = $2",
		txHash, id,
	)
	if err != nil {
		return err
	}

	// Update worker_wallets total_withdrawn
	_, err = tx.Exec(ctx,
		"UPDATE worker_wallets SET total_withdrawn = total_withdrawn + $1, pending_balance = pending_balance - $1, updated_at = NOW() WHERE worker_id = $2",
		amount, workerID,
	)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// BlockUserBalance deducts from active_balance and adds to blocked_balance.
func (r *WalletRepository) BlockUserBalance(ctx context.Context, userID string, amount float64) error {
	res, err := r.pool.Exec(ctx,
		"UPDATE user_wallets SET active_balance = active_balance - $1, blocked_balance = blocked_balance + $1, updated_at = NOW() WHERE user_id = $2 AND active_balance >= $1",
		amount, userID,
	)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return fmt.Errorf("insufficient balance or user not found")
	}
	return nil
}

// WorkerWallet represents worker_wallets row
type WorkerWallet struct {
	WorkerID         string  `json:"worker_id"`
	PendingBalance   float64 `json:"pending_balance"`
	ConfirmedBalance float64 `json:"confirmed_balance"`
	TotalEarned      float64 `json:"total_earned"`
	TotalWithdrawn   float64 `json:"total_withdrawn"`
	IsFrozen         bool    `json:"is_frozen"`
}

// GetWorkerWallet retrieves a worker's wallet
func (r *WalletRepository) GetWorkerWallet(ctx context.Context, workerID string) (*WorkerWallet, error) {
	var w WorkerWallet
	err := r.pool.QueryRow(ctx,
		"SELECT worker_id, pending_balance, confirmed_balance, total_earned, total_withdrawn, is_frozen FROM worker_wallets WHERE worker_id = $1",
		workerID,
	).Scan(&w.WorkerID, &w.PendingBalance, &w.ConfirmedBalance, &w.TotalEarned, &w.TotalWithdrawn, &w.IsFrozen)
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// WorkerEarning represents worker_earnings row
type WorkerEarning struct {
	JobID         string  `json:"job_id"`
	NetAmount     float64 `json:"net_amount"`
	ComputeTimeMs int64   `json:"compute_time_ms"`
	Status        string  `json:"status"`
}

// GetWorkerEarnings retrieves all earnings for a worker
func (r *WalletRepository) GetWorkerEarnings(ctx context.Context, workerID string) ([]WorkerEarning, error) {
	rows, err := r.pool.Query(ctx, "SELECT job_id, net_amount, compute_time_ms, status FROM worker_earnings WHERE worker_id = $1 ORDER BY created_at DESC", workerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var earnings []WorkerEarning
	for rows.Next() {
		var e WorkerEarning
		if err := rows.Scan(&e.JobID, &e.NetAmount, &e.ComputeTimeMs, &e.Status); err != nil {
			return nil, err
		}
		earnings = append(earnings, e)
	}
	return earnings, nil
}

// CreateWithdrawalRequest creates a new request and deducts from confirmed_balance, adding to pending_balance
func (r *WalletRepository) CreateWithdrawalRequest(ctx context.Context, workerID string, amount float64, currency, method, dest string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Deduct from confirmed, add to pending
	res, err := tx.Exec(ctx,
		"UPDATE worker_wallets SET confirmed_balance = confirmed_balance - $1, pending_balance = pending_balance + $1, updated_at = NOW() WHERE worker_id = $2 AND confirmed_balance >= $1 AND is_frozen = FALSE",
		amount, workerID,
	)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return fmt.Errorf("insufficient confirmed balance, worker frozen, or worker not found")
	}

	_, err = tx.Exec(ctx,
		"INSERT INTO withdrawal_requests (worker_id, amount, currency, payout_method, destination, status) VALUES ($1, $2, $3, $4, $5, 'PENDING')",
		workerID, amount, currency, method, dest,
	)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// GetPendingWithdrawals gets all PENDING withdrawals for admin
func (r *WalletRepository) GetPendingWithdrawals(ctx context.Context) ([]WithdrawalRequest, error) {
	rows, err := r.pool.Query(ctx, "SELECT id, worker_id, amount, currency, destination FROM withdrawal_requests WHERE status = 'PENDING'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []WithdrawalRequest
	for rows.Next() {
		var req WithdrawalRequest
		if err := rows.Scan(&req.ID, &req.WorkerID, &req.Amount, &req.Currency, &req.Destination); err != nil {
			return nil, err
		}
		requests = append(requests, req)
	}
	return requests, nil
}

// UpdateWithdrawalStatus updates status to APPROVED or REJECTED.
// If REJECTED, refunds pending_balance back to confirmed_balance.
func (r *WalletRepository) UpdateWithdrawalStatus(ctx context.Context, id, status string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var workerID string
	var amount float64
	err = tx.QueryRow(ctx, "UPDATE withdrawal_requests SET status = $1, processed_at = NOW() WHERE id = $2 AND status = 'PENDING' RETURNING worker_id, amount", status, id).Scan(&workerID, &amount)
	if err != nil {
		return fmt.Errorf("withdrawal not found or already processed: %w", err)
	}

	if status == "REJECTED" {
		_, err = tx.Exec(ctx,
			"UPDATE worker_wallets SET pending_balance = pending_balance - $1, confirmed_balance = confirmed_balance + $1, updated_at = NOW() WHERE worker_id = $2",
			amount, workerID,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}
