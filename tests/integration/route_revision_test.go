//go:build integration

package integration_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// PrepareRevisionResponse is the typed response from
// POST /api/v1/routes/{routeID}/revisions/prepare.
type PrepareRevisionResponse struct {
	RevisionID string `json:"revision_id"`
	RouteID    string `json:"route_id"`
	ExpiresAt  string `json:"expires_at"`
}

// ApplyRevisionWaypointResult is a single waypoint entry in
// an apply-revision response.
type ApplyRevisionWaypointResult struct {
	WaypointID  string `json:"waypoint_id"`
	Position    int    `json:"position"`
	ImageID     string `json:"image_id"`
	UploadURL   string `json:"upload_url"`
	UploadToken string `json:"upload_token"`
	ExpiresAt   string `json:"expires_at"`
}

// ApplyRevisionResponse is the typed response from
// POST /api/v1/routes/{routeID}/revisions/{revisionID}/apply.
type ApplyRevisionResponse struct {
	RevisionID string                        `json:"revision_id"`
	RouteID    string                        `json:"route_id"`
	Status     string                        `json:"status"`
	Waypoints  []ApplyRevisionWaypointResult `json:"waypoints"`
}

// CommitRevisionResponse is the typed response from
// POST /api/v1/routes/{routeID}/revisions/{revisionID}/commit.
type CommitRevisionResponse struct {
	RouteID        string `json:"route_id"`
	Version        int    `json:"version"`
	TotalWaypoints int    `json:"total_waypoints"`
	UpdatedAt      string `json:"updated_at"`
}

// prepareRevision calls POST /api/v1/routes/{routeID}/revisions/prepare.
// Returns (PrepareRevisionResponse, statusCode).
func prepareRevision(
	t *testing.T,
	routeID string,
	authToken string,
) (PrepareRevisionResponse, int) {
	t.Helper()

	resp := doRequest(
		t,
		http.MethodPost,
		apiURL+"/api/v1/routes/"+routeID+"/revisions/prepare",
		nil,
		authToken,
	)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return PrepareRevisionResponse{}, resp.StatusCode
	}

	var result PrepareRevisionResponse

	err := json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(t, err, "prepareRevision: failed to decode response")

	return result, resp.StatusCode
}

// applyRevision calls POST
// /api/v1/routes/{routeID}/revisions/{revisionID}/apply.
// Returns (ApplyRevisionResponse, statusCode).
func applyRevision(
	t *testing.T,
	routeID string,
	revisionID string,
	waypoints []map[string]any,
	authToken string,
) (ApplyRevisionResponse, int) {
	t.Helper()

	body := map[string]any{
		"route_id":       routeID,
		"revision_id":    revisionID,
		"waypoints":      waypoints,
		"location_name":  "Integration Test Location",
		"address":        "123 Integration Test Street, Test City",
		"start_point":    "Main entrance, ground floor",
		"end_point":      "Test destination, 2nd floor",
		"visibility":     "private",
		"access_method":  "open",
		"lifecycle_type": "permanent",
		"owner_type":     "anonymous",
	}

	resp := doRequest(
		t,
		http.MethodPost,
		apiURL+"/api/v1/routes/"+routeID+
			"/revisions/"+revisionID+"/apply",
		body,
		authToken,
	)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ApplyRevisionResponse{}, resp.StatusCode
	}

	var result ApplyRevisionResponse

	err := json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(t, err, "applyRevision: failed to decode response")

	return result, resp.StatusCode
}

// commitRevision calls POST
// /api/v1/routes/{routeID}/revisions/{revisionID}/commit.
// Returns (CommitRevisionResponse, statusCode).
func commitRevision(
	t *testing.T,
	routeID string,
	revisionID string,
	authToken string,
) (CommitRevisionResponse, int) {
	t.Helper()

	resp := doRequest(
		t,
		http.MethodPost,
		apiURL+"/api/v1/routes/"+routeID+
			"/revisions/"+revisionID+"/commit",
		nil,
		authToken,
	)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return CommitRevisionResponse{}, resp.StatusCode
	}

	var result CommitRevisionResponse

	err := json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(t, err, "commitRevision: failed to decode response")

	return result, resp.StatusCode
}

