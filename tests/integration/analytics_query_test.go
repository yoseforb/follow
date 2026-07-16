//go:build integration

package integration_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRouteAnalyticsSummary_OwnerSees_Data(
	t *testing.T,
) {
	ownerToken, routeID := createAndPublishRoute(t)
	t.Cleanup(func() { deleteRoute(t, routeID, ownerToken) })

	resp, summary := getRouteSummary(
		t, routeID, ownerToken, "", "",
	)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, routeID, summary.RouteID)
	assert.NotEmpty(t, summary.From)
	assert.NotEmpty(t, summary.To)
	assert.NotNil(t, summary.Sources)
	assert.NotNil(t, summary.AccessModes)
}

func TestRouteAnalyticsSummary_NonOwner_Gets403(
	t *testing.T,
) {
	ownerToken, routeID := createAndPublishRoute(t)
	t.Cleanup(func() { deleteRoute(t, routeID, ownerToken) })

	otherID, otherToken, _ := createAnonymousUser(t)
	t.Cleanup(func() {
		deleteUser(t, otherID, otherToken)
	})

	resp, _ := getRouteSummary(
		t, routeID, otherToken, "", "",
	)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestRouteAnalyticsSummary_NonexistentRoute_Gets404(
	t *testing.T,
) {
	_, token, _ := createAnonymousUser(t)
	t.Cleanup(func() { deleteUser(t, "", token) })

	resp, _ := getRouteSummary(
		t, uuid.New().String(), token, "", "",
	)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestRouteAnalyticsSummary_NoAuth_Gets401(t *testing.T) {
	resp, _ := getRouteSummary(
		t, uuid.New().String(), "", "", "",
	)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestRouteAnalyticsSummary_InvalidDateRange_ToBeforeFrom(
	t *testing.T,
) {
	ownerToken, routeID := createAndPublishRoute(t)
	t.Cleanup(func() { deleteRoute(t, routeID, ownerToken) })

	resp, _ := getRouteSummary(
		t, routeID, ownerToken, "2026-07-15", "2026-07-01",
	)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestRouteAnalyticsSummary_InvalidDateRange_ExceedsRetention(
	t *testing.T,
) {
	ownerToken, routeID := createAndPublishRoute(t)
	t.Cleanup(func() { deleteRoute(t, routeID, ownerToken) })

	resp, _ := getRouteSummary(
		t, routeID, ownerToken, "2025-01-01", "2026-07-15",
	)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestRouteAnalyticsSummary_NullableFields_NoNavigations(
	t *testing.T,
) {
	ownerToken, routeID := createAndPublishRoute(t)
	t.Cleanup(func() { deleteRoute(t, routeID, ownerToken) })

	resp, summary := getRouteSummary(
		t, routeID, ownerToken, "", "",
	)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Nil(
		t, summary.CompletionRate,
		"completion_rate must be nil when no navigations exist",
	)
	assert.Nil(
		t, summary.AvgDurationSeconds,
		"avg_duration_seconds must be nil when no navigations exist",
	)
}

func TestRouteAnalyticsSummary_ZeroNumerator_NotNil(
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
	require.Equal(t, http.StatusNoContent, status)

	summary := waitForNavigationCount(
		t, routeID, ownerToken, 1, 10*time.Second,
	)

	require.NotNil(
		t, summary.CompletionRate,
		"completion_rate must be present (not nil) when "+
			"denominator is positive",
	)
	assert.InDelta(
		t, 0.0, *summary.CompletionRate, 0.001,
		"completion_rate must be 0 when all navigations "+
			"are abandoned",
	)
}

func TestRouteHourlyStats_ZeroFilled_Ascending(
	t *testing.T,
) {
	ownerToken, routeID := createAndPublishRoute(t)
	t.Cleanup(func() { deleteRoute(t, routeID, ownerToken) })

	navigatorID, navigatorToken, _ := createAnonymousUser(t)
	t.Cleanup(func() {
		deleteUser(t, navigatorID, navigatorToken)
	})

	accessRoute(
		t, routeID, navigatorToken,
		map[string]string{"src": "qr"},
	)
	waitForAccessCount(
		t, routeID, ownerToken, 1, 10*time.Second,
	)

	resp, stats := getRouteHourlyStats(
		t, routeID, ownerToken, "24",
	)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.GreaterOrEqual(
		t, len(stats.Points), 24,
		"must return at least 24 hourly points",
	)

	for i := 1; i < len(stats.Points); i++ {
		assert.Greater(
			t,
			stats.Points[i].Hour,
			stats.Points[i-1].Hour,
			"points must be ascending by hour",
		)
	}

	totalAccess := 0
	for _, p := range stats.Points {
		totalAccess += p.AccessCount
	}
	assert.GreaterOrEqual(
		t, totalAccess, 1,
		"at least one hourly bucket must have access_count >= 1",
	)
}

func TestRouteHourlyStats_DefaultHours(t *testing.T) {
	ownerToken, routeID := createAndPublishRoute(t)
	t.Cleanup(func() { deleteRoute(t, routeID, ownerToken) })

	resp, stats := getRouteHourlyStats(
		t, routeID, ownerToken, "",
	)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.GreaterOrEqual(
		t, len(stats.Points), 48,
		"default hours must be at least 48",
	)
}

func TestRouteHourlyStats_OwnershipEnforced(t *testing.T) {
	ownerToken, routeID := createAndPublishRoute(t)
	t.Cleanup(func() { deleteRoute(t, routeID, ownerToken) })

	otherID, otherToken, _ := createAnonymousUser(t)
	t.Cleanup(func() {
		deleteUser(t, otherID, otherToken)
	})

	resp, _ := getRouteHourlyStats(
		t, routeID, otherToken, "",
	)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestRouteDailyStats_ZeroFilled_Ascending(
	t *testing.T,
) {
	ownerToken, routeID := createAndPublishRoute(t)
	t.Cleanup(func() { deleteRoute(t, routeID, ownerToken) })

	today := time.Now().UTC().Format("2006-01-02")
	sevenDaysAgo := time.Now().UTC().
		AddDate(0, 0, -6).
		Format("2006-01-02")

	resp, stats := getRouteDailyStats(
		t, routeID, ownerToken, sevenDaysAgo, today,
	)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(
		t, stats.Points, 7,
		"must return exactly 7 daily points",
	)

	for i := 1; i < len(stats.Points); i++ {
		assert.Greater(
			t,
			stats.Points[i].Date,
			stats.Points[i-1].Date,
			"points must be ascending by date",
		)
	}
}

func TestRouteDailyStats_InvalidDateRange_Exceeds365(
	t *testing.T,
) {
	ownerToken, routeID := createAndPublishRoute(t)
	t.Cleanup(func() { deleteRoute(t, routeID, ownerToken) })

	resp, _ := getRouteDailyStats(
		t, routeID, ownerToken, "2024-01-01", "2026-07-15",
	)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestRouteDailyStats_OwnershipEnforced(t *testing.T) {
	ownerToken, routeID := createAndPublishRoute(t)
	t.Cleanup(func() { deleteRoute(t, routeID, ownerToken) })

	otherID, otherToken, _ := createAnonymousUser(t)
	t.Cleanup(func() {
		deleteUser(t, otherID, otherToken)
	})

	resp, _ := getRouteDailyStats(
		t, routeID, otherToken, "", "",
	)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestOwnerRouteSummaries_ReturnsOwnRoutesOnly(
	t *testing.T,
) {
	tokenA, routeA := createAndPublishRoute(t)
	t.Cleanup(func() { deleteRoute(t, routeA, tokenA) })

	tokenB, routeB := createAndPublishRoute(t)
	t.Cleanup(func() { deleteRoute(t, routeB, tokenB) })

	resp, summaries := getOwnerRouteSummaries(
		t, tokenA, "", "",
	)

	require.Equal(t, http.StatusOK, resp.StatusCode)

	routeIDs := make(map[string]bool)
	for _, r := range summaries.Routes {
		routeIDs[r.RouteID] = true
	}

	assert.True(
		t, routeIDs[routeA],
		"owner A must see their own route",
	)
	assert.False(
		t, routeIDs[routeB],
		"owner A must NOT see owner B's route",
	)
}

func TestOwnerRouteSummaries_EmptyArrayForRoutelessUser(
	t *testing.T,
) {
	userID, token, _ := createAnonymousUser(t)
	t.Cleanup(func() { deleteUser(t, userID, token) })

	resp, summaries := getOwnerRouteSummaries(
		t, token, "", "",
	)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotNil(
		t, summaries.Routes,
		"routes must be an empty array, not null",
	)
	assert.Empty(t, summaries.Routes)
}

func TestOwnerRouteSummaries_DateRangeValidation(
	t *testing.T,
) {
	userID, token, _ := createAnonymousUser(t)
	t.Cleanup(func() { deleteUser(t, userID, token) })

	resp, _ := getOwnerRouteSummaries(
		t, token, "2026-07-15", "2026-07-01",
	)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
