// Copyright (c) 2025-2026, s0up and the autobrr contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"golang.org/x/time/rate"

	"github.com/autobrr/qui/internal/auth"
	"github.com/autobrr/qui/internal/domain"
	"github.com/autobrr/qui/internal/models"
	"github.com/autobrr/qui/internal/qbittorrent"
)

type AuthHandler struct {
	authService    *auth.Service
	sessionManager *scs.SessionManager
	oidcHandler    *OIDCHandler
	config         *domain.Config
	instanceStore  *models.InstanceStore
	clientPool     *qbittorrent.ClientPool
	syncManager    *qbittorrent.SyncManager
	loginLimiter   *rate.Limiter
	// loginMu serialises password checks so parallel guesses cannot all pass the
	// limiter before any of them consumes a token.
	loginMu sync.Mutex
}

// newLoginLimiter caps failed password attempts process-wide, including account switches.
func newLoginLimiter() *rate.Limiter {
	return rate.NewLimiter(rate.Every(time.Minute/5), 5)
}

// requireJSON rejects credential posts that do not declare a JSON body. A
// browser form can post text/plain cross-site without a preflight; JSON cannot.
func requireJSON(w http.ResponseWriter, r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		RespondError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	return true
}

func renewSessionToken(ctx context.Context, sessionManager *scs.SessionManager) error {
	if err := sessionManager.RenewToken(ctx); err != nil {
		// Clear marks the old session for saving without authentication, even
		// when the store cannot delete it. LoadAndSave handles save errors.
		return errors.Join(err, sessionManager.Clear(ctx))
	}
	return nil
}

func NewAuthHandler(
	authService *auth.Service,
	sessionManager *scs.SessionManager, config *domain.Config,
	instanceStore *models.InstanceStore,
	clientPool *qbittorrent.ClientPool,
	syncManager *qbittorrent.SyncManager,
) (*AuthHandler, error) {
	h := &AuthHandler{
		authService:    authService,
		sessionManager: sessionManager,
		instanceStore:  instanceStore,
		clientPool:     clientPool,
		syncManager:    syncManager,
		config:         config,
		loginLimiter:   newLoginLimiter(),
	}

	// Initialize OIDC handler if enabled
	if config.OIDCEnabled {
		oidcHandler, err := NewOIDCHandler(config, sessionManager, authService)
		if err != nil {
			return nil, fmt.Errorf("init OIDC handler: %w", err)
		}
		h.oidcHandler = oidcHandler
	}

	return h, nil
}

// GetOIDCHandler returns the OIDC handler if configured
func (h *AuthHandler) GetOIDCHandler() *OIDCHandler {
	return h.oidcHandler
}

// rejectIfAuthDisabled returns true (and writes a 403 response) when
// authentication is disabled, signalling the caller to return early.
func (h *AuthHandler) rejectIfAuthDisabled(w http.ResponseWriter) bool {
	if h.config != nil && h.config.IsAuthDisabled() {
		RespondError(w, http.StatusForbidden, "Endpoint disabled when authentication is disabled")
		return true
	}
	return false
}

// SetupRequest represents the initial setup request
type SetupRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// LoginRequest represents a login request
type LoginRequest struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	RememberMe bool   `json:"remember_me"`
}

// ChangePasswordRequest represents a password change request
type ChangePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

type switchUser struct {
	ID       int             `json:"id"`
	Username string          `json:"username"`
	Role     models.UserRole `json:"role"`
	Current  bool            `json:"current"`
}

type addSwitchUserRequest struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	SwitchNow bool   `json:"switchNow"`
}