// publishRoute calls POST /api/v1/routes/{routeID}/publish
// and asserts a 200 OK response.
func publishRoute(t *testing.T, routeID, authToken string) {
	t.Helper()

	resp := doRequest(
		t,
		http.MethodPost,
		apiURL+"/api/v1/routes/"+routeID+"/publish",
		nil,
		authToken,
	)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode,
		"publishRoute: expected 200 OK",
	)
}

// buildExistingImageWaypoint builds a waypoint body that reuses an
// already-processed image by image_id.
func buildExistingImageWaypoint(
	pos int,
	imageID string,
) map[string]any {
	m := markerForPosition(pos)

	return map[string]any{
		"image_id":    imageID,
		"marker_x":    m.X,
		"marker_y":    m.Y,
		"marker_type": "next_step",
		"description": "Revised waypoint " + string(rune('A'+pos)),
	}
}

// buildNewImageWaypoint builds a waypoint body that requests a new image
// upload using image_metadata.
func buildNewImageWaypoint(
	pos int,
	filename string,
	fileSize int,
) map[string]any {
	m := markerForPosition(pos)

	return map[string]any{
		"image_metadata": map[string]any{
			"content_type":      "image/jpeg",
			"file_size":         fileSize,
			"original_filename": filename,
		},
		"marker_x":    m.X,
		"marker_y":    m.Y,
		"marker_type": "next_step",
		"description": "New waypoint " + string(rune('A'+pos)),
	}
}

// setupPublishedRoute creates a user, prepares a route, creates waypoints,
// uploads images, waits for ready, then publishes. Returns (routeID,
// authToken, imageIDs, cleanup).
func setupPublishedRoute(
	t *testing.T,
) (routeID, authToken string, imageIDs []string) {
	t.Helper()

	_, authToken = createAnonymousUser(t)
	routeID = prepareRoute(t, authToken)
	t.Cleanup(func() { deleteRoute(t, routeID, authToken) })

	cwResp := createRouteWithWaypoints(
		t,
		authToken,
		routeID,
		defaultTestImages,
	)

	// Upload images to the gateway.
	for _, entry := range cwResp.PresignedURLs {
		imgData := loadTestImage(t, defaultTestImages[entry.Position].Filename)
		uploadResp := uploadToGateway(
			t,
			entry.UploadURL,
			entry.UploadToken,
			imgData,
		)
		uploadResp.Body.Close()

		require.Equal(
			t,
			http.StatusAccepted,
			uploadResp.StatusCode,
			"setupPublishedRoute: upload for position %d must return 202",
			entry.Position,
		)
	}

	// Wait for all images to be processed.
	waitForRouteReady(t, routeID, authToken, 60*time.Second)

	// Publish the route.
	publishRoute(t, routeID, authToken)

	// Collect image IDs from the create-waypoints response.
	imageIDs = make([]string, len(cwResp.PresignedURLs))
	for _, entry := range cwResp.PresignedURLs {
		imageIDs[entry.Position] = entry.ImageID
	}

	return routeID, authToken, imageIDs
}

// getRouteVersion retrieves the current version field from GET
// /api/v1/routes/{routeID}.
func getRouteVersion(
	t *testing.T,
	routeID string,
	authToken string,
) int {
	t.Helper()

	resp := doRequest(
		t,
		http.MethodGet,
		apiURL+"/api/v1/routes/"+routeID,
		nil,
		authToken,
	)

	var body map[string]any

	err := json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	require.NoError(t, err, "getRouteVersion: failed to decode route response")

	route, ok := body["route"].(map[string]any)
	require.True(t, ok, "getRouteVersion: response must contain 'route' object")

	version, _ := route["version"].(float64)

	return int(version)
}

