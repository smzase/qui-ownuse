// Copyright (c) 2026, s0up and the autobrr contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/autobrr/qui/internal/api/ctxkeys"
	"github.com/autobrr/qui/internal/models"
)

func TestCrossSeedRoutesKeepInstanceAccess(t *testing.T) {
	f := newRouteFixture(t)
	owner, err := f.users.CreateWithRole(t.Context(), "owner", "fixture", models.UserRoleUser)
	require.NoError(t, err)
	viewer, err := f.users.CreateWithRole(t.Context(), "viewer", "fixture", models.UserRoleUser)
	require.NoError(t, err)
	source := f.source("source", models.FilesystemModeLocal, t.TempDir())
	target := f.instance("target", f.qbt, models.FilesystemModeNone)
	for _, id := range []int{source.ID, target.ID} {
		require.NoError(t, f.instances.SetOwner(t.Context(), id, owner.ID))
	}
	route := &models.CrossSeedRoute{SourceInstanceID: source.ID, CrossSeedInstanceID: target.ID, Rows: []models.CrossSeedRouteRow{{BaseDir: source.HardlinkBaseDir}}}
	require.NoError(t, f.routes.Upsert(t.Context(), route))
	ctx := context.WithValue(t.Context(), ctxkeys.UserID, viewer.ID)
	ctx = context.WithValue(ctx, ctxkeys.UserRole, "user")
	request := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var payload strings.Builder
		if body != nil {
			require.NoError(t, json.NewEncoder(&payload).Encode(body))
		}
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(payload.String())))
		return rec
	}
	root := "/api/cross-seed/routes/"
	sourcePath := root + strconv.Itoa(source.ID)
	body := crossSeedRouteRequest{CrossSeedInstanceID: target.ID, Rows: route.Rows}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		require.Equal(t, http.StatusNotFound, request(method, sourcePath, body).Code)
	}
	require.NoError(t, f.instances.Share(t.Context(), source.ID, viewer.ID, owner.ID))
	rec := request(http.MethodGet, root, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, "[]", rec.Body.String())
	for _, tc := range []struct{ method, suffix string }{{http.MethodGet, ""}, {http.MethodPut, ""}, {http.MethodPost, "/test"}} {
		rec := request(tc.method, sourcePath+tc.suffix, body)
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	}
	handler := &CrossSeedHandler{instanceStore: f.instances, routes: f.routes}
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/cross-seed/apply", nil)
	_, err = handler.scopeCrossSeedInstanceIDs(req, nil)
	require.ErrorIs(t, err, errCrossSeedInstanceForbidden)
	_, err = handler.scopeCrossSeedInstanceIDs(req, []int{source.ID})
	require.ErrorIs(t, err, errCrossSeedInstanceForbidden)
	rec = httptest.NewRecorder()
	payload, err := json.Marshal(map[string]any{"instanceIds": []int{source.ID}})
	require.NoError(t, err)
	handler.AutobrrApply(rec, httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/cross-seed/apply", strings.NewReader(string(payload))))
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

	require.NoError(t, f.instances.Share(t.Context(), target.ID, viewer.ID, owner.ID))
	rec = request(http.MethodGet, root, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var visible []*models.CrossSeedRoute
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &visible))
	require.Len(t, visible, 1)
	require.Equal(t, target.ID, visible[0].CrossSeedInstanceID)
	rec = request(http.MethodGet, sourcePath, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = request(http.MethodPut, sourcePath, body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	scoped, err := handler.scopeCrossSeedInstanceIDs(req, nil)
	require.NoError(t, err)
	require.ElementsMatch(t, []int{source.ID, target.ID}, scoped)

	require.NoError(t, f.instances.Unshare(t.Context(), target.ID, viewer.ID))
	rec = request(http.MethodDelete, sourcePath, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	saved, err := f.routes.CrossSeedRouteStore.Get(t.Context(), source.ID)
	require.NoError(t, err)
	require.Nil(t, saved)
}
