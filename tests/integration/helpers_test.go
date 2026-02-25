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
	"strconv"
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

// streamMessage is a single message read from a Valkey stream.
type streamMessage struct {
	ID     string
	Fields map[string]string
}

// invalidImageBytes returns bytes that are not a valid image format.
// Suitable for testing upload rejection paths.
func invalidImageBytes() []byte {
	return []byte("this is definitely not an image file %^&*!")
}

// uploadToGateway sends a PUT request with raw imageBytes to uploadURL.
// Content-Type is intentionally not set; the gateway derives it from JWT
// claims. Returns the HTTP response — caller is responsible for closing
// the body.
func uploadToGateway(
	t *testing.T,
	uploadURL string,
	imageBytes []byte,
) *http.Response {
	t.Helper()

	req, err := http.NewRequest(
		http.MethodPut, uploadURL, bytes.NewReader(imageBytes),
	)
	require.NoError(t, err)

	client := &http.Client{Timeout: 30 * time.Second}

	resp, err := client.Do(req)
	require.NoError(t, err)

	return resp
}

// markerCoords holds a pair of normalised (x, y) marker coordinates.
type markerCoords struct {
	X float64
	Y float64
}

// defaultMarkers provides the standard set of marker coordinates used by
// createRouteWithWaypoints for the first three waypoints. Additional
// waypoints continue the pattern.
var defaultMarkers = []markerCoords{
	{X: 0.10, Y: 0.20},
	{X: 0.30, Y: 0.40},
	{X: 0.50, Y: 0.60},
}

// markerForPosition returns marker coordinates for a given waypoint position.
// For positions beyond the predefined set the coordinates are computed from
// the position index so they remain unique and predictable.
func markerForPosition(pos int) markerCoords {
	if pos < len(defaultMarkers) {
		return defaultMarkers[pos]
	}

	step := float64(pos+1) * 0.10
	return markerCoords{X: step, Y: step + 0.10}
}

// waypointImageSpec pairs a waypoint position with the test image filename to
// use for that waypoint.
type waypointImageSpec struct {
	Filename string
}

// defaultTestImages is the standard set of image specs used by tests that
// need two waypoints with real images but do not care which images are used.
var defaultTestImages = []waypointImageSpec{
	{"pexels-punttim-240223.jpg"},
	{"pexels-arthurbrognoli-2260838.jpg"},
}

// buildWaypointBody constructs a single waypoint map suitable for use in the
// create-waypoints request body. The filename and fileSize are derived from
// the actual test image so the gateway JWT file-size limit is not exceeded.
func buildWaypointBody(
	pos int,
	filename string,
	fileSize int,
) map[string]any {
	m := markerForPosition(pos)

	return map[string]any{
		"marker_x":    m.X,
		"marker_y":    m.Y,
		"marker_type": "next_step",
		"description": "Waypoint " + strconv.Itoa(pos+1),
		"image_metadata": map[string]any{
			"content_type":      "image/jpeg",
			"file_size":         fileSize,
			"original_filename": filename,
		},
	}
}

// createRouteWithWaypoints calls POST .../create-waypoints and returns the
// decoded CreateWaypointsResponse. Each element of images specifies the test
// image for the corresponding waypoint; the actual file size is read from
// disk so it matches what the gateway JWT encodes as the upload size limit.
func createRouteWithWaypoints(
	t *testing.T,
	authToken string,
	routeID string,
	images []waypointImageSpec,
) CreateWaypointsResponse {
	t.Helper()

	waypoints := make([]map[string]any, len(images))
	for i, spec := range images {
		imgBytes := loadTestImage(t, spec.Filename)
		waypoints[i] = buildWaypointBody(i, spec.Filename, len(imgBytes))
	}

	body := map[string]any{
		"route_id":       routeID,
		"name":           "Integration Test Route",
		"description":    "Created by integration test",
		"visibility":     "private",
		"access_method":  "open",
		"lifecycle_type": "permanent",
		"owner_type":     "anonymous",
		"waypoints":      waypoints,
	}

	url := apiURL + "/api/v1/routes/" + routeID + "/create-waypoints"

	resp := doRequest(t, http.MethodPost, url, body, authToken)
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"createRouteWithWaypoints: expected 200",
	)
	defer resp.Body.Close()

	var result CreateWaypointsResponse

	err := json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(t, err,
		"createRouteWithWaypoints: failed to decode response",
	)

	return result
}

// imageStatusKey returns the Valkey key for an image's status hash.
func imageStatusKey(imageID string) string {
	return "image:status:" + imageID
}

// waitForImageStatus polls the Valkey hash at image:status:{imageID} every
// 200ms until the "stage" field equals expectedStage or timeout elapses.
// Calls t.Fatalf if the stage is not reached within timeout.
func waitForImageStatus(
	t *testing.T,
	client valkeygo.Client,
	imageID string,
	expectedStage string,
	timeout time.Duration,
) {
	t.Helper()

	const pollInterval = 200 * time.Millisecond

	deadline := time.Now().Add(timeout)
	key := imageStatusKey(imageID)

	for time.Now().Before(deadline) {
		fields := hGetAll(t, client, key)

		if fields["stage"] == expectedStage {
			return
		}

		time.Sleep(pollInterval)
	}

	fields := hGetAll(t, client, key)
	t.Fatalf(
		"waitForImageStatus: image %s stage=%q not reached within %s; "+
			"final fields: %v",
		imageID, expectedStage, timeout, fields,
	)
}

