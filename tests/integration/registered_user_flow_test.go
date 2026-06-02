//go:build integration

package integration_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
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
// code from Mailpit, and confirm. Returns the new user ID
// (PK-swapped after promotion), access token, and refresh
// token. Caller must clearMailbox before calling if needed.
func registerAndConfirm(
	t *testing.T,
	anonToken string,
	email string,
) (newUserID, accessToken, refreshToken string) {
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

	uid, ok := confirmBody["user_id"].(string)
	require.True(t, ok,
		"registerAndConfirm: missing user_id",
	)

	at, ok := confirmBody["access_token"].(string)
	require.True(t, ok,
		"registerAndConfirm: missing access_token",
	)

	rt, _ := confirmBody["refresh_token"].(string)

	return uid, at, rt
}

// TestRegistrationFullFlow exercises the complete
// anonymous -> pending -> registered lifecycle with route
// owner_type transition.
func TestRegistrationFullFlow(t *testing.T) {
	clearMailbox(t)

	// Step 1: Create anonymous user
	t.Log("Step 1: Create anonymous user")

	userID, token, _ := createAnonymousUser(t)
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
	newUserID, _ := confirmBody["user_id"].(string)
	assert.NotEqual(t, userID, newUserID,
		"Step 5: user_id must change after PK swap",
	)
	assert.NotEmpty(t, newUserID,
		"Step 5: user_id must not be empty",
	)

	regToken, ok := confirmBody["access_token"].(string)
	require.True(t, ok,
		"Step 5: response must contain access_token",
	)
	require.NotEmpty(t, regToken,
		"Step 5: access_token must not be empty",
	)
	assert.NotEmpty(t, confirmBody["access_token_expires_at"],
		"Step 5: access_token_expires_at must be present",
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

	userIDA, tokenA, _ := createAnonymousUser(t)
	t.Cleanup(func() { deleteUser(t, userIDA, tokenA) })

	emailA := uniqueEmail()
	_, regTokenA, _ := registerAndConfirm(
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
	assert.NotEmpty(t, loginBody["user_id"])
	assert.NotEmpty(t, loginBody["access_token"])
	assert.NotEmpty(t, loginBody["access_token_expires_at"])

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

	_, tokenB, _ := createAnonymousUser(t)
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

	_, tokenA, _ := createAnonymousUser(t)
	_, _, _ = registerAndConfirm(t, tokenA, email)

	// Attempt to register user B with the same email
	t.Log("Test: Register user B with same email")

	_, tokenB, _ := createAnonymousUser(t)

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

	userID, token, _ := createAnonymousUser(t)
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
	_, regToken, _ := registerAndConfirm(
		t, token, email,
	)
	token = regToken

	// Verify all routes accessible and owner_type = user
	t.Log("Step 3: Verify routes accessible with new JWT")

	for _, rid := range routeIDs {
		waitForOwnerType(
			t, rid, regToken, "user", 10*time.Second,
		)

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
	}
}

// TestVerificationCodeSecurity verifies that wrong codes
// are rejected, attempts are limited, and resend provides
// a fresh code that succeeds.
func TestVerificationCodeSecurity(t *testing.T) {
	clearMailbox(t)

	_, token, _ := createAnonymousUser(t)
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

	_, token, _ := createAnonymousUser(t)
	email := uniqueEmail()
	_, _, _ = registerAndConfirm(t, token, email)

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

// --- Error / edge-case tests below ---

// TestRegisterAlreadyRegistered verifies that a fully
// registered user cannot register again (terminal state).
func TestRegisterAlreadyRegistered(t *testing.T) {
	clearMailbox(t)

	_, token, _ := createAnonymousUser(t)
	email := uniqueEmail()
	_, regToken, _ := registerAndConfirm(t, token, email)

	resp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/register",
		map[string]any{
			"email":    "other-" + uniqueEmail(),
			"password": testPassword,
		},
		regToken,
	)
	require.Equal(t,
		http.StatusConflict, resp.StatusCode,
		"registered user re-register must return 409",
	)

	body := decodeJSON(t, resp)
	assert.Equal(t, "already_registered", body["name"])
}

// TestRegisterAlreadyPending verifies that a pending user
// cannot start registration a second time.
func TestRegisterAlreadyPending(t *testing.T) {
	clearMailbox(t)

	_, token, _ := createAnonymousUser(t)
	email := uniqueEmail()

	// First register → pending
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

	// Second register → 409
	dupResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/register",
		map[string]any{
			"email":    "other-" + uniqueEmail(),
			"password": testPassword,
		},
		token,
	)
	require.Equal(t,
		http.StatusConflict, dupResp.StatusCode,
		"pending user re-register must return 409",
	)

	body := decodeJSON(t, dupResp)
	assert.Equal(t,
		"registration_already_started", body["name"],
	)
}

// TestRegisterNoAuth verifies that register without a JWT
// returns 401.
func TestRegisterNoAuth(t *testing.T) {
	resp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/register",
		map[string]any{
			"email":    uniqueEmail(),
			"password": testPassword,
		},
		"",
	)
	require.Equal(t,
		http.StatusUnauthorized, resp.StatusCode,
		"register without JWT must return 401",
	)
	resp.Body.Close()
}

