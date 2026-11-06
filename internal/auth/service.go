// Copyright (c) 2025-2026, s0up and the autobrr contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/rs/zerolog/log"

	"github.com/autobrr/qui/internal/dbinterface"
	"github.com/autobrr/qui/internal/models"
)

const (
	SessionName = "qui_user_session"
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrNotSetup           = errors.New("initial setup required")
)

type Service struct {
	userStore   *models.UserStore
	apiKeyStore *models.APIKeyStore
}

func NewService(db dbinterface.Querier) *Service {
	return &Service{
		userStore:   models.NewUserStore(db),
		apiKeyStore: models.NewAPIKeyStore(db),
	}
}

// SetupUser creates the initial user account
func (s *Service) SetupUser(ctx context.Context, username, password string) (*models.User, error) {
	// Check if user already exists
	exists, err := s.userStore.Exists(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to check user existence: %w", err)
	}
	if exists {
		return nil, models.ErrUserAlreadyExists
	}

	// Validate password strength
	if len(password) < 8 {
		return nil, errors.New("password must be at least 8 characters long")
	}

	// Hash password
	hashedPassword, err := HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	// Create user
	user, err := s.userStore.CreateWithRole(ctx, username, hashedPassword, models.UserRoleAdmin)
	if err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}

	log.Info().Msgf("Initial user '%s' created successfully", username)
	return user, nil
}

// Login validates credentials and returns the user
func (s *Service) Login(ctx context.Context, username, password string) (*models.User, error) {
	// Check if setup is complete
	exists, err := s.userStore.Exists(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to check user existence: %w", err)
	}
	if !exists {
		return nil, ErrNotSetup
	}

	// Get user by username
	user, err := s.userStore.GetByUsernameWithRole(ctx, username)
	if err != nil {
		if errors.Is(err, models.ErrUserNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("failed to get user: %w", err)
	}

	// Verify password
	valid, err := VerifyPassword(password, user.PasswordHash)
	if err != nil {
		return nil, fmt.Errorf("failed to verify password: %w", err)
	}
	if !valid {
		return nil, ErrInvalidCredentials
	}

	return user, nil
}

// ChangePassword updates the user's password
func (s *Service) ChangePassword(ctx context.Context, oldPassword, newPassword string) error {
	// Get the current user
	user, err := s.userStore.Get(ctx)
	if err != nil {
		return fmt.Errorf("failed to get user: %w", err)
	}

	// Verify old password
	valid, err := VerifyPassword(oldPassword, user.PasswordHash)
	if err != nil {
		return fmt.Errorf("failed to verify password: %w", err)
	}
	if !valid {
		return ErrInvalidCredentials
	}

	// Validate new password strength
	if len(newPassword) < 8 {
		return errors.New("password must be at least 8 characters long")
	}

	// Hash new password
	hashedPassword, err := HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}

	// Update password
	if err := s.userStore.UpdatePassword(ctx, hashedPassword); err != nil {
		return fmt.Errorf("failed to update password: %w", err)
	}

	log.Info().Msg("Password changed successfully")
	return nil
}

// API Key Management

// CreateAPIKey generates a new API key
func (s *Service) CreateAPIKey(ctx context.Context, name string, permissions []models.Permission, instanceIDs []int) (string, *models.APIKey, error) {
	return s.apiKeyStore.Create(ctx, name, permissions, instanceIDs)
}

func (s *Service) UpdateAPIKey(ctx context.Context, id int, name string, permissions []models.Permission, instanceIDs []int) (*models.APIKey, error) {
	return s.apiKeyStore.Update(ctx, id, name, permissions, instanceIDs)
}

func (s *Service) CreateAPIKeyForUser(ctx context.Context, userID int, name string, permissions []models.Permission, instanceIDs []int) (string, *models.APIKey, error) {
	return s.apiKeyStore.CreateForUser(ctx, userID, name, permissions, instanceIDs)
}

// ValidateAPIKey checks if an API key is valid
func (s *Service) ValidateAPIKey(ctx context.Context, key string) (*models.APIKey, error) {
	return s.apiKeyStore.ValidateAPIKey(ctx, key)
}

// ListAPIKeys returns all API keys
func (s *Service) ListAPIKeys(ctx context.Context) ([]*models.APIKey, error) {
	return s.apiKeyStore.List(ctx)
}

func (s *Service) ListAPIKeysForUser(ctx context.Context, userID int, admin bool) ([]*models.APIKey, error) {
	return s.apiKeyStore.ListForUser(ctx, userID, admin)
}

