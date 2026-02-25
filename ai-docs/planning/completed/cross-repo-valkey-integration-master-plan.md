# Cross-Repo Valkey Integration Master Plan

**Date:** 2026-02-15
**Status:** Active
**Scope:** Wire Valkey messaging across follow-pkg, follow-api, and follow-image-gateway

---

## Completed Prerequisites

**Relative Marker Coordinates Migration** - COMPLETED 2026-02-16

The marker coordinates migration from absolute pixels to normalized floats (0.0-1.0) has been completed. This is a prerequisite for Valkey integration because:
- Markers are now scale-invariant (no need to recalculate them when image gateway resizes images)
- API database schema uses DOUBLE PRECISION for marker_x and marker_y columns
- All APIs accept/return normalized float64 coordinates
- Flutter app uses double values directly

**Impact on This Plan:**
- No need for marker auto-scaling logic in ProcessImageResultUseCase
- Waypoint status transitions remain unchanged (markers are stored as-is)
- Image dimension processing in gateway pipeline does NOT affect stored marker coordinates

---

## Mission

Wire Valkey (Redis-compatible) messaging across the Follow platform to enable asynchronous inter-service communication between follow-api and follow-image-gateway. This connects the pieces that are already built (gateway pipeline, API domains) through a shared Valkey wrapper.

## Valkey Key Patterns (Source of Truth)

| Key Pattern | Type | Writer | Reader | Purpose |
|-------------|------|--------|--------|---------|
| `image:upload:{image_id}` | String (NX + TTL 1h) | Gateway | Gateway | One-time upload guard (duplicate prevention) |
| `image:status:{image_id}` | Hash (TTL 1h) | API (queued) + Gateway (stages) | API (SSE/polling) | Real-time progress tracking |
| `image:result` | Stream (consumer group) | Gateway | API (`api-workers` group) | Processing results delivery |

## Repo-Specific Plans

| Repo | Plan Location | Tasks | Story Points |
|------|---------------|-------|--------------|
| **follow-pkg** | `follow-pkg/ai-docs/planning/active/valkey-wrapper-implementation-plan.md` | 12 | 27 |
| **follow-api** | `follow-api/ai-docs/planning/active/valkey-integration-plan.md` | 15 | 45 |
| **follow-image-gateway** | `follow-image-gateway/ai-docs/planning/active/valkey-integration-plan.md` | 12 | 30 |
| **Total** | | **39** | **102** |

## Execution Order and Dependencies

```
Phase A: follow-pkg (foundation)         [27 pts]
    |
    | BLOCKS
    v
Phase B: follow-api + follow-image-gateway  [45 + 30 = 75 pts]
    (in parallel)
    |
    | BLOCKS
    v
Phase C: Cross-repo integration testing   [from repo plans]
    |
    v
Phase D: Docker Compose + cleanup         [this plan]
```

### Phase A: follow-pkg Valkey Wrapper (MUST complete first)

Implements the shared `Client` interface, `ValkeyClient` (wrapping valkey-go), `FakeValkeyClient`, and all higher-level components (Consumer, Producer, ProgressTracker, UploadGuard, Reclaimer, HealthChecker).

**Key deliverables:**
- `Client` interface with all Valkey operations (Streams, Hashes, Strings, Groups)
- `ValkeyClient` production implementation wrapping `valkey-io/valkey-go`
- `FakeValkeyClient` test double for unit testing in consuming services
- `Consumer` -- generic XREADGROUP loop with MessageHandler callback
- `Producer` -- XADD with optional MAXLEN trimming
- `ProgressTracker` -- Hash ops with automatic TTL for `image:status:{id}`
- `UploadGuard` -- SET NX EX for `image:upload:{id}`
- `Reclaimer` -- XAUTOCLAIM-based orphan recovery
- `HealthChecker` -- Ping + StreamGroupExists

**Completion signal:** All 12 tasks pass quality gates, `go test -race -cover ./...` passes.

### Phase B: Service Integration (parallel after Phase A)

#### B1: follow-api (45 story points)