// TestRegisterInvalidEmail verifies that a malformed email
// is rejected with 400.
func TestRegisterInvalidEmail(t *testing.T) {
	_, token, _ := createAnonymousUser(t)

	resp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/register",
		map[string]any{
			"email":    "not-an-email",
			"password": testPassword,
		},
		token,
	)
	require.Equal(t,
		http.StatusBadRequest, resp.StatusCode,
		"invalid email must return 400",
	)
	resp.Body.Close()
}

// TestRegisterShortPassword verifies that a password shorter
// than 8 characters is rejected.
func TestRegisterShortPassword(t *testing.T) {
	_, token, _ := createAnonymousUser(t)

	resp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/register",
		map[string]any{
			"email":    uniqueEmail(),
			"password": "short",
		},
		token,
	)
	require.Equal(t,
		http.StatusBadRequest, resp.StatusCode,
		"short password must return 400",
	)
	resp.Body.Close()
}

// TestConfirmNotPending verifies that an anonymous user
// (not pending) cannot confirm registration.
func TestConfirmNotPending(t *testing.T) {
	_, token, _ := createAnonymousUser(t)

	resp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/confirm-registration",
		map[string]any{"code": "123456"},
		token,
	)
	require.Equal(t,
		http.StatusBadRequest, resp.StatusCode,
		"confirm on anonymous user must return 400",
	)

	body := decodeJSON(t, resp)
	assert.Equal(t,
		"registration_not_started", body["name"],
	)
}

// TestResendNotPending verifies that an anonymous user
// cannot resend a verification code.
func TestResendNotPending(t *testing.T) {
	_, token, _ := createAnonymousUser(t)

	resp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/resend-verification",
		map[string]any{},
		token,
	)
	require.Equal(t,
		http.StatusBadRequest, resp.StatusCode,
		"resend on anonymous user must return 400",
	)

	body := decodeJSON(t, resp)
	assert.Equal(t,
		"registration_not_started", body["name"],
	)
}

// TestLoginNonExistentEmail verifies that login with an
// unknown email returns 401 (never leaks email existence).
func TestLoginNonExistentEmail(t *testing.T) {
	resp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/login",
		map[string]any{
			"email":    "nobody-" + uniqueEmail(),
			"password": testPassword,
		},
		"",
	)
	require.Equal(t,
		http.StatusUnauthorized, resp.StatusCode,
		"non-existent email login must return 401",
	)

	body := decodeJSON(t, resp)
	assert.Equal(t, "invalid_credentials", body["name"])
}

// TestForgotPasswordNonExistentEmail verifies that
// forgot-password with an unknown email still returns 204
// (never leaks email existence).
func TestForgotPasswordNonExistentEmail(t *testing.T) {
	resp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/forgot-password",
		map[string]any{
			"email": "nobody-" + uniqueEmail(),
		},
		"",
	)
	require.Equal(t,
		http.StatusNoContent, resp.StatusCode,
		"forgot-password for unknown email must return 204",
	)
	resp.Body.Close()
}

// TestForgotPasswordAnonymousUser verifies that
// forgot-password for an anonymous user (not registered)
// returns 204 silently.
func TestForgotPasswordAnonymousUser(t *testing.T) {
	resp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/forgot-password",
		map[string]any{
			"email": uniqueEmail(),
		},
		"",
	)
	require.Equal(t,
		http.StatusNoContent, resp.StatusCode,
		"forgot-password for anonymous must return 204",
	)
	resp.Body.Close()
}

