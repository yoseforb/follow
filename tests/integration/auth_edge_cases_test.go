//go:build integration

package integration_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestRefreshWithGarbageToken verifies that POST /auth/refresh
// with a non-JWT string returns 401, not 500.
func TestRefreshWithGarbageToken(t *testing.T) {
	resp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/refresh",
		map[string]any{
			"refresh_token": "this-is-not-a-jwt",
		},
		"",
	)
	require.Equal(t,
		http.StatusUnauthorized, resp.StatusCode,
		"garbage refresh token must return 401",
	)
	resp.Body.Close()
}

// TestLogoutIdempotency verifies that calling logout twice
// on the same session does not crash. The second call may
// return 204 (idempotent) or 401 (session gone).
func TestLogoutIdempotency(t *testing.T) {
	clearMailbox(t)

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

	accessToken, _ := loginBody["access_token"].(string)
	require.NotEmpty(t, accessToken)

	// First logout
	resp1 := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/logout",
		nil,
		accessToken,
	)
	require.Equal(t,
		http.StatusNoContent, resp1.StatusCode,
		"first logout must return 204",
	)
	resp1.Body.Close()

	// Second logout — session already deleted
	resp2 := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/logout",
		nil,
		accessToken,
	)

	status := resp2.StatusCode
	resp2.Body.Close()

	require.True(t,
		status == http.StatusNoContent ||
			status == http.StatusUnauthorized,
		"second logout must return 204 or 401 (got %d)",
		status,
	)
}

// TestLogoutAllIdempotency verifies that calling logout-all
// twice does not crash. The second call deletes 0 rows.
func TestLogoutAllIdempotency(t *testing.T) {
	clearMailbox(t)

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

	accessToken, _ := loginBody["access_token"].(string)
	require.NotEmpty(t, accessToken)

	// First logout-all
	resp1 := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/logout-all",
		nil,
		accessToken,
	)
	require.Equal(t,
		http.StatusNoContent, resp1.StatusCode,
		"first logout-all must return 204",
	)
	resp1.Body.Close()

	// Second logout-all — no sessions left
	resp2 := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/logout-all",
		nil,
		accessToken,
	)

	status := resp2.StatusCode
	resp2.Body.Close()

	require.True(t,
		status == http.StatusNoContent ||
			status == http.StatusUnauthorized,
		"second logout-all must return 204 or 401 (got %d)",
		status,
	)
}

// TestConcurrentDoubleRefresh fires two refresh calls
// simultaneously with the same token. Neither must 500.
func TestConcurrentDoubleRefresh(t *testing.T) {
	clearMailbox(t)

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

	refreshToken, _ := loginBody["refresh_token"].(string)
	require.NotEmpty(t, refreshToken)

	type result struct {
		name   string
		status int
	}

	results := make(chan result, 2)

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}

	var wg sync.WaitGroup

	wg.Add(2)

	for i := range 2 {
		go func(idx int) {
			defer wg.Done()

			body, _ := json.Marshal(map[string]any{
				"refresh_token": refreshToken,
			})

			req, err := http.NewRequest(
				http.MethodPost,
				apiURL+"/api/v1/auth/refresh",
				bytes.NewReader(body),
			)
			if err != nil {
				results <- result{
					name:   "refresh-" + string(rune('A'+idx)),
					status: -1,
				}
				return
			}

			req.Header.Set(
				"Content-Type", "application/json",
			)

			resp, err := client.Do(req)
			if err != nil {
				results <- result{
					name:   "refresh-" + string(rune('A'+idx)),
					status: -1,
				}
				return
			}
			resp.Body.Close()

			results <- result{
				name:   "refresh-" + string(rune('A'+idx)),
				status: resp.StatusCode,
			}
		}(i)
	}

	wg.Wait()
	close(results)

	for r := range results {
		require.True(t,
			r.status >= 200 && r.status < 500,
			"%s must not return 5xx (got %d)",
			r.name, r.status,
		)
		t.Logf("%s returned %d", r.name, r.status)
	}
}