// Setup handles initial user setup
func (h *AuthHandler) Setup(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfAuthDisabled(w) {
		return
	}

	if h.config != nil && h.config.OIDCEnabled {
		RespondError(w, http.StatusForbidden, "Setup is disabled when OIDC is enabled")
		return
	}

	if !requireJSON(w, r) {
		return
	}

	// Check if setup is already complete
	complete, err := h.authService.IsSetupComplete(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("Failed to check setup status")
		RespondError(w, http.StatusInternalServerError, "Failed to check setup status")
		return
	}

	if complete {
		RespondError(w, http.StatusBadRequest, "Setup already completed")
		return
	}

	var req SetupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		RespondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Validate input
	if req.Username == "" || req.Password == "" {
		RespondError(w, http.StatusBadRequest, "Username and password are required")
		return
	}

	// Create user
	user, err := h.authService.SetupUser(r.Context(), req.Username, req.Password)
	if err != nil {
		log.Error().Err(err).Msg("Failed to create user")
		RespondError(w, http.StatusInternalServerError, "Failed to create user")
		return
	}

	// Create session using SCS
	// Renew token to prevent session fixation attacks
	if err := renewSessionToken(r.Context(), h.sessionManager); err != nil {
		log.Error().Err(err).Msg("Failed to renew session token")
		RespondError(w, http.StatusInternalServerError, "Failed to create session")
		return
	}

	h.startSession(r.Context(), user, false)

	RespondJSON(w, http.StatusCreated, map[string]any{
		"message": "Setup completed successfully",
		"user":    authUserResponse(user),
	})
}

// warmSession prefetches data to improve perceived performance after login.
func (h *AuthHandler) warmSession(ctx context.Context, userID int, admin bool) {
	instances, err := h.instanceStore.ListForUser(ctx, userID, admin)
	if err != nil {
		log.Error().Err(err).Msg("Failed to list instances for session warming")
		return
	}

	var activeInstances []*models.Instance
	for _, instance := range instances {
		if !instance.IsActive {
			log.Debug().
				Int("instance_id", instance.ID).
				Str("instance_name", instance.Name).
				Msg("Skipping session warmup for disabled instance")
			continue
		}
		activeInstances = append(activeInstances, instance)
	}

	if len(activeInstances) == 0 {
		log.Debug().Msg("Skipping session warmup: no active instances")
		return
	}

	// Warm instance connections concurrently
	for _, instance := range activeInstances {
		inst := instance
		go func(inst *models.Instance) {
			// Derive context from parent to respect cancellation
			warmCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()

			_, err := h.clientPool.GetClientWithTimeout(warmCtx, inst.ID, 3*time.Second)
			if err != nil {
				log.Error().
					Int("instance_id", inst.ID).
					Str("instance_name", inst.Name).
					Err(err).
					Msg("Failed to warm instance connection")
				return
			}

			log.Debug().
				Int("instance_id", inst.ID).
				Str("instance_name", inst.Name).
				Msg("Successfully warmed instance connection")
		}(inst)
	}

	// Prefetch torrent data for the first active instance
	targetInstance := activeInstances[0]

	go func() {
		warmCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		_, err := h.syncManager.GetTorrentsWithFilters(
			warmCtx,
			targetInstance.ID,
			1,
			0,
			"added_on",
			"desc",
			"",
			qbittorrent.FilterOptions{},
		)
		if err != nil {
			log.Error().
				Int("instance_id", targetInstance.ID).
				Err(err).
				Msg("Failed to prefetch torrents during session warming")
			return
		}

		log.Debug().
			Int("instance_id", targetInstance.ID).
			Msg("Successfully prefetched torrents during session warming")
	}()
}

var errTooManyLogins = errors.New("too many failed login attempts")

// checkPassword runs one password check at a time. Only a failed check
// consumes a limiter token.
func (h *AuthHandler) checkPassword(ctx context.Context, username, password string) (*models.User, error) {
	h.loginMu.Lock()
	defer h.loginMu.Unlock()

	if h.loginLimiter.Tokens() < 1 {
		return nil, errTooManyLogins
	}
	user, err := h.authService.Login(ctx, username, password)
	if errors.Is(err, auth.ErrInvalidCredentials) {
		h.loginLimiter.Allow()
	}
	return user, err
}