// TestResetPasswordWrongCode verifies that reset-password
// with a wrong code returns 400.
func TestResetPasswordWrongCode(t *testing.T) {
	clearMailbox(t)

	_, token, _ := createAnonymousUser(t)
	email := uniqueEmail()
	_, _, _ = registerAndConfirm(t, token, email)

	clearMailbox(t)

	forgotResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/forgot-password",
		map[string]any{"email": email},
		"",
	)
	require.Equal(t,
		http.StatusNoContent, forgotResp.StatusCode,
	)
	forgotResp.Body.Close()

	// Wait for email to confirm code was sent
	_ = waitForEmail(t, email)

	resp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/reset-password",
		map[string]any{
			"email":        email,
			"code":         "000000",
			"new_password": "newpassword123",
		},
		"",
	)
	require.Equal(t,
		http.StatusBadRequest, resp.StatusCode,
		"reset with wrong code must return 400",
	)

	body := decodeJSON(t, resp)
	assert.Equal(t, "invalid_code", body["name"])
}

// TestResetPasswordShortNewPassword verifies that
// reset-password rejects a new password shorter than 8
// characters.
func TestResetPasswordShortNewPassword(t *testing.T) {
	resp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/reset-password",
		map[string]any{
			"email":        uniqueEmail(),
			"code":         "123456",
			"new_password": "short",
		},
		"",
	)
	require.Equal(t,
		http.StatusBadRequest, resp.StatusCode,
		"reset with short password must return 400",
	)
	resp.Body.Close()
}

// TestRegisteredTokenWorksForAuthEndpoints verifies that
// the JWT returned by confirm-registration and login can
// be used for authenticated endpoints.
func TestRegisteredTokenWorksForAuthEndpoints(t *testing.T) {
	clearMailbox(t)

	_, token, _ := createAnonymousUser(t)
	email := uniqueEmail()
	newUserID, regToken, regRefreshToken := registerAndConfirm(
		t, token, email,
	)

	// Use registered token to GET own user (new UUID after PK swap)
	getResp := doRequest(
		t, http.MethodGet,
		apiURL+"/api/v1/users/anonymous/"+newUserID,
		nil,
		regToken,
	)
	require.Equal(t,
		http.StatusOK, getResp.StatusCode,
		"registered token must access user endpoint",
	)
	getResp.Body.Close()

	// Use refresh token to refresh (NoSecurity — no auth header)
	refreshResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/refresh",
		map[string]any{"refresh_token": regRefreshToken},
		"",
	)
	require.Equal(t,
		http.StatusOK, refreshResp.StatusCode,
		"registered refresh token must refresh",
	)
	refreshResp.Body.Close()

	// Login and verify that token also works
	loginResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/login",
		map[string]any{
			"email":    email,
			"password": testPassword,
		},
		"",
	)
	require.Equal(t, http.StatusOK, loginResp.StatusCode)

	loginBody := decodeJSON(t, loginResp)

	loginToken, _ := loginBody["access_token"].(string)
	require.NotEmpty(t, loginToken)

	// Login token can also access endpoints
	getResp2 := doRequest(
		t, http.MethodGet,
		apiURL+"/api/v1/users/anonymous/"+newUserID,
		nil,
		loginToken,
	)
	require.Equal(t,
		http.StatusOK, getResp2.StatusCode,
		"login token must access user endpoint",
	)
	getResp2.Body.Close()
}

// TestLoginInvalidEmailFormat verifies that login with a
// malformed email returns 400.
func TestLoginInvalidEmailFormat(t *testing.T) {
	resp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/login",
		map[string]any{
			"email":    "not-an-email",
			"password": testPassword,
		},
		"",
	)
	require.Equal(t,
		http.StatusBadRequest, resp.StatusCode,
		"login with invalid email must return 400",
	)
	resp.Body.Close()
}

// TestForgotPasswordInvalidEmail verifies that
// forgot-password with a malformed email returns 400.
func TestForgotPasswordInvalidEmail(t *testing.T) {
	resp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/forgot-password",
		map[string]any{
			"email": "not-an-email",
		},
		"",
	)
	require.Equal(t,
		http.StatusBadRequest, resp.StatusCode,
		"forgot-password with invalid email must return 400",
	)
	resp.Body.Close()
}

// TestResendTooSoon verifies that resending a verification
// code before the cooldown expires returns 429.
func TestResendTooSoon(t *testing.T) {
	clearMailbox(t)

	_, token, _ := createAnonymousUser(t)
	email := uniqueEmail()

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

	// Resend immediately — should be too soon
	// (AUTH_RESEND_COOLDOWN=1s in test env, but we call
	// within milliseconds of register)
	resendResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/resend-verification",
		map[string]any{},
		token,
	)
	require.Equal(t,
		http.StatusTooManyRequests,
		resendResp.StatusCode,
		"immediate resend must return 429",
	)

	body := decodeJSON(t, resendResp)
	assert.Equal(t, "resend_too_soon", body["name"])
}