// TestAccountDeletionKillsSessions verifies that confirming
// account deletion invalidates all active sessions.
func TestAccountDeletionKillsSessions(t *testing.T) {
	clearMailbox(t)

	// Register and login
	_, anonToken, _ := createAnonymousUser(t)
	email := uniqueEmail()
	_, regToken, _ := registerAndConfirm(t, anonToken, email)

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

	accessToken, _ := loginBody["access_token"].(string)
	require.NotEmpty(t, accessToken)

	refreshToken, _ := loginBody["refresh_token"].(string)
	require.NotEmpty(t, refreshToken)

	// Request account deletion
	t.Log("Step 1: Request account deletion")

	clearMailbox(t)

	reqResp := requestAccountDeletion(t, regToken)
	require.Equal(t,
		http.StatusNoContent, reqResp.StatusCode,
	)
	reqResp.Body.Close()

	// Extract deletion code
	msgID := waitForEmail(t, email)
	code := extractVerificationCode(t, msgID)

	// Confirm deletion
	t.Log("Step 2: Confirm account deletion")

	confirmResp := confirmAccountDeletion(t, regToken, code)
	require.Equal(t,
		http.StatusNoContent, confirmResp.StatusCode,
	)
	confirmResp.Body.Close()

	// Refresh with old token must fail
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
		"refresh after account deletion must return 401",
	)
	refreshResp.Body.Close()

	_ = accessToken
}

// TestResetPasswordCodeExhaustion verifies that submitting
// 5 wrong reset codes returns 400, and the 6th returns 429.
func TestResetPasswordCodeExhaustion(t *testing.T) {
	clearMailbox(t)

	_, anonToken, _ := createAnonymousUser(t)
	email := uniqueEmail()
	_, _, _ = registerAndConfirm(t, anonToken, email)

	// Request password reset
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

	// Get the real code (we won't use it)
	msgID := waitForEmail(t, email)
	_ = extractVerificationCode(t, msgID)

	// Submit wrong code 5 times → 400 each
	t.Log("Step 1: Submit wrong code 5 times")

	const maxAttempts = 5

	for i := range maxAttempts {
		wrongResp := doRequest(
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
			http.StatusBadRequest,
			wrongResp.StatusCode,
			"attempt %d: wrong code must return 400",
			i+1,
		)
		wrongResp.Body.Close()
	}

	// 6th attempt → 429
	t.Log("Step 2: 6th attempt returns 429")

	exhaustedResp := doRequest(
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
		http.StatusTooManyRequests,
		exhaustedResp.StatusCode,
		"6th attempt must return 429",
	)
	exhaustedResp.Body.Close()
}

// TestCrossUserSessionIsolation verifies that user A's
// token cannot access user B's data.
func TestCrossUserSessionIsolation(t *testing.T) {
	clearMailbox(t)

	// Register user A
	_, anonA, _ := createAnonymousUser(t)
	emailA := uniqueEmail()
	_, _, _ = registerAndConfirm(t, anonA, emailA)

	loginA := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/login",
		map[string]any{
			"email":    emailA,
			"password": testPassword,
		},
		"",
	)
	require.Equal(t, http.StatusOK, loginA.StatusCode)

	bodyA := decodeJSON(t, loginA)

	tokenA, _ := bodyA["access_token"].(string)
	require.NotEmpty(t, tokenA)

	// Register user B
	clearMailbox(t)

	_, anonB, _ := createAnonymousUser(t)
	emailB := uniqueEmail()
	userBID, _, _ := registerAndConfirm(t, anonB, emailB)

	// User A tries to access user B's data
	t.Log("User A accesses user B's endpoint")

	getResp := doRequest(
		t, http.MethodGet,
		apiURL+"/api/v1/users/anonymous/"+userBID,
		nil,
		tokenA,
	)

	status := getResp.StatusCode
	getResp.Body.Close()

	require.True(t,
		status == http.StatusForbidden ||
			status == http.StatusNotFound,
		"cross-user access must return 403 or 404 (got %d)",
		status,
	)
}

