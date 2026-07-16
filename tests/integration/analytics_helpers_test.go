//go:build integration

package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// createAndPublishRoute creates an anonymous user, prepares a route,
// creates waypoints, uploads images, waits for ready, and publishes.
// Returns userID, authToken, routeID for further use.
func createAndPublishRoute(
	t *testing.T,
) (authToken, routeID string) {
	t.Helper()

	_, authToken, _ = createAnonymousUser(t)

	routeID = prepareRoute(t, authToken)

	cwResp := createRouteWithWaypoints(
		t, authToken, routeID, defaultTestImages,
	)

	for i, entry := range cwResp.PresignedURLs {
		imgBytes := loadTestImage(
			t, defaultTestImages[i].Filename,
		)
		resp := uploadToGateway(
			t, entry.UploadURL, entry.UploadToken, imgBytes,
		)
		resp.Body.Close()
		require.Equal(
			t, http.StatusAccepted, resp.StatusCode,
			"createAndPublishRoute: upload %d failed", i,
		)
	}

	waitForRouteReady(t, routeID, authToken, 60*time.Second)

	publishRoute(t, routeID, authToken)

	return authToken, routeID
}

// accessRoute sends GET /api/v1/routes/{routeID} with optional
// query params. Returns the HTTP status code.
func accessRoute(
	t *testing.T,
	routeID, authToken string,
	queryParams map[string]string,
) {
	t.Helper()

	u := fmt.Sprintf(
		"%s/api/v1/routes/%s", apiURL, routeID,
	)

	if len(queryParams) > 0 {
		vals := url.Values{}
		for k, v := range queryParams {
			vals.Set(k, v)
		}
		u += "?" + vals.Encode()
	}

	resp := doRequest(
		t, http.MethodGet, u, nil, authToken,
	)
	defer resp.Body.Close()

	require.Equal(
		t, http.StatusOK, resp.StatusCode,
		"accessRoute: expected 200",
	)
}

// recordNavigationSession POSTs a navigation session to the
// telemetry endpoint. Returns the HTTP status code.
func recordNavigationSession(
	t *testing.T,
	routeID, authToken string,
	session NavigationSessionPayload,
) int {
	t.Helper()

	url := fmt.Sprintf(
		"%s/api/v1/routes/%s/navigation-sessions",
		apiURL, routeID,
	)

	resp := doRequest(
		t, http.MethodPost, url, session, authToken,
	)
	defer resp.Body.Close()

	return resp.StatusCode
}

// NavigationSessionPayload is the request body for
// POST /routes/{route_id}/navigation-sessions.
type NavigationSessionPayload struct {
	SessionID        string           `json:"session_id"`
	StartedAt        string           `json:"started_at"`
	EndedAt          string           `json:"ended_at"`
	Outcome          string           `json:"outcome"`
	WaypointsReached int              `json:"waypoints_reached"`
	TotalWaypoints   int              `json:"total_waypoints"`
	WaypointTimings  []WaypointTiming `json:"waypoint_timings,omitempty"`
	AppVersion       string           `json:"app_version,omitempty"`
}

// WaypointTiming is timing data for a single waypoint.
type WaypointTiming struct {
	Position  int    `json:"position"`
	ReachedAt string `json:"reached_at"`
}

// newPlausibleSession builds a plausible completed navigation session
// with realistic timing (satisfies min_duration_per_waypoint=2s,
// min_median_waypoint_gap=1s).
func newPlausibleSession(
	totalWaypoints int, //nolint:unparam // helper accepts any count
) NavigationSessionPayload {
	now := time.Now().UTC()
	start := now.Add(-time.Duration(totalWaypoints) * 5 * time.Second)

	timings := make([]WaypointTiming, totalWaypoints)
	for i := range totalWaypoints {
		timings[i] = WaypointTiming{
			Position: i,
			ReachedAt: start.Add(time.Duration(i+1) * 5 * time.Second).
				Format(time.RFC3339),
		}
	}

	return NavigationSessionPayload{
		SessionID:        uuid.New().String(),
		StartedAt:        start.Format(time.RFC3339),
		EndedAt:          now.Format(time.RFC3339),
		Outcome:          "completed",
		WaypointsReached: totalWaypoints,
		TotalWaypoints:   totalWaypoints,
		WaypointTimings:  timings,
		AppVersion:       "1.0.0-test",
	}
}

