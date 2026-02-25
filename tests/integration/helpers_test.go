//go:build integration

package integration_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	valkeygo "github.com/valkey-io/valkey-go"
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

// SSEEvent represents a parsed Server-Sent Event.
type SSEEvent struct {
	Type string
	Data string
	ID   string
}

// readSSEEvents reads Server-Sent Events from an io.Reader until the context
// is cancelled or the reader returns io.EOF.
// Events are sent to the events channel.
func readSSEEvents(
	ctx context.Context,
	reader io.Reader,
	events chan<- SSEEvent,
) {
	defer close(events)

	scanner := bufio.NewScanner(reader)
	var currentEvent SSEEvent

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line := scanner.Text()

		if line == "" {
			// Empty line marks end of event
			emitEventIfNeeded(ctx, &currentEvent, events)
			currentEvent = SSEEvent{}
			continue
		}

		parseSSELine(line, &currentEvent)
	}

	// Handle final event if no trailing newline
	emitEventIfNeeded(ctx, &currentEvent, events)
}

func parseSSELine(line string, event *SSEEvent) {
	switch {
	case strings.HasPrefix(line, "event:"):
		_, val, _ := strings.Cut(line, ":")
		event.Type = strings.TrimSpace(val)
	case strings.HasPrefix(line, "data:"):
		_, val, _ := strings.Cut(line, ":")
		event.Data = strings.TrimSpace(val)
	case strings.HasPrefix(line, "id:"):
		_, val, _ := strings.Cut(line, ":")
		event.ID = strings.TrimSpace(val)
	}
}

func emitEventIfNeeded(
	ctx context.Context,
	event *SSEEvent,
	events chan<- SSEEvent,
) {
	if event.Type == "" && event.Data == "" {
		return
	}

	if event.Type == "" {
		event.Type = "message"
	}

	select {
	case events <- *event:
	case <-ctx.Done():
	}
}

// newValkeyClient creates a new Valkey client.
//
//nolint:unused // Helper for future SSE/Valkey integration tests
func newValkeyClient(t *testing.T) valkeygo.Client {
	t.Helper()

	client, err := valkeygo.NewClient(valkeygo.ClientOption{
		InitAddress:  []string{valkeyAddress},
		DisableCache: true,
	})
	require.NoError(t, err,
		"newValkeyClient: failed to create client",
	)

	t.Cleanup(func() { client.Close() })

	return client
}