// Login handles user login
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfAuthDisabled(w) {
		return
	}

	if !requireJSON(w, r) {
		return
	}

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		RespondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	user, err := h.checkPassword(r.Context(), req.Username, req.Password)
	if err != nil {
		if errors.Is(err, errTooManyLogins) {
			RespondError(w, http.StatusTooManyRequests, "Too many failed login attempts, try again later")
			return
		}
		if errors.Is(err, auth.ErrInvalidCredentials) {
			RespondError(w, http.StatusUnauthorized, "Invalid credentials")
			return
		}
		if errors.Is(err, auth.ErrNotSetup) {
			RespondError(w, http.StatusPreconditionRequired, "Initial setup required")
			return
		}
		log.Error().Err(err).Msg("Login failed")
		RespondError(w, http.StatusInternalServerError, "Login failed")
		return
	}

	// Create session using SCS
	// Renew token to prevent session fixation attacks
	if err := renewSessionToken(r.Context(), h.sessionManager); err != nil {
		log.Error().Err(err).Msg("Failed to renew session token")
		RespondError(w, http.StatusInternalServerError, "Failed to create session")
		return
	}

	h.startSession(r.Context(), user, req.RememberMe)

	// Warm the session by prefetching data in the background
	// Use a detached context since this should continue even after the HTTP request completes
	go h.warmSession( //nolint:gosec // G118: session warm-up must outlive the login request
		context.Background(),
		user.ID,
		user.Role == models.UserRoleAdmin,
	)

	RespondJSON(w, http.StatusOK, map[string]any{
		"message": "Login successful",
		"user":    authUserResponse(user),
	})
}

// Logout handles user logout
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfAuthDisabled(w) {
		return
	}

	// Destroy the session
	if err := h.sessionManager.Destroy(r.Context()); err != nil {
		log.Error().Err(err).Msg("Failed to destroy session")
		RespondError(w, http.StatusInternalServerError, "Failed to logout")
		return
	}

	RespondJSON(w, http.StatusOK, map[string]string{
		"message": "Logged out successfully",
	})
}

func syntheticAdminResponse() map[string]any {
	return map[string]any{
		"id":          1,
		"role":        models.UserRoleAdmin,
		"username":    "admin",
		"auth_method": "none",
	}
}

// GetCurrentUser returns the current user information
func (h *AuthHandler) GetCurrentUser(w http.ResponseWriter, r *http.Request) {
	// When auth is disabled, return a synthetic user
	if h.config != nil && h.config.IsAuthDisabled() {
		RespondJSON(w, http.StatusOK, syntheticAdminResponse())
		return
	}

	// Check if the session is authenticated (works for both regular and OIDC auth)
	authenticated := h.sessionManager.GetBool(r.Context(), "authenticated")
	if !authenticated {
		RespondError(w, http.StatusUnauthorized, "Not authenticated")
		return
	}

	username := h.sessionManager.GetString(r.Context(), "username")
	if username == "" {
		RespondError(w, http.StatusInternalServerError, "Invalid session data")
		return
	}

	// For OIDC users, we might not have a user_id
	userID := h.sessionManager.GetInt(r.Context(), "user_id")
	authMethod := h.sessionManager.GetString(r.Context(), "auth_method")

	response := map[string]any{
		"username": username,
	}

	if user, err := h.authService.FindUser(r.Context(), username); err == nil {
		response["role"] = user.Role
		response["permissions"] = user.Permissions
	}

	// Only include ID if it exists (for built-in auth users)
	if userID != 0 {
		response["id"] = userID
	}

	// Include auth method if available
	if authMethod != "" {
		response["auth_method"] = authMethod
	}

	RespondJSON(w, http.StatusOK, response)
}

// Validate checks if the user has a valid session (used for OIDC callback)
func (h *AuthHandler) Validate(w http.ResponseWriter, r *http.Request) {
	if h.config != nil && h.config.IsAuthDisabled() {
		RespondJSON(w, http.StatusOK, syntheticAdminResponse())
		return
	}

	authenticated := h.sessionManager.GetBool(r.Context(), "authenticated")
	if !authenticated {
		RespondError(w, http.StatusUnauthorized, "Not authenticated")
		return
	}

	username := h.sessionManager.GetString(r.Context(), "username")
	userID := h.sessionManager.GetInt(r.Context(), "user_id")
	authMethod := h.sessionManager.GetString(r.Context(), "auth_method")
	profilePicture := h.sessionManager.GetString(r.Context(), "profile_picture")

	response := map[string]any{
		"username":        username,
		"auth_method":     authMethod,
		"profile_picture": profilePicture,
	}
	if userID > 0 {
		response["id"] = userID
		if user, err := h.authService.GetUser(r.Context(), userID); err == nil {
			response["role"] = user.Role
			response["permissions"] = user.Permissions
		}
	}

	RespondJSON(w, http.StatusOK, response)
}