// getRouteWaypointCount retrieves total_waypoints from the route response.
func getRouteWaypointCount(
	t *testing.T,
	routeID string,
	authToken string,
) int {
	t.Helper()

	resp := doRequest(
		t,
		http.MethodGet,
		apiURL+"/api/v1/routes/"+routeID,
		nil,
		authToken,
	)

	var body map[string]any

	err := json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	require.NoError(t, err,
		"getRouteWaypointCount: failed to decode route response",
	)

	count, _ := body["total_waypoints"].(float64)

	return int(count)
}

// TestRevision_HappyPath_AllExistingImages tests the full revision lifecycle
// where all waypoints reuse existing (already-processed) images.
// The revision should become "ready" immediately without image upload,
// and can be committed right away.
func TestRevision_HappyPath_AllExistingImages(t *testing.T) {
	routeID, authToken, imageIDs := setupPublishedRoute(t)

	// Step 1: Prepare revision.
	prepResp, status := prepareRevision(t, routeID, authToken)
	require.Equal(t, http.StatusCreated, status,
		"prepare_revision must return 201",
	)

	require.NotEmpty(t, prepResp.RevisionID,
		"prepare_revision must return non-empty revision_id",
	)
	assert.Equal(t, routeID, prepResp.RouteID,
		"prepare_revision route_id must match",
	)
	assert.NotEmpty(t, prepResp.ExpiresAt,
		"prepare_revision must return non-empty expires_at",
	)

	revisionID := prepResp.RevisionID

	// Step 2: Apply revision using all existing image IDs.
	waypoints := make([]map[string]any, len(imageIDs))
	for i, imgID := range imageIDs {
		waypoints[i] = buildExistingImageWaypoint(i, imgID)
	}

	applyResp, status := applyRevision(
		t,
		routeID,
		revisionID,
		waypoints,
		authToken,
	)
	require.Equal(t, http.StatusOK, status,
		"apply_revision must return 200",
	)

	assert.Equal(t, revisionID, applyResp.RevisionID,
		"apply_revision revision_id must match",
	)
	assert.Equal(t, routeID, applyResp.RouteID,
		"apply_revision route_id must match",
	)

	// All images are existing — the revision must be ready immediately.
	assert.Equal(t, "ready", applyResp.Status,
		"apply_revision status must be 'ready' when all images are existing",
	)

	require.Len(t, applyResp.Waypoints, len(imageIDs),
		"apply_revision must return one waypoint entry per input waypoint",
	)

	for i, wp := range applyResp.Waypoints {
		assert.NotEmptyf(t, wp.WaypointID,
			"apply_revision waypoints[%d].waypoint_id must not be empty", i,
		)
		assert.NotEmptyf(t, wp.ImageID,
			"apply_revision waypoints[%d].image_id must not be empty", i,
		)
		// No upload URLs expected for existing images.
		assert.Emptyf(t, wp.UploadURL,
			"apply_revision waypoints[%d].upload_url must be empty "+
				"for existing images", i,
		)
	}

	// Step 3: Capture the current version before committing.
	versionBefore := getRouteVersion(t, routeID, authToken)

	// Step 4: Commit the revision.
	commitResp, status := commitRevision(t, routeID, revisionID, authToken)
	require.Equal(t, http.StatusOK, status,
		"commit_revision must return 200",
	)

	assert.Equal(t, routeID, commitResp.RouteID,
		"commit_revision route_id must match",
	)
	assert.Greater(t, commitResp.Version, versionBefore,
		"commit_revision must increment route version",
	)
	assert.Equal(t, len(imageIDs), commitResp.TotalWaypoints,
		"commit_revision total_waypoints must match waypoint count",
	)
	assert.NotEmpty(t, commitResp.UpdatedAt,
		"commit_revision updated_at must not be empty",
	)

	// Step 5: Verify the route remains published after commit.
	verifyResp := doRequest(
		t,
		http.MethodGet,
		apiURL+"/api/v1/routes/"+routeID,
		nil,
		authToken,
	)

	var verifyBody map[string]any

	err := json.NewDecoder(verifyResp.Body).Decode(&verifyBody)
	verifyResp.Body.Close()
	require.NoError(t, err,
		"post-commit GET route must decode successfully",
	)

	route, ok := verifyBody["route"].(map[string]any)
	require.True(t, ok,
		"post-commit GET route must contain 'route' object",
	)

	assert.Equal(t, "published", route["route_status"],
		"route must remain published after commit",
	)
}

