//go:build integration

package integration_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlatformDailyStats_AdminSees_Data(t *testing.T) {
	token := adminToken(t)

	resp, stats := getPlatformDailyStats(t, token, "", "")

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotEmpty(t, stats.From)
	assert.NotEmpty(t, stats.To)
	assert.NotNil(t, stats.Points)

	for i := 1; i < len(stats.Points); i++ {
		assert.Greater(
			t,
			stats.Points[i].Date,
			stats.Points[i-1].Date,
			"platform daily points must be ascending",
		)
	}
}

func TestPlatformDailyStats_NonAdmin_Gets403(t *testing.T) {
	userID, token, _ := createAnonymousUser(t)
	t.Cleanup(func() { deleteUser(t, userID, token) })

	resp, _ := getPlatformDailyStats(t, token, "", "")
	defer resp.Body.Close()

	assert.Contains(
		t,
		[]int{
			http.StatusForbidden,
			http.StatusUnauthorized,
		},
		resp.StatusCode,
		"non-admin must get 401 or 403",
	)
}

func TestPlatformDailyStats_NoAuth_Gets401(t *testing.T) {
	resp, _ := getPlatformDailyStats(t, "", "", "")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestPlatformDailyStats_DateRangeValidation(
	t *testing.T,
) {
	token := adminToken(t)

	t.Run("ToBeforeFrom", func(t *testing.T) {
		resp, _ := getPlatformDailyStats(
			t, token, "2026-07-15", "2026-07-01",
		)
		defer resp.Body.Close()
		assert.Equal(
			t, http.StatusBadRequest, resp.StatusCode,
		)
	})

	t.Run("Exceeds365Days", func(t *testing.T) {
		resp, _ := getPlatformDailyStats(
			t, token, "2024-01-01", "2026-07-15",
		)
		defer resp.Body.Close()
		assert.Equal(
			t, http.StatusBadRequest, resp.StatusCode,
		)
	})
}

func TestPlatformKillGates_AdminSees_Data(t *testing.T) {
	token := adminToken(t)

	resp, kg := getPlatformKillGates(t, token, "", "")

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotEmpty(t, kg.From)
	assert.NotEmpty(t, kg.To)
}

func TestPlatformKillGates_NonAdmin_Gets403(t *testing.T) {
	userID, token, _ := createAnonymousUser(t)
	t.Cleanup(func() { deleteUser(t, userID, token) })

	resp, _ := getPlatformKillGates(t, token, "", "")
	defer resp.Body.Close()

	assert.Contains(
		t,
		[]int{
			http.StatusForbidden,
			http.StatusUnauthorized,
		},
		resp.StatusCode,
		"non-admin must get 401 or 403",
	)
}

func TestPlatformKillGates_NullableRates(t *testing.T) {
	token := adminToken(t)

	resp, kg := getPlatformKillGates(t, token, "", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	totalNav := kg.Metrics.NavigationsCompleted +
		kg.Metrics.NavigationsAbandoned

	if totalNav == 0 {
		assert.Nil(
			t, kg.Metrics.CompletionRate,
			"completion_rate must be nil with zero navigations",
		)
	}

	if kg.Metrics.RoutesCreated == 0 {
		assert.Nil(
			t, kg.Metrics.ConversionLoopRate,
			"conversion_loop_rate must be nil with "+
				"zero routes created",
		)
	}
}

func TestPlatformKillGates_DateRangeExceedsRetention(
	t *testing.T,
) {
	token := adminToken(t)

	resp, _ := getPlatformKillGates(
		t, token, "2025-01-01", "2026-07-15",
	)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