// CheckSetupRequired checks if initial setup is required
func (h *AuthHandler) CheckSetupRequired(w http.ResponseWriter, r *http.Request) {
	if h.config != nil && (h.config.IsAuthDisabled() || h.config.OIDCEnabled) {
		RespondJSON(w, http.StatusOK, map[string]any{
			"setupRequired": false,
		})
		return
	}

	complete, err := h.authService.IsSetupComplete(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("Failed to check setup status")
		RespondError(w, http.StatusInternalServerError, "Failed to check setup status")
		return
	}

	RespondJSON(w, http.StatusOK, map[string]any{
		"setupRequired": !complete,
	})
}

// ChangePassword handles password change requests
func (h *AuthHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfAuthDisabled(w) {
		return
	}

	var req ChangePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		RespondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	userID := currentUserID(r)
	if err := h.authService.ChangePasswordForUser(r.Context(), userID, req.CurrentPassword, req.NewPassword); err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			RespondError(w, http.StatusUnauthorized, "Invalid current password")
			return
		}
		log.Error().Err(err).Msg("Failed to change password")
		RespondError(w, http.StatusInternalServerError, "Failed to change password")
		return
	}

	// Revoke this account on every browser without signing out other users.
	err := h.sessionManager.Iterate(r.Context(), func(ctx context.Context) error {
		if h.sessionManager.GetInt(ctx, "user_id") == userID {
			return h.sessionManager.Destroy(ctx)
		}
		return nil
	})
	if err == nil {
		err = h.sessionManager.Destroy(r.Context())
	}
	if err != nil {
		log.Error().Err(err).Msg("Failed to end sessions after password change")
		RespondError(w, http.StatusInternalServerError, "Password changed, but ending sessions failed")
		return
	}

	RespondJSON(w, http.StatusOK, map[string]string{
		"message": "Password changed successfully",
	})
}

func authUserResponse(user *models.User) map[string]any {
	return map[string]any{
		"id":          user.ID,
		"username":    user.Username,
		"role":        user.Role,
		"permissions": user.Permissions,
	}
}

func (h *AuthHandler) startSession(ctx context.Context, user *models.User, rememberMe bool) {
	h.sessionManager.Put(ctx, "authenticated", true)
	h.sessionManager.Put(ctx, "user_id", user.ID)
	h.sessionManager.Put(ctx, "username", user.Username)
	h.sessionManager.Put(ctx, "auth_method", "password")
	h.sessionManager.RememberMe(ctx, rememberMe)
}

func (h *AuthHandler) requireSession(w http.ResponseWriter, r *http.Request) bool {
	if h.sessionManager.GetBool(r.Context(), "authenticated") {
		return true
	}
	RespondError(w, http.StatusUnauthorized, "Session authentication required")
	return false
}

// ListSwitchUsers returns all accounts to administrators. Standard users see
// their current account; the browser merges locally saved accounts into this
// list and verifies their credentials through AddSwitchUser when selected.
func (h *AuthHandler) ListSwitchUsers(w http.ResponseWriter, r *http.Request) {
	if !h.requireSession(w, r) {
		return
	}
	currentID := currentUserID(r)
	current, err := h.authService.GetUser(r.Context(), currentID)
	if err != nil {
		RespondError(w, http.StatusUnauthorized, "Not authenticated")
		return
	}

	if isAdmin(r) {
		accounts, err := h.authService.ListUsers(r.Context())
		if err != nil {
			RespondError(w, http.StatusInternalServerError, "Failed to list users")
			return
		}
		response := make([]switchUser, 0, len(accounts))
		for _, account := range accounts {
			response = append(response, switchUser{
				ID: account.ID, Username: account.Username, Role: account.Role,
				Current: account.ID == currentID,
			})
		}
		RespondJSON(w, http.StatusOK, response)
		return
	}

	RespondJSON(w, http.StatusOK, []switchUser{{
		ID: current.ID, Username: current.Username, Role: current.Role, Current: true,
	}})
}

