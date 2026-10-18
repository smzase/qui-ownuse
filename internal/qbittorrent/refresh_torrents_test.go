// Copyright (c) 2026, s0up and the autobrr contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package qbittorrent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	qbt "github.com/autobrr/go-qbittorrent"
	"github.com/stretchr/testify/require"

	"github.com/autobrr/qui/internal/models"
)

func TestRefreshTorrentsReplacesIncompleteBaseline(t *testing.T) {
	var repaired atomic.Bool
	var fullReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/app/webapiVersion":
			_, _ = w.Write([]byte("2.9.0"))
		case "/api/v2/app/version":
			_, _ = w.Write([]byte("v4.6.0"))
		case "/api/v2/sync/maindata":
			data := qbt.MainData{Rid: 7}
			if r.URL.Query().Get("rid") == "0" || r.URL.Query().Get("rid") == "" {
				fullReads.Add(1)
				data.FullUpdate = true
				data.Torrents = map[string]qbt.Torrent{"a": {Hash: "a", Name: ""}, "stale": {Hash: "stale", Name: "Removed fixture"}}
				if repaired.Load() {
					data.Torrents = map[string]qbt.Torrent{"a": {Hash: "a", Name: "Complete fixture"}, "b": {Hash: "b", Name: "New fixture"}}
				}
			}
			_ = json.NewEncoder(w).Encode(data)
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()
	pool := setupTestPool(t)
	defer pool.Close()
	instance, err := pool.instanceStore.Create(t.Context(), models.ClientTypeQbittorrent, "Refresh fixture", server.URL, "", "", nil, nil, false, nil)
	require.NoError(t, err)
	manager := NewSyncManager(pool, nil)
	client, err := pool.GetClient(t.Context(), instance.ID)
	require.NoError(t, err)
	original := client.GetSyncManager()
	repaired.Store(true)
	require.NoError(t, original.Sync(t.Context()))
	row, ok := original.GetTorrent("a")
	require.True(t, ok)
	require.Empty(t, row.Name, "ordinary incremental sync must reproduce the stale row")
	require.EqualValues(t, 1, fullReads.Load())

	require.NoError(t, manager.RefreshTorrents(t.Context(), instance.ID))
	client, err = pool.GetClientOffline(t.Context(), instance.ID)
	require.NoError(t, err)
	require.NotSame(t, original, client.GetSyncManager())
	require.EqualValues(t, 2, fullReads.Load())
	rows := client.GetSyncManager().GetTorrentsUnchecked(qbt.TorrentFilterOptions{})
	require.ElementsMatch(t, []qbt.Torrent{{Hash: "a", Name: "Complete fixture"}, {Hash: "b", Name: "New fixture"}}, rows)
}

func TestRefreshTorrentsTransmission(t *testing.T) {
	var refreshed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		arguments := map[string]any{}
		switch request.Method {
		case "session-get":
			arguments = map[string]any{"version": "4.0.6", "rpc-version": 17, "download-dir": "/data"}
		case "torrent-get":
			name := "Before refresh"
			if refreshed.Load() {
				name = "After refresh"
			}
			arguments["torrents"] = []map[string]any{{"id": 1, "hashString": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "name": name, "status": 6, "totalSize": 100, "sizeWhenDone": 100, "percentDone": 1}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "arguments": arguments})
	}))
	defer server.Close()
	pool := setupTestPool(t)
	defer pool.Close()
	instance, err := pool.instanceStore.Create(t.Context(), models.ClientTypeTransmission, "RPC refresh fixture", server.URL, "", "", nil, nil, false, nil)
	require.NoError(t, err)
	manager := NewSyncManager(pool, nil)
	before, err := pool.GetClient(t.Context(), instance.ID)
	require.NoError(t, err)
	refreshed.Store(true)
	require.NoError(t, manager.RefreshTorrents(t.Context(), instance.ID))
	after, err := pool.GetClientOffline(t.Context(), instance.ID)
	require.NoError(t, err)
	require.NotSame(t, before, after)
	rows := after.GetSyncManager().GetTorrentsUnchecked(qbt.TorrentFilterOptions{})
	require.Len(t, rows, 1)
	require.Equal(t, "After refresh", rows[0].Name)
}