// TestRevision_HappyPath_MixedImages tests a revision that mixes existing
// images with new image uploads. Because new images require gateway
// processing, the apply response status must be "pending".
func TestRevision_HappyPath_MixedImages(t *testing.T) {
	routeID, authToken, imageIDs := setupPublishedRoute(t)

	// Prepare revision.
	prepResp, status := prepareRevision(t, routeID, authToken)
	require.Equal(t, http.StatusCreated, status,
		"prepare_revision must return 201",
	)

	revisionID := prepResp.RevisionID

	// Build mixed waypoints: reuse first image, request a new upload for
	// the second.
	newImageSpec := defaultTestImages[1]
	newImageBytes := loadTestImage(t, newImageSpec.Filename)

	waypoints := []map[string]any{
		buildExistingImageWaypoint(0, imageIDs[0]),
		buildNewImageWaypoint(1, newImageSpec.Filename, len(newImageBytes)),
	}

	applyResp, status := applyRevision(
		t,
		routeID,
		revisionID,
		waypoints,
		authToken,
	)
	require.Equal(t, http.StatusOK, status,
		"apply_revision must return 200",
	)

	// Mixed images — at least one new image → status must be "pending".
	assert.Equal(t, "pending", applyResp.Status,
		"apply_revision status must be 'pending' when new images are "+
			"requested",
	)

	// The new-image waypoint must have an upload URL.
	require.Len(t, applyResp.Waypoints, 2,
		"apply_revision must return 2 waypoints",
	)

	var newImageWaypoint *ApplyRevisionWaypointResult

	for i := range applyResp.Waypoints {
		if applyResp.Waypoints[i].UploadURL != "" {
			newImageWaypoint = &applyResp.Waypoints[i]

			break
		}
	}

	require.NotNil(t, newImageWaypoint,
		"apply_revision must provide an upload_url for the new-image waypoint",
	)
	assert.NotEmpty(t, newImageWaypoint.UploadToken,
		"new-image waypoint must have a non-empty upload_token",
	)
}

// TestRevision_DuplicateRevisionRejection verifies that attempting to prepare
// a second revision on a route that already has an open revision is rejected
// with an error (route_state_error → 422).
func TestRevision_DuplicateRevisionRejection(t *testing.T) {
	routeID, authToken, _ := setupPublishedRoute(t)

	// First prepare must succeed.
	_, status := prepareRevision(t, routeID, authToken)
	require.Equal(t, http.StatusCreated, status,
		"first prepare_revision must return 201",
	)

	// Second prepare on the same route must fail with a conflict-like
	// error. The server returns 422 (route_state_error) for this case.
	resp := doRequest(
		t,
		http.MethodPost,
		apiURL+"/api/v1/routes/"+routeID+"/revisions/prepare",
		nil,
		authToken,
	)
	defer resp.Body.Close()

	assert.NotEqualf(t, http.StatusCreated, resp.StatusCode,
		"second prepare_revision must not return 201",
	)
	assert.GreaterOrEqual(t, resp.StatusCode, 400,
		"second prepare_revision must return an error status code",
	)
}