Consumes the follow-pkg wrapper to:
- Initialize ValkeyClient in App lifecycle
- Run background consumer goroutine on `image:result` stream
- Implement `ProcessImageResultUseCase` (DB transaction: update Image + Waypoint + Route)
- Confirm waypoint status when image processing succeeds (marker coordinates are scale-invariant -- no rescaling needed)
- Auto-transition routes PENDING -> READY when all images processed
- Add SSE endpoint (`GET /images/status/stream`) for real-time progress
- Add polling endpoint (`GET /images/{id}/status`) as fallback
- Implement Ed25519 JWT signing for gateway upload tokens
- Update `CreateRouteWithWaypoints` to generate gateway upload URLs
- Implement `PublishRouteUseCase` (replaces ConfirmRouteWaypoints)
- Add background job for expiring stale PENDING images
- Database migrations: Image dimension columns, route status enum updates

#### B2: follow-image-gateway (30 story points)

Consumes the follow-pkg wrapper to:
- Initialize ValkeyClient in App lifecycle (step table)
- Add upload guard (SET NX) to upload handler after JWT validation
- Inject progress tracking into pipeline stage boundaries (fire-and-forget)
- Implement ResultPublisher: listen to pipeline output, publish to `image:result`
- Update health checker with Valkey ping
- Add Valkey to per-service Docker Compose
- Graceful degradation: pipeline continues if Valkey is unavailable

### Phase C: Cross-Repo Integration Testing

**Location:** `/home/yoseforb/pkg/follow/tests/integration/` (coordination repo)

**Purpose:** Validate the complete end-to-end Valkey messaging flow between follow-api and follow-image-gateway. These tests exercise the full stack (PostgreSQL + Valkey + MinIO + follow-api + follow-image-gateway) to verify that asynchronous inter-service communication works correctly.

**Pattern:** Dual-mode infrastructure adapted from follow-image-gateway's integration tests (same testcontainers-go/compose pattern).

#### Test Infrastructure

**Dual-Mode Support:**

The integration tests run in two modes selected via `INTEGRATION_TEST_MODE` environment variable:

| Mode | Default | Description | Use Case |
|------|---------|-------------|----------|
| **docker** | Yes | Uses `testcontainers-go/modules/compose` to start the FULL stack from parent `docker-compose.yml`. All services run as containers. | CI/CD pipelines, reproducible isolated testing |
| **local** | No | Connects to already-running services. Developer manually starts `docker compose up` or runs services individually. Configurable via env vars. | Local development, debugging, faster iteration |

**Mode Selection:**

```bash
# Docker mode (default, CI/CD)
go test -tags=integration -v ./tests/integration/

# Explicit docker mode
INTEGRATION_TEST_MODE=docker go test -tags=integration -v ./tests/integration/

# Local mode (developer workflow)
INTEGRATION_TEST_MODE=local \
  API_URL=http://localhost:18080 \
  GATEWAY_URL=http://localhost:18090 \
  VALKEY_ADDRESS=localhost:16379 \
  go test -tags=integration -v ./tests/integration/
```

**Docker Mode Configuration:**

Uses `testcontainers-go/modules/compose` to reference the parent `docker-compose.yml` which contains ALL services (PostgreSQL, Valkey, MinIO, follow-api, follow-image-gateway). Services use non-conflicting ports:

| Service | Docker Port | Purpose |
|---------|-------------|---------|
| PostgreSQL | 15432 | Database |
| Valkey | 16379 | Messaging |
| MinIO | 19000 (API), 19001 (Console) | Object storage |
| follow-api | 18080 | API server |
| follow-image-gateway | 18090 | Image processing |

**Local Mode Configuration:**

Environment variables allow connecting to existing services:

| Variable | Default | Description |
|----------|---------|-------------|
| `API_URL` | http://localhost:18080 | follow-api base URL |
| `GATEWAY_URL` | http://localhost:18090 | follow-image-gateway base URL |
| `VALKEY_ADDRESS` | localhost:16379 | Valkey server address |
| `POSTGRES_DSN` | (constructed) | PostgreSQL connection string |

#### File Structure

```
/home/yoseforb/pkg/follow/tests/integration/
├── go.mod                      # Separate Go module for integration tests
├── go.sum
├── main_test.go                # TestMain, dual-mode setup/teardown, shared helpers
├── valkey_messaging_test.go    # 7 cross-repo Valkey integration tests
├── helpers_test.go             # Shared test helpers (JWT, HTTP, Valkey, image gen)
└── README.md                   # Documentation and running instructions
```

**Build Tag:** All test files use `//go:build integration` to exclude them from regular `go test ./...` runs.