// hGetAll reads all fields of a Valkey hash and returns them as a
// map[string]string. Returns an empty map if the key does not exist.
func hGetAll(
	t *testing.T,
	client valkeygo.Client,
	key string,
) map[string]string {
	t.Helper()

	result, err := client.Do(
		context.Background(),
		client.B().Hgetall().Key(key).Build(),
	).AsStrMap()
	require.NoError(t, err,
		"hGetAll: HGETALL failed for key %s", key,
	)

	return result
}

// keyExists returns true if the given Valkey key exists.
func keyExists(
	t *testing.T,
	client valkeygo.Client,
	key string,
) bool {
	t.Helper()

	count, err := client.Do(
		context.Background(),
		client.B().Exists().Key(key).Build(),
	).AsInt64()
	require.NoError(t, err,
		"keyExists: EXISTS failed for key %s", key,
	)

	return count > 0
}

// xReadGroupNoAck reads up to count messages from streamKey using the given
// consumer group and consumer name. Messages are added to the PEL (pending
// entry list) so they must be explicitly acknowledged with xAck.
// Uses a 1-second block timeout to avoid hanging indefinitely.
func xReadGroupNoAck(
	t *testing.T,
	client valkeygo.Client,
	streamKey string,
	group string,
	consumer string,
	count int64,
) []streamMessage {
	t.Helper()

	const blockMs = 1000

	resp, err := client.Do(
		context.Background(),
		client.B().Xreadgroup().
			Group(group, consumer).
			Count(count).
			Block(blockMs).
			Streams().
			Key(streamKey).
			Id(">").
			Build(),
	).AsXRead()
	if err != nil {
		// XREADGROUP returns a nil/empty response when no messages are
		// available within the block timeout — treat this as an empty
		// result rather than a fatal error.
		return nil
	}

	entries, ok := resp[streamKey]
	if !ok {
		return nil
	}

	msgs := make([]streamMessage, 0, len(entries))

	for _, e := range entries {
		msgs = append(msgs, streamMessage{
			ID:     e.ID,
			Fields: e.FieldValues,
		})
	}

	return msgs
}

// xAck acknowledges one or more messages in a consumer group.
func xAck(
	t *testing.T,
	client valkeygo.Client,
	streamKey string,
	group string,
	ids ...string,
) {
	t.Helper()

	err := client.Do(
		context.Background(),
		client.B().Xack().
			Key(streamKey).
			Group(group).
			Id(ids...).
			Build(),
	).Error()
	require.NoError(t, err,
		"xAck: XACK failed for stream %s group %s ids %v",
		streamKey, group, ids,
	)
}

// xPendingCount returns the number of messages in the pending entry list
// for the given stream and consumer group.
// XPENDING summary response is an array: [count, min-id, max-id, consumers].
// The first element is the total pending count.
func xPendingCount(
	t *testing.T,
	client valkeygo.Client,
	streamKey string,
	group string,
) int64 {
	t.Helper()

	raw, err := client.Do(
		context.Background(),
		client.B().Xpending().
			Key(streamKey).
			Group(group).
			Build(),
	).ToArray()
	require.NoError(t, err,
		"xPendingCount: XPENDING failed for stream %s group %s",
		streamKey, group,
	)

	if len(raw) == 0 {
		return 0
	}

	count, err := raw[0].AsInt64()
	require.NoError(t, err,
		"xPendingCount: failed to parse count from XPENDING response",
	)

	return count
}

// xAutoClaim claims pending messages that have been idle for at least
// minIdleTime from streamKey, reassigning them to newConsumer.
// Returns up to count messages starting from cursor "0-0".
// XAUTOCLAIM response is an array: [next-cursor, [entries...], [deleted...]].
func xAutoClaim(
	t *testing.T,
	client valkeygo.Client,
	streamKey string,
	group string,
	newConsumer string,
	minIdleTime time.Duration,
	count int64,
) []streamMessage {
	t.Helper()

	idleStr := strconv.FormatInt(minIdleTime.Milliseconds(), 10)

	raw, err := client.Do(
		context.Background(),
		client.B().Xautoclaim().
			Key(streamKey).
			Group(group).
			Consumer(newConsumer).
			MinIdleTime(idleStr).
			Start("0-0").
			Count(count).
			Build(),
	).ToArray()
	require.NoError(t, err,
		"xAutoClaim: XAUTOCLAIM failed for stream %s group %s",
		streamKey, group,
	)

	// Response: [next-cursor, [entries...], [deleted-ids...]]
	// entries are at index 1.
	if len(raw) < 2 {
		return nil
	}

	entries, err := raw[1].AsXRange()
	require.NoError(t, err,
		"xAutoClaim: failed to parse entries from XAUTOCLAIM response",
	)

	msgs := make([]streamMessage, 0, len(entries))

	for _, e := range entries {
		msgs = append(msgs, streamMessage{
			ID:     e.ID,
			Fields: e.FieldValues,
		})
	}

	return msgs
}
