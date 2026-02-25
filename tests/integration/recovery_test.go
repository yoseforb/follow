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

// TestValkeyRecovery_PendingMessageRetained verifies that Valkey's Pending
// Entries List (PEL) correctly holds messages that were delivered but not
// acknowledged, and that XAUTOCLAIM can recover them.
//
// This test simulates a consumer crash by reading a message from a test
// consumer group WITHOUT acknowledging it, then verifying the message
// remains in the PEL and can be re-claimed by a new consumer via
// XAUTOCLAIM.
func TestValkeyRecovery_PendingMessageRetained(t *testing.T) {
	_, token := createAnonymousUser(t)
	routeID := prepareRoute(t, token)
	route := createRouteWithWaypoints(t, token, routeID, defaultTestImages)
	t.Cleanup(func() { deleteRoute(t, routeID, token) })

	vc := newValkeyClient(t)
	imageID := route.PresignedURLs[0].ImageID
	testGroup := "test-recovery-" + imageID[:8]

	// Create dedicated observer group BEFORE uploading the image, using
	// "$" so the group only tracks messages that arrive after this point.
	// This guarantees our XREADGROUP ">" call will see the result message
	// published by the gateway without racing against the api-workers group
	// that may have already consumed an earlier message.
	ctx := context.Background()

	_ = vc.Do(
		ctx,
		vc.B().XgroupCreate().
			Key("image:result").
			Group(testGroup).
			Id("$").
			Mkstream().
			Build(),
	).Error()

	t.Cleanup(func() {
		_ = vc.Do(
			context.Background(),
			vc.B().XgroupDestroy().
				Key("image:result").
				Group(testGroup).
				Build(),
		)
	})

	// Upload image and wait for gateway to process.
	resp := uploadToGateway(
		t,
		route.PresignedURLs[0].UploadURL,
		loadTestImage(t, "pexels-punttim-240223.jpg"),
	)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()

	// Wait for gateway to publish result to stream.
	waitForImageStatus(t, vc, imageID, "done", 30*time.Second)

	// Poll for our result message WITHOUT acking — simulates consumer
	// crash. Use a deadline loop to handle timing variance between the
	// gateway publishing the message and our consumer reading it.
	const (
		searchTimeout  = 15 * time.Second
		searchInterval = 500 * time.Millisecond
		readCount      = int64(10)
	)

	consumer1 := "crash-consumer-1"
	deadline := time.Now().Add(searchTimeout)

	var ourMsgID string

	for ourMsgID == "" && time.Now().Before(deadline) {
		messages := xReadGroupNoAck(
			t, vc, "image:result", testGroup, consumer1, readCount,
		)

		for _, msg := range messages {
			if msg.Fields["image_id"] == imageID {
				ourMsgID = msg.ID

				break
			}
		}

		if ourMsgID == "" {
			time.Sleep(searchInterval)
		}
	}

	require.NotEmpty(t, ourMsgID,
		"should have read result message for image %s", imageID,
	)

	// Verify message is in PEL (pending, not acknowledged).
	pendingCount := xPendingCount(t, vc, "image:result", testGroup)
	assert.GreaterOrEqual(t, pendingCount, int64(1),
		"unacknowledged message should be in PEL",
	)

	// Simulate consumer restart: new consumer claims the pending message.
	// Use minIdleTime=0 to claim immediately (for testing purposes).
	time.Sleep(100 * time.Millisecond) // Small wait before autoclaim.

	consumer2 := "restart-consumer-2"
	claimed := xAutoClaim(
		t, vc, "image:result", testGroup, consumer2,
		0, // minIdleTime: 0 for test purposes.
		10,
	)

	// Verify our message was re-claimed.
	var reclaimedMsg streamMessage

	for _, msg := range claimed {
		if msg.ID == ourMsgID {
			reclaimedMsg = msg

			break
		}
	}

	require.NotEmpty(t, reclaimedMsg.ID,
		"pending message should be re-claimed by new consumer",
	)
	assert.Equal(t, imageID, reclaimedMsg.Fields["image_id"])

	// Ack the message to clean up.
	xAck(t, vc, "image:result", testGroup, reclaimedMsg.ID)

	// Verify PEL count decreased.
	afterAckPending := xPendingCount(t, vc, "image:result", testGroup)
	assert.Equal(t, pendingCount-1, afterAckPending,
		"PEL count should decrease by 1 after ack",
	)
}