**Dependencies (go.mod):**

The integration test module is separate from service repos and only needs:
- HTTP client (stdlib `net/http`)
- Valkey client (`valkey-io/valkey-go`) for direct Valkey inspection
- JWT signing (`crypto/ed25519`, `github.com/golang-jwt/jwt/v5`) for generating upload tokens
- testcontainers-go (`github.com/testcontainers/testcontainers-go/modules/compose`)
- Testing utilities (`github.com/stretchr/testify/assert`, `github.com/stretchr/testify/require`)

**Note:** These are E2E tests that test via HTTP and Valkey protocols. They do NOT import follow-api or follow-image-gateway service code directly.

#### main_test.go Structure

Adapts the gateway's dual-mode pattern from `follow-image-gateway/tests/integration/main_test.go`:

```go
//go:build integration

package integration

import (
    "context"
    "flag"
    "log/slog"
    "os"
    "testing"
    "time"

    "github.com/testcontainers/testcontainers-go/modules/compose"
)

var (
    apiURL         string                // follow-api base URL
    gatewayURL     string                // follow-image-gateway base URL
    valkeyAddress  string                // Valkey server address
    postgresDSN    string                // PostgreSQL connection string
    testPrivateKey ed25519.PrivateKey    // Ed25519 signing key
    testPublicKey  ed25519.PublicKey     // Ed25519 verification key
    composeStack   compose.ComposeStack  // Docker compose stack (docker mode only)
)

func TestMain(m *testing.M) {
    mode := os.Getenv("INTEGRATION_TEST_MODE")
    if mode == "" {
        mode = "docker"
    }
    slog.Info("integration test mode", "mode", mode)

    exitCode := 1
    switch mode {
    case "docker":
        setupDocker()
        exitCode = m.Run()
        teardownDocker()
    case "local":
        setupLocal()
        exitCode = m.Run()
    default:
        slog.Error("unknown mode", "mode", mode)
    }

    os.Exit(exitCode)
}

func setupDocker() {
    loadEnvConfig()  // Load Ed25519 keypair from /etc/follow-image-gateway

    // Navigate to parent directory for docker-compose.yml
    projectRoot, _ := filepath.Abs(filepath.Join("..", ".."))
    composeFilePath := filepath.Join(projectRoot, "docker-compose.yml")

    stack, _ := compose.NewDockerCompose(composeFilePath)
    composeStack = stack.WithEnv(map[string]string{
        "API_PORT":       "18080",
        "GATEWAY_PORT":   "18090",
        "VALKEY_PORT":    "16379",
        "POSTGRES_PORT":  "15432",
        "MINIO_PORT":     "19000",
        // ... other env vars for container names, network prefix
    })

    _ = composeStack.Up(context.Background())

    // Set service URLs for tests
    apiURL = "http://localhost:18080"
    gatewayURL = "http://localhost:18090"
    valkeyAddress = "localhost:16379"
    postgresDSN = "postgres://follow:password@localhost:15432/follow?sslmode=disable"

    // Wait for all services to be healthy (poll health endpoints)
    waitForService(apiURL + "/health")
    waitForService(gatewayURL + "/health")
    waitForValkey(valkeyAddress)
}

func setupLocal() {
    loadEnvConfig()

    // Read from environment or use defaults
    apiURL = getEnvOrDefault("API_URL", "http://localhost:18080")
    gatewayURL = getEnvOrDefault("GATEWAY_URL", "http://localhost:18090")
    valkeyAddress = getEnvOrDefault("VALKEY_ADDRESS", "localhost:16379")
    postgresDSN = getEnvOrDefault("POSTGRES_DSN",
        "postgres://follow:password@localhost:15432/follow?sslmode=disable")

    // Verify services are running
    checkServiceHealth(apiURL + "/health")
    checkServiceHealth(gatewayURL + "/health")
    checkValkey(valkeyAddress)
}

func teardownDocker() {
    if composeStack != nil {
        _ = composeStack.Down(context.Background())
    }
}

func loadEnvConfig() {
    // Load Ed25519 keypair from /etc/follow-image-gateway
    // (Same pattern as gateway's integration tests)
    env := LoadEnvFile("/etc/follow-image-gateway")
    privateKeyStr := env["FOLLOW_API_ED25519_PRIVATE_KEY"]
    testPrivateKey, _ = auth.ParseEd25519PrivateKey(privateKeyStr)
    testPublicKey = testPrivateKey.Public().(ed25519.PublicKey)
}
```

