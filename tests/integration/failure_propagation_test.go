//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	valkeygo "github.com/valkey-io/valkey-go"
)

// TestValkeyFailurePropagation_InvalidImageRejectedByGateway verifies that
// uploading bytes that are not a valid image causes the gateway pipeline to
// set stage=failed on the image:status:{id} Valkey hash.
func TestValkeyFailurePropagation_InvalidImageRejectedByGateway(
	t *testing.T,
) {
	_, token := createAnonymousUser(t)
	routeID := prepareRoute(t, token)
	route := createRouteWithWaypoints(t, token, routeID, defaultTestImages)
	t.Cleanup(func() { deleteRoute(t, routeID, token) })

	imageID := route.PresignedURLs[0].ImageID
	vc := newValkeyClient(t)

	// Upload invalid bytes. The gateway may accept at HTTP level (202)
	// and fail during pipeline processing, or reject immediately
	// (400/415). Both are valid — we care about the eventual Valkey
	// status, not the HTTP response code.
	resp := uploadToGateway(
		t, route.PresignedURLs[0].UploadURL, invalidImageBytes(),
	)
	resp.Body.Close()

	// Wait for failure status in Valkey.
	waitForImageStatus(t, vc, imageID, "failed", 30*time.Second)

	// Verify the hash has error information.
	statusKey := imageStatusKey(imageID)
	fields := hGetAll(t, vc, statusKey)

	assert.Equal(t, "failed", fields["stage"])

	// May contain "error_code" field — assert non-empty if present.
	if errCode, ok := fields["error_code"]; ok {
		assert.NotEmpty(t, errCode)
	}
}

// TestValkeyFailurePropagation_FailureStreamMessage verifies that the gateway
// publishes a failure message to the image:result stream when an invalid image
// is uploaded. Uses a separate observer consumer group to avoid interfering
// with the api-workers group used by follow-api.
func TestValkeyFailurePropagation_FailureStreamMessage(t *testing.T) {
	_, token := createAnonymousUser(t)
	routeID := prepareRoute(t, token)
	route := createRouteWithWaypoints(t, token, routeID, defaultTestImages)
	t.Cleanup(func() { deleteRoute(t, routeID, token) })

	vc := newValkeyClient(t)
	imageID := route.PresignedURLs[0].ImageID

	// Use a unique observer group per test run to avoid cross-test
	// contamination. The group name must not collide with api-workers.
	observerGroup := "test-observers-" + imageID[:8]

	// Create observer consumer group BEFORE upload so we capture all
	// messages from the stream beginning.
	ctx := context.Background()

	_ = vc.Do(
		ctx,
		vc.B().XgroupCreate().
			Key("image:result").
			Group(observerGroup).
			Id("0").
			Mkstream().Build(),
	).Error()

	t.Cleanup(func() {
		vc.Do(
			context.Background(),
			vc.B().XgroupDestroy().
				Key("image:result").
				Group(observerGroup).Build(),
		)
	})

	// Upload invalid image bytes.
	resp := uploadToGateway(
		t, route.PresignedURLs[0].UploadURL, invalidImageBytes(),
	)
	resp.Body.Close()

	// Wait for the Valkey status hash to show failure before reading
	// the stream, to ensure the message has been published.
	waitForImageStatus(t, vc, imageID, "failed", 30*time.Second)

	// Read from the observer group to verify a failure message exists
	// in the image:result stream for this image.
	const (
		searchTimeout  = 15 * time.Second
		searchInterval = 500 * time.Millisecond
		readCount      = int64(10)
	)

	deadline := time.Now().Add(searchTimeout)

	var failureMsg streamMessage

	found := false

	for !found && time.Now().Before(deadline) {
		messages := xReadGroupNoAck(
			t, vc, "image:result", observerGroup,
			"test-observer", readCount,
		)

		for _, msg := range messages {
			if msg.Fields["image_id"] == imageID &&
				msg.Fields["status"] == "failed" {
				failureMsg = msg
				found = true

				break
			}
		}

		if !found {
			time.Sleep(searchInterval)
		}
	}

	require.True(
		t,
		found,
		"failure message for image %s should appear in image:result stream",
		imageID,
	)

	assert.Equal(t, "failed", failureMsg.Fields["status"])
	assert.Equal(t, imageID, failureMsg.Fields["image_id"])
	assert.NotEmpty(
		t,
		failureMsg.Fields["error_code"],
		"failure message should include error_code",
	)
}

// Compile-time assertion: valkeygo is used via the vc variable type returned
// by newValkeyClient. The import must remain explicit.
var _ valkeygo.Client
