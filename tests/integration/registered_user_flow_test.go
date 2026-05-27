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

const testPassword = "securepass123"

// waitForOwnerType polls GET /api/v1/routes/{routeID}
// until owner_type equals expectedType or timeout elapses.
func waitForOwnerType(
	t *testing.T,
	routeID string,
	authToken string,
	expectedType string,
	timeout time.Duration,
) {
	t.Helper()

	const pollInterval = 200 * time.Millisecond

	deadline := time.Now().Add(timeout)
	routeURL := apiURL + "/api/v1/routes/" + routeID

	for time.Now().Before(deadline) {
		resp := doRequest(
			t, http.MethodGet, routeURL,
			nil, authToken,
		)

		var body map[string]any

		err := json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()

		if err == nil {
			route, _ := body["route"].(map[string]any)
			if route != nil {
				ot, _ := route["owner_type"].(string)
				if ot == expectedType {
					return
				}
			}
		}

		time.Sleep(pollInterval)
	}

	t.Fatalf(
		"waitForOwnerType: route %s did not reach "+
			"owner_type=%q within %s",
		routeID, expectedType, timeout,
	)
}

// registerAndConfirm is a convenience helper that performs
// the full registration flow: register, extract verification
// code from Mailpit, and confirm. Returns the registered JWT.
// Caller must clearMailbox before calling if needed.
func registerAndConfirm(
	t *testing.T,
	anonToken string,
	email string,
) string {
	t.Helper()

	regResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/register",
		map[string]any{
			"email":    email,
			"password": testPassword,
		},
		anonToken,
	)
	require.Equal(t, http.StatusOK, regResp.StatusCode,
		"registerAndConfirm: register failed",
	)
	regResp.Body.Close()

	msgID := waitForEmail(t, email)
	code := extractVerificationCode(t, msgID)

	confirmResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/confirm-registration",
		map[string]any{"code": code},
		anonToken,
	)
	require.Equal(t,
		http.StatusOK, confirmResp.StatusCode,
		"registerAndConfirm: confirm failed",
	)

	confirmBody := decodeJSON(t, confirmResp)

	token, ok := confirmBody["token"].(string)
	require.True(t, ok,
		"registerAndConfirm: missing token",
	)

	return token
}

// TestRegistrationFullFlow exercises the complete
// anonymous -> pending -> registered lifecycle with route
// owner_type transition.
func TestRegistrationFullFlow(t *testing.T) {
	clearMailbox(t)

	// Step 1: Create anonymous user
	t.Log("Step 1: Create anonymous user")

	userID, token := createAnonymousUser(t)
	t.Cleanup(func() { deleteUser(t, userID, token) })

	// Step 2: Create a route as anonymous
	t.Log("Step 2: Create route as anonymous user")

	routeID := prepareRoute(t, token)

	cwResp := createRouteWithWaypoints(
		t, token, routeID, defaultTestImages,
	)
	require.Equal(t, routeID, cwResp.RouteID)

	t.Cleanup(func() {
		deleteRoute(t, routeID, token)
	})

	// Verify initial owner_type
	getResp := doRequest(
		t, http.MethodGet,
		apiURL+"/api/v1/routes/"+routeID,
		nil, token,
	)
	require.Equal(t, http.StatusOK, getResp.StatusCode)

	getBody := decodeJSON(t, getResp)

	route, _ := getBody["route"].(map[string]any)
	require.NotNil(t, route)
	assert.Equal(t, "anonymous", route["owner_type"],
		"Step 2: initial owner_type must be anonymous",
	)

	// Step 3: Register with email and password
	t.Log("Step 3: Register with email and password")

	email := uniqueEmail()

	regResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/register",
		map[string]any{
			"email":        email,
			"password":     testPassword,
			"display_name": "Integration Test User",
		},
		token,
	)
	require.Equal(t, http.StatusOK, regResp.StatusCode,
		"Step 3: register must return 200",
	)

	regBody := decodeJSON(t, regResp)
	assert.Equal(t, userID, regBody["user_id"],
		"Step 3: user_id must match anonymous user",
	)

	// Step 4: Extract verification code from Mailpit
	t.Log("Step 4: Extract verification code")

	msgID := waitForEmail(t, email)
	code := extractVerificationCode(t, msgID)
	require.Len(t, code, 6,
		"Step 4: code must be 6 digits",
	)

	// Step 5: Confirm registration
	t.Log("Step 5: Confirm registration")

	confirmResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/confirm-registration",
		map[string]any{"code": code},
		token,
	)
	require.Equal(t,
		http.StatusOK, confirmResp.StatusCode,
		"Step 5: confirm must return 200",
	)

	confirmBody := decodeJSON(t, confirmResp)
	assert.Equal(t, userID, confirmBody["user_id"],
		"Step 5: user_id must be unchanged",
	)

	regToken, ok := confirmBody["token"].(string)
	require.True(t, ok,
		"Step 5: response must contain token",
	)
	require.NotEmpty(t, regToken,
		"Step 5: token must not be empty",
	)
	assert.NotEmpty(t, confirmBody["expires_at"],
		"Step 5: expires_at must be present",
	)

	// Update token for cleanup closures
	token = regToken

	// Step 6: Verify route owner_type transitioned
	t.Log("Step 6: Verify route owner_type = user")

	waitForOwnerType(
		t, routeID, regToken, "user", 10*time.Second,
	)
}