#### helpers_test.go Structure

Shared test helpers used by all test cases:

```go
//go:build integration

package integration

// JWT Token Generation
func createUploadToken(t *testing.T, imageID, storageKey string, maxSize int64) string {
    // Sign Ed25519 JWT token with test private key
    // Claims: image_id, storage_key, content_type, max_file_size, exp
}

// HTTP Request Helpers
func uploadImageToGateway(t *testing.T, token string, imageBytes []byte) (*http.Response, error) {
    // PUT {gatewayURL}/upload?token={token} with raw binary body
}

func createRouteWithWaypoints(t *testing.T, apiURL string, waypoints []WaypointInput) CreateRouteResponse {
    // POST {apiURL}/routes/{id}/create-waypoints
}

func publishRoute(t *testing.T, apiURL string, routeID string) (*http.Response, error) {
    // POST {apiURL}/routes/{id}/publish
}

// Valkey Inspection Helpers
func readValkeyHash(t *testing.T, key string) map[string]string {
    // HGETALL {key} - read image:status:{id} hash
}

func readValkeyStream(t *testing.T, streamKey, groupName, consumerName string) []StreamMessage {
    // XREADGROUP GROUP {group} {consumer} STREAMS {stream} >
}

func checkUploadGuard(t *testing.T, imageID string) bool {
    // GET image:upload:{imageID} - check if upload guard exists
}

// Image Generation
func smallJPEG() []byte {
    // Minimal valid 1x1 JPEG (same pattern as gateway integration tests)
}

func invalidImage() []byte {
    // Non-image bytes (e.g., random data or script)
}

// Wait-for-Condition Helpers
func waitForRouteStatus(t *testing.T, routeID string, expectedStatus string, timeout time.Duration) {
    // Poll GET /routes/{id} until status matches expected
}

func waitForImageStatus(t *testing.T, imageID string, expectedStatus string, timeout time.Duration) {
    // Poll GET /images/{id}/status until status matches expected
}

// Database Inspection (optional)
func queryImageFromDB(t *testing.T, imageID string) ImageRecord {
    // SELECT * FROM image.images WHERE id = $1
}

func queryWaypointFromDB(t *testing.T, waypointID string) WaypointRecord {
    // SELECT * FROM route.waypoints WHERE id = $1
}
```

#### Test Cases (valkey_messaging_test.go)

**Test 1: Full Upload-Process-Result Flow**

Validates the complete end-to-end flow from route creation to route ready state.

```go
func TestFullUploadProcessResultFlow(t *testing.T) {
    // 1. API: Create route with 3 waypoints
    response := createRouteWithWaypoints(t, apiURL, []WaypointInput{
        {ContentType: "image/jpeg", FileSize: 2048, MarkerX: 0.10, MarkerY: 0.20},
        {ContentType: "image/jpeg", FileSize: 2048, MarkerX: 0.15, MarkerY: 0.25},
        {ContentType: "image/jpeg", FileSize: 2048, MarkerX: 0.20, MarkerY: 0.30},
    })
    assert.Len(t, response.UploadURLs, 3)

    // 2. Verify initial status in Valkey (API writes "queued")
    for _, url := range response.UploadURLs {
        status := readValkeyHash(t, "image:status:"+url.ImageID)
        assert.Equal(t, "queued", status["stage"])
    }

    // 3. Client: Upload images to gateway
    for _, url := range response.UploadURLs {
        resp, err := uploadImageToGateway(t, url.Token, smallJPEG())
        require.NoError(t, err)
        assert.Equal(t, http.StatusAccepted, resp.StatusCode)
    }

    // 4. Wait for gateway processing (watch Valkey status transitions)
    for _, url := range response.UploadURLs {
        waitForImageStatus(t, url.ImageID, "done", 30*time.Second)
    }

    // 5. Verify results published to image:result stream
    messages := readValkeyStream(t, "image:result", "api-workers", "test-consumer")
    assert.GreaterOrEqual(t, len(messages), 3)

    // 6. Wait for API to consume results and update DB
    waitForRouteStatus(t, response.RouteID, "ready", 10*time.Second)

    // 7. Verify Image entities updated in DB
    for _, url := range response.UploadURLs {
        image := queryImageFromDB(t, url.ImageID)
        assert.Equal(t, "PROCESSED", image.Status)
        assert.NotEmpty(t, image.SHA256)
        assert.Greater(t, image.ProcessedWidth, 0)
    }

    // 8. Verify Waypoint markers unchanged (scale-invariant)
    for i, waypointID := range response.WaypointIDs {
        waypoint := queryWaypointFromDB(t, waypointID)
        assert.Equal(t, "CONFIRMED", waypoint.Status)
        // Marker coordinates remain unchanged (normalized, scale-invariant)
        assert.Equal(t, response.Waypoints[i].MarkerX, waypoint.MarkerX)
        assert.Equal(t, response.Waypoints[i].MarkerY, waypoint.MarkerY)
    }
}
```

