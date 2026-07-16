//go:build integration

package integration_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRouteAccess_EventRecorded_HourlyStats(
	t *testing.T,
) {
	ownerToken, routeID := createAndPublishRoute(t)
	t.Cleanup(func() { deleteRoute(t, routeID, ownerToken) })

	navigatorID, navigatorToken, _ := createAnonymousUser(t)
	t.Cleanup(func() {
		deleteUser(t, navigatorID, navigatorToken)
	})

	accessRoute(
		t, routeID, navigatorToken,
		map[string]string{"src": "qr"},
	)

	waitForAccessCount(
		t, routeID, ownerToken, 1, 10*time.Second,
	)

	resp, stats := getRouteHourlyStats(
		t, routeID, ownerToken, "1",
	)

	require.Equal(t, http.StatusOK, resp.StatusCode)

	totalAccess := 0
	for _, p := range stats.Points {
		totalAccess += p.AccessCount
	}
	assert.GreaterOrEqual(t, totalAccess, 1)
}

func TestRouteAccess_SourceAttribution(t *testing.T) {
	ownerToken, routeID := createAndPublishRoute(t)
	t.Cleanup(func() { deleteRoute(t, routeID, ownerToken) })

	navigatorID, navigatorToken, _ := createAnonymousUser(t)
	t.Cleanup(func() {
		deleteUser(t, navigatorID, navigatorToken)
	})

	accessRoute(
		t, routeID, navigatorToken,
		map[string]string{"src": "wa"},
	)
	accessRoute(
		t, routeID, navigatorToken,
		map[string]string{"src": "qr"},
	)
	accessRoute(
		t, routeID, navigatorToken,
		map[string]string{"src": "qr"},
	)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, summary := getRouteSummary(
			t, routeID, ownerToken, "", "",
		)
		if resp.StatusCode != http.StatusOK {
			time.Sleep(200 * time.Millisecond)
			continue
		}

		sourceMap := make(map[string]int)
		for _, s := range summary.Sources {
			sourceMap[s.Source] = s.Count
		}

		if sourceMap["qr"] >= 2 && sourceMap["wa"] >= 1 {
			return
		}

		time.Sleep(200 * time.Millisecond)
	}

	t.Fatal(
		"source attribution did not reach expected " +
			"counts within 10s",
	)
}

func TestRouteAccess_AccessModeBreakdown(t *testing.T) {
	ownerToken, routeID := createAndPublishRoute(t)
	t.Cleanup(func() { deleteRoute(t, routeID, ownerToken) })

	navigatorID, navigatorToken, _ := createAnonymousUser(t)
	t.Cleanup(func() {
		deleteUser(t, navigatorID, navigatorToken)
	})

	accessRoute(
		t, routeID, navigatorToken,
		map[string]string{"include_images": "true"},
	)
	accessRoute(
		t, routeID, navigatorToken,
		nil,
	)

	waitForAccessCount(
		t, routeID, ownerToken, 2, 10*time.Second,
	)

	resp, summary := getRouteSummary(
		t, routeID, ownerToken, "", "",
	)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	modeMap := make(map[string]int)
	for _, m := range summary.AccessModes {
		modeMap[m.Mode] = m.Count
	}

	assert.GreaterOrEqual(
		t, modeMap["download"], 1,
		"include_images=true must count as download",
	)
	assert.GreaterOrEqual(
		t, modeMap["view"], 1,
		"access without include_images must count as view",
	)
}

func TestRouteSave_EventRecorded(t *testing.T) {
	ownerToken, routeID := createAndPublishRoute(t)
	t.Cleanup(func() { deleteRoute(t, routeID, ownerToken) })

	saverID, saverToken, _ := createAnonymousUser(t)
	t.Cleanup(func() {
		deleteUser(t, saverID, saverToken)
	})

	status := saveRoute(t, routeID, saverToken)
	require.Equal(t, http.StatusOK, status)

	waitForSaveCount(
		t, routeID, ownerToken, 1, 10*time.Second,
	)
}