// TestLoginFlow verifies login with correct credentials,
// wrong password, and a pending (unverified) account.
func TestLoginFlow(t *testing.T) {
	clearMailbox(t)

	// Setup: register and confirm user A
	t.Log("Setup: Create and register user A")

	userIDA, tokenA := createAnonymousUser(t)
	t.Cleanup(func() { deleteUser(t, userIDA, tokenA) })

	emailA := uniqueEmail()
	regTokenA := registerAndConfirm(
		t, tokenA, emailA,
	)
	tokenA = regTokenA

	// Step 1: Login with correct credentials
	t.Log("Step 1: Login with correct credentials")

	loginResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/login",
		map[string]any{
			"email":    emailA,
			"password": testPassword,
		},
		"",
	)
	require.Equal(t,
		http.StatusOK, loginResp.StatusCode,
		"Step 1: login must return 200",
	)

	loginBody := decodeJSON(t, loginResp)
	assert.Equal(t, userIDA, loginBody["user_id"])
	assert.NotEmpty(t, loginBody["token"])
	assert.NotEmpty(t, loginBody["expires_at"])

	// Step 2: Login with wrong password
	t.Log("Step 2: Login with wrong password")

	badPwResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/login",
		map[string]any{
			"email":    emailA,
			"password": "wrongpassword99",
		},
		"",
	)
	require.Equal(t,
		http.StatusUnauthorized, badPwResp.StatusCode,
		"Step 2: wrong password must return 401",
	)
	badPwResp.Body.Close()

	// Step 3: Login with pending (unverified) user
	t.Log("Step 3: Login with pending user")

	clearMailbox(t)

	_, tokenB := createAnonymousUser(t)
	emailB := uniqueEmail()

	pendingResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/register",
		map[string]any{
			"email":    emailB,
			"password": testPassword,
		},
		tokenB,
	)
	require.Equal(t,
		http.StatusOK, pendingResp.StatusCode,
		"Step 3: register B must succeed",
	)
	pendingResp.Body.Close()

	// Pending user login: returns 401 (implementation
	// hides user state for security — see Login use case
	// step 3: "must be registered → ErrInvalidCredentials")
	pendingLoginResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/login",
		map[string]any{
			"email":    emailB,
			"password": testPassword,
		},
		"",
	)
	require.Equal(t,
		http.StatusUnauthorized,
		pendingLoginResp.StatusCode,
		"Step 3: pending user login must return 401",
	)
	pendingLoginResp.Body.Close()
}

// TestDuplicateEmailRejection verifies that registering
// a second user with an already-taken email returns 409.
func TestDuplicateEmailRejection(t *testing.T) {
	clearMailbox(t)

	email := uniqueEmail()

	// Register and confirm user A with this email
	t.Log("Setup: Register and confirm user A")

	_, tokenA := createAnonymousUser(t)
	_ = registerAndConfirm(t, tokenA, email)

	// Attempt to register user B with the same email
	t.Log("Test: Register user B with same email")

	_, tokenB := createAnonymousUser(t)

	dupResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/register",
		map[string]any{
			"email":    email,
			"password": testPassword,
		},
		tokenB,
	)
	require.Equal(t,
		http.StatusConflict, dupResp.StatusCode,
		"duplicate email must return 409",
	)
	dupResp.Body.Close()
}

// TestRoutesSurviveRegistration creates multiple routes as
// an anonymous user, registers, and verifies all routes
// remain accessible with owner_type transitioned to "user".
func TestRoutesSurviveRegistration(t *testing.T) {
	clearMailbox(t)

	userID, token := createAnonymousUser(t)
	t.Cleanup(func() { deleteUser(t, userID, token) })

	// Create 3 routes
	t.Log("Step 1: Create 3 routes as anonymous")

	const routeCount = 3

	routeIDs := make([]string, routeCount)
	for i := range routeCount {
		rid := prepareRoute(t, token)
		createRouteWithWaypoints(
			t, token, rid, defaultTestImages,
		)
		routeIDs[i] = rid

		t.Cleanup(func() {
			deleteRoute(t, rid, token)
		})
	}

	// Register and confirm
	t.Log("Step 2: Register and confirm")

	email := uniqueEmail()
	regToken := registerAndConfirm(
		t, token, email,
	)
	token = regToken

	// Verify all routes accessible and owner_type = user
	t.Log("Step 3: Verify routes accessible with new JWT")

	for _, rid := range routeIDs {
		resp := doRequest(
			t, http.MethodGet,
			apiURL+"/api/v1/routes/"+rid,
			nil, regToken,
		)
		require.Equal(t,
			http.StatusOK, resp.StatusCode,
			"route %s must be accessible", rid,
		)
		resp.Body.Close()

		waitForOwnerType(
			t, rid, regToken, "user", 10*time.Second,
		)
	}
}

