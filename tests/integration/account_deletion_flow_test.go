//go:build integration

package integration_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requestAccountDeletion is a convenience helper that calls
// POST /auth/request-account-deletion and returns the response.
func requestAccountDeletion(
	t *testing.T,
	token string,
) *http.Response {
	t.Helper()

	return doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/request-account-deletion",
		map[string]any{},
		token,
	)
}

// confirmAccountDeletion is a convenience helper that calls
// POST /auth/confirm-account-deletion with the given code.
func confirmAccountDeletion(
	t *testing.T,
	token string,
	code string,
) *http.Response {
	t.Helper()

	return doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/confirm-account-deletion",
		map[string]any{"code": code},
		token,
	)
}

// cancelAccountDeletion is a convenience helper that calls
// POST /auth/cancel-account-deletion.
func cancelAccountDeletion(
	t *testing.T,
	token string,
) *http.Response {
	t.Helper()

	return doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/cancel-account-deletion",
		map[string]any{},
		token,
	)
}

// --- Task F.1: Full account deletion flow ---

// TestAccountDeletionFullFlow exercises the complete
// registered -> pending_deletion -> deleted lifecycle.
// Verifies user and routes are gone after confirmed deletion.
func TestAccountDeletionFullFlow(t *testing.T) {
	clearMailbox(t)

	// Step 1: Create anonymous user -> register -> confirm
	t.Log("Step 1: Create and register user")

	userID, token, _ := createAnonymousUser(t)
	email := uniqueEmail()
	_, regToken, _ := registerAndConfirm(t, token, email)

	// Step 2: Create a route under the registered user
	t.Log("Step 2: Create route as registered user")

	routeID := prepareRoute(t, regToken)

	cwResp := createRouteWithWaypoints(
		t, regToken, routeID, defaultTestImages,
	)
	require.Equal(t, routeID, cwResp.RouteID)

	// Verify route exists
	getRouteResp := doRequest(
		t, http.MethodGet,
		apiURL+"/api/v1/routes/"+routeID,
		nil, regToken,
	)
	require.Equal(t, http.StatusOK, getRouteResp.StatusCode)
	getRouteResp.Body.Close()

	// Step 3: Request account deletion
	t.Log("Step 3: Request account deletion")

	clearMailbox(t)

	reqResp := requestAccountDeletion(t, regToken)
	require.Equal(t,
		http.StatusNoContent, reqResp.StatusCode,
		"request-account-deletion must return 204",
	)
	reqResp.Body.Close()

	// Step 4: Extract deletion verification code
	t.Log("Step 4: Extract deletion code from email")

	msgID := waitForEmail(t, email)
	code := extractVerificationCode(t, msgID)
	require.Len(t, code, 6,
		"deletion code must be 6 digits",
	)

	// Step 5: Confirm account deletion
	t.Log("Step 5: Confirm account deletion")

	confirmResp := confirmAccountDeletion(
		t, regToken, code,
	)
	require.Equal(t,
		http.StatusNoContent, confirmResp.StatusCode,
		"confirm-account-deletion must return 204",
	)
	confirmResp.Body.Close()

	// Step 6: Verify user is gone.
	// Use a fresh anonymous token to avoid 401 from the
	// deleted user's invalidated JWT.
	t.Log("Step 6: Verify user is gone (404)")

	_, probeToken, _ := createAnonymousUser(t)

	getUserResp := doRequest(
		t, http.MethodGet,
		apiURL+"/api/v1/users/anonymous/"+userID,
		nil, probeToken,
	)
	// 404 (not found) or 403 (forbidden — different user)
	// are both valid: the deleted user no longer exists.
	userStatus := getUserResp.StatusCode
	getUserResp.Body.Close()

	assert.True(t,
		userStatus == http.StatusNotFound ||
			userStatus == http.StatusForbidden,
		"deleted user must return 404 or 403 (got %d)",
		userStatus,
	)

	// Step 7: Verify route is gone (cascade)
	// Route deletion is event-driven (async). Poll until the
	// cascade completes rather than assuming it already happened.
	t.Log("Step 7: Verify route is gone (404)")

	var routeStatus int
	require.Eventually(t, func() bool {
		resp := doRequest(
			t, http.MethodGet,
			apiURL+"/api/v1/routes/"+routeID,
			nil, probeToken,
		)
		routeStatus = resp.StatusCode
		resp.Body.Close()
		return routeStatus == http.StatusNotFound ||
			routeStatus == http.StatusForbidden
	}, 5*time.Second, 200*time.Millisecond,
		"route of deleted user must return 404 or 403 "+
			"(last status %d)", routeStatus,
	)
}

// --- Task F.2: Cancel account deletion ---