func TestAnalytics_AccountDeletion_ErasesAnalytics(
	t *testing.T,
) {
	ownerToken, routeID := createAndPublishRoute(t)
	t.Cleanup(func() { deleteRoute(t, routeID, ownerToken) })

	clearMailbox(t)

	_, navigatorAnonToken, _ := createAnonymousUser(t)
	email := uniqueEmail()
	_, navigatorToken, _ := registerAndConfirm(
		t, navigatorAnonToken, email,
	)

	accessRoute(
		t, routeID, navigatorToken,
		map[string]string{"src": "qr"},
	)
	waitForAccessCount(
		t, routeID, ownerToken, 1, 10*time.Second,
	)

	session := newPlausibleSession(2)
	s := recordNavigationSession(
		t, routeID, navigatorToken, session,
	)
	require.Equal(t, http.StatusNoContent, s)

	summary := waitForNavigationCount(
		t, routeID, ownerToken, 1, 15*time.Second,
	)
	visitorsBefore := summary.UniqueVisitors
	require.GreaterOrEqual(
		t, visitorsBefore, 2,
		"pre-deletion: must have at least 2 unique visitors "+
			"(owner + navigator)",
	)

	clearMailbox(t)

	reqResp := requestAccountDeletion(t, navigatorToken)
	require.Equal(
		t, http.StatusNoContent, reqResp.StatusCode,
	)
	reqResp.Body.Close()

	msgID := waitForEmail(t, email)
	code := extractVerificationCode(t, msgID)

	confirmResp := confirmAccountDeletion(
		t, navigatorToken, code,
	)
	require.Equal(
		t, http.StatusNoContent, confirmResp.StatusCode,
	)
	confirmResp.Body.Close()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)

		resp, after := getRouteSummary(
			t, routeID, ownerToken, "", "",
		)
		if resp.StatusCode != http.StatusOK {
			continue
		}

		if after.UniqueVisitors < visitorsBefore {
			return
		}
	}

	t.Fatalf(
		"unique_visitors did not decrease from %d within "+
			"15s after account deletion erasure",
		visitorsBefore,
	)
}

func TestAnalytics_NavigatorFirstAttribution(t *testing.T) {
	ownerAToken, routeA := createAndPublishRoute(t)
	t.Cleanup(func() { deleteRoute(t, routeA, ownerAToken) })

	navigatorID, navigatorToken, _ := createAnonymousUser(t)
	t.Cleanup(func() {
		deleteUser(t, navigatorID, navigatorToken)
	})

	accessRoute(
		t, routeA, navigatorToken,
		map[string]string{"src": "qr"},
	)
	waitForAccessCount(
		t, routeA, ownerAToken, 1, 10*time.Second,
	)

	routeB := prepareRoute(t, navigatorToken)
	cwResp := createRouteWithWaypoints(
		t, navigatorToken, routeB, defaultTestImages,
	)

	for i, entry := range cwResp.PresignedURLs {
		imgBytes := loadTestImage(
			t, defaultTestImages[i].Filename,
		)
		resp := uploadToGateway(
			t, entry.UploadURL, entry.UploadToken, imgBytes,
		)
		resp.Body.Close()
	}

	waitForRouteReady(
		t, routeB, navigatorToken, 60*time.Second,
	)
	publishRoute(t, routeB, navigatorToken)
	t.Cleanup(func() {
		deleteRoute(t, routeB, navigatorToken)
	})

	time.Sleep(5 * time.Second)

	adminTok := adminToken(t)
	kgResp, kg := getPlatformKillGates(
		t, adminTok, "", "",
	)
	require.Equal(t, http.StatusOK, kgResp.StatusCode)

	assert.GreaterOrEqual(
		t,
		kg.Metrics.RoutesCreatedNavigatorFirst, 1,
		"navigator who accessed another route then created "+
			"their own must be counted as navigator-first",
	)
}