// TestConfirmNoAuth verifies that confirm-registration
// without a JWT returns 401.
func TestConfirmNoAuth(t *testing.T) {
	resp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/confirm-registration",
		map[string]any{"code": "123456"},
		"",
	)
	require.Equal(t,
		http.StatusUnauthorized, resp.StatusCode,
		"confirm without JWT must return 401",
	)
	resp.Body.Close()
}

// TestResendNoAuth verifies that resend-verification
// without a JWT returns 401.
func TestResendNoAuth(t *testing.T) {
	resp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/resend-verification",
		map[string]any{},
		"",
	)
	require.Equal(t,
		http.StatusUnauthorized, resp.StatusCode,
		"resend without JWT must return 401",
	)
	resp.Body.Close()
}

// --- Concurrency and idempotency tests below ---

// TestConcurrentRegisterSameEmail verifies that when two
// anonymous users race to register the same email, exactly
// one succeeds (200) and the other gets 409 (email_taken).
func TestConcurrentRegisterSameEmail(t *testing.T) {
	clearMailbox(t)

	email := uniqueEmail()

	const racers = 5

	type result struct {
		status int
	}

	results := make(chan result, racers)

	// Create N anonymous users upfront
	tokens := make([]string, racers)
	for i := range racers {
		_, tok, _ := createAnonymousUser(t)
		tokens[i] = tok
	}

	// Fire all registrations concurrently.
	// doRequest uses require (can't call from goroutines),
	// so we build and send requests manually here.
	var wg sync.WaitGroup

	wg.Add(racers)

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}

	for i := range racers {
		go func(token string) {
			defer wg.Done()

			body, _ := json.Marshal(map[string]any{
				"email":    email,
				"password": testPassword,
			})

			req, err := http.NewRequest(
				http.MethodPost,
				apiURL+"/api/v1/auth/register",
				bytes.NewReader(body),
			)
			if err != nil {
				results <- result{status: -1}
				return
			}

			req.Header.Set(
				"Content-Type", "application/json",
			)
			req.Header.Set(
				"Authorization", "Bearer "+token,
			)

			resp, err := client.Do(req)
			if err != nil {
				results <- result{status: -1}
				return
			}
			resp.Body.Close()

			results <- result{status: resp.StatusCode}
		}(tokens[i])
	}

	wg.Wait()
	close(results)

	var ok, conflict, other int

	for r := range results {
		switch r.status {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		default:
			other++
			t.Errorf(
				"unexpected status %d from racer",
				r.status,
			)
		}
	}

	assert.Equal(t, 1, ok,
		"exactly one racer must succeed (200)",
	)
	assert.Equal(t, racers-1, conflict,
		"all other racers must get 409",
	)
	assert.Equal(t, 0, other,
		"no unexpected status codes",
	)
}

// TestConfirmRegistrationIdempotency verifies that calling
// confirm-registration a second time with the same code
// after the first success fails (verification record was
// deleted on first confirm).
func TestConfirmRegistrationIdempotency(t *testing.T) {
	clearMailbox(t)

	_, token, _ := createAnonymousUser(t)
	email := uniqueEmail()

	// Register
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

	msgID := waitForEmail(t, email)
	code := extractVerificationCode(t, msgID)

	// First confirm → success
	firstResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/confirm-registration",
		map[string]any{"code": code},
		token,
	)
	require.Equal(t,
		http.StatusOK, firstResp.StatusCode,
		"first confirm must succeed",
	)

	firstBody := decodeJSON(t, firstResp)

	regToken, _ := firstBody["access_token"].(string)
	require.NotEmpty(t, regToken)

	// Second confirm with same code → must fail.
	// User is now registered (terminal state), so this
	// should return 409 (already_registered) or 400
	// (registration_not_started) depending on which guard
	// fires first.
	secondResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/confirm-registration",
		map[string]any{"code": code},
		regToken,
	)

	// Accept either 409 (already_registered — user is now
	// registered) or 400 (registration_not_started — user
	// is no longer pending). Both are correct rejections.
	status := secondResp.StatusCode
	secondResp.Body.Close()

	assert.True(t,
		status == http.StatusConflict ||
			status == http.StatusBadRequest,
		"second confirm must fail (got %d)", status,
	)
}

