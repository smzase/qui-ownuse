// Copyright (c) 2026, s0up and the autobrr contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package qbittorrent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/autobrr/qui/internal/models"
)

func TestTransmissionPreferencesRefreshTheSharedView(t *testing.T) {
	var downloadDir atomic.Value
	downloadDir.Store("/data/before")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method    string         `json:"method"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		arguments := map[string]any{}
		switch request.Method {
		case "session-get":
			arguments = map[string]any{"version": "4.0.6", "rpc-version": 17, "download-dir": downloadDir.Load()}
		case "session-set":
			downloadDir.Store(request.Arguments["download-dir"])
		case "torrent-get":
			arguments["torrents"] = []any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "arguments": arguments})
	}))
	defer server.Close()
	pool := setupTestPool(t)
	defer pool.Close()
	instance, err := pool.instanceStore.Create(t.Context(), models.ClientTypeTransmission, "Preferences fixture", server.URL, "", "", nil, nil, false, nil)
	require.NoError(t, err)
	manager := NewSyncManager(pool, nil)
	readSavePath := func() string {
		t.Helper()
		fields, err := manager.GetPreferenceView(t.Context(), instance.ID)
		require.NoError(t, err)
		for _, field := range fields {
			if field.Key == "save_path" {
				var value string
				require.NoError(t, json.Unmarshal(field.Value, &value))
				return value
			}
		}
		t.Fatal("save_path missing from the shared preference view")
		return ""
	}
	require.Equal(t, "/data/before", readSavePath())
	require.NoError(t, manager.SetTransmissionPreferences(t.Context(), instance.ID, map[string]any{"download-dir": "/data/after"}))
	require.Equal(t, "/data/after", readSavePath())
}
