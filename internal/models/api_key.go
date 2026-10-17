// Copyright (c) 2025-2026, s0up and the autobrr contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package models

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/autobrr/qui/internal/dbinterface"
)

var ErrAPIKeyNotFound = errors.New("api key not found")
var ErrInvalidAPIKey = errors.New("invalid api key")

type APIKey struct {
	ID         int        `json:"id"`
	UserID     int        `json:"-"`
	KeyHash    string     `json:"-"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

type APIKeyStore struct {
	db dbinterface.Querier
}

func NewAPIKeyStore(db dbinterface.Querier) *APIKeyStore {
	return &APIKeyStore{db: db}
}

// GenerateAPIKey generates a new API key
func GenerateAPIKey() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// HashAPIKey creates a SHA256 hash of the API key
func HashAPIKey(key string) string {
	hash := sha256.Sum256([]byte(key))
	return hex.EncodeToString(hash[:])
}

func (s *APIKeyStore) Create(ctx context.Context, name string) (string, *APIKey, error) {
	return s.CreateForUser(ctx, 1, name)
}

func (s *APIKeyStore) CreateForUser(ctx context.Context, userID int, name string) (string, *APIKey, error) {
	// Generate new API key
	rawKey, err := GenerateAPIKey()
	if err != nil {
		return "", nil, fmt.Errorf("failed to generate API key: %w", err)
	}

	// Hash the key for storage
	keyHash := HashAPIKey(rawKey)

	// Start a transaction
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Intern the name
	ids, err := dbinterface.InternStringNullable(ctx, tx, &name)
	if err != nil {
		return "", nil, fmt.Errorf("failed to intern name: %w", err)
	}

	// Insert the API key
	apiKey := &APIKey{}
	var createdAt, lastUsedAt sql.NullTime
	err = tx.QueryRowContext(ctx, `
		INSERT INTO api_keys (key_hash, name_id, user_id)
		VALUES (?, ?, ?)
		RETURNING id, key_hash, user_id, created_at, last_used_at
	`, keyHash, ids[0], userID).Scan(
		&apiKey.ID,
		&apiKey.KeyHash,
		&apiKey.UserID,
		&createdAt,
		&lastUsedAt,
	)

	if err != nil {
		return "", nil, err
	}

	if err = tx.Commit(); err != nil {
		return "", nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	apiKey.Name = name
	apiKey.CreatedAt = createdAt.Time
	if lastUsedAt.Valid {
		apiKey.LastUsedAt = &lastUsedAt.Time
	}

	// Return both the raw key (to show user once) and the model
	return rawKey, apiKey, nil
}

func (s *APIKeyStore) GetByHash(ctx context.Context, keyHash string) (*APIKey, error) {
	query := `
		SELECT ak.id, ak.key_hash, ak.user_id, sp.value, ak.created_at, ak.last_used_at
		FROM api_keys ak
		INNER JOIN string_pool sp ON ak.name_id = sp.id
		WHERE ak.key_hash = ?
	`

	var id int
	var keyHashResult, name string
	var userID int
	var createdAt sql.NullTime
	var lastUsedAt sql.NullTime

	err := s.db.QueryRowContext(ctx, query, keyHash).Scan(
		&id,
		&keyHashResult,
		&userID,
		&name,
		&createdAt,
		&lastUsedAt,
	)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrAPIKeyNotFound
		}

		return nil, err
	}

	apiKey := &APIKey{
		ID:        id,
		UserID:    userID,
		KeyHash:   keyHashResult,
		Name:      name,
		CreatedAt: createdAt.Time,
	}

	if lastUsedAt.Valid {
		apiKey.LastUsedAt = &lastUsedAt.Time
	}

	return apiKey, nil
}

func (s *APIKeyStore) List(ctx context.Context) ([]*APIKey, error) {
	return s.list(ctx, `
		SELECT ak.id, ak.key_hash, ak.user_id, sp.value, ak.created_at, ak.last_used_at
		FROM api_keys ak
		INNER JOIN string_pool sp ON ak.name_id = sp.id
		ORDER BY ak.created_at DESC
	`)
}

func (s *APIKeyStore) ListForUser(ctx context.Context, userID int, admin bool) ([]*APIKey, error) {
	if admin {
		return s.List(ctx)
	}

	return s.list(ctx, `
		SELECT ak.id, ak.key_hash, ak.user_id, sp.value, ak.created_at, ak.last_used_at
		FROM api_keys ak
		INNER JOIN string_pool sp ON ak.name_id = sp.id
		WHERE ak.user_id = ?
		ORDER BY ak.created_at DESC
	`, userID)
}

func (s *APIKeyStore) list(ctx context.Context, query string, args ...any) ([]*APIKey, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	keys := make([]*APIKey, 0)
	for rows.Next() {
		var id int
		var keyHash, name string
		var userID int
		var createdAt sql.NullTime
		var lastUsedAt sql.NullTime

		err := rows.Scan(
			&id,
			&keyHash,
			&userID,
			&name,
			&createdAt,
			&lastUsedAt,
		)
		if err != nil {
			return nil, err
		}

		apiKey := &APIKey{
			ID:        id,
			UserID:    userID,
			KeyHash:   keyHash,
			Name:      name,
			CreatedAt: createdAt.Time,
		}

		if lastUsedAt.Valid {
			apiKey.LastUsedAt = &lastUsedAt.Time
		}

		keys = append(keys, apiKey)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	return keys, nil
}

func (s *APIKeyStore) UpdateLastUsed(ctx context.Context, id int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	query := `
		UPDATE api_keys 
		SET last_used_at = ?
		WHERE id = ?
	`

	// Bind UTC: CURRENT_TIMESTAMP would be the Postgres session's local time, and the column has no zone.
	result, err := tx.ExecContext(ctx, query, time.Now().UTC(), id)
	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if rows == 0 {
		return ErrAPIKeyNotFound
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

func (s *APIKeyStore) Delete(ctx context.Context, id int) error {
	return s.delete(ctx, id, 0, true)
}

func (s *APIKeyStore) DeleteForUser(ctx context.Context, id, userID int, admin bool) error {
	return s.delete(ctx, id, userID, admin)
}

func (s *APIKeyStore) delete(ctx context.Context, id, userID int, admin bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	query := `DELETE FROM api_keys WHERE id = ?`
	args := []any{id}
	if !admin {
		query += ` AND user_id = ?`
		args = append(args, userID)
	}

	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if rows == 0 {
		return ErrAPIKeyNotFound
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// ValidateAPIKey validates a raw API key and returns the associated APIKey if valid
func (s *APIKeyStore) ValidateAPIKey(ctx context.Context, rawKey string) (*APIKey, error) {
	keyHash := HashAPIKey(rawKey)

	apiKey, err := s.GetByHash(ctx, keyHash)
	if err != nil {
		if errors.Is(err, ErrAPIKeyNotFound) {
			return nil, ErrInvalidAPIKey
		}
		return nil, err
	}

	// autobrr sends API key requests often, so the write happens at most once a
	// minute, and it outlives the request that triggered it. Concurrent requests
	// that all see an old time each write once. A negative age is a row that older
	// qui wrote as local time on a Postgres session east of UTC; write then.
	age := time.Duration(-1)
	if apiKey.LastUsedAt != nil {
		age = time.Since(*apiKey.LastUsedAt)
	}
	if age < 0 || age >= time.Minute {
		go func() {
			writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := s.UpdateLastUsed(writeCtx, apiKey.ID); err != nil {
				log.Warn().Err(err).Int("keyId", apiKey.ID).Msg("failed to update API key last used timestamp")
			}
		}()
	}

	return apiKey, nil
}
