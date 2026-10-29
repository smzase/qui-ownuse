package sse

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/autobrr/qui/internal/api/ctxkeys"
	"github.com/autobrr/qui/internal/models"
	"github.com/autobrr/qui/internal/testutil/testdb"
)

func TestSubscriptionChangesKeepConnectionOwnership(t *testing.T) {
	manager := NewStreamManager(nil, nil, nil)
	t.Cleanup(func() { _ = manager.Shutdown(t.Context()) })
	conn := manager.openConnection(false, 1)
	conn.markReady()
	conn.subs["existing"] = &subscriptionState{clientKey: "list"}
	for _, userID := range []int{2, 1} {
		ctx := context.WithValue(t.Context(), ctxkeys.UserID, userID)
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/stream/subscriptions", strings.NewReader(fmt.Sprintf(`{"connectionId":%q,"remove":["list"]}`, conn.id)))
		rec := httptest.NewRecorder()
		manager.ChangeSubscriptions(rec, req)
		if userID == 2 {
			require.Equal(t, http.StatusNotFound, rec.Code)
			require.Contains(t, conn.subs, "existing", "another user must not remove the owner's subscription")
		} else {
			require.Equal(t, http.StatusNoContent, rec.Code)
			require.Empty(t, conn.subs)
		}
	}
}

func TestInitialAndAddedSubscriptionsKeepInstanceAccess(t *testing.T) {
	db := testdb.NewMigratedSQLite(t, "stream-fork-access")
	_, err := db.ExecContext(t.Context(), "INSERT INTO users (id, username, password_hash, role) VALUES (1, 'alice', 'fixture', 'user'), (2, 'bob', 'fixture', 'user')")
	require.NoError(t, err)
	store, err := models.NewInstanceStore(db, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	instance, err := store.Create(t.Context(), models.ClientTypeTransmission, "fixture", "http://127.0.0.1:1", "", "", nil, nil, false, nil)
	require.NoError(t, err)
	require.NoError(t, store.SetOwner(t.Context(), instance.ID, 1))
	manager := NewStreamManager(nil, nil, store)
	t.Cleanup(func() { _ = manager.Shutdown(t.Context()) })
	ctx := context.WithValue(t.Context(), ctxkeys.UserID, 2)
	ctx = context.WithValue(ctx, ctxkeys.UserRole, "user")
	payload := streamPayload(instance.ID, "private")
	rec := httptest.NewRecorder()
	manager.Serve(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/stream?streams="+streamsQuery(t, payload), nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
	rec = httptest.NewRecorder()
	manager.ChangeSubscriptions(rec, httptest.NewRequestWithContext(ctx, http.MethodPost, "/stream/subscriptions", strings.NewReader(fmt.Sprintf(`{"connectionId":"any","add":[{"instanceId":%d,"key":"private"}]}`, instance.ID))))
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Empty(t, manager.subscriptions)

	require.NoError(t, store.Share(t.Context(), instance.ID, 2, 1))
	rec = httptest.NewRecorder()
	require.True(t, manager.validateInstances(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/stream", nil), []streamRequest{{options: StreamOptions{InstanceID: instance.ID}}}))
}
