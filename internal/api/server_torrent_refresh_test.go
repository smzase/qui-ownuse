// Copyright (c) 2026, s0up and the autobrr contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/autobrr/qui/internal/auth"
	"github.com/autobrr/qui/internal/models"
	"github.com/autobrr/qui/internal/testutil/testdb"
)

func TestTorrentRefreshRequiresInstanceAccess(t *testing.T) {
	deps := newTestDependencies(t)
	db := testdb.NewMigratedSQLite(t, "torrent-refresh-access")
	deps.AuthService = auth.NewService(db)
	owner, err := deps.AuthService.SetupUser(t.Context(), "owner", "password1234")
	require.NoError(t, err)
	other, err := deps.AuthService.CreateManagedUser(t.Context(), "other", "password1234", models.UserRoleUser, nil)
	require.NoError(t, err)
	deps.InstanceStore, err = models.NewInstanceStore(db, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	instance, err := deps.InstanceStore.Create(t.Context(), models.ClientTypeTransmission, "Refresh fixture", "http://127.0.0.1:1", "", "", nil, nil, false, nil)
	require.NoError(t, err)
	require.NoError(t, deps.InstanceStore.SetOwner(t.Context(), instance.ID, owner.ID))
	key, _, err := deps.AuthService.CreateAPIKeyForUser(t.Context(), other.ID, "refresh fixture")
	require.NoError(t, err)
	router, err := NewServer(deps).Handler()
	require.NoError(t, err)
	var handler http.Handler
	require.NoError(t, chi.Walk(router, func(method, route string, _ http.Handler, middleware ...func(http.Handler) http.Handler) error {
		if method == http.MethodPost && route == "/api/instances/{instanceID}/torrents/refresh" {
			handler = chi.Chain(middleware...).HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
		}
		return nil
	}))
	require.NotNil(t, handler)
	request := func(apiKey string) int {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, fmt.Sprintf("/api/instances/%d/torrents/refresh", instance.ID), nil)
		req.Header.Set("X-API-Key", apiKey)
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		routeContext := chi.NewRouteContext()
		routeContext.URLParams.Add("instanceID", strconv.Itoa(instance.ID))
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeContext))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	require.Equal(t, http.StatusForbidden, request(""))
	require.Equal(t, http.StatusUnauthorized, request("invalid-api-key"))
	require.Equal(t, http.StatusNotFound, request(key))
	require.NoError(t, deps.InstanceStore.Share(t.Context(), instance.ID, other.ID, owner.ID))
	require.Equal(t, http.StatusNoContent, request(key), "a shared user can refresh without admin permissions")
}
