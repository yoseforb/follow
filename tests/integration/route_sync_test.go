//go:build integration

package integration_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SyncRouteSpec specifies a route to sync by route_id and version.
type SyncRouteSpec struct {
	RouteID string `json:"route_id"`
	Version int    `json:"version"`
}

// SyncRouteUpdateEntry is a single entry in the "updated" list of a
// sync response. Mirrors the FollowRouteDetails Goa type (same as
// GET /routes/{id}?include_images=true).
type SyncRouteUpdateEntry struct {
	Route          map[string]any   `json:"route"`
	Waypoints      []map[string]any `json:"waypoints"`
	TotalWaypoints int              `json:"total_waypoints"`
	CanNavigate    bool             `json:"can_navigate"`
	ImagesIncluded bool             `json:"images_included"`
}

// SyncRouteResponse is the typed response from POST /api/v1/routes/sync.
type SyncRouteResponse struct {
	Updated   []SyncRouteUpdateEntry `json:"updated"`
	Unchanged []string               `json:"unchanged"`
	NotFound  []string               `json:"not_found"`
}

// syncRoutes calls POST /api/v1/routes/sync with the given list of
// route specs. Returns (SyncRouteResponse, statusCode).
func syncRoutes(
	t *testing.T,
	specs []SyncRouteSpec,
	authToken string,
) (SyncRouteResponse, int) {
	t.Helper()

	body := map[string]any{
		"routes": specs,
	}

	resp := doRequest(
		t,
		http.MethodPost,
		apiURL+"/api/v1/routes/sync",
		body,
		authToken,
	)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return SyncRouteResponse{}, resp.StatusCode
	}

	var result SyncRouteResponse

	err := json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(t, err, "syncRoutes: failed to decode response")

	return result, resp.StatusCode
}