**Test 2: Upload Guard (Duplicate Prevention)**

Validates that the Valkey SET NX upload guard prevents duplicate image uploads.

```go
func TestUploadGuardPreventsDuplicates(t *testing.T) {
    // 1. Create route with 1 waypoint
    response := createRouteWithWaypoints(t, apiURL, []WaypointInput{
        {ContentType: "image/jpeg", FileSize: 2048, MarkerX: 0.10, MarkerY: 0.20},
    })
    uploadURL := response.UploadURLs[0]

    // 2. First upload -> 202 Accepted
    resp1, err := uploadImageToGateway(t, uploadURL.Token, smallJPEG())
    require.NoError(t, err)
    assert.Equal(t, http.StatusAccepted, resp1.StatusCode)

    // 3. Verify upload guard exists in Valkey
    guardExists := checkUploadGuard(t, uploadURL.ImageID)
    assert.True(t, guardExists)

    // 4. Second upload with SAME token -> 409 Conflict
    resp2, err := uploadImageToGateway(t, uploadURL.Token, smallJPEG())
    require.NoError(t, err)
    assert.Equal(t, http.StatusConflict, resp2.StatusCode)
}
```

**Test 3: Progress Tracking via Valkey Hash**

Validates that the image:status:{id} hash updates correctly through pipeline stages.

```go
func TestProgressTrackingViaValkeyHash(t *testing.T) {
    response := createRouteWithWaypoints(t, apiURL, []WaypointInput{
        {ContentType: "image/jpeg", FileSize: 2048, MarkerX: 0.10, MarkerY: 0.20},
    })
    imageID := response.UploadURLs[0].ImageID

    // Initial: queued (written by API)
    status := readValkeyHash(t, "image:status:"+imageID)
    assert.Equal(t, "queued", status["stage"])

    // Upload image
    _, _ = uploadImageToGateway(t, response.UploadURLs[0].Token, smallJPEG())

    // Poll hash for stage transitions
    stages := []string{"waiting_upload", "validating", "decoding", "processing", "encoding", "uploading_to_storage", "done"}
    seenStages := make(map[string]bool)

    timeout := time.After(30 * time.Second)
    ticker := time.NewTicker(200 * time.Millisecond)
    defer ticker.Stop()

    for {
        select {
        case <-timeout:
            t.Fatal("timeout waiting for progress stages")
        case <-ticker.C:
            status := readValkeyHash(t, "image:status:"+imageID)
            if stage, ok := status["stage"]; ok {
                seenStages[stage] = true
                if stage == "done" {
                    // Verify we saw at least some intermediate stages
                    assert.True(t, seenStages["validating"])
                    assert.True(t, seenStages["encoding"])
                    return
                }
            }
        }
    }
}
```

**Test 4: Failure Propagation (Invalid Image)**

Validates that upload failures are correctly communicated back to follow-api.

```go
func TestFailurePropagationInvalidImage(t *testing.T) {
    response := createRouteWithWaypoints(t, apiURL, []WaypointInput{
        {ContentType: "image/jpeg", FileSize: 2048, MarkerX: 0.10, MarkerY: 0.20},
    })
    imageID := response.UploadURLs[0].ImageID

    // Upload invalid file (not an image)
    _, _ = uploadImageToGateway(t, response.UploadURLs[0].Token, invalidImage())

    // Wait for failure status
    waitForImageStatus(t, imageID, "failed", 30*time.Second)

    // Verify failure result published to stream
    messages := readValkeyStream(t, "image:result", "api-workers", "test-consumer")
    var failureMsg *StreamMessage
    for _, msg := range messages {
        if msg.Fields["image_id"] == imageID && msg.Fields["status"] == "failed" {
            failureMsg = &msg
            break
        }
    }
    require.NotNil(t, failureMsg)
    assert.Contains(t, failureMsg.Fields["error_code"], "INVALID")

    // Verify API consumed failure and marked Image as FAILED
    image := queryImageFromDB(t, imageID)
    assert.Equal(t, "FAILED", image.Status)
    assert.NotEmpty(t, image.ErrorCode)
}
```

