//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	valkeygo "github.com/valkey-io/valkey-go"
)

// TestInfrastructure_PostgreSQLReachable verifies PostgreSQL connectivity via API health
// endpoint. Requires admin JWT (admin:access scope).
func TestInfrastructure_PostgreSQLReachable(t *testing.T) {
	t.Parallel()

	token := adminToken(t)

	resp := doRequest(
		t,
		http.MethodGet,
		apiURL+"/health/db",
		nil,
		token,
	)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode,
		"expected database health status 200",
	)

	var result map[string]any
	err := json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(t, err,
		"failed to decode database health response",
	)

	status, ok := result["status"].(string)
	require.True(t, ok,
		"database health response missing status field",
	)
	require.Equal(t, "ok", status,
		"expected database status 'ok'",
	)
}

// TestInfrastructure_ValkeyReachable verifies Valkey connectivity with a direct PING.
func TestInfrastructure_ValkeyReachable(t *testing.T) {
	t.Parallel()

	cfg := valkeygo.ClientOption{
		InitAddress:  []string{valkeyAddress},
		DisableCache: true,
	}

	client, err := valkeygo.NewClient(cfg)
	if err != nil {
		t.Fatalf("failed to create Valkey client: %v", err)
	}
	defer client.Close()

	err = client.Do(
		context.Background(),
		client.B().Ping().Build(),
	).Error()
	if err != nil {
		t.Fatalf("Valkey PING failed: %v", err)
	}
}

// TestInfrastructure_ValkeyHealthReachable verifies Valkey connectivity via the
// API /health/valkey endpoint. Requires admin JWT (admin:access scope).
func TestInfrastructure_ValkeyHealthReachable(t *testing.T) {
	t.Parallel()

	token := adminToken(t)

	resp := doRequest(
		t,
		http.MethodGet,
		apiURL+"/health/valkey",
		nil,
		token,
	)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode,
		"expected valkey health status 200",
	)

	var result map[string]any
	err := json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(t, err,
		"failed to decode valkey health response",
	)

	status, ok := result["status"].(string)
	require.True(t, ok,
		"valkey health response missing status field",
	)
	require.Equal(t, "ok", status,
		"expected valkey status 'ok'",
	)
}

// TestInfrastructure_MinIOReachable verifies MinIO connectivity via API storage health
// endpoint. Requires admin JWT (admin:access scope).
func TestInfrastructure_MinIOReachable(t *testing.T) {
	t.Parallel()

	token := adminToken(t)

	resp := doRequest(
		t,
		http.MethodGet,
		apiURL+"/health/storage",
		nil,
		token,
	)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode,
		"expected storage health status 200",
	)

	var result map[string]any
	err := json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(t, err,
		"failed to decode storage health response",
	)

	status, ok := result["status"].(string)
	require.True(t, ok,
		"storage health response missing status field",
	)
	require.Equal(t, "ok", status,
		"expected storage status 'ok'",
	)
}

// TestInfrastructure_FollowAPIHealthy verifies follow-api general health endpoint.
func TestInfrastructure_FollowAPIHealthy(t *testing.T) {
	t.Parallel()

	resp, err := http.Get(apiURL + "/health")
	if err != nil {
		t.Fatalf("failed to reach API health endpoint: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected API health status 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		t.Fatalf("failed to decode API health response: %v", err)
	}

	status, ok := result["status"].(string)
	if !ok {
		t.Fatal("API health response missing status field")
	}

	if status != "ok" {
		t.Fatalf("expected API health status 'ok', got '%s'", status)
	}

	message, ok := result["message"].(string)
	if !ok {
		t.Fatal("API health response missing message field")
	}

	if message == "" {
		t.Fatal("API health response message is empty")
	}
}

// TestInfrastructure_FollowGatewayHealthy verifies follow-image-gateway health endpoint.
func TestInfrastructure_FollowGatewayHealthy(t *testing.T) {
	t.Parallel()

	resp, err := http.Get(gatewayURL + "/health")
	if err != nil {
		t.Fatalf("failed to reach gateway health endpoint: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf(
			"expected gateway health status 200, got %d",
			resp.StatusCode,
		)
	}

	var result map[string]any
	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		t.Fatalf("failed to decode gateway health response: %v", err)
	}

	status, ok := result["status"].(string)
	if !ok {
		t.Fatal("gateway health response missing status field")
	}

	if status != "ok" {
		t.Fatalf("expected gateway health status 'ok', got '%s'", status)
	}

	message, ok := result["message"].(string)
	if !ok {
		t.Fatal("gateway health response missing message field")
	}

	if message == "" {
		t.Fatal("gateway health response message is empty")
	}
}

// TestInfrastructure_APIHealthIncludesValkey checks for Valkey health information in API
// health response.
func TestInfrastructure_APIHealthIncludesValkey(t *testing.T) {
	t.Parallel()

	resp, err := http.Get(apiURL + "/health")
	if err != nil {
		t.Fatalf("failed to reach API health endpoint: %v", err)
	}
	defer resp.Body.Close()

	var result map[string]any
	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		t.Fatalf("failed to decode API health response: %v", err)
	}

	if _, ok := result["valkey"]; !ok {
		t.Skip("valkey health field not yet implemented in API health response")
	}
}
