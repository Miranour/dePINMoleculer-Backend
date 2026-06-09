package service

import (
	"context"
	"errors"
	"fmt"
	"os"

	"depin-backend/internal/repository"

	"golang.org/x/crypto/bcrypt"
	"google.golang.org/api/idtoken"
)

type AuthService struct {
	userRepo *repository.UserRepository
}

func NewAuthService(userRepo *repository.UserRepository) *AuthService {
	return &AuthService{userRepo: userRepo}
}

// RegisterEmailUser registers a new user with email and password
func (s *AuthService) RegisterEmailUser(ctx context.Context, email, password string) (*repository.User, error) {
	// Check if user already exists
	existing, err := s.userRepo.GetUserByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, errors.New("user with this email already exists")
	}

	// Hash password
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}
	hashStr := string(hash)

	// Create user
	return s.userRepo.CreateUser(ctx, email, &hashStr, "email", nil, "researcher")
}

// LoginEmailUser authenticates a user with email and password
func (s *AuthService) LoginEmailUser(ctx context.Context, email, password string) (*repository.User, error) {
	user, err := s.userRepo.GetUserByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, errors.New("invalid email or password")
	}
	if user.PasswordHash == nil {
		return nil, errors.New("invalid email or password") // Could be an OAuth user
	}

	err = bcrypt.CompareHashAndPassword([]byte(*user.PasswordHash), []byte(password))
	if err != nil {
		return nil, errors.New("invalid email or password")
	}

	return user, nil
}

// AuthenticateWithGoogle validates a Google ID token and creates/returns the user
func (s *AuthService) AuthenticateWithGoogle(ctx context.Context, tokenStr string) (*repository.User, error) {
	clientID := os.Getenv("GOOGLE_CLIENT_ID")
	if clientID == "" {
		return nil, errors.New("google client ID not configured")
	}

	payload, err := idtoken.Validate(ctx, tokenStr, clientID)
	if err != nil {
		return nil, fmt.Errorf("invalid google token: %w", err)
	}

	email := payload.Claims["email"].(string)
	providerID := payload.Subject

	// Check if user exists by provider
	user, err := s.userRepo.GetUserByProvider(ctx, "google", providerID)
	if err != nil {
		return nil, err
	}
	if user != nil {
		return user, nil
	}

	// Check if user exists by email (link accounts if they do, or create new)
	user, err = s.userRepo.GetUserByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	if user != nil {
		// Update user to link Google account? For MVP, we can just return error or link it
		return nil, errors.New("user with this email already exists, please login with email")
	}

	// Create new Google user
	return s.userRepo.CreateUser(ctx, email, nil, "google", &providerID, "researcher")
}

// StartVerification is a mock function to initiate the verification process
func (s *AuthService) StartVerification(ctx context.Context, userID string) error {
	// In a real scenario, this would send an email with a code or link
	// For now, we assume success
	return nil
}

// CompleteVerification marks the user as verified
func (s *AuthService) CompleteVerification(ctx context.Context, userID string, code string) error {
	// In a real scenario, we would verify the code
	if code != "123456" { // Hardcoded mock code for MVP
		return errors.New("invalid verification code")
	}

	return s.userRepo.MarkUserAsVerified(ctx, userID)
}

// GetUserDetails fetches a user by ID
func (s *AuthService) GetUserDetails(ctx context.Context, userID string) (*repository.User, error) {
	// Let's assume there is a GetUserByID in repo, but we need to implement it first.
	// Actually, wait, let's implement GetUserByID in repo.
	return s.userRepo.GetUserByID(ctx, userID)
}