// TestResetPasswordIdempotency verifies that using the
// same reset code twice fails on the second attempt
// (verification record deleted after first reset).
func TestResetPasswordIdempotency(t *testing.T) {
	clearMailbox(t)

	_, token, _ := createAnonymousUser(t)
	email := uniqueEmail()
	_, _, _ = registerAndConfirm(t, token, email)

	clearMailbox(t)

	// Request password reset
	forgotResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/forgot-password",
		map[string]any{"email": email},
		"",
	)
	require.Equal(t,
		http.StatusNoContent, forgotResp.StatusCode,
	)
	forgotResp.Body.Close()

	msgID := waitForEmail(t, email)
	code := extractVerificationCode(t, msgID)

	// First reset → success
	firstResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/reset-password",
		map[string]any{
			"email":        email,
			"code":         code,
			"new_password": "firstnewpass123",
		},
		"",
	)
	require.Equal(t,
		http.StatusNoContent, firstResp.StatusCode,
		"first reset must succeed",
	)
	firstResp.Body.Close()

	// Second reset with same code → must fail
	secondResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/reset-password",
		map[string]any{
			"email":        email,
			"code":         code,
			"new_password": "secondnewpass456",
		},
		"",
	)

	// Verification record was deleted; expect 400
	// (invalid_code or code_expired) or similar.
	assert.NotEqual(t,
		http.StatusNoContent,
		secondResp.StatusCode,
		"second reset with same code must fail",
	)
	secondResp.Body.Close()

	// Verify the first password is the one that stuck
	loginResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/login",
		map[string]any{
			"email":    email,
			"password": "firstnewpass123",
		},
		"",
	)
	require.Equal(t,
		http.StatusOK, loginResp.StatusCode,
		"login with first new password must work",
	)
	loginResp.Body.Close()

	// Second password must NOT work
	loginResp2 := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/login",
		map[string]any{
			"email":    email,
			"password": "secondnewpass456",
		},
		"",
	)
	require.Equal(t,
		http.StatusUnauthorized,
		loginResp2.StatusCode,
		"login with second password must fail",
	)
	loginResp2.Body.Close()
}

// --- Token expiry tests below ---
// These tests restart follow-api with JWT_ANONYMOUS_ACCESS_TTL=2s
// and JWT_REGISTERED_ACCESS_TTL=2s, run the expiry subtests,
// then restore the normal API.
// Local mode only — docker mode cannot restart individual
// containers mid-test.

// restartAPIProcess kills the current follow-api subprocess
// and starts a new one with the given extra env vars.
// Local mode only — docker compose cannot cleanly restart
// a single container without reconciling the full project.
func restartAPIProcess(
	t *testing.T,
	extraEnv ...string,
) {
	t.Helper()

	killProcessGroup(
		"follow-api", apiProcess, apiDrainWait,
	)

	projectRoot, err := filepath.Abs(
		filepath.Join("..", ".."),
	)
	require.NoError(t, err)

	apiDir := filepath.Join(projectRoot, "follow-api")
	apiPort := portFromURL(apiURL, "8085")
	gatewayPort := portFromURL(gatewayURL, "8095")

	apiProcess = exec.Command(
		"go", "run", "./cmd/server",
		"-host", "localhost",
		"-port", apiPort,
		"-log-level", "debug",
		"-runtime-timeout", "0",
	)
	apiProcess.Dir = apiDir
	apiProcess.Env = append(
		os.Environ(),
		"GATEWAY_BASE_URL=http://localhost:"+gatewayPort,
		"RATE_LIMIT_ENABLED=false",
		"REAPER_SCAN_INTERVAL=1s",
		"REAPER_STALE_THRESHOLD=2s",
		"RECLAIMER_IDLE_TIMEOUT=5s",
		"RECLAIMER_SCAN_INTERVAL=2s",
		"AUTH_RESEND_COOLDOWN=1s",
	)
	apiProcess.Env = append(
		apiProcess.Env, extraEnv...,
	)
	apiProcess.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	apiDrainWait = pipeOutput(apiProcess)

	err = apiProcess.Start()
	require.NoError(t, err,
		"restartAPIProcess: failed to start",
	)

	waitForService(apiURL + "/health")
}

