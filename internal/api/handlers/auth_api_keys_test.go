// Copyright (c) 2025-2026, s0up and the autobrr contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/autobrr/qui/internal/api/ctxkeys"
	"github.com/autobrr/qui/internal/auth"
	"github.com/autobrr/qui/internal/models"
	"github.com/autobrr/qui/internal/testutil/testdb"
)

type apiKeyResponse struct {
	ID          int                 `json:"id"`
	Name        string              `json:"name"`
	Permissions []models.Permission `json:"permissions"`
	InstanceIDs []int               `json:"instanceIds"`
}

func TestAPIKeyScopesKeepForkOwnership(t *testing.T) {
	db := testdb.NewMigratedSQLite(t, "api-key-ownership")
	service := auth.NewService(db)
	_, err := service.SetupUser(t.Context(), "admin", "fixture-password")
	require.NoError(t, err)
	alice, err := service.CreateManagedUser(t.Context(), "alice", "fixture-password", models.UserRoleUser, nil)
	require.NoError(t, err)
	bob, err := service.CreateManagedUser(t.Context(), "bob", "fixture-password", models.UserRoleUser, nil)
	require.NoError(t, err)
	instances, err := models.NewInstanceStore(db, []byte("01234567890123456789012345678901"))
	require.NoError(t, err)
	instance, err := instances.Create(t.Context(), models.ClientTypeTransmission, "fixture", "http://127.0.0.1:1", "", "", nil, nil, false, nil)
	require.NoError(t, err)
	require.NoError(t, instances.SetOwner(t.Context(), instance.ID, alice.ID))
	h := &AuthHandler{authService: service, instanceStore: instances}
	router := chi.NewRouter()
	router.Get("/keys", h.ListAPIKeys)
	router.Post("/keys", h.CreateAPIKey)
	router.Put("/keys/{id}", h.UpdateAPIKey)
	router.Delete("/keys/{id}", h.DeleteAPIKey)
	send := func(userID int, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		ctx := context.WithValue(req.Context(), ctxkeys.UserID, userID)
		ctx = context.WithValue(ctx, ctxkeys.UserRole, string(models.UserRoleUser))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req.WithContext(ctx))
		return rec
	}
	body := fmt.Sprintf(`{"name":"alice reader","permissions":[{"area":"torrents","level":"read"}],"instanceIds":[%d]}`, instance.ID)
	denied := send(bob.ID, http.MethodPost, "/keys", body)
	require.Equal(t, http.StatusBadRequest, denied.Code, denied.Body.String())
	created := send(alice.ID, http.MethodPost, "/keys", body)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	var key struct {
		ID  int    `json:"id"`
		Key string `json:"key"`
	}
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &key))
	stored, err := service.ValidateAPIKey(t.Context(), key.Key)
	require.NoError(t, err)
	require.Equal(t, alice.ID, stored.UserID)
	require.Equal(t, []int{instance.ID}, stored.InstanceIDs)
	require.True(t, stored.Allows(models.AreaTorrents, models.LevelRead))
	require.False(t, stored.Allows(models.AreaTorrents, models.LevelWrite))
	path := fmt.Sprintf("/keys/%d", key.ID)
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		denied = send(bob.ID, method, path, `{"name":"takeover","permissions":[{"area":"torrents","level":"write"}]}`)
		require.Equal(t, http.StatusNotFound, denied.Code, denied.Body.String())
	}
	listed := send(bob.ID, http.MethodGet, "/keys", "")
	require.Equal(t, http.StatusOK, listed.Code)
	require.JSONEq(t, "[]", listed.Body.String())
	updated := send(alice.ID, http.MethodPut, path, body)
	require.Equal(t, http.StatusOK, updated.Code, updated.Body.String())
	stored, err = service.ValidateAPIKey(t.Context(), key.Key)
	require.NoError(t, err)
	require.Equal(t, alice.ID, stored.UserID)
	require.False(t, stored.Allows(models.AreaTorrents, models.LevelWrite))
}

