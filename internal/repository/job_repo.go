package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Job struct {
	ID                string    `json:"id"`
	UserID            string    `json:"user_id"`
	SmilesString      string    `json:"smiles_string"`
	TargetPdbUrl      string    `json:"target_pdb_url"`
	TargetPdbId       string    `json:"target_pdb_id"`
	MaxExhaustiveness int32     `json:"max_exhaustiveness"`
	Cost              float64   `json:"cost"`
	Status            string    `json:"status"`
	ResultEnergy      *float64  `json:"result_energy"`
	ResultPdbqtUrl    *string   `json:"result_pdbqt_url"`
	ErrorMessage      *string   `json:"error_message"`
	CreatedAt         time.Time `json:"created_at"`
	StartedAt         *time.Time `json:"started_at"`
	CompletedAt       *time.Time `json:"completed_at"`
}

type JobRepository struct {
	pool *pgxpool.Pool
}

func NewJobRepository(pool *pgxpool.Pool) *JobRepository {
	return &JobRepository{pool: pool}
}

// CreateJob inserts a new job into the database
func (r *JobRepository) CreateJob(ctx context.Context, jobID, userID, smiles, pdbUrl, pdbId string, exhaustiveness int32, cost float64) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO jobs (id, user_id, smiles_string, target_pdb_url, target_pdb_id, max_exhaustiveness, cost, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, 'QUEUED')`,
		jobID, userID, smiles, pdbUrl, pdbId, exhaustiveness, cost,
	)
	if err != nil {
		return fmt.Errorf("failed to create job: %w", err)
	}
	return nil
}

// GetJobsByUserID fetches jobs for a specific user with pagination
func (r *JobRepository) GetJobsByUserID(ctx context.Context, userID string, limit, offset int) ([]Job, int, error) {
	// Get total count
	var total int
	err := r.pool.QueryRow(ctx, "SELECT COUNT(*) FROM jobs WHERE user_id = $1", userID).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count jobs: %w", err)
	}

	// Get jobs
	rows, err := r.pool.Query(ctx, 
		`SELECT id, user_id, smiles_string, target_pdb_url, target_pdb_id, max_exhaustiveness, cost, status, 
		        result_energy, result_pdbqt_url, error_message, created_at, started_at, completed_at 
		 FROM jobs WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		userID, limit, offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to query jobs: %w", err)
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(
			&j.ID, &j.UserID, &j.SmilesString, &j.TargetPdbUrl, &j.TargetPdbId, &j.MaxExhaustiveness, &j.Cost, &j.Status,
			&j.ResultEnergy, &j.ResultPdbqtUrl, &j.ErrorMessage, &j.CreatedAt, &j.StartedAt, &j.CompletedAt,
		); err != nil {
			return nil, 0, err
		}
		jobs = append(jobs, j)
	}

	return jobs, total, nil
}

// GetJobByID fetches a single job
func (r *JobRepository) GetJobByID(ctx context.Context, jobID string) (*Job, error) {
	var j Job
	err := r.pool.QueryRow(ctx,
		`SELECT id, user_id, smiles_string, target_pdb_url, target_pdb_id, max_exhaustiveness, cost, status, 
		        result_energy, result_pdbqt_url, error_message, created_at, started_at, completed_at 
		 FROM jobs WHERE id = $1`,
		jobID,
	).Scan(
		&j.ID, &j.UserID, &j.SmilesString, &j.TargetPdbUrl, &j.TargetPdbId, &j.MaxExhaustiveness, &j.Cost, &j.Status,
		&j.ResultEnergy, &j.ResultPdbqtUrl, &j.ErrorMessage, &j.CreatedAt, &j.StartedAt, &j.CompletedAt,
	)
	
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query job: %w", err)
	}
	return &j, nil
}

// UpdateJobStatus updates the status and result of a job
func (r *JobRepository) UpdateJobStatus(ctx context.Context, jobID, status string, resultEnergy *float64, errorMsg *string) error {
	var err error
	if status == "RUNNING" {
		_, err = r.pool.Exec(ctx, "UPDATE jobs SET status = $1, started_at = NOW() WHERE id = $2", status, jobID)
	} else if status == "COMPLETED" || status == "FAILED" || status == "ABORTED" {
		_, err = r.pool.Exec(ctx, 
			"UPDATE jobs SET status = $1, result_energy = $2, error_message = $3, completed_at = NOW() WHERE id = $4", 
			status, resultEnergy, errorMsg, jobID,
		)
	} else {
		_, err = r.pool.Exec(ctx, "UPDATE jobs SET status = $1 WHERE id = $2", status, jobID)
	}

	if err != nil {
		return fmt.Errorf("failed to update job status: %w", err)
	}
	return nil
}