func TestAnalytics_E2E_FullLifecycle(t *testing.T) {
	ownerToken, routeID := createAndPublishRoute(t)
	t.Cleanup(func() { deleteRoute(t, routeID, ownerToken) })

	nav1ID, nav1Token, _ := createAnonymousUser(t)
	t.Cleanup(func() { deleteUser(t, nav1ID, nav1Token) })

	nav2ID, nav2Token, _ := createAnonymousUser(t)
	t.Cleanup(func() { deleteUser(t, nav2ID, nav2Token) })

	accessRoute(
		t, routeID, nav1Token,
		map[string]string{"src": "qr"},
	)
	accessRoute(
		t, routeID, nav2Token,
		map[string]string{
			"src":            "wa",
			"include_images": "true",
		},
	)
	accessRoute(
		t, routeID, nav1Token,
		map[string]string{"src": "qr"},
	)

	waitForAccessCount(
		t, routeID, ownerToken, 3, 10*time.Second,
	)

	completed1 := newPlausibleSession(2)
	s1 := recordNavigationSession(
		t, routeID, nav1Token, completed1,
	)
	require.Equal(t, http.StatusNoContent, s1)

	completed2 := newPlausibleSession(2)
	s2 := recordNavigationSession(
		t, routeID, nav2Token, completed2,
	)
	require.Equal(t, http.StatusNoContent, s2)

	abandoned := newAbandonedSession(1, 2)
	s3 := recordNavigationSession(
		t, routeID, nav1Token, abandoned,
	)
	require.Equal(t, http.StatusNoContent, s3)

	summary := waitForNavigationCount(
		t, routeID, ownerToken, 3, 10*time.Second,
	)

	assert.GreaterOrEqual(
		t, summary.AccessCount, 3,
		"must have at least 3 accesses",
	)
	assert.Equal(
		t, 2, summary.NavigationsCompleted,
		"must have 2 completed navigations",
	)
	assert.Equal(
		t, 1, summary.NavigationsAbandoned,
		"must have 1 abandoned navigation",
	)

	require.NotNil(t, summary.CompletionRate)
	expectedRate := float64(2) / float64(3)
	assert.InDelta(
		t, expectedRate, *summary.CompletionRate, 0.01,
		"completion_rate must be 2/3",
	)

	require.NotNil(
		t, summary.AvgDurationSeconds,
		"avg_duration_seconds must be present",
	)

	sourceMap := make(map[string]int)
	for _, s := range summary.Sources {
		sourceMap[s.Source] = s.Count
	}
	assert.GreaterOrEqual(t, sourceMap["qr"], 2)
	assert.GreaterOrEqual(t, sourceMap["wa"], 1)

	resp, hourly := getRouteHourlyStats(
		t, routeID, ownerToken, "1",
	)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	totalHourlyAccess := 0
	for _, p := range hourly.Points {
		totalHourlyAccess += p.AccessCount
	}
	assert.GreaterOrEqual(
		t, totalHourlyAccess, 3,
		"hourly stats must reflect all accesses",
	)

	adminTok := adminToken(t)

	kgResp, kg := getPlatformKillGates(
		t, adminTok, "", "",
	)
	require.Equal(t, http.StatusOK, kgResp.StatusCode)

	assert.GreaterOrEqual(
		t,
		kg.Metrics.NavigationsCompleted+
			kg.Metrics.NavigationsAbandoned,
		3,
		"platform kill-gates must include the test navigations",
	)

	pdResp, pd := getPlatformDailyStats(
		t, adminTok, "", "",
	)
	require.Equal(t, http.StatusOK, pdResp.StatusCode)
	assert.NotEmpty(t, pd.Points)

	ownerResp, ownerSummaries := getOwnerRouteSummaries(
		t, ownerToken, "", "",
	)
	require.Equal(t, http.StatusOK, ownerResp.StatusCode)

	found := false
	for _, r := range ownerSummaries.Routes {
		if r.RouteID == routeID {
			found = true
			assert.GreaterOrEqual(t, r.AccessCount, 3)
			assert.Equal(t, 2, r.NavigationsCompleted)
			assert.Equal(t, 1, r.NavigationsAbandoned)
		}
	}
	assert.True(
		t, found,
		"owner route summaries must include the test route",
	)
}