// TestSync_TwoRoutes_VersionProgression exercises the sync endpoint with
// two separate routes across version transitions.
//
// Scenario:
//  1. Create two separate users (userA creates routes, userB syncs them)
//  2. UserA creates and publishes 2 routes with real images
//  3. UserB syncs both routes at version 0 → both in "updated"
//  4. UserB syncs again with correct versions → both in "unchanged"
//  5. UserA modifies one route (metadata change bumps version)
//  6. UserB syncs with old versions → modified route in "updated",
//     other in "unchanged"
//  7. UserA deletes one route
//  8. UserB syncs → deleted route in "not_found"
//
//nolint:maintidx // intentionally long: covers 8-step sync scenario end-to-end
func TestSync_TwoRoutes_VersionProgression(t *testing.T) {
	// Step 1: Create users
	t.Log("Step 1: Create users")

	_, userAToken, _ := createAnonymousUser(t)
	_, userBToken, _ := createAnonymousUser(t)

	// Step 2: UserA creates and publishes two routes with different images
	t.Log("Step 2: UserA creates and publishes two routes")

	// Route 1: uses pexels-punttim-240223.jpg and
	// pexels-arthurbrognoli-2260838.jpg
	route1ID := prepareRoute(t, userAToken)
	t.Cleanup(func() { deleteRoute(t, route1ID, userAToken) })

	createRoute1Resp := createRouteWithWaypoints(
		t,
		userAToken,
		route1ID,
		[]waypointImageSpec{
			{"pexels-punttim-240223.jpg"},
			{"pexels-arthurbrognoli-2260838.jpg"},
		},
	)

	// Upload images for Route 1
	for _, entry := range createRoute1Resp.PresignedURLs {
		var filename string
		if entry.Position == 0 {
			filename = "pexels-punttim-240223.jpg"
		} else {
			filename = "pexels-arthurbrognoli-2260838.jpg"
		}
		imgData := loadTestImage(t, filename)
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
			"upload for Route 1 position %d must return 202",
			entry.Position,
		)
	}

	waitForRouteReady(t, route1ID, userAToken, 60*time.Second)
	publishRoute(t, route1ID, userAToken)

	route1Version := getRouteVersion(t, route1ID, userAToken)
	t.Logf("Route 1 published with version: %d", route1Version)

	// Route 2: uses pexels-hikaique-114797.jpg and
	// pexels-bluemix-12062129.jpg
	route2ID := prepareRoute(t, userAToken)
	t.Cleanup(func() { deleteRoute(t, route2ID, userAToken) })

	createRoute2Resp := createRouteWithWaypoints(
		t,
		userAToken,
		route2ID,
		[]waypointImageSpec{
			{"pexels-hikaique-114797.jpg"},
			{"pexels-bluemix-12062129.jpg"},
		},
	)

	// Upload images for Route 2
	for _, entry := range createRoute2Resp.PresignedURLs {
		var filename string
		if entry.Position == 0 {
			filename = "pexels-hikaique-114797.jpg"
		} else {
			filename = "pexels-bluemix-12062129.jpg"
		}
		imgData := loadTestImage(t, filename)
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
			"upload for Route 2 position %d must return 202",
			entry.Position,
		)
	}

	waitForRouteReady(t, route2ID, userAToken, 60*time.Second)
	publishRoute(t, route2ID, userAToken)

	route2Version := getRouteVersion(t, route2ID, userAToken)
	t.Logf("Route 2 published with version: %d", route2Version)

	// Step 3: UserB syncs both routes at version 0 (old)
	t.Log("Step 3: UserB syncs both routes at version 0")

	syncResp1, status := syncRoutes(
		t,
		[]SyncRouteSpec{
			{RouteID: route1ID, Version: 0},
			{RouteID: route2ID, Version: 0},
		},
		userBToken,
	)
	require.Equal(t, http.StatusOK, status,
		"sync must return 200",
	)

	// Both routes should be in "updated" (version mismatch)
	require.Len(t, syncResp1.Updated, 2,
		"both routes should be in updated when syncing at version 0",
	)
	require.Empty(t, syncResp1.Unchanged,
		"no routes should be unchanged at version 0",
	)
	require.Empty(t, syncResp1.NotFound,
		"no routes should be not_found",
	)

	// Verify both updated entries have the route object
	for _, entry := range syncResp1.Updated {
		assert.NotNil(t, entry.Route,
			"updated entry must have route object",
		)
		routeStatus, _ := entry.Route["route_status"].(string)
		assert.Equal(t, "published", routeStatus,
			"route status must be published",
		)
	}

	// Step 4: UserB syncs again with current versions
	t.Log("Step 4: UserB syncs with current versions")

	syncResp2, status := syncRoutes(
		t,
		[]SyncRouteSpec{
			{RouteID: route1ID, Version: route1Version},
			{RouteID: route2ID, Version: route2Version},
		},
		userBToken,
	)
	require.Equal(t, http.StatusOK, status,
		"second sync must return 200",
	)

	// Both routes should be in "unchanged" (version match)
	require.Empty(t, syncResp2.Updated,
		"no routes should be updated with matching versions",
	)
	require.Len(t, syncResp2.Unchanged, 2,
		"both routes should be unchanged",
	)
	require.Empty(t, syncResp2.NotFound,
		"no routes should be not_found",
	)

	// Verify unchanged list contains the correct route IDs
	unchangedMap := make(map[string]bool)
	for _, id := range syncResp2.Unchanged {
		unchangedMap[id] = true
	}
	assert.True(t, unchangedMap[route1ID],
		"route1 must be in unchanged list",
	)
	assert.True(t, unchangedMap[route2ID],
		"route2 must be in unchanged list",
	)

	// Step 5: UserA modifies Route 1 metadata (bumps version)
	t.Log("Step 5: UserA modifies Route 1 metadata")

	updateBody := map[string]any{
		"location_name": "Modified Location Name",
		"address":       "Updated Address String",
	}

	updateResp := doRequest(
		t,
		http.MethodPut,
		apiURL+"/api/v1/routes/"+route1ID,
		updateBody,
		userAToken,
	)
	require.Equal(t, http.StatusOK, updateResp.StatusCode,
		"update route must return 200",
	)
	updateResp.Body.Close()

	// Get updated version
	route1VersionAfterUpdate := getRouteVersion(
		t,
		route1ID,
		userAToken,
	)
	require.Greater(t, route1VersionAfterUpdate, route1Version,
		"version must increment after metadata update",
	)
	t.Logf("Route 1 version after update: %d", route1VersionAfterUpdate)

	// Step 6: UserB syncs with old versions
	t.Log("Step 6: UserB syncs with old versions")

	syncResp3, status := syncRoutes(
		t,
		[]SyncRouteSpec{
			{RouteID: route1ID, Version: route1Version},
			{RouteID: route2ID, Version: route2Version},
		},
		userBToken,
	)
	require.Equal(t, http.StatusOK, status,
		"third sync must return 200",
	)

	// Route 1 should be in "updated" (version changed),
	// Route 2 should be in "unchanged"
	require.Len(t, syncResp3.Updated, 1,
		"only modified route should be in updated",
	)
	require.Len(t, syncResp3.Unchanged, 1,
		"unmodified route should be in unchanged",
	)
	require.Empty(t, syncResp3.NotFound,
		"no routes should be not_found",
	)

	// Verify Route 1 is in updated
	assert.Equal(
		t,
		route1ID,
		syncResp3.Updated[0].Route["route_id"],
		"updated entry must be route1",
	)

	// Verify Route 2 is in unchanged
	assert.True(t, containsString(syncResp3.Unchanged, route2ID),
		"route2 must be in unchanged list",
	)

	// Step 7: UserA deletes Route 1
	t.Log("Step 7: UserA deletes Route 1")

	deleteResp := doRequest(
		t,
		http.MethodDelete,
		apiURL+"/api/v1/routes/"+route1ID,
		nil,
		userAToken,
	)
	require.Equal(t, http.StatusOK, deleteResp.StatusCode,
		"delete must return 200",
	)
	deleteResp.Body.Close()

	// Step 8: UserB syncs deleted route
	t.Log("Step 8: UserB syncs deleted route")

	syncResp4, status := syncRoutes(
		t,
		[]SyncRouteSpec{
			{RouteID: route1ID, Version: route1VersionAfterUpdate},
			{RouteID: route2ID, Version: route2Version},
		},
		userBToken,
	)
	require.Equal(t, http.StatusOK, status,
		"fourth sync must return 200",
	)

	// Route 1 should be in "not_found", Route 2 in "unchanged"
	require.Empty(t, syncResp4.Updated,
		"no routes should be updated",
	)
	require.Len(t, syncResp4.Unchanged, 1,
		"undeleted route should be unchanged",
	)
	require.Len(t, syncResp4.NotFound, 1,
		"deleted route should be not_found",
	)

	// Verify deleted route is in not_found
	assert.True(t, containsString(syncResp4.NotFound, route1ID),
		"deleted route must be in not_found list",
	)

	// Verify undeleted route is in unchanged
	assert.True(t, containsString(syncResp4.Unchanged, route2ID),
		"undeleted route must be in unchanged list",
	)

	t.Log("All sync flow steps completed successfully")
}

// containsString returns true if the slice contains the given string.
func containsString(slice []string, target string) bool {
	return slices.Contains(slice, target)
}
