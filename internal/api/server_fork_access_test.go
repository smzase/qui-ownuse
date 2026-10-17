// Copyright (c) 2026, s0up and the autobrr contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/autobrr/qui/internal/api/ctxkeys"
	"github.com/autobrr/qui/internal/auth"
	"github.com/autobrr/qui/internal/models"
	"github.com/autobrr/qui/internal/testutil/testdb"
)

func TestUncompressedRoutesKeepMultiUserAccess(t *testing.T) {
	deps := newTestDependencies(t)
	db := testdb.NewMigratedSQLite(t, "uncompressed-access")
	deps.AuthService = auth.NewService(db)
	_, err := deps.AuthService.SetupUser(t.Context(), "admin", "password1234")
	require.NoError(t, err)
	alice, err := deps.AuthService.CreateManagedUser(t.Context(), "alice", "password1234", models.UserRoleUser, nil)
	require.NoError(t, err)
	bob, err := deps.AuthService.CreateManagedUser(t.Context(), "bob", "password1234", models.UserRoleUser, nil)
	require.NoError(t, err)
	deps.InstanceStore, err = models.NewInstanceStore(db, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	instance, err := deps.InstanceStore.Create(t.Context(), models.ClientTypeTransmission, "fixture", "http://127.0.0.1:1", "", "", nil, nil, false, nil)
	require.NoError(t, err)
	require.NoError(t, deps.InstanceStore.SetOwner(t.Context(), instance.ID, alice.ID))
	key, _, err := deps.AuthService.CreateAPIKeyForUser(t.Context(), alice.ID, "fixture")
	require.NoError(t, err)
	bobKey, _, err := deps.AuthService.CreateAPIKeyForUser(t.Context(), bob.ID, "fixture")
	require.NoError(t, err)
	ctx, err := deps.SessionManager.Load(t.Context(), "")
	require.NoError(t, err)
	deps.SessionManager.Put(ctx, "authenticated", true)
	deps.SessionManager.Put(ctx, "username", alice.Username)
	deps.SessionManager.Put(ctx, "user_id", alice.ID)
	token, _, err := deps.SessionManager.Commit(ctx)
	require.NoError(t, err)
	router, err := NewServer(deps).Handler()
	require.NoError(t, err)

	for _, cookieAuth := range []bool{false, true} {
		for _, path := range []string{
			"/api/stream",
			fmt.Sprintf("/api/instances/%d/rss/events", instance.ID),
			fmt.Sprintf("/api/instances/%d/torrents/sample/files/0/download", instance.ID),
		} {
			t.Run(fmt.Sprintf("%s/cookie=%t", path, cookieAuth), func(t *testing.T) {
				handler := withRouteRegisteredMiddleware(t, router, path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					assert.Equal(t, alice.ID, r.Context().Value(ctxkeys.UserID))
					assert.Equal(t, "user", r.Context().Value(ctxkeys.UserRole))
					w.WriteHeader(http.StatusNoContent)
				}))
				req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
				if cookieAuth {
					req.AddCookie(&http.Cookie{Name: deps.SessionManager.Cookie.Name, Value: token})
				} else {
					req.Header.Set("X-API-Key", key)
				}
				rec := httptest.NewRecorder()
				deps.SessionManager.LoadAndSave(handler).ServeHTTP(rec, req)
				require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
			})
		}
	}
	for _, path := range []string{
		fmt.Sprintf("/api/instances/%d/rss/events", instance.ID),
		fmt.Sprintf("/api/instances/%d/torrents/sample/files/0/download", instance.ID),
		"/api/logs/stream",
	} {
		handler := withRouteRegisteredMiddleware(t, router, path, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("unauthorized request reached the handler")
		}))
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		req.Header.Set("X-API-Key", bobKey)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if path == "/api/logs/stream" {
			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		} else {
			require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		}
	}
	require.NoError(t, deps.AuthService.UpdateUserPermissions(t.Context(), alice.ID, []models.UserPermission{models.PermissionManageLogs}))
	handler := withRouteRegisteredMiddleware(t, router, "/api/logs/stream", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/logs/stream", nil)
	req.Header.Set("X-API-Key", key)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
}