// TestRegisterMissingRequiredFields verifies that POST
// /auth/register with empty body returns 400.
func TestRegisterMissingRequiredFields(t *testing.T) {
	_, token, _ := createAnonymousUser(t)

	resp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/register",
		map[string]any{},
		token,
	)
	require.Equal(t,
		http.StatusBadRequest, resp.StatusCode,
		"register with empty body must return 400",
	)
	resp.Body.Close()
}

// TestLoginMissingRequiredFields verifies that POST
// /auth/login with missing fields returns 400.
func TestLoginMissingRequiredFields(t *testing.T) {
	// Missing password
	resp1 := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/login",
		map[string]any{
			"email": "valid@test.com",
		},
		"",
	)
	require.Equal(t,
		http.StatusBadRequest, resp1.StatusCode,
		"login without password must return 400",
	)
	resp1.Body.Close()

	// Missing email
	resp2 := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/login",
		map[string]any{
			"password": testPassword,
		},
		"",
	)
	require.Equal(t,
		http.StatusBadRequest, resp2.StatusCode,
		"login without email must return 400",
	)
	resp2.Body.Close()
}

// TestForgotPasswordPendingUser verifies that forgot-password
// for a pending (unconfirmed) user returns 204, never leaking
// that the email exists in pending state.
func TestForgotPasswordPendingUser(t *testing.T) {
	clearMailbox(t)

	_, token, _ := createAnonymousUser(t)
	email := uniqueEmail()

	// Register but do NOT confirm — user stays pending
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

	// Forgot password for pending email
	forgotResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/forgot-password",
		map[string]any{"email": email},
		"",
	)
	require.Equal(t,
		http.StatusNoContent, forgotResp.StatusCode,
		"forgot-password for pending user must return 204",
	)
	forgotResp.Body.Close()
}

// TestRefreshWithEmptyString verifies that POST /auth/refresh
// with an empty refresh_token returns 400 or 401, not 500.
func TestRefreshWithEmptyString(t *testing.T) {
	resp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/refresh",
		map[string]any{
			"refresh_token": "",
		},
		"",
	)

	status := resp.StatusCode
	resp.Body.Close()

	require.True(t,
		status == http.StatusBadRequest ||
			status == http.StatusUnauthorized,
		"empty refresh token must return 400 or 401 "+
			"(got %d)", status,
	)
}

// TestResendVerificationInvalidatesOldCode verifies that
// resending a verification code invalidates the previous
// code. Code A (before resend) must fail; code B (after
// resend) must succeed.
func TestResendVerificationInvalidatesOldCode(t *testing.T) {
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

	// Get code A
	msgIDA := waitForEmail(t, email)
	codeA := extractVerificationCode(t, msgIDA)

	// Wait for resend cooldown (1s in test env + margin)
	time.Sleep(2 * time.Second)

	// Resend
	clearMailbox(t)

	resendResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/resend-verification",
		map[string]any{},
		token,
	)
	require.Equal(t,
		http.StatusNoContent, resendResp.StatusCode,
		"resend must return 204",
	)
	resendResp.Body.Close()

	// Get code B
	msgIDB := waitForEmail(t, email)
	codeB := extractVerificationCode(t, msgIDB)

	// Code A must fail
	t.Log("Step 1: Old code A must fail")

	confirmA := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/confirm-registration",
		map[string]any{"code": codeA},
		token,
	)

	statusA := confirmA.StatusCode
	confirmA.Body.Close()

	require.True(t,
		statusA == http.StatusBadRequest ||
			statusA == http.StatusUnauthorized,
		"old code A must be rejected (got %d)", statusA,
	)

	// Code B must succeed
	t.Log("Step 2: New code B must succeed")

	confirmB := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/confirm-registration",
		map[string]any{"code": codeB},
		token,
	)
	require.Equal(t,
		http.StatusOK, confirmB.StatusCode,
		"new code B must succeed",
	)
	confirmB.Body.Close()
}

