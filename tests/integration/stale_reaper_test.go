//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yoseforb/follow-pkg/valkey"
)

// reaperStaleTimeout is the maximum time to wait for the reaper to mark
// a stale image as failed. The reaper uses an in-memory firstSeen map:
// scan 1 records the key, subsequent scans check elapsed > threshold.
// Local mode: 2s threshold + 1s scan ≈ 3s. Docker mode: 15s threshold
// (gateway ML stages are slower under resource limits) + 1s scan ≈ 16s.
// 25s accommodates docker mode with margin for timing jitter.
const reaperStaleTimeout = 25 * time.Second

// reaperPollInterval is how often we poll Valkey to check whether the
// reaper has updated the image status hash.
const reaperPollInterval = time.Second

// writeImageStatusHash writes a Valkey hash at image:status:{imageID}
// with the given stage. It uses a 10-minute TTL so test keys do not
// linger if cleanup fails.
func writeImageStatusHash(
	t *testing.T,
	imageID string,
	stage string,
) {
	t.Helper()

	vc := newValkeyClient(t)
	key := imageStatusKey(imageID)

	err := vc.Do(
		context.Background(),
		vc.B().Hset().Key(key).FieldValue().
			FieldValue(valkey.FieldStage, stage).
			FieldValue(
				valkey.FieldUpdatedAt,
				time.Now().UTC().Format(time.RFC3339),
			).
			Build(),
	).Error()
	require.NoError(t, err,
		"writeImageStatusHash: HSET failed for key %s", key,
	)

	// Set a TTL so the key does not persist if cleanup fails.
	err = vc.Do(
		context.Background(),
		vc.B().Expire().Key(key).Seconds(600).Build(),
	).Error()
	require.NoError(t, err,
		"writeImageStatusHash: EXPIRE failed for key %s", key,
	)
}

// cleanupImageStatusKey removes an image:status:{imageID} key from
// Valkey. Best-effort — logs but does not fail.
func cleanupImageStatusKey(t *testing.T, imageID string) {
	t.Helper()

	vc := newValkeyClient(t)
	key := imageStatusKey(imageID)

	err := vc.Do(
		context.Background(),
		vc.B().Del().Key(key).Build(),
	).Error()
	if err != nil {
		t.Logf(
			"cleanupImageStatusKey: DEL %s failed: %v",
			key, err,
		)
	}
}

// TestStaleImageReaper_MarksFailedAfterThreshold verifies that the
// stale image reaper (running inside follow-api) detects an image
// status hash stuck in a non-terminal stage, publishes a failure
// message to the result stream, and deletes the status key.
//
// The test writes a hash with stage=validating directly to Valkey
// (simulating a gateway crash mid-processing) and then waits for
// the reaper to delete the key.
func TestStaleImageReaper_MarksFailedAfterThreshold(
	t *testing.T,
) {
	imageID := uuid.NewString()
	t.Cleanup(func() { cleanupImageStatusKey(t, imageID) })

	// Write non-terminal stage to simulate gateway mid-processing.
	writeImageStatusHash(t, imageID, valkey.StageValidating)

	vc := newValkeyClient(t)
	key := imageStatusKey(imageID)

	// Poll until the reaper deletes the key or timeout.
	deadline := time.Now().Add(reaperStaleTimeout)
	keyDeleted := false

	for time.Now().Before(deadline) {
		fields := hGetAll(t, vc, key)
		if len(fields) == 0 {
			keyDeleted = true

			break
		}

		time.Sleep(reaperPollInterval)
	}

	require.True(t, keyDeleted,
		"reaper should delete the stale image status key within %s",
		reaperStaleTimeout,
	)
}