**Test 5: SSE Event Streaming**

Validates real-time Server-Sent Events from follow-api during image processing.

```go
func TestSSEEventStreaming(t *testing.T) {
    response := createRouteWithWaypoints(t, apiURL, []WaypointInput{
        {ContentType: "image/jpeg", FileSize: 2048, MarkerX: 0.10, MarkerY: 0.20},
    })
    imageID := response.UploadURLs[0].ImageID

    // Connect SSE client to API's status stream endpoint
    sseURL := fmt.Sprintf("%s/images/status/stream?image_ids=%s", apiURL, imageID)
    req, _ := http.NewRequest("GET", sseURL, nil)
    req.Header.Set("Accept", "text/event-stream")

    client := &http.Client{Timeout: 0} // No timeout for streaming
    resp, err := client.Do(req)
    require.NoError(t, err)
    defer resp.Body.Close()

    assert.Equal(t, http.StatusOK, resp.StatusCode)
    assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

    // Start reading events in goroutine
    events := make(chan SSEEvent, 10)
    go readSSEEvents(t, resp.Body, events)

    // Upload image
    _, _ = uploadImageToGateway(t, response.UploadURLs[0].Token, smallJPEG())

    // Collect events
    seenEvents := make(map[string]bool)
    timeout := time.After(30 * time.Second)

    for {
        select {
        case <-timeout:
            t.Fatal("timeout waiting for SSE events")
        case event := <-events:
            seenEvents[event.Type] = true
            if event.Type == "image_processed" {
                // Verify event data includes preview URL (markers unchanged)
                assert.Contains(t, event.Data, "preview_url")
                return
            }
        }
    }

    // Should have seen at least image_progress and image_processed
    assert.True(t, seenEvents["image_progress"])
    assert.True(t, seenEvents["image_processed"])
}
```

**Test 6: Recovery After Restart (Pending Message Re-read)**

Validates that pending messages are correctly re-processed after consumer restart.

```go
func TestRecoveryAfterRestart(t *testing.T) {
    response := createRouteWithWaypoints(t, apiURL, []WaypointInput{
        {ContentType: "image/jpeg", FileSize: 2048, MarkerX: 0.10, MarkerY: 0.20},
    })
    imageID := response.UploadURLs[0].ImageID

    // Upload image and wait for result to be published
    _, _ = uploadImageToGateway(t, response.UploadURLs[0].Token, smallJPEG())
    time.Sleep(5 * time.Second) // Wait for gateway to publish result

    // Read result WITHOUT acking (simulate consumer crash before ack)
    messages := readValkeyStreamNoAck(t, "image:result", "api-workers", "test-consumer-1")
    require.Len(t, messages, 1)
    resultMsg := messages[0]

    // Verify message is in Pending Entries List (PEL)
    pendingCount := getStreamPendingCount(t, "image:result", "api-workers")
    assert.Equal(t, 1, pendingCount)

    // Simulate consumer restart: new consumer reads pending messages
    pendingMessages := readValkeyStreamPending(t, "image:result", "api-workers", "test-consumer-2")
    require.Len(t, pendingMessages, 1)
    assert.Equal(t, resultMsg.ID, pendingMessages[0].ID)

    // Ack the message
    ackValkeyMessage(t, "image:result", "api-workers", pendingMessages[0].ID)

    // Verify message removed from PEL
    pendingCount = getStreamPendingCount(t, "image:result", "api-workers")
    assert.Equal(t, 0, pendingCount)
}
```

**Test 7: Service Health Checks Include Valkey**

Validates that both services report Valkey status in their health endpoints.

