//go:build integration

package integration_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValkeyUploadGuard_PreventsDuplicateUploads verifies that the upload
// guard uses SET NX EX at image:upload:{image_id} to ensure each image is
// only processed once. A second upload with the same token must be rejected
// with 409 Conflict.
func TestValkeyUploadGuard_PreventsDuplicateUploads(t *testing.T) {
	_, token := createAnonymousUser(t)
	routeID := prepareRoute(t, token)
	route := createRouteWithWaypoints(t, token, routeID, defaultTestImages)

	t.Cleanup(func() { deleteRoute(t, routeID, token) })

	uploadEntry := route.PresignedURLs[0]
	vc := newValkeyClient(t)

	// First upload must succeed with 202 Accepted.
	resp1 := uploadToGateway(
		t, uploadEntry.UploadURL, loadTestImage(t, "pexels-punttim-240223.jpg"),
	)
	require.Equal(
		t,
		http.StatusAccepted,
		resp1.StatusCode,
		"first upload should return 202 Accepted",
	)
	resp1.Body.Close()

	// Upload guard key must appear in Valkey after the first upload.
	guardKey := "image:upload:" + uploadEntry.ImageID

	require.Eventually(
		t,
		func() bool { return keyExists(t, vc, guardKey) },
		5*time.Second,
		100*time.Millisecond,
		"upload guard key should exist after first upload",
	)

	// Second upload with the same token must be rejected with 409 Conflict.
	resp2 := uploadToGateway(
		t,
		uploadEntry.UploadURL,
		loadTestImage(t, "pexels-punttim-240223.jpg"),
	)
	require.Equal(
		t,
		http.StatusConflict,
		resp2.StatusCode,
		"duplicate upload should return 409 Conflict",
	)
	resp2.Body.Close()

	// Guard key must persist after the rejected duplicate.
	assert.True(
		t,
		keyExists(t, vc, guardKey),
		"upload guard key should still exist after rejected duplicate",
	)
}

// TestValkeyUploadGuard_DifferentImagesAccepted verifies that different image
// IDs obtained from the same route are each accepted independently, so
// uploading image A does not block image B.
func TestValkeyUploadGuard_DifferentImagesAccepted(t *testing.T) {
	_, token := createAnonymousUser(t)
	routeID := prepareRoute(t, token)
	route := createRouteWithWaypoints(t, token, routeID, defaultTestImages)

	t.Cleanup(func() { deleteRoute(t, routeID, token) })

	// Upload first image — must succeed.
	resp1 := uploadToGateway(
		t,
		route.PresignedURLs[0].UploadURL,
		loadTestImage(t, "pexels-punttim-240223.jpg"),
	)
	require.Equal(t, http.StatusAccepted, resp1.StatusCode)
	resp1.Body.Close()

	// Upload second image (different image_id) — must also succeed.
	resp2 := uploadToGateway(
		t,
		route.PresignedURLs[1].UploadURL,
		loadTestImage(t, "pexels-arthurbrognoli-2260838.jpg"),
	)
	require.Equal(
		t,
		http.StatusAccepted,
		resp2.StatusCode,
		"uploading a different image should succeed even if another "+
			"was already uploaded",
	)
	resp2.Body.Close()
}