// TestTokenExpiry restarts the API with a 2s JWT TTL, runs
// all expiry subtests, then restores the normal API.
func TestTokenExpiry(t *testing.T) {
	if envOrDefault("INTEGRATION_TEST_MODE", "local") != "local" {
		t.Skip(
			"token expiry tests require API restart " +
				"(local mode only)",
		)
	}

	restartAPIProcess(
		t,
		"JWT_ANONYMOUS_ACCESS_TTL=2s",
		"JWT_REGISTERED_ACCESS_TTL=2s",
	)
	t.Cleanup(func() {
		restartAPIProcess(t)
	})

	t.Run("AnonymousToken", func(t *testing.T) {
		_, token, _ := createAnonymousUser(t)

		resp := doRequest(
			t, http.MethodPost,
			apiURL+"/api/v1/routes/prepare",
			map[string]any{},
			token,
		)
		require.Equal(t,
			http.StatusOK, resp.StatusCode,
			"fresh anonymous token must work",
		)
		resp.Body.Close()

		time.Sleep(3 * time.Second)

		expResp := doRequest(
			t, http.MethodPost,
			apiURL+"/api/v1/routes/prepare",
			map[string]any{},
			token,
		)
		require.Equal(t,
			http.StatusUnauthorized,
			expResp.StatusCode,
			"expired anonymous token must return 401",
		)
		expResp.Body.Close()
	})

	t.Run("RegisteredToken", func(t *testing.T) {
		clearMailbox(t)

		_, anonToken, _ := createAnonymousUser(t)
		email := uniqueEmail()
		newUserID, regToken, _ := registerAndConfirm(
			t, anonToken, email,
		)

		resp := doRequest(
			t, http.MethodGet,
			apiURL+"/api/v1/users/anonymous/"+newUserID,
			nil, regToken,
		)
		require.Equal(t,
			http.StatusOK, resp.StatusCode,
			"fresh registered token must work",
		)
		resp.Body.Close()

		time.Sleep(3 * time.Second)

		expResp := doRequest(
			t, http.MethodGet,
			apiURL+"/api/v1/users/anonymous/"+newUserID,
			nil, regToken,
		)
		require.Equal(t,
			http.StatusUnauthorized,
			expResp.StatusCode,
			"expired registered token must return 401",
		)
		expResp.Body.Close()

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
		)

		loginBody := decodeJSON(t, loginResp)

		freshToken, _ := loginBody["access_token"].(string)
		require.NotEmpty(t, freshToken)

		freshResp := doRequest(
			t, http.MethodGet,
			apiURL+"/api/v1/users/anonymous/"+newUserID,
			nil, freshToken,
		)
		require.Equal(t,
			http.StatusOK, freshResp.StatusCode,
			"fresh login token must work",
		)
		freshResp.Body.Close()
	})

	t.Run("RefreshExtends", func(t *testing.T) {
		_, _, anonRefreshToken := createAnonymousUser(t)

		time.Sleep(500 * time.Millisecond)

		refreshResp := doRequest(
			t, http.MethodPost,
			apiURL+"/api/v1/auth/refresh",
			map[string]any{
				"refresh_token": anonRefreshToken,
			},
			"",
		)
		require.Equal(t,
			http.StatusOK, refreshResp.StatusCode,
			"refresh within TTL must succeed",
		)

		refreshBody := decodeJSON(t, refreshResp)

		newToken, _ := refreshBody["access_token"].(string)
		require.NotEmpty(t, newToken)

		time.Sleep(1 * time.Second)

		resp := doRequest(
			t, http.MethodPost,
			apiURL+"/api/v1/routes/prepare",
			map[string]any{},
			newToken,
		)
		require.Equal(t,
			http.StatusOK, resp.StatusCode,
			"refreshed token must still be valid",
		)
		resp.Body.Close()

		time.Sleep(3 * time.Second)

		expResp := doRequest(
			t, http.MethodPost,
			apiURL+"/api/v1/routes/prepare",
			map[string]any{},
			newToken,
		)
		require.Equal(t,
			http.StatusUnauthorized,
			expResp.StatusCode,
			"refreshed token must eventually expire",
		)
		expResp.Body.Close()
	})
}

// --- Session lifecycle tests below ---

