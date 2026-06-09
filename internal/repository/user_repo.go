package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type User struct {
	ID           string
	Email        string
	PasswordHash *string
	AuthProvider string
	ProviderID   *string
	IsVerified   bool
	Role         string
}

type UserRepository struct {
	pool *pgxpool.Pool
}

func NewUserRepository(pool *pgxpool.Pool) *UserRepository {
	return &UserRepository{pool: pool}
}

// CreateUser inserts a new user into the database
func (r *UserRepository) CreateUser(ctx context.Context, email string, passwordHash *string, authProvider string, providerID *string, role string) (*User, error) {
	var user User
	err := r.pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, auth_provider, provider_id, role) 
		 VALUES ($1, $2, $3, $4, $5) 
		 RETURNING id, email, password_hash, auth_provider, provider_id, is_verified, role`,
		email, passwordHash, authProvider, providerID, role,
	).Scan(&user.ID, &user.Email, &user.PasswordHash, &user.AuthProvider, &user.ProviderID, &user.IsVerified, &user.Role)
	
	if err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}
	
	return &user, nil
}

// GetUserByEmail finds a user by their email
func (r *UserRepository) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	var user User
	err := r.pool.QueryRow(ctx,
		`SELECT id, email, password_hash, auth_provider, provider_id, is_verified, role 
		 FROM users WHERE email = $1`,
		email,
	).Scan(&user.ID, &user.Email, &user.PasswordHash, &user.AuthProvider, &user.ProviderID, &user.IsVerified, &user.Role)
	
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil // User not found
		}
		return nil, fmt.Errorf("failed to get user by email: %w", err)
	}
	
	return &user, nil
}

// GetUserByProvider finds a user by their OAuth provider and provider ID
func (r *UserRepository) GetUserByProvider(ctx context.Context, authProvider, providerID string) (*User, error) {
	var user User
	err := r.pool.QueryRow(ctx,
		`SELECT id, email, password_hash, auth_provider, provider_id, is_verified, role 
		 FROM users WHERE auth_provider = $1 AND provider_id = $2`,
		authProvider, providerID,
	).Scan(&user.ID, &user.Email, &user.PasswordHash, &user.AuthProvider, &user.ProviderID, &user.IsVerified, &user.Role)
	
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil // User not found
		}
		return nil, fmt.Errorf("failed to get user by provider: %w", err)
	}
	
	return &user, nil
}

// MarkUserAsVerified updates a user's is_verified status
func (r *UserRepository) MarkUserAsVerified(ctx context.Context, userID string) error {
	res, err := r.pool.Exec(ctx, "UPDATE users SET is_verified = TRUE, updated_at = NOW() WHERE id = $1", userID)
	if err != nil {
		return fmt.Errorf("failed to mark user as verified: %w", err)
	}
	if res.RowsAffected() == 0 {
		return errors.New("user not found")
	}
	return nil
}

// GetUserByID finds a user by their UUID
func (r *UserRepository) GetUserByID(ctx context.Context, id string) (*User, error) {
	var user User
	err := r.pool.QueryRow(ctx,
		`SELECT id, email, password_hash, auth_provider, provider_id, is_verified, role 
		 FROM users WHERE id = $1`,
		id,
	).Scan(&user.ID, &user.Email, &user.PasswordHash, &user.AuthProvider, &user.ProviderID, &user.IsVerified, &user.Role)
	
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil // User not found
		}
		return nil, fmt.Errorf("failed to get user by id: %w", err)
	}
	
	return &user, nil
}