// newAbandonedSession builds a plausible abandoned navigation session.
func newAbandonedSession(
	waypointsReached, totalWaypoints int,
) NavigationSessionPayload {
	now := time.Now().UTC()
	start := now.Add(
		-time.Duration(waypointsReached) * 5 * time.Second,
	)

	timings := make([]WaypointTiming, waypointsReached)
	for i := range waypointsReached {
		timings[i] = WaypointTiming{
			Position: i,
			ReachedAt: start.Add(time.Duration(i+1) * 5 * time.Second).
				Format(time.RFC3339),
		}
	}

	return NavigationSessionPayload{
		SessionID:        uuid.New().String(),
		StartedAt:        start.Format(time.RFC3339),
		EndedAt:          now.Format(time.RFC3339),
		Outcome:          "abandoned",
		WaypointsReached: waypointsReached,
		TotalWaypoints:   totalWaypoints,
		WaypointTimings:  timings,
	}
}

// AnalyticsSummary is the typed response from
// GET /analytics/routes/{route_id}/summary.
type AnalyticsSummary struct {
	RouteID              string            `json:"route_id"`
	From                 string            `json:"from"`
	To                   string            `json:"to"`
	AccessCount          int               `json:"access_count"`
	UniqueVisitors       int               `json:"unique_visitors"`
	AnonymousVisitors    int               `json:"anonymous_visitors"`
	RegisteredVisitors   int               `json:"registered_visitors"`
	SaveCount            int               `json:"save_count"`
	NavigationsCompleted int               `json:"navigations_completed"`
	NavigationsAbandoned int               `json:"navigations_abandoned"`
	CompletionRate       *float64          `json:"completion_rate,omitempty"`
	AvgDurationSeconds   *int              `json:"avg_duration_seconds,omitempty"`
	Sources              []SourceCount     `json:"sources"`
	AccessModes          []AccessModeCount `json:"access_modes"`
}

// SourceCount is a source breakdown entry.
type SourceCount struct {
	Source string `json:"source"`
	Count  int    `json:"count"`
}

// AccessModeCount is an access mode breakdown entry.
type AccessModeCount struct {
	Mode  string `json:"mode"`
	Count int    `json:"count"`
}

// HourlyStatsResponse is the typed response from
// GET /analytics/routes/{route_id}/hourly.
type HourlyStatsResponse struct {
	RouteID string             `json:"route_id"`
	From    string             `json:"from"`
	To      string             `json:"to"`
	Points  []HourlyStatsPoint `json:"points"`
}

// HourlyStatsPoint is a single hourly data point.
type HourlyStatsPoint struct {
	Hour        string `json:"hour"`
	AccessCount int    `json:"access_count"`
	SaveCount   int    `json:"save_count"`
}

// DailyStatsResponse is the typed response from
// GET /analytics/routes/{route_id}/daily.
type DailyStatsResponse struct {
	RouteID string            `json:"route_id"`
	From    string            `json:"from"`
	To      string            `json:"to"`
	Points  []DailyStatsPoint `json:"points"`
}

// DailyStatsPoint is a single daily data point.
type DailyStatsPoint struct {
	Date                 string `json:"date"`
	AccessCount          int    `json:"access_count"`
	UniqueVisitors       int    `json:"unique_visitors"`
	AnonymousVisitors    int    `json:"anonymous_visitors"`
	RegisteredVisitors   int    `json:"registered_visitors"`
	SaveCount            int    `json:"save_count"`
	NavigationsCompleted int    `json:"navigations_completed"`
	NavigationsAbandoned int    `json:"navigations_abandoned"`
	AvgDurationSeconds   *int   `json:"avg_duration_seconds,omitempty"`
}

// OwnerRouteSummariesResponse is the typed response from
// GET /analytics/routes/summary.
type OwnerRouteSummariesResponse struct {
	From   string                  `json:"from"`
	To     string                  `json:"to"`
	Routes []OwnerRouteSummaryItem `json:"routes"`
}

// OwnerRouteSummaryItem is one route entry in the owner summaries.
type OwnerRouteSummaryItem struct {
	RouteID              string            `json:"route_id"`
	AccessCount          int               `json:"access_count"`
	UniqueVisitors       int               `json:"unique_visitors"`
	AnonymousVisitors    int               `json:"anonymous_visitors"`
	RegisteredVisitors   int               `json:"registered_visitors"`
	SaveCount            int               `json:"save_count"`
	NavigationsCompleted int               `json:"navigations_completed"`
	NavigationsAbandoned int               `json:"navigations_abandoned"`
	CompletionRate       *float64          `json:"completion_rate,omitempty"`
	AvgDurationSeconds   *int              `json:"avg_duration_seconds,omitempty"`
	Sources              []SourceCount     `json:"sources"`
	AccessModes          []AccessModeCount `json:"access_modes"`
}

// PlatformDailyStatsResponse is the typed response from
// GET /analytics/platform/daily.
type PlatformDailyStatsResponse struct {
	From   string                   `json:"from"`
	To     string                   `json:"to"`
	Points []PlatformDailyStatPoint `json:"points"`
}