// TestLogoutSingleDevice logs in, calls POST /auth/logout
// with the access token, and verifies the refresh token from
// that session is dead (session deleted server-side).
func TestLogoutSingleDevice(t *testing.T) {
	clearMailbox(t)

	// Setup: register a user
	t.Log("Setup: Register and confirm user")

	_, anonToken, _ := createAnonymousUser(t)
	email := uniqueEmail()
	_, regToken, _ := registerAndConfirm(
		t, anonToken, email,
	)

	// Step 1: Login to get a session with refresh token
	t.Log("Step 1: Login")

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
	)

	loginBody := decodeJSON(t, loginResp)

	accessToken, _ := loginBody["access_token"].(string)
	require.NotEmpty(t, accessToken)

	refreshToken, _ := loginBody["refresh_token"].(string)
	require.NotEmpty(t, refreshToken)

	// Step 2: Logout with the access token
	t.Log("Step 2: POST /auth/logout")

	logoutResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/logout",
		nil,
		accessToken,
	)
	require.Equal(t,
		http.StatusNoContent, logoutResp.StatusCode,
		"logout must return 204",
	)
	logoutResp.Body.Close()

	// Step 3: Refresh with the old refresh token → must fail
	t.Log("Step 3: Refresh with old token fails")

	refreshResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/refresh",
		map[string]any{
			"refresh_token": refreshToken,
		},
		"",
	)
	require.Equal(t,
		http.StatusUnauthorized,
		refreshResp.StatusCode,
		"refresh after logout must return 401",
	)
	refreshResp.Body.Close()

	// Step 4: Login still works (account is not deleted)
	t.Log("Step 4: Login still works")

	reloginResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/login",
		map[string]any{
			"email":    email,
			"password": testPassword,
		},
		"",
	)
	require.Equal(t,
		http.StatusOK, reloginResp.StatusCode,
		"login after logout must succeed",
	)
	reloginResp.Body.Close()

	_ = regToken
}

// TestLogoutAllDevices logs in from two "devices", calls
// POST /auth/logout-all, and verifies both refresh tokens
// are dead.
func TestLogoutAllDevices(t *testing.T) {
	clearMailbox(t)

	// Setup: register a user
	t.Log("Setup: Register and confirm user")

	_, anonToken, _ := createAnonymousUser(t)
	email := uniqueEmail()
	_, _, _ = registerAndConfirm(t, anonToken, email)

	// Step 1: Login from device A
	t.Log("Step 1: Login device A")

	loginA := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/login",
		map[string]any{
			"email":    email,
			"password": testPassword,
		},
		"",
	)
	require.Equal(t, http.StatusOK, loginA.StatusCode)

	bodyA := decodeJSON(t, loginA)

	tokenA, _ := bodyA["access_token"].(string)
	require.NotEmpty(t, tokenA)

	refreshA, _ := bodyA["refresh_token"].(string)
	require.NotEmpty(t, refreshA)

	// Step 2: Login from device B
	t.Log("Step 2: Login device B")

	loginB := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/login",
		map[string]any{
			"email":    email,
			"password": testPassword,
		},
		"",
	)
	require.Equal(t, http.StatusOK, loginB.StatusCode)

	bodyB := decodeJSON(t, loginB)

	tokenB, _ := bodyB["access_token"].(string)
	require.NotEmpty(t, tokenB)

	refreshB, _ := bodyB["refresh_token"].(string)
	require.NotEmpty(t, refreshB)

	// Step 3: Logout all using device A's token
	t.Log("Step 3: POST /auth/logout-all")

	logoutResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/logout-all",
		nil,
		tokenA,
	)
	require.Equal(t,
		http.StatusNoContent, logoutResp.StatusCode,
		"logout-all must return 204",
	)
	logoutResp.Body.Close()

	// Step 4: Both refresh tokens are dead
	t.Log("Step 4: Both refresh tokens fail")

	refreshRespA := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/refresh",
		map[string]any{
			"refresh_token": refreshA,
		},
		"",
	)
	require.Equal(t,
		http.StatusUnauthorized,
		refreshRespA.StatusCode,
		"device A refresh after logout-all must return 401",
	)
	refreshRespA.Body.Close()

	refreshRespB := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/refresh",
		map[string]any{
			"refresh_token": refreshB,
		},
		"",
	)
	require.Equal(t,
		http.StatusUnauthorized,
		refreshRespB.StatusCode,
		"device B refresh after logout-all must return 401",
	)
	refreshRespB.Body.Close()

	// Step 5: Fresh login still works
	t.Log("Step 5: Fresh login works")

	freshLogin := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/login",
		map[string]any{
			"email":    email,
			"password": testPassword,
		},
		"",
	)
	require.Equal(t,
		http.StatusOK, freshLogin.StatusCode,
		"login after logout-all must succeed",
	)
	freshLogin.Body.Close()
}