// AddSwitchUser verifies credentials and optionally switches the current
// session. The browser stores any credentials it wants to reuse locally.
func (h *AuthHandler) AddSwitchUser(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfAuthDisabled(w) {
		return
	}
	if !h.requireSession(w, r) || !requireJSON(w, r) {
		return
	}

	var req addSwitchUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Username == "" || req.Password == "" {
		RespondError(w, http.StatusBadRequest, "Username and password are required")
		return
	}
	user, err := h.checkPassword(r.Context(), req.Username, req.Password)
	if err != nil {
		if errors.Is(err, errTooManyLogins) {
			RespondError(w, http.StatusTooManyRequests, "Too many failed login attempts, try again later")
			return
		}
		if errors.Is(err, auth.ErrInvalidCredentials) {
			RespondError(w, http.StatusUnauthorized, "Invalid credentials")
			return
		}
		RespondError(w, http.StatusInternalServerError, "Failed to verify credentials")
		return
	}

	if req.SwitchNow {
		if err := renewSessionToken(r.Context(), h.sessionManager); err != nil {
			log.Error().Err(err).Msg("Failed to renew session token")
			RespondError(w, http.StatusInternalServerError, "Failed to create session")
			return
		}
		h.startSession(r.Context(), user, true)
		go h.warmSession(context.Background(), user.ID, user.Role == models.UserRoleAdmin) //nolint:gosec // session warm-up must outlive the request
	}

	RespondJSON(w, http.StatusOK, authUserResponse(user))
}

// API Key Management

// CreateAPIKeyRequest represents a request to create an API key
type CreateAPIKeyRequest struct {
	Name string `json:"name"`
}

// CreateAPIKey creates a new API key
func (h *AuthHandler) CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfAuthDisabled(w) {
		return
	}

	var req CreateAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		RespondError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Name == "" {
		RespondError(w, http.StatusBadRequest, "API key name is required")
		return
	}

	// Create API key
	rawKey, apiKey, err := h.authService.CreateAPIKeyForUser(r.Context(), currentUserID(r), req.Name)
	if err != nil {
		log.Error().Err(err).Msg("Failed to create API key")
		RespondError(w, http.StatusInternalServerError, "Failed to create API key")
		return
	}

	RespondJSON(w, http.StatusCreated, map[string]any{
		"id":        apiKey.ID,
		"name":      apiKey.Name,
		"key":       rawKey, // Only shown once
		"createdAt": apiKey.CreatedAt,
		"message":   "Save this key securely - it will not be shown again",
	})
}

// ListAPIKeys returns all API keys
func (h *AuthHandler) ListAPIKeys(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfAuthDisabled(w) {
		return
	}

	keys, err := h.authService.ListAPIKeysForUser(r.Context(), currentUserID(r), isAdmin(r))
	if err != nil {
		log.Error().Err(err).Msg("Failed to list API keys")
		RespondError(w, http.StatusInternalServerError, "Failed to list API keys")
		return
	}

	RespondJSON(w, http.StatusOK, keys)
}

// DeleteAPIKey deletes an API key
func (h *AuthHandler) DeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	if h.rejectIfAuthDisabled(w) {
		return
	}

	// Get API key ID from URL parameter
	idStr := chi.URLParam(r, "id")
	if idStr == "" {
		RespondError(w, http.StatusBadRequest, "API key ID is required")
		return
	}

	id, err := strconv.Atoi(idStr)
	if err != nil {
		RespondError(w, http.StatusBadRequest, "Invalid API key ID")
		return
	}

	if err := h.authService.DeleteAPIKeyForUser(r.Context(), id, currentUserID(r), isAdmin(r)); err != nil {
		if errors.Is(err, models.ErrAPIKeyNotFound) {
			RespondError(w, http.StatusNotFound, "API key not found")
			return
		}
		log.Error().Err(err).Msg("Failed to delete API key")
		RespondError(w, http.StatusInternalServerError, "Failed to delete API key")
		return
	}

	RespondJSON(w, http.StatusOK, map[string]string{
		"message": "API key deleted successfully",
	})
}