// TestVerificationCodeSecurity verifies that wrong codes
// are rejected, attempts are limited, and resend provides
// a fresh code that succeeds.
func TestVerificationCodeSecurity(t *testing.T) {
	clearMailbox(t)

	_, token := createAnonymousUser(t)
	email := uniqueEmail()

	// Register
	t.Log("Step 1: Register")

	regResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/register",
		map[string]any{
			"email":    email,
			"password": testPassword,
		},
		token,
	)
	require.Equal(t, http.StatusOK, regResp.StatusCode)
	regResp.Body.Close()

	// Get the real code (we won't use it — we'll send
	// wrong codes to exhaust attempts)
	msgID := waitForEmail(t, email)
	_ = extractVerificationCode(t, msgID)

	// Step 2: Submit wrong code 5 times → 400 each
	t.Log("Step 2: Submit wrong code 5 times")

	const maxAttempts = 5

	for i := range maxAttempts {
		wrongResp := doRequest(
			t, http.MethodPost,
			apiURL+"/api/v1/auth/confirm-registration",
			map[string]any{"code": "000000"},
			token,
		)
		require.Equal(t,
			http.StatusBadRequest,
			wrongResp.StatusCode,
			"attempt %d: wrong code must return 400",
			i+1,
		)
		wrongResp.Body.Close()
	}

	// Step 3: 6th attempt → 429 (too many attempts)
	t.Log("Step 3: 6th attempt returns 429")

	exhaustedResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/confirm-registration",
		map[string]any{"code": "000000"},
		token,
	)
	require.Equal(t,
		http.StatusTooManyRequests,
		exhaustedResp.StatusCode,
		"6th attempt must return 429",
	)
	exhaustedResp.Body.Close()

	// Step 4: Resend verification code
	t.Log("Step 4: Resend verification code")

	clearMailbox(t)

	// Wait for resend cooldown (AUTH_RESEND_COOLDOWN=1s
	// in test env; 2s sleep for safety margin)
	time.Sleep(2 * time.Second)

	resendResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/resend-verification",
		map[string]any{},
		token,
	)
	require.Equal(t,
		http.StatusNoContent,
		resendResp.StatusCode,
		"resend must return 204",
	)
	resendResp.Body.Close()

	// Step 5: Extract new code and confirm
	t.Log("Step 5: Confirm with new code")

	newMsgID := waitForEmail(t, email)
	newCode := extractVerificationCode(t, newMsgID)

	confirmResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/confirm-registration",
		map[string]any{"code": newCode},
		token,
	)
	require.Equal(t,
		http.StatusOK, confirmResp.StatusCode,
		"confirm with new code must return 200",
	)
	confirmResp.Body.Close()
}

// TestPasswordResetFlow verifies the full forgot-password
// and reset-password cycle: old password stops working,
// new password works.
func TestPasswordResetFlow(t *testing.T) {
	clearMailbox(t)

	// Setup: Create and register a user
	t.Log("Setup: Register and confirm user")

	_, token := createAnonymousUser(t)
	email := uniqueEmail()
	_ = registerAndConfirm(t, token, email)

	// Step 1: Request password reset
	t.Log("Step 1: Forgot password")

	clearMailbox(t)

	forgotResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/forgot-password",
		map[string]any{"email": email},
		"",
	)
	require.Equal(t,
		http.StatusNoContent, forgotResp.StatusCode,
		"forgot-password must return 204",
	)
	forgotResp.Body.Close()

	// Step 2: Extract reset code from email
	t.Log("Step 2: Extract reset code")

	msgID := waitForEmail(t, email)
	resetCode := extractVerificationCode(t, msgID)
	require.Len(t, resetCode, 6)

	// Step 3: Reset password
	t.Log("Step 3: Reset password")

	const newPassword = "newsecurepass456"

	resetResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/reset-password",
		map[string]any{
			"email":        email,
			"code":         resetCode,
			"new_password": newPassword,
		},
		"",
	)
	require.Equal(t,
		http.StatusNoContent, resetResp.StatusCode,
		"reset-password must return 204",
	)
	resetResp.Body.Close()

	// Step 4: Login with new password succeeds
	t.Log("Step 4: Login with new password")

	newPwResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/login",
		map[string]any{
			"email":    email,
			"password": newPassword,
		},
		"",
	)
	require.Equal(t,
		http.StatusOK, newPwResp.StatusCode,
		"login with new password must return 200",
	)
	newPwResp.Body.Close()

	// Step 5: Login with old password fails
	t.Log("Step 5: Login with old password fails")

	oldPwResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/login",
		map[string]any{
			"email":    email,
			"password": testPassword,
		},
		"",
	)
	require.Equal(t,
		http.StatusUnauthorized, oldPwResp.StatusCode,
		"login with old password must return 401",
	)
	oldPwResp.Body.Close()
}