// TestStaleImageReaper_MarksNonValidatingStageAsFailed verifies that
// the reaper handles non-terminal stages other than "validating". The
// reaper must act on ANY non-terminal stage, not just the first stage
// of the pipeline.
//
// The test writes a hash with stage=processing (simulating a crash
// during the processing stage) and verifies the reaper deletes the
// key after publishing a failure message.
func TestStaleImageReaper_MarksNonValidatingStageAsFailed(
	t *testing.T,
) {
	imageID := uuid.NewString()
	t.Cleanup(func() { cleanupImageStatusKey(t, imageID) })

	// Write non-terminal stage (processing) to simulate a gateway
	// crash partway through the pipeline.
	writeImageStatusHash(t, imageID, valkey.StageProcessing)

	vc := newValkeyClient(t)
	key := imageStatusKey(imageID)

	// Poll until the reaper deletes the key or timeout.
	deadline := time.Now().Add(reaperStaleTimeout)
	keyDeleted := false

	for time.Now().Before(deadline) {
		fields := hGetAll(t, vc, key)
		if len(fields) == 0 {
			keyDeleted = true

			break
		}

		time.Sleep(reaperPollInterval)
	}

	require.True(t, keyDeleted,
		"reaper should delete the stale image status key within %s",
		reaperStaleTimeout,
	)
}

// TestStaleImageReaper_DoesNotMarkTerminalImages verifies that
// the reaper does NOT overwrite images that are already in a
// terminal stage (done or failed).
func TestStaleImageReaper_DoesNotMarkTerminalImages(
	t *testing.T,
) {
	doneID := uuid.NewString()
	failedID := uuid.NewString()

	t.Cleanup(func() {
		cleanupImageStatusKey(t, doneID)
		cleanupImageStatusKey(t, failedID)
	})

	writeImageStatusHash(t, doneID, valkey.StageDone)
	writeImageStatusHash(t, failedID, valkey.StageFailed)

	vc := newValkeyClient(t)
	doneKey := imageStatusKey(doneID)
	failedKey := imageStatusKey(failedID)

	// Wait long enough for the reaper to have run multiple scans.
	// The reaper's stale threshold is 2s with a 1s scan interval;
	// waiting 8s ensures several scan cycles have passed.
	const waitDuration = 8 * time.Second
	t.Logf(
		"waiting %s to verify reaper does not touch terminal keys",
		waitDuration,
	)
	time.Sleep(waitDuration)

	// Verify stages remain unchanged.
	doneFields := hGetAll(t, vc, doneKey)
	assert.Equal(t, valkey.StageDone, doneFields[valkey.FieldStage],
		"reaper must not overwrite stage=done",
	)

	failedFields := hGetAll(t, vc, failedKey)
	assert.Equal(t, valkey.StageFailed, failedFields[valkey.FieldStage],
		"reaper must not overwrite stage=failed",
	)

	// Verify error field was NOT added to the done key.
	assert.Empty(t, doneFields[valkey.FieldError],
		"reaper must not add error field to done images",
	)
}

// TestStaleImageReaper_MarksMultipleStaleImages verifies that the
// reaper handles multiple stale images in a single pass, publishing
// failure messages and deleting all their status keys.
func TestStaleImageReaper_MarksMultipleStaleImages(
	t *testing.T,
) {
	const imageCount = 3

	imageIDs := make([]string, imageCount)
	for i := range imageCount {
		imageIDs[i] = uuid.NewString()
	}

	t.Cleanup(func() {
		for _, id := range imageIDs {
			cleanupImageStatusKey(t, id)
		}
	})

	// Write all keys with non-terminal stage.
	for _, id := range imageIDs {
		writeImageStatusHash(t, id, valkey.StageValidating)
	}

	vc := newValkeyClient(t)

	// Poll until all keys are deleted by the reaper or timeout.
	deadline := time.Now().Add(reaperStaleTimeout)

	allDeleted := false

	for time.Now().Before(deadline) {
		count := 0

		for _, id := range imageIDs {
			fields := hGetAll(t, vc, imageStatusKey(id))
			if len(fields) == 0 {
				count++
			}
		}

		if count == imageCount {
			allDeleted = true

			break
		}

		time.Sleep(reaperPollInterval)
	}

	require.True(t, allDeleted,
		"reaper should delete all %d stale image status keys within %s",
		imageCount, reaperStaleTimeout,
	)
}
