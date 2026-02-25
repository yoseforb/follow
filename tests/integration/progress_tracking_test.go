//go:build integration

package integration_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValkeyProgressTracking_InitialStatusSetByAPI verifies that follow-api
// writes {stage: "queued"} to image:status:{image_id} hash immediately when
// creating a route (before any upload occurs).
func TestValkeyProgressTracking_InitialStatusSetByAPI(t *testing.T) {
	_, token := createAnonymousUser(t)
	routeID := prepareRoute(t, token)
	route := createRouteWithWaypoints(t, token, routeID, defaultTestImages)
	t.Cleanup(func() { deleteRoute(t, routeID, token) })

	vc := newValkeyClient(t)

	// Verify each image has initial "queued" status set by API.
	for _, urlEntry := range route.PresignedURLs {
		statusKey := imageStatusKey(urlEntry.ImageID)
		fields := hGetAll(t, vc, statusKey)
		assert.Equal(
			t,
			"queued",
			fields["stage"],
			"API should write stage=queued to image:status:{id} on route creation",
		)
	}
}

// TestValkeyProgressTracking_StageTransitionsOnUpload verifies that the
// gateway updates image:status:{image_id} hash as the image moves through
// pipeline stages. Expected stages: queued -> validating -> analyzing ->
// transforming -> uploading_to_storage -> done (not all intermediate stages
// may be observable).
func TestValkeyProgressTracking_StageTransitionsOnUpload(
	t *testing.T,
) {
	_, token := createAnonymousUser(t)
	routeID := prepareRoute(t, token)
	route := createRouteWithWaypoints(t, token, routeID, defaultTestImages)
	t.Cleanup(func() { deleteRoute(t, routeID, token) })

	imageID := route.PresignedURLs[0].ImageID
	vc := newValkeyClient(t)

	// Upload image.
	resp := uploadToGateway(
		t,
		route.PresignedURLs[0].UploadURL,
		loadTestImage(t, "pexels-punttim-240223.jpg"),
	)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()

	// Poll hash for stage transitions.
	seenStages := make(map[string]bool)
	statusKey := imageStatusKey(imageID)

	deadline := time.Now().Add(30 * time.Second)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for time.Now().Before(deadline) {
		<-ticker.C

		fields := hGetAll(t, vc, statusKey)
		if stage, ok := fields["stage"]; ok {
			seenStages[stage] = true
			if stage == "done" {
				break
			}
		}
	}

	// Must have reached terminal "done" state.
	assert.True(
		t,
		seenStages["done"],
		"image:status hash must reach stage=done after processing",
	)

	// Verify the hash contains meaningful completion data.
	finalFields := hGetAll(t, vc, statusKey)
	assert.Equal(t, "done", finalFields["stage"])
}

// TestValkeyProgressTracking_TTLSet verifies that the image:status:{id} hash
// key has a TTL (it must expire eventually — no orphan keys).
func TestValkeyProgressTracking_TTLSet(t *testing.T) {
	_, token := createAnonymousUser(t)
	routeID := prepareRoute(t, token)
	route := createRouteWithWaypoints(t, token, routeID, defaultTestImages)
	t.Cleanup(func() { deleteRoute(t, routeID, token) })

	vc := newValkeyClient(t)
	imageID := route.PresignedURLs[0].ImageID
	statusKey := imageStatusKey(imageID)

	// Use TTL command to verify the key has an expiration.
	cmd := vc.B().Ttl().Key(statusKey).Build()

	ttlSeconds, err := vc.Do(context.Background(), cmd).AsInt64()
	require.NoError(t, err)
	assert.Positive(
		t,
		ttlSeconds,
		"image:status key should have a TTL set",
	)
	assert.LessOrEqual(
		t,
		ttlSeconds,
		int64(3600),
		"image:status TTL should be at most 1 hour (3600 seconds)",
	)
}