```go
func TestServiceHealthChecksIncludeValkey(t *testing.T) {
    // Test API health endpoint
    apiHealth, err := http.Get(apiURL + "/health")
    require.NoError(t, err)
    defer apiHealth.Body.Close()

    assert.Equal(t, http.StatusOK, apiHealth.StatusCode)

    var apiHealthData map[string]interface{}
    json.NewDecoder(apiHealth.Body).Decode(&apiHealthData)
    assert.Contains(t, apiHealthData, "valkey")
    assert.Equal(t, "ok", apiHealthData["valkey"])

    // Test Gateway health endpoint
    gwHealth, err := http.Get(gatewayURL + "/health")
    require.NoError(t, err)
    defer gwHealth.Body.Close()

    assert.Equal(t, http.StatusOK, gwHealth.StatusCode)

    var gwHealthData map[string]interface{}
    json.NewDecoder(gwHealth.Body).Decode(&gwHealthData)
    assert.Contains(t, gwHealthData, "valkey")
    assert.Equal(t, "ok", gwHealthData["valkey"])
}
```

#### README.md

Documentation for running the integration tests:

```markdown
# Cross-Repo Valkey Integration Tests

End-to-end tests for Valkey messaging between follow-api and follow-image-gateway.

## Prerequisites

### Both Modes
- Go 1.25+
- Ed25519 keypair in `/etc/follow-image-gateway`

### Docker Mode
- Docker Engine
- Docker Compose V2

### Local Mode
- All services running:
  - follow-api (port 18080)
  - follow-image-gateway (port 18090)
  - Valkey (port 16379)
  - PostgreSQL (port 15432)
  - MinIO (port 19000)

## Running Tests

### Docker Mode (default, CI/CD)
```bash
go test -tags=integration -v ./tests/integration/
```

### Local Mode (developer workflow)
```bash
# Terminal 1: Start services
cd /home/yoseforb/pkg/follow
docker compose up

# Terminal 2: Run tests
INTEGRATION_TEST_MODE=local go test -tags=integration -v ./tests/integration/
```

## Test Cases

| # | Test | Coverage |
|---|------|----------|
| 1 | TestFullUploadProcessResultFlow | Complete route creation → upload → processing → result consumption → route ready |
| 2 | TestUploadGuardPreventsDuplicates | SET NX prevents duplicate uploads |
| 3 | TestProgressTrackingViaValkeyHash | image:status:{id} hash updates through pipeline stages |
| 4 | TestFailurePropagationInvalidImage | Gateway failure published to stream, API marks Image as FAILED |
| 5 | TestSSEEventStreaming | Real-time SSE events from API during processing |
| 6 | TestRecoveryAfterRestart | Pending messages re-read after consumer restart |
| 7 | TestServiceHealthChecksIncludeValkey | Health endpoints report Valkey status |

## Troubleshooting

| Problem | Solution |
|---------|----------|
| Ed25519 keypair not found | Run key generation setup (see main README) |
| Docker mode port conflicts | Check ports 15432, 16379, 18080, 18090, 19000 are free |
| Local mode connection refused | Verify all services are running and healthy |
| Tests timeout | Increase context timeout in test code or check service logs |
```

#### Dependencies Summary

**Phase C implementation requires:**

1. **Coordination repo setup:**
   - Create `tests/integration/` directory
   - Initialize separate Go module: `go mod init follow-integration-tests`
   - Add dependencies: valkey-go, testcontainers-go, testify, jwt

2. **Parent docker-compose.yml updates:**
   - Parameterize ports for all services
   - Add health checks to all services
   - Configure Valkey service with proper persistence and memory settings

3. **Test implementation:**
   - Copy dual-mode pattern from gateway integration tests
   - Implement 7 test cases covering all cross-repo flows
   - Create comprehensive helper library for JWT, HTTP, Valkey operations

4. **Documentation:**
   - README with clear running instructions for both modes
   - Troubleshooting guide
   - CI/CD integration examples

#### Success Criteria

Phase C is complete when:

- [ ] All 7 integration tests pass in both docker and local modes
- [ ] Tests verify complete end-to-end flow: route creation → upload → processing → result consumption → route ready
- [ ] Upload guard (SET NX) prevents duplicate uploads (409 Conflict)
- [ ] Progress tracking hash updates correctly through all pipeline stages
- [ ] Failure messages propagate from gateway to API correctly
- [ ] SSE events stream in real-time from API to clients
- [ ] Pending messages are re-read after consumer restart
- [ ] Health endpoints include Valkey status checks
- [ ] Tests run successfully in CI/CD pipeline (docker mode)
- [ ] Documentation enables developers to run tests locally