// TestRevision_CommitNotReadyRevision verifies that committing a revision
// whose images are still pending (not all processed) returns an error.
func TestRevision_CommitNotReadyRevision(t *testing.T) {
	routeID, authToken, imageIDs := setupPublishedRoute(t)

	// Prepare revision.
	prepResp, status := prepareRevision(t, routeID, authToken)
	require.Equal(t, http.StatusCreated, status,
		"prepare_revision must return 201",
	)

	revisionID := prepResp.RevisionID

	// Apply with at least one new image so the revision stays "pending".
	newImageSpec := defaultTestImages[0]
	newImageBytes := loadTestImage(t, newImageSpec.Filename)

	waypoints := []map[string]any{
		buildExistingImageWaypoint(0, imageIDs[0]),
		buildNewImageWaypoint(1, newImageSpec.Filename, len(newImageBytes)),
	}

	_, status = applyRevision(
		t,
		routeID,
		revisionID,
		waypoints,
		authToken,
	)
	require.Equal(t, http.StatusOK, status,
		"apply_revision must return 200",
	)

	// Immediately attempt to commit without waiting for image processing.
	// The revision is still "pending" — the commit must be rejected.
	resp := doRequest(
		t,
		http.MethodPost,
		apiURL+"/api/v1/routes/"+routeID+
			"/revisions/"+revisionID+"/commit",
		nil,
		authToken,
	)
	defer resp.Body.Close()

	assert.NotEqual(t, http.StatusOK, resp.StatusCode,
		"commit of a pending revision must not return 200",
	)
	assert.GreaterOrEqual(t, resp.StatusCode, 400,
		"commit of a pending revision must return an error status code",
	)
}

// TestRevision_OwnershipEnforcement_PrepareByOtherUser verifies that a user
// cannot prepare a revision on a route they do not own.
func TestRevision_OwnershipEnforcement_PrepareByOtherUser(t *testing.T) {
	// Create route as userA.
	routeID, tokenA, _ := setupPublishedRoute(t)

	// Create userB.
	_, tokenB := createAnonymousUser(t)

	// UserB attempts to prepare a revision on userA's route.
	resp := doRequest(
		t,
		http.MethodPost,
		apiURL+"/api/v1/routes/"+routeID+"/revisions/prepare",
		nil,
		tokenB,
	)
	defer resp.Body.Close()

	assert.True(t,
		resp.StatusCode == http.StatusForbidden ||
			resp.StatusCode == http.StatusUnauthorized,
		"prepare_revision by non-owner must return 403 or 401, got %d",
		resp.StatusCode,
	)

	// Verify userA's token still works — route was not modified.
	verifyResp := doRequest(
		t,
		http.MethodGet,
		apiURL+"/api/v1/routes/"+routeID,
		nil,
		tokenA,
	)
	defer verifyResp.Body.Close()

	assert.Equal(t, http.StatusOK, verifyResp.StatusCode,
		"route must remain accessible to its owner after rejected prepare",
	)
}

// TestRevision_OwnershipEnforcement_CommitByOtherUser verifies that a user
// cannot commit a revision they did not create (or for a route they don't own).
func TestRevision_OwnershipEnforcement_CommitByOtherUser(t *testing.T) {
	// Create route as userA and prepare+apply a ready revision.
	routeID, tokenA, imageIDs := setupPublishedRoute(t)

	prepResp, status := prepareRevision(t, routeID, tokenA)
	require.Equal(t, http.StatusCreated, status,
		"prepare_revision by owner must succeed",
	)

	revisionID := prepResp.RevisionID

	// Apply with all existing images so the revision is ready.
	waypoints := make([]map[string]any, len(imageIDs))
	for i, imgID := range imageIDs {
		waypoints[i] = buildExistingImageWaypoint(i, imgID)
	}

	_, status = applyRevision(
		t,
		routeID,
		revisionID,
		waypoints,
		tokenA,
	)
	require.Equal(t, http.StatusOK, status,
		"apply_revision by owner must succeed",
	)

	// Create userB and attempt to commit the revision.
	_, tokenB := createAnonymousUser(t)

	resp := doRequest(
		t,
		http.MethodPost,
		apiURL+"/api/v1/routes/"+routeID+
			"/revisions/"+revisionID+"/commit",
		nil,
		tokenB,
	)
	defer resp.Body.Close()

	assert.True(t,
		resp.StatusCode == http.StatusForbidden ||
			resp.StatusCode == http.StatusUnauthorized,
		"commit_revision by non-owner must return 403 or 401, got %d",
		resp.StatusCode,
	)
}

