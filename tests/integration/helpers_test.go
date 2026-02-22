//go:build integration

package integration_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// PresignedURLEntry is a single presigned upload URL entry returned by
// the create-waypoints endpoint.
type PresignedURLEntry struct {
	ImageID   string `json:"image_id"`
	UploadURL string `json:"upload_url"`
	Position  int    `json:"position"`
	ExpiresAt string `json:"expires_at"`
}

// CreateWaypointsResponse is the typed response from POST
// /api/v1/routes/{routeID}/create-waypoints.
type CreateWaypointsResponse struct {
	RouteID       string              `json:"route_id"`
	RouteStatus   string              `json:"route_status"`
	WaypointIDs   []string            `json:"waypoint_ids"`
	PresignedURLs []PresignedURLEntry `json:"presigned_urls"`
	CreatedAt     string              `json:"created_at"`
	WaypointCount int                 `json:"waypoint_count"`
}

// ReplaceImagePrepareResponse is the typed response from POST
// .../replace-image/prepare.
type ReplaceImagePrepareResponse struct {
	ImageID   string `json:"image_id"`
	UploadURL string `json:"upload_url"`
	ExpiresAt string `json:"expires_at"`
}

// testdataDir returns the absolute path to the testdata directory relative to
// this test file.
func testdataDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "testdata"
	}

	return filepath.Join(filepath.Dir(file), "testdata")
}

// loadTestImage reads a test image from testdata/ directory.
// Calls t.Fatal if the file does not exist.
func loadTestImage(t *testing.T, filename string) []byte {
	t.Helper()

	path := filepath.Join(testdataDir(), filename)

	data, err := os.ReadFile(path)
	require.NoErrorf(t, err,
		"loadTestImage: failed to read %s", path,
	)

	return data
}

// sha256Hex computes SHA256 hash of data, returns lowercase hex string (64 chars).
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// doRequest sends an HTTP request and returns the response.
// Encodes body as JSON if non-nil.
// Sets Content-Type: application/json and Authorization: Bearer {authToken}
// if authToken is non-empty.
// Calls t.Fatal on transport errors.
func doRequest(
	t *testing.T,
	method, url string,
	body any,
	authToken string,
) *http.Response {
	t.Helper()

	var reqBody io.Reader

	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoErrorf(t, err,
			"doRequest: failed to marshal body for %s %s", method, url,
		)

		reqBody = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, url, reqBody)
	require.NoErrorf(t, err,
		"doRequest: failed to create request for %s %s", method, url,
	)

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}

	client := &http.Client{Timeout: 30 * time.Second}

	resp, err := client.Do(req)
	require.NoErrorf(t, err,
		"doRequest: transport error for %s %s", method, url,
	)

	return resp
}

// decodeJSON decodes the response body into map[string]any.
// Closes the body and calls t.Fatal on decode errors.
func decodeJSON(
	t *testing.T,
	resp *http.Response,
) map[string]any {
	t.Helper()

	defer resp.Body.Close()

	var result map[string]any

	err := json.NewDecoder(resp.Body).Decode(&result)
	require.NoErrorf(t, err,
		"decodeJSON: failed to decode response body (status %d)",
		resp.StatusCode,
	)

	return result
}

// createAnonymousUser calls POST /api/v1/users/anonymous.
// Returns user_id and JWT token.
func createAnonymousUser(t *testing.T) (userID, token string) {
	t.Helper()

	resp := doRequest(
		t,
		http.MethodPost,
		apiURL+"/api/v1/users/anonymous",
		map[string]any{},
		"",
	)

	require.Equalf(t, http.StatusOK, resp.StatusCode,
		"createAnonymousUser: expected 200, got %d", resp.StatusCode,
	)

	result := decodeJSON(t, resp)

	userIDVal, ok := result["user_id"].(string)
	require.True(t, ok,
		"createAnonymousUser: response missing string user_id",
	)
	require.NotEmpty(t, userIDVal,
		"createAnonymousUser: user_id is empty",
	)

	tokenVal, ok := result["token"].(string)
	require.True(t, ok,
		"createAnonymousUser: response missing string token",
	)
	require.NotEmpty(t, tokenVal,
		"createAnonymousUser: token is empty",
	)

	return userIDVal, tokenVal
}

// prepareRoute calls POST /api/v1/routes/prepare.
// Returns route_id string.
func prepareRoute(t *testing.T, authToken string) string {
	t.Helper()

	resp := doRequest(
		t,
		http.MethodPost,
		apiURL+"/api/v1/routes/prepare",
		map[string]any{},
		authToken,
	)

	require.Equalf(t, http.StatusOK, resp.StatusCode,
		"prepareRoute: expected 200, got %d", resp.StatusCode,
	)

	result := decodeJSON(t, resp)

	routeID, ok := result["route_id"].(string)
	require.True(t, ok,
		"prepareRoute: response missing string route_id",
	)
	require.NotEmpty(t, routeID,
		"prepareRoute: route_id is empty",
	)

	return routeID
}

// deleteRoute calls DELETE /api/v1/routes/{routeID}.
// Best-effort — does not call t.Fatal on failure, only t.Log.
func deleteRoute(t *testing.T, routeID, authToken string) {
	t.Helper()

	if routeID == "" {
		return
	}

	resp := doRequest(
		t,
		http.MethodDelete,
		apiURL+"/api/v1/routes/"+routeID,
		nil,
		authToken,
	)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Logf(
			"deleteRoute: cleanup DELETE /api/v1/routes/%s returned %d",
			routeID,
			resp.StatusCode,
		)
	}
}

// waitForRouteStatus polls GET /api/v1/routes/{routeID} every 1s until
// route_status matches expectedStatus or timeout expires.
// Non-200 HTTP responses and JSON decode failures are tolerated during polling
// (logged and retried). Only calls t.Fatalf on timeout.
func waitForRouteStatus(
	t *testing.T,
	routeID, authToken, expectedStatus string,
	timeout time.Duration,
) {
	t.Helper()

	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		func() {
			resp := doRequest(
				t,
				http.MethodGet,
				apiURL+"/api/v1/routes/"+routeID,
				nil,
				authToken,
			)
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Logf(
					"waitForRouteStatus: route %s returned HTTP %d, retrying",
					routeID,
					resp.StatusCode,
				)

				return
			}

			var result map[string]any

			err := json.NewDecoder(resp.Body).Decode(&result)
			if err != nil {
				t.Logf(
					"waitForRouteStatus: route %s JSON decode error: %v, retrying",
					routeID,
					err,
				)

				return
			}

			routeObj, ok := result["route"].(map[string]any)
			if !ok {
				t.Logf(
					"waitForRouteStatus: route %s response missing \"route\" object, retrying",
					routeID,
				)

				return
			}

			status, _ := routeObj["route_status"].(string)
			if status == expectedStatus {
				t.Logf(
					"waitForRouteStatus: route %s reached status %q",
					routeID,
					expectedStatus,
				)

				// Signal success by advancing deadline so the outer loop exits.
				deadline = time.Time{}

				return
			}

			t.Logf(
				"waitForRouteStatus: route %s status=%q, waiting for %q",
				routeID,
				status,
				expectedStatus,
			)
		}()

		// deadline set to zero means we reached the expected status.
		if deadline.IsZero() {
			return
		}

		time.Sleep(1 * time.Second)
	}

	t.Fatalf(
		"waitForRouteStatus: route %s did not reach status %q within %s",
		routeID,
		expectedStatus,
		timeout,
	)
}
