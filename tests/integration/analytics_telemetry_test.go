//go:build integration

package integration_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordNavigationSession_HappyPath_Completed(
	t *testing.T,
) {
	ownerToken, routeID := createAndPublishRoute(t)
	t.Cleanup(func() { deleteRoute(t, routeID, ownerToken) })

	navigatorID, navigatorToken, _ := createAnonymousUser(t)
	t.Cleanup(func() {
		deleteUser(t, navigatorID, navigatorToken)
	})

	session := newPlausibleSession(2)
	status := recordNavigationSession(
		t, routeID, navigatorToken, session,
	)
	assert.Equal(t, http.StatusNoContent, status)
}

func TestRecordNavigationSession_HappyPath_Abandoned(
	t *testing.T,
) {
	ownerToken, routeID := createAndPublishRoute(t)
	t.Cleanup(func() { deleteRoute(t, routeID, ownerToken) })

	navigatorID, navigatorToken, _ := createAnonymousUser(t)
	t.Cleanup(func() {
		deleteUser(t, navigatorID, navigatorToken)
	})

	session := newAbandonedSession(1, 2)
	status := recordNavigationSession(
		t, routeID, navigatorToken, session,
	)
	assert.Equal(t, http.StatusNoContent, status)
}

func TestRecordNavigationSession_Idempotent(t *testing.T) {
	ownerToken, routeID := createAndPublishRoute(t)
	t.Cleanup(func() { deleteRoute(t, routeID, ownerToken) })

	navigatorID, navigatorToken, _ := createAnonymousUser(t)
	t.Cleanup(func() {
		deleteUser(t, navigatorID, navigatorToken)
	})

	session := newPlausibleSession(2)

	status1 := recordNavigationSession(
		t, routeID, navigatorToken, session,
	)
	assert.Equal(t, http.StatusNoContent, status1)

	status2 := recordNavigationSession(
		t, routeID, navigatorToken, session,
	)
	assert.Equal(t, http.StatusNoContent, status2,
		"idempotent retry must also return 204",
	)
}

func TestRecordNavigationSession_RouteNotFound(t *testing.T) {
	_, token, _ := createAnonymousUser(t)
	t.Cleanup(func() { deleteUser(t, "", token) })

	session := newPlausibleSession(2)
	status := recordNavigationSession(
		t, uuid.New().String(), token, session,
	)
	assert.Equal(t, http.StatusNotFound, status)
}

func TestRecordNavigationSession_NoAuth(t *testing.T) {
	url := fmt.Sprintf(
		"%s/api/v1/routes/%s/navigation-sessions",
		apiURL, uuid.New().String(),
	)
	resp := doRequest(t, http.MethodPost, url, map[string]any{
		"session_id": uuid.New().String(),
		"started_at": time.Now().
			Add(-10 * time.Second).
			Format(time.RFC3339),
		"ended_at":          time.Now().Format(time.RFC3339),
		"outcome":           "completed",
		"waypoints_reached": 2,
		"total_waypoints":   2,
	}, "")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestRecordNavigationSession_InvalidPayload(t *testing.T) {
	ownerToken, routeID := createAndPublishRoute(t)
	t.Cleanup(func() { deleteRoute(t, routeID, ownerToken) })

	navigatorID, navigatorToken, _ := createAnonymousUser(t)
	t.Cleanup(func() {
		deleteUser(t, navigatorID, navigatorToken)
	})

	url := fmt.Sprintf(
		"%s/api/v1/routes/%s/navigation-sessions",
		apiURL, routeID,
	)

	resp := doRequest(
		t, http.MethodPost, url,
		map[string]any{"outcome": "completed"},
		navigatorToken,
	)
	defer resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestRecordNavigationSession_AnyUserCanRecord(
	t *testing.T,
) {
	ownerToken, routeID := createAndPublishRoute(t)
	t.Cleanup(func() { deleteRoute(t, routeID, ownerToken) })

	navigatorID, navigatorToken, _ := createAnonymousUser(t)
	t.Cleanup(func() {
		deleteUser(t, navigatorID, navigatorToken)
	})

	session := newPlausibleSession(2)
	status := recordNavigationSession(
		t, routeID, navigatorToken, session,
	)
	assert.Equal(
		t, http.StatusNoContent, status,
		"non-owner must be able to record a navigation session",
	)
}