// TestConfirmRegistrationSessionRotation verifies that the
// refresh token from confirm-registration supports rotation
// and that reuse detection revokes the entire session family
// (OAuth 2.0 Security BCP).
func TestConfirmRegistrationSessionRotation(t *testing.T) {
	clearMailbox(t)

	_, anonToken, _ := createAnonymousUser(t)
	email := uniqueEmail()
	_, _, confirmRefresh := registerAndConfirm(
		t, anonToken, email,
	)
	require.NotEmpty(t, confirmRefresh,
		"confirm-registration must return refresh_token",
	)

	// Refresh with the confirm-registration token
	t.Log("Step 1: Refresh with confirm token")

	refreshResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/refresh",
		map[string]any{
			"refresh_token": confirmRefresh,
		},
		"",
	)
	require.Equal(t,
		http.StatusOK, refreshResp.StatusCode,
		"refresh with confirm token must succeed",
	)

	refreshBody := decodeJSON(t, refreshResp)

	newRefresh, _ := refreshBody["refresh_token"].(string)
	require.NotEmpty(t, newRefresh)
	require.NotEqual(t, confirmRefresh, newRefresh,
		"refresh must return a rotated token",
	)

	// Wait for rotation grace window
	time.Sleep(2 * time.Second)

	// Old confirm token must be dead — reuse detected,
	// entire session family revoked (OAuth 2.0 Security BCP).
	t.Log("Step 2: Replay old token → reuse detection")

	replayResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/refresh",
		map[string]any{
			"refresh_token": confirmRefresh,
		},
		"",
	)
	require.Equal(t,
		http.StatusUnauthorized,
		replayResp.StatusCode,
		"replayed confirm token must return 401",
	)
	replayResp.Body.Close()

	// New token also dead — session family revoked
	t.Log("Step 3: New token also dead (family revoked)")

	newResp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/auth/refresh",
		map[string]any{
			"refresh_token": newRefresh,
		},
		"",
	)
	require.Equal(t,
		http.StatusUnauthorized,
		newResp.StatusCode,
		"rotated token must also return 401 "+
			"(session family revoked)",
	)
	newResp.Body.Close()
}

// TestTamperedJWT verifies that modifying a JWT payload
// without re-signing causes 401. Takes a valid token,
// base64-decodes the payload, changes user_id, re-encodes,
// and sends it. The signature check must reject it.
func TestTamperedJWT(t *testing.T) {
	_, token, _ := createAnonymousUser(t)

	// JWT format: header.payload.signature
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3,
		"JWT must have 3 parts",
	)

	// Decode payload
	payloadBytes, err := base64.RawURLEncoding.DecodeString(
		parts[1],
	)
	require.NoError(t, err, "failed to decode JWT payload")

	var payload map[string]any

	err = json.Unmarshal(payloadBytes, &payload)
	require.NoError(t, err, "failed to parse JWT payload")

	// Tamper: change the subject (user ID)
	payload["sub"] = "00000000-0000-0000-0000-000000000000"

	tamperedPayload, err := json.Marshal(payload)
	require.NoError(t, err)

	parts[1] = base64.RawURLEncoding.EncodeToString(
		tamperedPayload,
	)

	tamperedToken := strings.Join(parts, ".")

	// Use tampered token
	resp := doRequest(
		t, http.MethodPost,
		apiURL+"/api/v1/routes/prepare",
		map[string]any{},
		tamperedToken,
	)

	status := resp.StatusCode
	resp.Body.Close()

	require.Equal(t,
		http.StatusUnauthorized, status,
		"tampered JWT must return 401",
	)
}