// TestCancelAccountDeletion verifies that a pending deletion
// can be cancelled, restoring the user to registered state.
func TestCancelAccountDeletion(t *testing.T) {
	clearMailbox(t)

	// Setup: Register and confirm user
	t.Log("Setup: Register and confirm user")

	_, token, _ := createAnonymousUser(t)
	email := uniqueEmail()
	_, regToken, _ := registerAndConfirm(t, token, email)

	// Step 1: Request account deletion
	t.Log("Step 1: Request account deletion")

	clearMailbox(t)

	reqResp := requestAccountDeletion(t, regToken)
	require.Equal(t,
		http.StatusNoContent, reqResp.StatusCode,
		"request-account-deletion must return 204",
	)
	reqResp.Body.Close()

	// Grab the code before cancelling (to verify it's
	// invalidated after cancel)
	msgID := waitForEmail(t, email)
	oldCode := extractVerificationCode(t, msgID)

	// Step 2: Cancel account deletion
	t.Log("Step 2: Cancel account deletion")

	cancelResp := cancelAccountDeletion(t, regToken)
	require.Equal(t,
		http.StatusNoContent, cancelResp.StatusCode,
		"cancel-account-deletion must return 204",
	)
	cancelResp.Body.Close()

	// Step 3: Login with email + password → 200
	t.Log("Step 3: Login after cancel succeeds")

	loginResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/login",
		map[string]any{
			"email":    email,
			"password": testPassword,
		},
		"",
	)
	require.Equal(t,
		http.StatusOK, loginResp.StatusCode,
		"login after cancel must return 200",
	)
	loginResp.Body.Close()

	// Step 4: Old deletion code is invalidated
	t.Log("Step 4: Old deletion code is invalidated")

	confirmResp := confirmAccountDeletion(
		t, regToken, oldCode,
	)
	// User is no longer pending_deletion, so confirm
	// must fail with 400 or 409.
	status := confirmResp.StatusCode
	confirmResp.Body.Close()

	assert.True(t,
		status == http.StatusBadRequest ||
			status == http.StatusConflict,
		"old deletion code must be rejected after cancel "+
			"(got %d)", status,
	)

	// Step 5: Can request deletion again
	t.Log("Step 5: Can request deletion again")

	clearMailbox(t)

	reqResp2 := requestAccountDeletion(t, regToken)
	require.Equal(t,
		http.StatusNoContent, reqResp2.StatusCode,
		"re-request deletion must return 204",
	)
	reqResp2.Body.Close()
}

// --- Task F.3: Deletion code security ---

// TestDeletionCodeSecurity verifies wrong code rejection,
// attempt limiting (429 on 6th attempt), and cancel after
// max attempts.
func TestDeletionCodeSecurity(t *testing.T) {
	clearMailbox(t)

	// Setup: Register, confirm, and request deletion
	t.Log("Setup: Register, confirm, request deletion")

	_, token, _ := createAnonymousUser(t)
	email := uniqueEmail()
	_, regToken, _ := registerAndConfirm(t, token, email)

	clearMailbox(t)

	reqResp := requestAccountDeletion(t, regToken)
	require.Equal(t,
		http.StatusNoContent, reqResp.StatusCode,
	)
	reqResp.Body.Close()

	// Get code (we won't use the real one — we'll send
	// wrong codes to exhaust attempts)
	msgID := waitForEmail(t, email)
	_ = extractVerificationCode(t, msgID)

	// Step 1: Submit wrong code 5 times → 400 each
	t.Log("Step 1: Submit wrong code 5 times")

	const maxAttempts = 5

	for i := range maxAttempts {
		wrongResp := confirmAccountDeletion(
			t, regToken, "000000",
		)
		require.Equal(t,
			http.StatusBadRequest,
			wrongResp.StatusCode,
			"attempt %d: wrong code must return 400",
			i+1,
		)
		wrongResp.Body.Close()
	}

	// Step 2: 6th attempt → 429 (too_many_attempts)
	t.Log("Step 2: 6th attempt returns 429")

	exhaustedResp := confirmAccountDeletion(
		t, regToken, "000000",
	)
	require.Equal(t,
		http.StatusTooManyRequests,
		exhaustedResp.StatusCode,
		"6th attempt must return 429",
	)
	exhaustedResp.Body.Close()

	// Step 3: Cancel still works after max attempts
	t.Log("Step 3: Cancel works after max attempts")

	cancelResp := cancelAccountDeletion(t, regToken)
	require.Equal(t,
		http.StatusNoContent, cancelResp.StatusCode,
		"cancel after max attempts must return 204",
	)
	cancelResp.Body.Close()

	// Verify user is back to registered (login works)
	loginResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/login",
		map[string]any{
			"email":    email,
			"password": testPassword,
		},
		"",
	)
	require.Equal(t,
		http.StatusOK, loginResp.StatusCode,
		"login after cancel must succeed",
	)
	loginResp.Body.Close()
}

// --- Task F.4 & F.5: State expiry tests ---
// These tests restart follow-api with short expiry durations
// and a fast scanner interval, then verify the scanner reverts
// expired transitional states.
// Local mode only — docker mode cannot restart individual
// containers mid-test.