### Phase D: Infrastructure and Cleanup

1. **Update parent `docker-compose.yml`**: Add Valkey 8.1 service (shared by both services)
2. **Update coordination repo CLAUDE.md** if needed (Valkey in service communication map)

---

## Parent Docker Compose Update

Add to `/home/yoseforb/pkg/follow/docker-compose.yml`:

```yaml
  valkey:
    image: valkey/valkey:8.1
    ports: ["6379:6379"]
    volumes: [valkey-data:/data]
    command: >
      valkey-server
      --appendonly yes
      --log-format logfmt
      --log-timestamp-format iso8601
      --maxmemory 256mb
      --maxmemory-policy noeviction
    healthcheck:
      test: ["CMD", "valkey-cli", "ping"]
      interval: 5s
      timeout: 3s
      retries: 5
```

Both `follow-api` and `follow-image-gateway` services add `depends_on: valkey: { condition: service_healthy }`.

---

## Technology Decisions (Final)

| Decision | Choice | Reference |
|----------|--------|-----------|
| Valkey client library | `valkey-io/valkey-go` (native API, NOT valkeycompat) | ADR-012, valkey-go research |
| Field types | `map[string]string` | valkey-go research Section 15 |
| Orphan recovery | `XAUTOCLAIM` (atomic) | valkey-go research Section 5 |
| Upload guard | `SET NX EX` (String with TTL) | Architecture doc Section 6.1 |
| Testing | Classical/Detroit, hand-written fakes | All repo CLAUDE.md files |
| Valkey server version | 8.1 | ADR-012, comprehensive guide |

---

## Risk Factors

| Risk | Impact | Mitigation |
|------|--------|------------|
| follow-pkg interface changes during API/gateway integration | Requires updating both consumers | Complete follow-pkg fully before starting Phase B |
| Message format drift between producer (gateway) and consumer (API) | Silent data loss or parse errors | Define message schema in architecture doc (Section 6.2) -- already done |
| Valkey unavailability | Pipeline stalls or errors | Gateway: graceful degradation (fire-and-forget). API: circuit breaker, 503 on route creation |
| Consumer group coordination | Wrong group names cause competing consumers | API uses `api-workers`, gateway does NOT consume from streams |
| Ed25519 key management | Key mismatch blocks uploads | Document key generation and distribution in deployment guide |

---

## Success Criteria

### Functional
- [ ] Gateway sets `image:upload:{id}` NX guard on upload receipt
- [ ] Gateway updates `image:status:{id}` hash at each pipeline stage
- [ ] Gateway publishes success/failure result to `image:result` stream
- [ ] API writes initial `{stage: "queued"}` to `image:status:{id}` on route creation
- [ ] API consumes results from `image:result` via XREADGROUP
- [ ] API updates Image, Waypoint, and Route in single DB transaction
- [ ] API confirms waypoint status when image processing succeeds (marker coordinates are scale-invariant)
- [ ] API auto-transitions routes PENDING -> READY when all images processed
- [ ] SSE endpoint streams progress events to connected clients
- [ ] Polling endpoint returns current status from PostgreSQL

### Non-Functional
- [ ] No goroutine leaks on shutdown (both services)
- [ ] Graceful degradation if Valkey unavailable (gateway continues, API returns 503)
- [ ] Memory usage stable (no leaks from unclaimed messages)
- [ ] All three repos pass quality gates independently
- [ ] Integration test: full upload -> process -> result -> route ready flow

---

## References

- Architecture: `ai-docs/architecture/image-gateway-architecture.md`
- ADR-012 (Valkey): `ai-docs/adr/012-valkey-over-redis.md`
- ADR-016 (Streams): `ai-docs/adr/follow-api-016-redis-streams-inter-service-communication.md`
- ADR-017 (Ed25519): `ai-docs/adr/follow-api-017-ed25519-asymmetric-signing.md`
- ADR-022 (Domain-agnostic): `ai-docs/adr/follow-api-022-domain-agnostic-processing.md`
- valkey-go Research: `follow-pkg/ai-docs/research/valkey-go-api-research.md`
- Valkey Guide: `follow-pkg/ai-docs/research/valkey-comprehensive-guide.md`