// PlatformDailyStatPoint is a single platform daily data point.
type PlatformDailyStatPoint struct {
	Date                        string `json:"date"`
	UsersCreated                int    `json:"users_created"`
	UsersRegistered             int    `json:"users_registered"`
	UsersDeleted                int    `json:"users_deleted"`
	RoutesCreated               int    `json:"routes_created"`
	RoutesPublished             int    `json:"routes_published"`
	RoutesDeleted               int    `json:"routes_deleted"`
	RoutesCreatedNavigatorFirst int    `json:"routes_created_navigator_first"`
}

// PlatformKillGatesResponse is the typed response from
// GET /analytics/platform/kill-gates.
type PlatformKillGatesResponse struct {
	From    string          `json:"from"`
	To      string          `json:"to"`
	Metrics KillGateMetrics `json:"metrics"`
}

// KillGateMetrics holds the kill-gate metric values.
type KillGateMetrics struct {
	NavigationsCompleted        int      `json:"navigations_completed"`
	NavigationsAbandoned        int      `json:"navigations_abandoned"`
	CompletionRate              *float64 `json:"completion_rate,omitempty"`
	ActiveHostCount             int      `json:"active_host_count"`
	RoutesCreated               int      `json:"routes_created"`
	RoutesCreatedNavigatorFirst int      `json:"routes_created_navigator_first"`
	ConversionLoopRate          *float64 `json:"conversion_loop_rate,omitempty"`
	PlausibleCompletedArrivals  int      `json:"plausible_completed_arrivals"`
}

// getRouteSummary calls GET /analytics/routes/{route_id}/summary.
func getRouteSummary(
	t *testing.T,
	routeID, authToken string,
	from, to string,
) (*http.Response, AnalyticsSummary) {
	t.Helper()

	url := fmt.Sprintf(
		"%s/api/v1/analytics/routes/%s/summary",
		apiURL, routeID,
	)
	url = appendDateParams(url, from, to)

	resp := doRequest(
		t, http.MethodGet, url, nil, authToken,
	)

	if resp.StatusCode != http.StatusOK {
		return resp, AnalyticsSummary{}
	}

	defer resp.Body.Close()

	var result AnalyticsSummary

	err := json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(
		t, err,
		"getRouteSummary: failed to decode response",
	)

	return resp, result
}

// getRouteHourlyStats calls
// GET /analytics/routes/{route_id}/hourly.
func getRouteHourlyStats(
	t *testing.T,
	routeID, authToken string,
	hours string,
) (*http.Response, HourlyStatsResponse) {
	t.Helper()

	url := fmt.Sprintf(
		"%s/api/v1/analytics/routes/%s/hourly",
		apiURL, routeID,
	)

	if hours != "" {
		url += "?hours=" + hours
	}

	resp := doRequest(
		t, http.MethodGet, url, nil, authToken,
	)

	if resp.StatusCode != http.StatusOK {
		return resp, HourlyStatsResponse{}
	}

	defer resp.Body.Close()

	var result HourlyStatsResponse

	err := json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(
		t, err,
		"getRouteHourlyStats: failed to decode response",
	)

	return resp, result
}

// getRouteDailyStats calls
// GET /analytics/routes/{route_id}/daily.
func getRouteDailyStats(
	t *testing.T,
	routeID, authToken string,
	from, to string,
) (*http.Response, DailyStatsResponse) {
	t.Helper()

	url := fmt.Sprintf(
		"%s/api/v1/analytics/routes/%s/daily",
		apiURL, routeID,
	)
	url = appendDateParams(url, from, to)

	resp := doRequest(
		t, http.MethodGet, url, nil, authToken,
	)

	if resp.StatusCode != http.StatusOK {
		return resp, DailyStatsResponse{}
	}

	defer resp.Body.Close()

	var result DailyStatsResponse

	err := json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(
		t, err,
		"getRouteDailyStats: failed to decode response",
	)

	return resp, result
}

// getOwnerRouteSummaries calls
// GET /analytics/routes/summary.
func getOwnerRouteSummaries(
	t *testing.T,
	authToken string,
	from, to string,
) (*http.Response, OwnerRouteSummariesResponse) {
	t.Helper()

	url := apiURL + "/api/v1/analytics/routes/summary"
	url = appendDateParams(url, from, to)

	resp := doRequest(
		t, http.MethodGet, url, nil, authToken,
	)

	if resp.StatusCode != http.StatusOK {
		return resp, OwnerRouteSummariesResponse{}
	}

	defer resp.Body.Close()

	var result OwnerRouteSummariesResponse

	err := json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(
		t, err,
		"getOwnerRouteSummaries: failed to decode response",
	)

	return resp, result
}