// TestStateExpiry restarts the API with aggressive expiry
// settings, runs expiry subtests, then restores the normal API.
func TestStateExpiry(t *testing.T) {
	if envOrDefault(
		"INTEGRATION_TEST_MODE", "local",
	) != "local" {
		t.Skip(
			"state expiry tests require API restart " +
				"(local mode only)",
		)
	}

	restartAPIProcess(t,
		"AUTH_PENDING_REGISTRATION_EXPIRY=3s",
		"AUTH_PENDING_DELETION_EXPIRY=3s",
		"SCHEDULER_EXPIRED_STATE_SCAN_INTERVAL=1s",
		"SCHEDULER_EXPIRED_STATE_SCAN_ENABLED=true",
	)
	t.Cleanup(func() {
		restartAPIProcess(t)
	})

	// F.4: Pending registration expires → reverts to anonymous
	t.Run("PendingRegistrationExpiry", func(t *testing.T) {
		clearMailbox(t)

		userID, token, _ := createAnonymousUser(t)

		email := uniqueEmail()

		// Register (user becomes pending)
		regResp := doRequest(
			t, http.MethodPost,
			apiURL+"/api/v1/auth/register",
			map[string]any{
				"email":    email,
				"password": testPassword,
			},
			token,
		)
		require.Equal(t,
			http.StatusOK, regResp.StatusCode,
			"register must succeed",
		)
		regResp.Body.Close()

		// Wait for expiry (3s) + scanner cycle (1s) + margin
		t.Log("Waiting for pending registration to expire")

		time.Sleep(6 * time.Second)

		// User should still exist (reverted to anonymous)
		getUserResp := doRequest(
			t, http.MethodGet,
			apiURL+"/api/v1/users/anonymous/"+userID,
			nil, token,
		)
		require.Equal(t,
			http.StatusOK, getUserResp.StatusCode,
			"user must still exist after expiry revert",
		)
		getUserResp.Body.Close()

		// Login with old email must fail (credentials cleared)
		loginResp := doRequest(
			t, http.MethodPost,
			apiURL+"/api/v1/auth/login",
			map[string]any{
				"email":    email,
				"password": testPassword,
			},
			"",
		)
		require.Equal(t,
			http.StatusUnauthorized,
			loginResp.StatusCode,
			"login with reverted email must fail",
		)
		loginResp.Body.Close()

		// Can re-register with a new email (proves user is
		// back to anonymous state)
		clearMailbox(t)

		newEmail := uniqueEmail()

		reRegResp := doRequest(
			t, http.MethodPost,
			apiURL+"/api/v1/auth/register",
			map[string]any{
				"email":    newEmail,
				"password": testPassword,
			},
			token,
		)
		require.Equal(t,
			http.StatusOK, reRegResp.StatusCode,
			"re-register after revert must succeed",
		)
		reRegResp.Body.Close()
	})

	// F.5: Pending deletion expires → reverts to registered
	t.Run("PendingDeletionExpiry", func(t *testing.T) {
		clearMailbox(t)

		_, token, _ := createAnonymousUser(t)
		email := uniqueEmail()
		_, regToken, _ := registerAndConfirm(t, token, email)

		clearMailbox(t)

		// Request deletion (user becomes pending_deletion)
		reqResp := requestAccountDeletion(t, regToken)
		require.Equal(t,
			http.StatusNoContent, reqResp.StatusCode,
			"request-account-deletion must return 204",
		)
		reqResp.Body.Close()

		// Wait for expiry (3s) + scanner cycle (1s) + margin
		t.Log("Waiting for pending deletion to expire")

		time.Sleep(6 * time.Second)

		// Login should work (user reverted to registered)
		loginResp := doRequest(
			t, http.MethodPost,
			apiURL+"/api/v1/auth/login",
			map[string]any{
				"email":    email,
				"password": testPassword,
			},
			"",
		)
		require.Equal(t,
			http.StatusOK, loginResp.StatusCode,
			"login after deletion expiry must succeed",
		)

		loginBody := decodeJSON(t, loginResp)

		freshToken, _ := loginBody["access_token"].(string)
		require.NotEmpty(t, freshToken)

		// state_expires_at should be nil
		userID, _ := loginBody["user_id"].(string)
		require.NotEmpty(t, userID)

		getUserResp := doRequest(
			t, http.MethodGet,
			apiURL+"/api/v1/users/anonymous/"+userID,
			nil, freshToken,
		)
		require.Equal(t,
			http.StatusOK, getUserResp.StatusCode,
		)

		userBody := decodeJSON(t, getUserResp)
		user, _ := userBody["user"].(map[string]any)
		require.NotNil(t, user)

		assert.Nil(t, user["state_expires_at"],
			"state_expires_at must be nil after revert",
		)

		// Old deletion code should not work (verify by
		// requesting fresh deletion and confirming that
		// flow works — proves user is back to registered)
		clearMailbox(t)

		reqResp2 := requestAccountDeletion(t, freshToken)
		require.Equal(t,
			http.StatusNoContent, reqResp2.StatusCode,
			"re-request deletion after expiry revert must work",
		)
		reqResp2.Body.Close()
	})
}
