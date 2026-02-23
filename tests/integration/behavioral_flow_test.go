//go:build integration

package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waypointImageMap maps waypoint position to the test image filename to use.
var waypointImageMap = map[int]string{
	0: "pexels-punttim-240223.jpg",
	1: "pexels-arthurbrognoli-2260838.jpg",
	2: "pexels-hikaique-114797.jpg",
}

// TestFullAPIBehavioralFlow exercises every major API endpoint in sequence,
// covering user creation, route lifecycle, waypoint management, image
// upload/replace, and route deletion.
//
//nolint:maintidx,gocognit // integration test: sequential steps require higher complexity
func TestFullAPIBehavioralFlow(t *testing.T) {
	// ------------------------------------------------------------------ //
	// Step 1: Create anonymous user                                        //
	// ------------------------------------------------------------------ //
	t.Log("Step 1: Create anonymous user")

	userID, authToken := createAnonymousUser(t)

	// Verify full schema fields beyond what the helper checks.
	step1Resp := doRequest(
		t,
		http.MethodPost,
		apiURL+"/api/v1/users/anonymous",
		map[string]any{},
		"",
	)
	step1Body := decodeJSON(t, step1Resp)

	assert.NotEmpty(t, step1Body["expires_at"],
		"Step 1: expires_at must not be empty",
	)
	assert.NotEmpty(t, step1Body["created_at"],
		"Step 1: created_at must not be empty",
	)

	// ------------------------------------------------------------------ //
	// Step 2: Get anonymous user                                           //
	// ------------------------------------------------------------------ //
	t.Log("Step 2: Get anonymous user")

	step2Resp := doRequest(
		t,
		http.MethodGet,
		apiURL+"/api/v1/users/anonymous/"+userID,
		nil,
		authToken,
	)

	require.Equal(t, http.StatusOK, step2Resp.StatusCode,
		"Step 2: expected 200 from GET /api/v1/users/anonymous/{userID}",
	)

	step2Body := decodeJSON(t, step2Resp)

	userObj, ok := step2Body["user"].(map[string]any)
	require.True(t, ok, "Step 2: response must contain a 'user' object")

	assert.Equal(t, userID, userObj["id"],
		"Step 2: user.id must match userID",
	)
	assert.NotEmpty(t, userObj["created_at"],
		"Step 2: user.created_at must not be empty",
	)

	// ------------------------------------------------------------------ //
	// Step 3: Refresh JWT token                                            //
	// ------------------------------------------------------------------ //
	t.Log("Step 3: Refresh JWT token")

	step3Resp := doRequest(
		t,
		http.MethodPost,
		apiURL+"/api/v1/auth/refresh",
		map[string]any{},
		authToken,
	)

	require.Equal(t, http.StatusOK, step3Resp.StatusCode,
		"Step 3: expected 200 from POST /api/v1/auth/refresh",
	)

	step3Body := decodeJSON(t, step3Resp)

	refreshedToken, ok := step3Body["token"].(string)
	require.True(t, ok, "Step 3: token must be a string")
	require.NotEmpty(t, refreshedToken,
		"Step 3: refreshed token must not be empty",
	)

	assert.NotEmpty(t, step3Body["expires_at"],
		"Step 3: expires_at must not be empty",
	)

	authToken = refreshedToken

	// ------------------------------------------------------------------ //
	// Step 4: Prepare route                                                //
	// ------------------------------------------------------------------ //
	t.Log("Step 4: Prepare route")

	routeID := prepareRoute(t, authToken)

	// Verify full schema fields beyond what the helper checks.
	step4Resp := doRequest(
		t,
		http.MethodPost,
		apiURL+"/api/v1/routes/prepare",
		map[string]any{},
		authToken,
	)
	step4Body := decodeJSON(t, step4Resp)

	assert.NotEmpty(t, step4Body["prepared_at"],
		"Step 4: prepared_at must not be empty",
	)

	// Register cleanup before Step 5 so the route is deleted on test
	// failure (best-effort; deleteRoute only logs on non-200).
	t.Cleanup(func() { deleteRoute(t, routeID, authToken) })

	// ------------------------------------------------------------------ //
	// Step 5: Create route with 3 waypoints                               //
	// ------------------------------------------------------------------ //
	t.Log("Step 5: Create route with 3 waypoints")

	createBody := map[string]any{
		"name":           "Test Integration Route",
		"description":    "Created by TestFullAPIBehavioralFlow",
		"visibility":     "private",
		"access_method":  "open",
		"lifecycle_type": "permanent",
		"owner_type":     "anonymous",
		"waypoints": []map[string]any{
			{
				"marker_x":    0.10,
				"marker_y":    0.20,
				"marker_type": "next_step",
				"description": "Waypoint 1 - turn left",
				"image_metadata": map[string]any{
					"original_filename": "pexels-punttim-240223.jpg",
					"content_type":      "image/jpeg",
					"file_size":         905489,
				},
			},
			{
				"marker_x":    0.30,
				"marker_y":    0.40,
				"marker_type": "next_step",
				"description": "Waypoint 2 - go straight",
				"image_metadata": map[string]any{
					"original_filename": "pexels-arthurbrognoli-2260838.jpg",
					"content_type":      "image/jpeg",
					"file_size":         786386,
				},
			},
			{
				"marker_x":    0.50,
				"marker_y":    0.60,
				"marker_type": "final_destination",
				"description": "Waypoint 3 - you have arrived",
				"image_metadata": map[string]any{
					"original_filename": "pexels-hikaique-114797.jpg",
					"content_type":      "image/jpeg",
					"file_size":         1159336,
				},
			},
		},
	}

	step5HTTPResp := doRequest(
		t,
		http.MethodPost,
		fmt.Sprintf(
			"%s/api/v1/routes/%s/create-waypoints",
			apiURL,
			routeID,
		),
		createBody,
		authToken,
	)

	require.Equal(t, http.StatusOK, step5HTTPResp.StatusCode,
		"Step 5: expected 200 from POST .../create-waypoints",
	)

	var step5 CreateWaypointsResponse

	step5RawBytes, err := io.ReadAll(step5HTTPResp.Body)
	require.NoError(t, err, "Step 5: failed to read create-waypoints body")
	step5HTTPResp.Body.Close()

	err = json.Unmarshal(step5RawBytes, &step5)
	require.NoError(
		t,
		err,
		"Step 5: failed to decode create-waypoints response",
	)

	assert.Equal(t, routeID, step5.RouteID,
		"Step 5: route_id must match",
	)
	assert.Equal(t, "pending", step5.RouteStatus,
		"Step 5: route_status must be pending",
	)

	require.Len(t, step5.WaypointIDs, 3,
		"Step 5: waypoint_ids must have exactly 3 entries",
	)

	for i, wid := range step5.WaypointIDs {
		assert.NotEmptyf(t, wid,
			"Step 5: waypoint_ids[%d] must not be empty", i,
		)
	}

	require.Len(t, step5.PresignedURLs, 3,
		"Step 5: presigned_urls must have exactly 3 entries",
	)

	validPositions := map[int]bool{0: true, 1: true, 2: true}

	for i, entry := range step5.PresignedURLs {
		assert.NotEmptyf(t, entry.ImageID,
			"Step 5: presigned_urls[%d].image_id must not be empty", i,
		)
		assert.NotEmptyf(t, entry.UploadURL,
			"Step 5: presigned_urls[%d].upload_url must not be empty", i,
		)
		assert.Truef(t, validPositions[entry.Position],
			"Step 5: presigned_urls[%d].position=%d not in {0,1,2}",
			i, entry.Position,
		)
		assert.NotEmptyf(t, entry.ExpiresAt,
			"Step 5: presigned_urls[%d].expires_at must not be empty", i,
		)
	}

	waypointIDs := step5.WaypointIDs

	originalMarkers := []struct{ X, Y float64 }{
		{0.10, 0.20},
		{0.30, 0.40},
		{0.50, 0.60},
	}

	// ------------------------------------------------------------------ //
	// Step 6: Upload images to gateway via presigned URLs                 //
	// ------------------------------------------------------------------ //
	t.Log("Step 6: Upload images to gateway via presigned URLs")

	for _, entry := range step5.PresignedURLs {
		imgFile, hasImage := waypointImageMap[entry.Position]
		require.Truef(t, hasImage,
			"Step 6: no image mapped for position %d", entry.Position,
		)

		imgData := loadTestImage(t, imgFile)

		req, reqErr := http.NewRequest(
			http.MethodPut,
			entry.UploadURL,
			bytes.NewReader(imgData),
		)
		require.NoErrorf(t, reqErr,
			"Step 6: failed to build PUT request for position %d",
			entry.Position,
		)

		req.Header.Set("Content-Type", "application/octet-stream")

		uploadClient := &http.Client{Timeout: 60 * time.Second}

		uploadResp, uploadErr := uploadClient.Do(req)
		require.NoErrorf(t, uploadErr,
			"Step 6: transport error uploading position %d", entry.Position,
		)

		uploadRespBytes, readErr := io.ReadAll(uploadResp.Body)
		uploadResp.Body.Close()
		require.NoErrorf(t, readErr,
			"Step 6: failed to read upload response body for position %d",
			entry.Position,
		)

		require.Equalf(t, http.StatusAccepted, uploadResp.StatusCode,
			"Step 6: expected 202 for position %d, got %d; body: %s",
			entry.Position, uploadResp.StatusCode, uploadRespBytes,
		)

		var uploadResult map[string]any

		err = json.Unmarshal(uploadRespBytes, &uploadResult)
		require.NoErrorf(t, err,
			"Step 6: failed to decode upload response for position %d",
			entry.Position,
		)

		uploadStatus, _ := uploadResult["status"].(string)

		validUploadStatuses := map[string]bool{
			"processing": true,
			"queued":     true,
			"pending":    true,
		}
		assert.Truef(
			t,
			validUploadStatuses[uploadStatus],
			"Step 6: position %d upload status=%q not in {processing,queued,pending}",
			entry.Position,
			uploadStatus,
		)

		t.Logf(
			"Step 6: uploaded position %d (image_id=%s, status=%s)",
			entry.Position, entry.ImageID, uploadStatus,
		)
	}

	// ------------------------------------------------------------------ //
	// Step 7: Wait for route to reach ready status                        //
	// ------------------------------------------------------------------ //
	t.Log("Step 7: Wait for route to reach ready status")

	waitForRouteStatus(t, routeID, authToken, "ready", 60*time.Second)

	// ------------------------------------------------------------------ //
	// Step 8: Publish route (READY → PUBLISHED)                           //
	// ------------------------------------------------------------------ //
	t.Log("Step 8: Publish route")

	step8Resp := doRequest(
		t,
		http.MethodPost,
		fmt.Sprintf(
			"%s/api/v1/routes/%s/publish",
			apiURL,
			routeID,
		),
		nil,
		authToken,
	)

	require.Equal(t, http.StatusOK, step8Resp.StatusCode,
		"Step 8: expected 200 from POST .../publish",
	)

	step8Body := decodeJSON(t, step8Resp)

	assert.Equal(t, routeID, step8Body["route_id"],
		"Step 8: route_id must match",
	)
	assert.Equal(t, "published", step8Body["route_status"],
		"Step 8: route_status must be published",
	)
	assert.NotEmpty(t, step8Body["published_at"],
		"Step 8: published_at must not be empty",
	)

	// ------------------------------------------------------------------ //
	// Step 9: Get route with images and verify marker coordinates         //
	// ------------------------------------------------------------------ //
	t.Log("Step 9: Get route with images and verify marker coordinates")

	step9Resp := doRequest(
		t,
		http.MethodGet,
		fmt.Sprintf(
			"%s/api/v1/routes/%s?include_images=true",
			apiURL,
			routeID,
		),
		nil,
		authToken,
	)

	require.Equal(t, http.StatusOK, step9Resp.StatusCode,
		"Step 9: expected 200 from GET /api/v1/routes/{routeID}",
	)

	step9Body := decodeJSON(t, step9Resp)

	routeObj9, ok := step9Body["route"].(map[string]any)
	require.True(t, ok,
		"Step 9: response must contain a 'route' object",
	)

	assert.Equal(t, routeID, routeObj9["route_id"],
		"Step 9: route.route_id must match",
	)
	assert.Equal(t, "published", routeObj9["route_status"],
		"Step 9: route.route_status must be published",
	)

	totalWaypoints, _ := step9Body["total_waypoints"].(float64)
	assert.InDelta(t, float64(3), totalWaypoints, 0,
		"Step 9: total_waypoints must be 3",
	)

	assert.Equal(t, true, step9Body["can_navigate"],
		"Step 9: can_navigate must be true",
	)
	assert.Equal(t, true, step9Body["images_included"],
		"Step 9: images_included must be true",
	)

	waypointsRaw, ok := step9Body["waypoints"].([]any)
	require.True(t, ok,
		"Step 9: response must contain a 'waypoints' array",
	)

	waypointIDSet := make(map[string]bool, len(waypointIDs))
	for _, wid := range waypointIDs {
		waypointIDSet[wid] = true
	}

	for i, wRaw := range waypointsRaw {
		wp, ok := wRaw.(map[string]any)
		require.Truef(t, ok,
			"Step 9: waypoints[%d] must be an object", i,
		)

		wpID, _ := wp["waypoint_id"].(string)
		assert.NotEmptyf(t, wpID,
			"Step 9: waypoints[%d].waypoint_id must not be empty", i,
		)
		assert.Truef(t, waypointIDSet[wpID],
			"Step 9: waypoints[%d].waypoint_id=%s not in expected set",
			i, wpID,
		)

		assert.NotEmptyf(t, wp["image_id"],
			"Step 9: waypoints[%d].image_id must not be empty", i,
		)
		assert.NotEmptyf(
			t,
			wp["navigation_image_url"],
			"Step 9: waypoints[%d].navigation_image_url must not be empty (include_images=true)",
			i,
		)

		posFloat, _ := wp["position"].(float64)
		pos := int(posFloat)

		if pos >= 0 && pos < len(originalMarkers) {
			markerX, _ := wp["marker_x"].(float64)
			markerY, _ := wp["marker_y"].(float64)

			assert.InDeltaf(t,
				originalMarkers[pos].X, markerX, 0.001,
				"Step 9: waypoints pos=%d marker_x mismatch", pos,
			)
			assert.InDeltaf(t,
				originalMarkers[pos].Y, markerY, 0.001,
				"Step 9: waypoints pos=%d marker_y mismatch", pos,
			)
		}
	}

	// ------------------------------------------------------------------ //
	// Step 10: List routes — verify route appears                         //
	// ------------------------------------------------------------------ //
	t.Log("Step 10: List routes — verify route appears")

	step10Resp := doRequest(
		t,
		http.MethodGet,
		apiURL+"/api/v1/routes?route_status=published&navigable_only=false",
		nil,
		authToken,
	)

	require.Equal(t, http.StatusOK, step10Resp.StatusCode,
		"Step 10: expected 200 from GET /api/v1/routes",
	)

	step10Body := decodeJSON(t, step10Resp)

	pagination, ok := step10Body["pagination"].(map[string]any)
	require.True(t, ok,
		"Step 10: response must contain a 'pagination' object",
	)

	paginationCount, _ := pagination["count"].(float64)
	assert.GreaterOrEqual(t, paginationCount, float64(1),
		"Step 10: pagination.count must be >= 1",
	)

	routesList, ok := step10Body["routes"].([]any)
	require.True(t, ok,
		"Step 10: response must contain a 'routes' array",
	)

	foundRoute := false

	for _, rRaw := range routesList {
		r, ok := rRaw.(map[string]any)
		if !ok {
			continue
		}

		if r["route_id"] == routeID {
			assert.Equal(t, "published", r["route_status"],
				"Step 10: found route must have route_status=published",
			)

			foundRoute = true

			break
		}
	}

	assert.True(
		t,
		foundRoute,
		"Step 10: route %s must appear in GET /api/v1/routes with route_status=published",
		routeID,
	)

	// ------------------------------------------------------------------ //
	// Step 11: Update route metadata                                      //
	// ------------------------------------------------------------------ //
	t.Log("Step 11: Update route metadata")

	step11MetaResp := doRequest(
		t,
		http.MethodPut,
		apiURL+"/api/v1/routes/"+routeID,
		map[string]any{
			"name":       "Updated Integration Route",
			"visibility": "public",
		},
		authToken,
	)

	require.Equal(t, http.StatusOK, step11MetaResp.StatusCode,
		"Step 11: expected 200 from PUT /api/v1/routes/{routeID}",
	)

	step11MetaBody := decodeJSON(t, step11MetaResp)

	assert.Equal(t, routeID, step11MetaBody["route_id"],
		"Step 11: route_id must match",
	)
	assert.NotEmpty(t, step11MetaBody["updated_at"],
		"Step 11: updated_at must not be empty",
	)

	updatedFields11Meta, ok := step11MetaBody["updated_fields"].([]any)
	require.True(t, ok, "Step 11: updated_fields must be an array")

	updatedFields11MetaSet := make(map[string]bool, len(updatedFields11Meta))
	for _, f := range updatedFields11Meta {
		if s, ok := f.(string); ok {
			updatedFields11MetaSet[s] = true
		}
	}

	assert.True(t, updatedFields11MetaSet["name"],
		"Step 11: updated_fields must contain 'name'",
	)
	assert.True(t, updatedFields11MetaSet["visibility"],
		"Step 11: updated_fields must contain 'visibility'",
	)

	// ------------------------------------------------------------------ //
	// Step 12: Update waypoint                                            //
	// ------------------------------------------------------------------ //
	t.Log("Step 12: Update waypoint")

	step12WpResp := doRequest(
		t,
		http.MethodPut,
		fmt.Sprintf(
			"%s/api/v1/routes/%s/waypoints/%s",
			apiURL,
			routeID,
			waypointIDs[0],
		),
		map[string]any{
			"description": "Updated waypoint description",
			"marker_x":    0.15,
			"marker_y":    0.25,
		},
		authToken,
	)

	require.Equal(t, http.StatusOK, step12WpResp.StatusCode,
		"Step 12: expected 200 from PUT .../waypoints/{waypointID}",
	)

	step12WpBody := decodeJSON(t, step12WpResp)

	assert.Equal(t, waypointIDs[0], step12WpBody["waypoint_id"],
		"Step 12: waypoint_id must match waypointIDs[0]",
	)
	assert.Equal(t, routeID, step12WpBody["route_id"],
		"Step 12: route_id must match",
	)
	assert.NotEmpty(t, step12WpBody["updated_at"],
		"Step 12: updated_at must not be empty",
	)

	updatedFields12Wp, ok := step12WpBody["updated_fields"].([]any)
	require.True(t, ok, "Step 12: updated_fields must be an array")

	updatedFields12WpSet := make(map[string]bool, len(updatedFields12Wp))
	for _, f := range updatedFields12Wp {
		if s, ok := f.(string); ok {
			updatedFields12WpSet[s] = true
		}
	}

	assert.True(t, updatedFields12WpSet["description"],
		"Step 12: updated_fields must contain 'description'",
	)
	assert.True(t, updatedFields12WpSet["marker_x"],
		"Step 12: updated_fields must contain 'marker_x'",
	)
	assert.True(t, updatedFields12WpSet["marker_y"],
		"Step 12: updated_fields must contain 'marker_y'",
	)

	// ------------------------------------------------------------------ //
	// Step 13: Replace waypoint image (prepare + upload + async swap)     //
	// ------------------------------------------------------------------ //
	t.Log("Step 13: Replace waypoint image")

	// 13a. Prepare — now includes marker coordinates (sent at prepare time,
	// stored as pending fields, atomically swapped by Valkey consumer).
	t.Log("Step 13a: Prepare image replacement (with markers)")

	step13aResp := doRequest(
		t,
		http.MethodPost,
		fmt.Sprintf(
			"%s/api/v1/routes/%s/waypoints/%s/replace-image/prepare",
			apiURL,
			routeID,
			waypointIDs[1],
		),
		map[string]any{
			"file_name":       "pexels-tuurt-2954405.jpg",
			"file_size_bytes": 1400255,
			"content_type":    "image/jpeg",
			"marker_x":        0.55,
			"marker_y":        0.65,
		},
		authToken,
	)

	require.Equal(t, http.StatusOK, step13aResp.StatusCode,
		"Step 13a: expected 200 from POST .../replace-image/prepare",
	)

	var replacePrep ReplaceImagePrepareResponse

	step13aBytes, readErr13a := io.ReadAll(step13aResp.Body)
	require.NoError(t, readErr13a, "Step 13a: failed to read prepare body")
	step13aResp.Body.Close()

	err = json.Unmarshal(step13aBytes, &replacePrep)
	require.NoError(t, err,
		"Step 13a: failed to decode replace-image/prepare response",
	)

	require.NotEmpty(t, replacePrep.ImageID,
		"Step 13a: image_id must not be empty",
	)
	require.NotEmpty(t, replacePrep.UploadURL,
		"Step 13a: upload_url must not be empty",
	)
	assert.NotEmpty(t, replacePrep.ExpiresAt,
		"Step 13a: expires_at must not be empty",
	)

	// 13b. Upload replacement image to gateway.
	t.Log("Step 13b: Upload replacement image")

	replacementImage := loadTestImage(t, "pexels-tuurt-2954405.jpg")

	uploadReq13b, err := http.NewRequest(
		http.MethodPut,
		replacePrep.UploadURL,
		bytes.NewReader(replacementImage),
	)
	require.NoError(t, err,
		"Step 13b: failed to build PUT request for replacement upload",
	)

	uploadReq13b.Header.Set("Content-Type", "application/octet-stream")

	client13b := &http.Client{Timeout: 60 * time.Second}

	uploadResp13b, uploadErr13b := client13b.Do(uploadReq13b)
	require.NoError(t, uploadErr13b,
		"Step 13b: transport error uploading replacement image",
	)

	uploadBytes13b, _ := io.ReadAll(uploadResp13b.Body)
	uploadResp13b.Body.Close()

	require.Equal(t, http.StatusAccepted, uploadResp13b.StatusCode,
		"Step 13b: expected 202 from replacement upload; body: %s",
		uploadBytes13b,
	)

	// 13c. Wait for async Valkey swap and verify replacement.
	// The gateway processes the image, publishes to Valkey image:result,
	// and the API consumer atomically swaps the waypoint's image + markers.
	// No client confirm call — the swap is fully automatic.
	t.Log("Step 13c: Wait for async image swap via Valkey")

	swapDeadline := time.Now().Add(15 * time.Second)
	swapVerified := false

	for time.Now().Before(swapDeadline) {
		checkResp := doRequest(
			t,
			http.MethodGet,
			fmt.Sprintf(
				"%s/api/v1/routes/%s?include_images=true",
				apiURL,
				routeID,
			),
			nil,
			authToken,
		)

		if checkResp.StatusCode != http.StatusOK {
			checkResp.Body.Close()
			time.Sleep(1 * time.Second)

			continue
		}

		var checkBody map[string]any

		decErr := json.NewDecoder(checkResp.Body).Decode(&checkBody)
		checkResp.Body.Close()

		if decErr != nil {
			time.Sleep(1 * time.Second)

			continue
		}

		wps, ok := checkBody["waypoints"].([]any)
		if !ok {
			time.Sleep(1 * time.Second)

			continue
		}

		for _, wRaw := range wps {
			wp, ok := wRaw.(map[string]any)
			if !ok {
				continue
			}

			wpID, _ := wp["waypoint_id"].(string)
			if wpID != waypointIDs[1] {
				continue
			}

			imgID, _ := wp["image_id"].(string)
			if imgID == replacePrep.ImageID {
				// Image swapped — verify markers were atomically updated.
				markerX, _ := wp["marker_x"].(float64)
				markerY, _ := wp["marker_y"].(float64)

				assert.InDelta(t, 0.55, markerX, 0.001,
					"Step 13c: after swap, marker_x must be 0.55",
				)
				assert.InDelta(t, 0.65, markerY, 0.001,
					"Step 13c: after swap, marker_y must be 0.65",
				)

				swapVerified = true
			}

			break // found waypointIDs[1], stop iterating waypoints
		}

		if swapVerified {
			t.Logf(
				"Step 13c: waypoint[1] image swapped to %s",
				replacePrep.ImageID,
			)

			break
		}

		time.Sleep(1 * time.Second)
	}

	require.True(t, swapVerified,
		"Step 13c: waypoint[1] image was not swapped to %s within 15s",
		replacePrep.ImageID,
	)

	// 13d. Verify route stayed PUBLISHED throughout replacement.
	t.Log("Step 13d: Verify route remains published after replacement")

	step13dResp := doRequest(
		t,
		http.MethodGet,
		fmt.Sprintf(
			"%s/api/v1/routes/%s",
			apiURL,
			routeID,
		),
		nil,
		authToken,
	)

	require.Equal(t, http.StatusOK, step13dResp.StatusCode,
		"Step 13d: expected 200 from GET /routes/{routeID}",
	)

	step13dBody := decodeJSON(t, step13dResp)

	routeObj13d, ok := step13dBody["route"].(map[string]any)
	require.True(t, ok,
		"Step 13d: response must contain a 'route' object",
	)

	assert.Equal(t, "published", routeObj13d["route_status"],
		"Step 13d: route must remain published after image replacement",
	)

	// ------------------------------------------------------------------ //
	// Step 14: Delete route                                               //
	// ------------------------------------------------------------------ //
	t.Log("Step 14: Delete route")

	step14Resp := doRequest(
		t,
		http.MethodDelete,
		apiURL+"/api/v1/routes/"+routeID,
		nil,
		authToken,
	)

	require.Equal(t, http.StatusOK, step14Resp.StatusCode,
		"Step 14: expected 200 from DELETE /api/v1/routes/{routeID}",
	)

	step14Body := decodeJSON(t, step14Resp)

	assert.Equal(t, routeID, step14Body["route_id"],
		"Step 14: route_id must match",
	)

	waypointsDeleted, _ := step14Body["waypoints_deleted"].(float64)
	assert.InDelta(t, float64(3), waypointsDeleted, 0,
		"Step 14: waypoints_deleted must be 3",
	)

	assert.Equal(t, true, step14Body["removed_from_database"],
		"Step 14: removed_from_database must be true",
	)
	assert.Equal(t, true, step14Body["removed_from_storage"],
		"Step 14: removed_from_storage must be true",
	)

	assert.NotEmpty(t, step14Body["deleted_at"],
		"Step 14: deleted_at must not be empty",
	)
}
