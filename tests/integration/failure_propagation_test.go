//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	valkeygo "github.com/valkey-io/valkey-go"
	"github.com/yoseforb/follow-pkg/valkey"
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
// is uploaded. Uses XRANGE to scan the stream idempotently so the message is
// never missed regardless of delivery ordering.
func TestValkeyFailurePropagation_FailureStreamMessage(t *testing.T) {
	_, token := createAnonymousUser(t)
	routeID := prepareRoute(t, token)
	route := createRouteWithWaypoints(t, token, routeID, defaultTestImages)
	t.Cleanup(func() { deleteRoute(t, routeID, token) })

	vc := newValkeyClient(t)
	imageID := route.PresignedURLs[0].ImageID

	// Capture a stream position marker before the upload so XRANGE only
	// scans messages published after this point. Using millisecond
	// precision with sequence 0 ensures any message published at or
	// after this instant is included.
	startID := fmt.Sprintf("%d-0", time.Now().UnixMilli())

	// Upload invalid image bytes.
	resp := uploadToGateway(
		t, route.PresignedURLs[0].UploadURL, invalidImageBytes(),
	)
	resp.Body.Close()

	// Wait for the Valkey status hash to show failure before scanning
	// the stream, so the result message is guaranteed to be present.
	waitForImageStatus(
		t, vc, imageID, valkey.StageFailed, 30*time.Second,
	)

	// Poll the stream with XRANGE until the failure message for this
	// image appears, or the search deadline elapses. XRANGE is
	// idempotent — every call returns all matching messages regardless
	// of prior reads, avoiding the one-delivery-per-consumer limitation
	// of XREADGROUP.
	const (
		searchTimeout  = 15 * time.Second
		searchInterval = 500 * time.Millisecond
	)

	ctx := context.Background()
	deadline := time.Now().Add(searchTimeout)

	var failureMsg streamMessage

	found := false

	for !found && time.Now().Before(deadline) {
		result, err := vc.Do(
			ctx,
			vc.B().Xrange().
				Key(valkey.StreamImageResult).
				Start(startID).
				End("+").
				Build(),
		).AsXRange()
		if err != nil {
			time.Sleep(searchInterval)

			continue
		}

		for _, entry := range result {
			if entry.FieldValues[valkey.ResultFieldImageID] == imageID &&
				entry.FieldValues[valkey.ResultFieldStatus] ==
					valkey.ResultStatusFailed {
				failureMsg = streamMessage{
					ID:     entry.ID,
					Fields: entry.FieldValues,
				}
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

	assert.Equal(
		t, valkey.ResultStatusFailed,
		failureMsg.Fields[valkey.ResultFieldStatus],
	)
	assert.Equal(
		t, imageID, failureMsg.Fields[valkey.ResultFieldImageID],
	)
	assert.NotEmpty(
		t,
		failureMsg.Fields[valkey.ResultFieldErrorCode],
		"failure message should include error_code",
	)
}

// Compile-time assertion: valkeygo is used via the vc variable type returned
// by newValkeyClient. The import must remain explicit.
var _ valkeygo.Client