// TestPasswordResetInvalidatesSessions verifies that
// resetting a password kills all existing sessions: login,
// reset password via forgot-password flow, verify old
// refresh token fails, verify new login works.
func TestPasswordResetInvalidatesSessions(t *testing.T) {
	clearMailbox(t)

	// Setup: register and login
	t.Log("Setup: Register, confirm, and login")

	_, anonToken, _ := createAnonymousUser(t)
	email := uniqueEmail()
	_, _, _ = registerAndConfirm(t, anonToken, email)

	loginResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/login",
		map[string]any{
			"email":    email,
			"password": testPassword,
		},
		"",
	)
	require.Equal(t, http.StatusOK, loginResp.StatusCode)

	loginBody := decodeJSON(t, loginResp)

	oldRefresh, _ := loginBody["refresh_token"].(string)
	require.NotEmpty(t, oldRefresh)

	// Step 1: Forgot password
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
	)
	forgotResp.Body.Close()

	// Step 2: Extract reset code and reset password
	t.Log("Step 2: Reset password")

	msgID := waitForEmail(t, email)
	resetCode := extractVerificationCode(t, msgID)

	const newPassword = "resetpass789"

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
	)
	resetResp.Body.Close()

	// Step 3: Old refresh token is dead
	t.Log("Step 3: Old refresh token fails")

	refreshResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/refresh",
		map[string]any{
			"refresh_token": oldRefresh,
		},
		"",
	)
	require.Equal(t,
		http.StatusUnauthorized,
		refreshResp.StatusCode,
		"refresh after password reset must return 401",
	)
	refreshResp.Body.Close()

	// Step 4: Login with new password works
	t.Log("Step 4: Login with new password works")

	newLoginResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/login",
		map[string]any{
			"email":    email,
			"password": newPassword,
		},
		"",
	)
	require.Equal(t,
		http.StatusOK, newLoginResp.StatusCode,
		"login with new password must succeed",
	)
	newLoginResp.Body.Close()

	// Step 5: Login with old password fails
	t.Log("Step 5: Login with old password fails")

	oldLoginResp := doRequest(
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
		oldLoginResp.StatusCode,
		"login with old password must return 401",
	)
	oldLoginResp.Body.Close()
}

// TestStaleAnonymousTokenAfterPromotion creates an anonymous
// user, saves the anonymous refresh token, registers+confirms
// (PK changes), then refreshes with the old anonymous refresh
// token. The anonymous refresh path is stateless (HMAC), so
// the refresh itself succeeds — but the resulting access token
// references the OLD UUID which no longer exists, so any API
// call with that access token returns 401 or 404.
func TestStaleAnonymousTokenAfterPromotion(t *testing.T) {
	clearMailbox(t)

	// Step 1: Create anonymous user and save refresh token
	t.Log("Step 1: Create anonymous user")

	anonUserID, anonToken, anonRefresh := createAnonymousUser(t)
	require.NotEmpty(t, anonRefresh,
		"anonymous user must have a refresh token",
	)

	// Step 2: Register and confirm (PK changes)
	t.Log("Step 2: Register and confirm")

	email := uniqueEmail()
	newUserID, regToken, _ := registerAndConfirm(
		t, anonToken, email,
	)
	require.NotEqual(t, anonUserID, newUserID,
		"user ID must change after PK swap",
	)

	_ = regToken

	// Step 3: Refresh with old anonymous refresh token
	t.Log("Step 3: Refresh with stale anonymous token")

	refreshResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/refresh",
		map[string]any{
			"refresh_token": anonRefresh,
		},
		"",
	)

	// The anonymous refresh path is stateless HMAC — the
	// server may either:
	// (a) succeed (200) and return an access token that
	//     references the old UUID, or
	// (b) reject (401) if the server detects the user was
	//     promoted and the anonymous refresh is invalidated.
	//
	// Both behaviors are acceptable. If (a), the access
	// token is useless because the old UUID no longer exists.
	if refreshResp.StatusCode == http.StatusOK {
		refreshBody := decodeJSON(t, refreshResp)

		staleToken, _ := refreshBody["access_token"].(string)
		require.NotEmpty(t, staleToken)

		// Step 4: Use stale access token → must fail
		t.Log("Step 4: API call with stale token fails")

		getResp := doRequest(
			t, http.MethodGet,
			apiURL+"/api/v1/users/anonymous/"+anonUserID,
			nil,
			staleToken,
		)

		// The old UUID no longer exists; expect 401
		// (token references deleted user) or 404
		// (user not found).
		status := getResp.StatusCode
		getResp.Body.Close()

		require.True(t,
			status == http.StatusUnauthorized ||
				status == http.StatusNotFound,
			"API call with stale token must return "+
				"401 or 404 (got %d)", status,
		)
	} else {
		// Server rejected the refresh outright — also valid
		refreshResp.Body.Close()

		require.Equal(t,
			http.StatusUnauthorized,
			refreshResp.StatusCode,
			"rejected anonymous refresh must return 401",
		)
		t.Log("Server rejected stale anonymous refresh (401)")
	}
}