// getPlatformDailyStats calls
// GET /analytics/platform/daily.
func getPlatformDailyStats(
	t *testing.T,
	authToken string,
	from, to string,
) (*http.Response, PlatformDailyStatsResponse) {
	t.Helper()

	url := apiURL + "/api/v1/analytics/platform/daily"
	url = appendDateParams(url, from, to)

	resp := doRequest(
		t, http.MethodGet, url, nil, authToken,
	)

	if resp.StatusCode != http.StatusOK {
		return resp, PlatformDailyStatsResponse{}
	}

	defer resp.Body.Close()

	var result PlatformDailyStatsResponse

	err := json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(
		t, err,
		"getPlatformDailyStats: failed to decode response",
	)

	return resp, result
}

// getPlatformKillGates calls
// GET /analytics/platform/kill-gates.
func getPlatformKillGates(
	t *testing.T,
	authToken string,
	from, to string,
) (*http.Response, PlatformKillGatesResponse) {
	t.Helper()

	url := apiURL + "/api/v1/analytics/platform/kill-gates"
	url = appendDateParams(url, from, to)

	resp := doRequest(
		t, http.MethodGet, url, nil, authToken,
	)

	if resp.StatusCode != http.StatusOK {
		return resp, PlatformKillGatesResponse{}
	}

	defer resp.Body.Close()

	var result PlatformKillGatesResponse

	err := json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(
		t, err,
		"getPlatformKillGates: failed to decode response",
	)

	return resp, result
}

// appendDateParams adds from/to query params to a URL.
func appendDateParams(url, from, to string) string {
	sep := "?"
	if from != "" {
		url += sep + "from=" + from
		sep = "&"
	}
	if to != "" {
		url += sep + "to=" + to
	}
	return url
}

// waitForAccessCount polls the route hourly stats until
// access_count reaches the expected value or timeout elapses.
// Accounts for async event processing (Watermill GoChannel).
func waitForAccessCount(
	t *testing.T,
	routeID, authToken string,
	expectedCount int,
	timeout time.Duration, //nolint:unparam // callers may vary timeout
) {
	t.Helper()

	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		resp, stats := getRouteHourlyStats(
			t, routeID, authToken, "1",
		)

		if resp.StatusCode == http.StatusOK {
			total := 0
			for _, p := range stats.Points {
				total += p.AccessCount
			}

			if total >= expectedCount {
				return
			}
		} else {
			resp.Body.Close()
		}

		time.Sleep(200 * time.Millisecond)
	}

	t.Fatalf(
		"waitForAccessCount: route %s did not reach "+
			"access_count=%d within %s",
		routeID, expectedCount, timeout,
	)
}

// waitForSaveCount polls the route hourly stats until save_count
// reaches the expected value or timeout elapses.
func waitForSaveCount(
	t *testing.T,
	routeID, authToken string,
	expectedCount int,
	timeout time.Duration,
) {
	t.Helper()

	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		resp, stats := getRouteHourlyStats(
			t, routeID, authToken, "1",
		)

		if resp.StatusCode == http.StatusOK {
			total := 0
			for _, p := range stats.Points {
				total += p.SaveCount
			}

			if total >= expectedCount {
				return
			}
		} else {
			resp.Body.Close()
		}

		time.Sleep(200 * time.Millisecond)
	}

	t.Fatalf(
		"waitForSaveCount: route %s did not reach "+
			"save_count=%d within %s",
		routeID, expectedCount, timeout,
	)
}

// waitForNavigationCount polls the route summary until
// navigations_completed + navigations_abandoned reaches the
// expected value or timeout elapses.
func waitForNavigationCount(
	t *testing.T,
	routeID, authToken string,
	expectedTotal int,
	timeout time.Duration,
) AnalyticsSummary {
	t.Helper()

	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		resp, summary := getRouteSummary(
			t, routeID, authToken, "", "",
		)

		if resp.StatusCode == http.StatusOK {
			total := summary.NavigationsCompleted +
				summary.NavigationsAbandoned

			if total >= expectedTotal {
				return summary
			}
		} else {
			resp.Body.Close()
		}

		time.Sleep(200 * time.Millisecond)
	}

	t.Fatalf(
		"waitForNavigationCount: route %s did not reach "+
			"navigation_count=%d within %s",
		routeID, expectedTotal, timeout,
	)

	return AnalyticsSummary{}
}

// saveRoute calls POST /api/v1/routes/{routeID}/save.
func saveRoute(
	t *testing.T,
	routeID, authToken string,
) int {
	t.Helper()

	url := fmt.Sprintf(
		"%s/api/v1/routes/%s/save", apiURL, routeID,
	)

	resp := doRequest(
		t, http.MethodPost, url, nil, authToken,
	)
	defer resp.Body.Close()

	return resp.StatusCode
}