// DeleteAPIKey removes an API key
func (s *Service) DeleteAPIKey(ctx context.Context, id int) error {
	return s.apiKeyStore.Delete(ctx, id)
}

func (s *Service) DeleteAPIKeyForUser(ctx context.Context, id, userID int, admin bool) error {
	return s.apiKeyStore.DeleteForUser(ctx, id, userID, admin)
}

// IsSetupComplete checks if initial setup has been completed
func (s *Service) IsSetupComplete(ctx context.Context) (bool, error) {
	return s.userStore.Exists(ctx)
}

func (s *Service) ListUsers(ctx context.Context) ([]*models.User, error) {
	return s.userStore.ListAccounts(ctx)
}

func (s *Service) HasPermission(ctx context.Context, userID int, role models.UserRole, permission models.UserPermission) (bool, error) {
	if role == models.UserRoleAdmin {
		return true, nil
	}
	return s.userStore.HasPermission(ctx, userID, permission)
}

func (s *Service) UpdateUserPermissions(ctx context.Context, id int, permissions []models.UserPermission) error {
	return s.userStore.SetPermissions(ctx, id, permissions)
}

func (s *Service) CreateManagedUser(ctx context.Context, username, password string, role models.UserRole, permissions []models.UserPermission) (*models.User, error) {
	if len(password) < 8 {
		return nil, errors.New("password must be at least 8 characters long")
	}
	if role == "" {
		role = models.UserRoleUser
	}
	if role != models.UserRoleAdmin && role != models.UserRoleUser {
		return nil, errors.New("invalid user role")
	}
	for _, permission := range permissions {
		if !models.IsValidUserPermission(permission) {
			return nil, errors.New("invalid user permission")
		}
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	user, err := s.userStore.CreateWithRole(ctx, username, hash, role)
	if err != nil {
		return nil, err
	}
	if role == models.UserRoleAdmin {
		return user, nil
	}
	if err := s.userStore.SetPermissions(ctx, user.ID, permissions); err != nil {
		return nil, err
	}
	user.Permissions = permissions
	return user, nil
}

// EnsureOIDCUser creates a local account for an OIDC identity when one does
// not already exist. The generated password is never exposed to the client.
func (s *Service) EnsureOIDCUser(ctx context.Context, username string) (*models.User, error) {
	user, err := s.userStore.GetByUsernameWithRole(ctx, username)
	if err == nil {
		return user, nil
	}
	if !errors.Is(err, models.ErrUserNotFound) {
		return nil, err
	}

	role := models.UserRoleUser
	exists, err := s.userStore.Exists(ctx)
	if err != nil {
		return nil, err
	}
	if !exists {
		role = models.UserRoleAdmin
	}

	randomPassword, err := models.GenerateAPIKey()
	if err != nil {
		return nil, err
	}
	passwordHash, err := HashPassword(randomPassword)
	if err != nil {
		return nil, err
	}

	return s.userStore.CreateWithRole(ctx, username, passwordHash, role)
}
func (s *Service) UpdateUserRole(ctx context.Context, id int, role models.UserRole) error {
	return s.userStore.UpdateRole(ctx, id, role)
}

func (s *Service) UpdateUserPassword(ctx context.Context, id int, passwordHash string) error {
	return s.userStore.UpdatePasswordForUser(ctx, id, passwordHash)
}

func (s *Service) ChangePasswordForUser(ctx context.Context, userID int, oldPassword, newPassword string) error {
	user, err := s.userStore.GetWithRole(ctx, userID)
	if err != nil {
		return err
	}
	valid, err := VerifyPassword(oldPassword, user.PasswordHash)
	if err != nil {
		return err
	}
	if !valid {
		return ErrInvalidCredentials
	}
	if len(newPassword) < 8 {
		return errors.New("password must be at least 8 characters long")
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	return s.userStore.UpdatePasswordForUser(ctx, userID, hash)
}
func (s *Service) DeleteManagedUser(ctx context.Context, id int) error {
	return s.userStore.DeleteAccount(ctx, id)
}
func (s *Service) GetUser(ctx context.Context, id int) (*models.User, error) {
	return s.userStore.GetWithRole(ctx, id)
}
func (s *Service) FindUser(ctx context.Context, username string) (*models.User, error) {
	return s.userStore.GetByUsernameWithRole(ctx, username)
}

func (s *Service) UpdateAPIKeyForUser(ctx context.Context, id, userID int, admin bool, name string, permissions []models.Permission, instanceIDs []int) (*models.APIKey, error) {
	return s.apiKeyStore.UpdateForUser(ctx, id, userID, admin, name, permissions, instanceIDs)
}