// TestRevision_RouteNotPublished verifies that prepare_revision is rejected
// when the route is not in "published" status.
func TestRevision_RouteNotPublished(t *testing.T) {
	_, authToken := createAnonymousUser(t)
	routeID := prepareRoute(t, authToken)
	t.Cleanup(func() { deleteRoute(t, routeID, authToken) })

	// Create waypoints but do NOT publish — route stays in pending/ready.
	cwResp := createRouteWithWaypoints(
		t,
		authToken,
		routeID,
		defaultTestImages,
	)

	// Upload images to bring the route to "ready".
	for _, entry := range cwResp.PresignedURLs {
		imgData := loadTestImage(
			t,
			defaultTestImages[entry.Position].Filename,
		)
		uploadResp := uploadToGateway(
			t,
			entry.UploadURL,
			entry.UploadToken,
			imgData,
		)
		uploadResp.Body.Close()
	}

	waitForRouteReady(t, routeID, authToken, 60*time.Second)

	// Attempt prepare_revision on a "ready" (not published) route.
	resp := doRequest(
		t,
		http.MethodPost,
		apiURL+"/api/v1/routes/"+routeID+"/revisions/prepare",
		nil,
		authToken,
	)
	defer resp.Body.Close()

	assert.NotEqual(t, http.StatusCreated, resp.StatusCode,
		"prepare_revision must not succeed on a non-published route",
	)
	assert.GreaterOrEqual(t, resp.StatusCode, 400,
		"prepare_revision on non-published route must return an error "+
			"status code",
	)
}

// TestRevision_RouteIDMismatch_OnApply verifies that applying a revision
// using a mismatched route_id (a different route than the one the revision
// belongs to) returns an error.
func TestRevision_RouteIDMismatch_OnApply(t *testing.T) {
	// Set up two separate published routes under the same user.
	routeIDA, authToken, imageIDsA := setupPublishedRoute(t)

	// Setup second route, re-using the same auth token user.
	routeIDB := prepareRoute(t, authToken)
	t.Cleanup(func() { deleteRoute(t, routeIDB, authToken) })

	cwRespB := createRouteWithWaypoints(
		t,
		authToken,
		routeIDB,
		defaultTestImages,
	)

	for _, entry := range cwRespB.PresignedURLs {
		imgData := loadTestImage(
			t,
			defaultTestImages[entry.Position].Filename,
		)
		uploadResp := uploadToGateway(
			t,
			entry.UploadURL,
			entry.UploadToken,
			imgData,
		)
		uploadResp.Body.Close()
	}

	waitForRouteReady(t, routeIDB, authToken, 60*time.Second)
	publishRoute(t, routeIDB, authToken)

	// Prepare a revision for routeA.
	prepResp, status := prepareRevision(t, routeIDA, authToken)
	require.Equal(t, http.StatusCreated, status,
		"prepare_revision for routeA must succeed",
	)

	revisionID := prepResp.RevisionID

	// Attempt to apply the revision using routeB's ID as the path param.
	// The server must reject this as a mismatch.
	waypoints := make([]map[string]any, len(imageIDsA))
	for i, imgID := range imageIDsA {
		waypoints[i] = buildExistingImageWaypoint(i, imgID)
	}

	resp := doRequest(
		t,
		http.MethodPost,
		apiURL+"/api/v1/routes/"+routeIDB+
			"/revisions/"+revisionID+"/apply",
		map[string]any{
			"route_id":       routeIDB,
			"revision_id":    revisionID,
			"waypoints":      waypoints,
			"visibility":     "private",
			"access_method":  "open",
			"lifecycle_type": "permanent",
			"owner_type":     "anonymous",
		},
		authToken,
	)
	defer resp.Body.Close()

	assert.NotEqual(t, http.StatusOK, resp.StatusCode,
		"apply_revision with mismatched route_id must not return 200",
	)
	assert.GreaterOrEqual(t, resp.StatusCode, 400,
		"apply_revision with mismatched route_id must return an error "+
			"status code",
	)
}