func TestAPIKeyCreateUpdateList(t *testing.T) {
	db := testdb.NewMigratedSQLite(t, "api-key-routes")
	instances, err := models.NewInstanceStore(db, []byte("01234567890123456789012345678901"))
	require.NoError(t, err)
	inst, err := instances.Create(t.Context(), models.ClientTypeQbittorrent, "first", "http://127.0.0.1:1", "admin", "secret", nil, nil, false, nil)
	require.NoError(t, err)
	h := &AuthHandler{authService: auth.NewService(db), instanceStore: instances}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), ctxkeys.UserID, 1)
			ctx = context.WithValue(ctx, ctxkeys.UserRole, string(models.UserRoleAdmin))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	router.Get("/api-keys", h.ListAPIKeys)
	router.Post("/api-keys", h.CreateAPIKey)
	router.Put("/api-keys/{id}", h.UpdateAPIKey)

	send := func(t *testing.T, method, path, body string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body)))
		return rec.Code, rec.Body.String()
	}
	decode := func(t *testing.T, body string, v any) {
		t.Helper()
		require.NoError(t, json.Unmarshal([]byte(body), v))
	}

	status, body := send(t, http.MethodPost, "/api-keys", `{"name":"old dialog"}`)
	require.Equal(t, http.StatusCreated, status, body)
	var created apiKeyResponse
	decode(t, body, &created)
	require.Equal(t, models.AllPermissions(), created.Permissions, "a create with no Permission list gets every Permission while the switch is on")
	require.Equal(t, []int{}, created.InstanceIDs)

	invalid := map[string]string{
		"empty Permission list": `{"name":"k","permissions":[]}`,
		"invalid instance id":   `{"name":"k","permissions":[{"area":"torrents","level":"read"}],"instanceIds":[1,0]}`,
		"unknown instance id":   `{"name":"k","permissions":[{"area":"torrents","level":"read"}],"instanceIds":[1,2]}`,
		"missing name":          `{"permissions":[{"area":"torrents","level":"read"}]}`,
	}
	for name, req := range invalid {
		status, body := send(t, http.MethodPost, "/api-keys", req)
		require.Equal(t, http.StatusBadRequest, status, "create, %s: %s", name, body)
		status, body = send(t, http.MethodPut, "/api-keys/1", req)
		require.Equal(t, http.StatusBadRequest, status, "update, %s: %s", name, body)
	}

	status, body = send(t, http.MethodPut, "/api-keys/1",
		`{"name":"dashboard","permissions":[{"area":"torrents","level":"read"}],"instanceIds":[1]}`)
	require.Equal(t, http.StatusOK, status, body)

	status, body = send(t, http.MethodGet, "/api-keys", "")
	require.Equal(t, http.StatusOK, status, body)
	var listed []apiKeyResponse
	decode(t, body, &listed)
	require.Equal(t, []apiKeyResponse{{
		ID:          created.ID,
		Name:        "dashboard",
		Permissions: []models.Permission{{Area: models.AreaTorrents, Level: models.LevelRead}},
		InstanceIDs: []int{inst.ID},
	}}, listed)

	require.NoError(t, instances.Delete(t.Context(), inst.ID))
	status, body = send(t, http.MethodPut, "/api-keys/1",
		`{"name":"dashboard","permissions":[{"area":"torrents","level":"read"}],"instanceIds":[1]}`)
	require.Equal(t, http.StatusOK, status, "an edit keeps a deleted instance in the limit: %s", body)
	status, body = send(t, http.MethodPost, "/api-keys", `{"name":"k","permissions":[{"area":"torrents","level":"read"}],"instanceIds":[1]}`)
	require.Equal(t, http.StatusBadRequest, status, "a new key cannot name a deleted instance: %s", body)

	status, _ = send(t, http.MethodPut, "/api-keys/99", `{"name":"k","permissions":[{"area":"torrents","level":"read"}]}`)
	require.Equal(t, http.StatusNotFound, status)

	t.Run("switch off", func(t *testing.T) {
		createWithoutPermissionsGetsAll = false
		t.Cleanup(func() { createWithoutPermissionsGetsAll = true })

		status, body := send(t, http.MethodPost, "/api-keys", `{"name":"old dialog"}`)
		require.Equal(t, http.StatusBadRequest, status, body)
	})
}
