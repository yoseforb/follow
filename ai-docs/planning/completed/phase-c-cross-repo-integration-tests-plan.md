# Phase C: Cross-Repo Integration Testing — Implementation Plan

**Date:** 2026-02-21
**Status:** Active
**Scope:** End-to-end integration tests for the complete Valkey messaging flow between follow-api and
follow-image-gateway, living in the coordination repo at `/home/yoseforb/pkg/follow/tests/integration/`

---

## Overview

### Feature Summary

Phase C implements a standalone Go integration test suite that validates the full cross-service
messaging flow: route creation in follow-api, image upload to follow-image-gateway, Valkey stream
messaging between services, and route status transitions. These tests exercise every public HTTP
endpoint and every Valkey key pattern from the outside, treating all services as black boxes
accessible only via HTTP and direct Valkey inspection.

**Black-box principle:** The test suite communicates with services ONLY via HTTP endpoints and
direct Valkey inspection. It does NOT import service code, sign tokens, load keys, or access
databases directly. Upload tokens are obtained from follow-api's presigned URLs -- the test suite
never creates tokens itself. The only environment variables the test suite reads are `API_URL`,
`GATEWAY_URL`, and `VALKEY_ADDRESS` (all with sensible defaults). Services read their own config
for database, MinIO, and Valkey connections -- the test suite does NOT pass infrastructure
addresses to the services.

### Why a Separate Module

The `tests/integration/` directory is its own Go module (`go.mod` with its own module path). This
is a deliberate architectural decision to prevent testcontainers-go and other heavy test-only
dependencies from appearing in either service's dependency graph. The test module imports neither
follow-api nor follow-image-gateway code; it communicates only through HTTP and the Valkey wire
protocol.

### Mode Strategy

**Local mode is the default.** The expected developer workflow is:

```
# Ensure PostgreSQL, MinIO, and Valkey are running via systemd (or equivalent)
# Then just run tests — the suite starts follow-api and follow-image-gateway automatically:
cd /home/yoseforb/pkg/follow/tests/integration
INTEGRATION_TEST_MODE=local go test -tags=integration -race -v .
```

In local mode:
- **Infrastructure services** (PostgreSQL, MinIO, Valkey) are assumed to be already running on the
  developer's machine (e.g., via systemd). The test suite does NOT start them.
- **Go services** (follow-api and follow-image-gateway) are started by the test suite itself using
  `exec.Command("go", "run", "./cmd/server")` from their respective repo directories. The suite
  starts both as child processes, waits for their health endpoints, runs the tests, then kills both
  processes on teardown.
- No `docker compose up` needed — the developer just runs the test command.

Docker mode (testcontainers-go/modules/compose) is for CI/CD only. It references the parent
`docker-compose.yml` (after port parameterization) and spins up the entire stack on non-conflicting
ports.

### Architecture Impact

- Creates a new top-level `tests/integration/` directory and Go module in the coordination repo
- Requires port parameterization of the parent `docker-compose.yml` (Task 1)
- No changes to any service code — purely observational via HTTP + Valkey

### Key Integration Points

| Service | Tested Via |
|---------|------------|
| follow-api | HTTP endpoints on `{API_URL}` |
| follow-image-gateway | HTTP endpoints on `{GATEWAY_URL}` |
| Valkey | Direct client connection (valkey-io/valkey-go) for key inspection |
| PostgreSQL | NOT directly queried — all state verified via HTTP responses |

### Valkey Key Patterns Under Test

| Key | Type | Asserted In |
|-----|------|-------------|
| `image:status:{image_id}` | Hash | Task 3, 4, 5 |
| `image:upload:{image_id}` | String (NX guard) | Task 4 |
| `image:result` | Stream (consumer group `api-workers`) | Task 2, 5, 6 |

### Success Criteria

Phase C is complete when:
- All tests in Tasks 1–8 pass in both local and docker modes
- `go test -tags=integration -race -v ./tests/integration/` exits 0 in CI/CD
- Developer can run `INTEGRATION_TEST_MODE=local` with infrastructure services running via systemd
- README documents both modes and troubleshooting steps

---

## File Structure

```
/home/yoseforb/pkg/follow/
├── docker-compose.yml              # MODIFIED: port parameterization (Task 1)
└── tests/integration/              # NEW: entire directory (Tasks 1–8)
    ├── go.mod                      # Separate module: follow-integration-tests
    ├── go.sum
    ├── main_test.go                # TestMain, dual-mode setup, shared globals
    ├── helpers_test.go             # HTTP, Valkey, JWT, image loading helpers
    ├── infrastructure_test.go      # Task 2: health check tests
    ├── behavioral_flow_test.go     # Task 3: full endpoint behavioral flow
    ├── upload_guard_test.go        # Task 4: Valkey SET NX duplicate prevention
    ├── progress_tracking_test.go   # Task 5: image:status:{id} hash transitions
    ├── failure_propagation_test.go # Task 6: invalid image failure flow
    ├── sse_streaming_test.go       # Task 7: SSE event streaming
    ├── recovery_test.go            # Task 8: pending message re-read after restart
    ├── README.md                   # Running instructions
    └── testdata/                   # EXISTING: real test images (Pexels, CC0-licensed)
        ├── pexels-hikaique-114797.jpg              (1.1 MB)
        ├── pexels-punttim-240223.jpg               (905 KB)
        ├── pexels-arthurbrognoli-2260838.jpg       (786 KB)
        ├── pexels-the-brainthings-...-15617058.jpg (710 KB)
        ├── pexels-bi-ravencrow-...-33327471.jpg    (978 KB)
        ├── pexels-pixabay-264502.jpg               (2.2 MB)
        ├── pexels-pixabay-264512.jpg               (1.9 MB)
        ├── pexels-tuurt-2954405.jpg                (1.4 MB)
        ├── pexels-tuurt-2954412.jpg                (1.9 MB)
        ├── pexels-wendywei-4027948.jpg             (5.6 MB)
        ├── ... (27 real JPEG images total, 710 KB – 5.6 MB)
        └── (see full listing in Task 4 — Image Loading section)
```

---

## Go Module Setup

**Module path:** `follow-integration-tests`

**`go.mod` dependencies (do not import service packages):**

```
github.com/valkey-io/valkey-go          v1.0+    Valkey client for direct inspection
github.com/testcontainers/testcontainers-go  v0.37+   Docker mode only
github.com/testcontainers/testcontainers-go/modules/compose  same version
github.com/stretchr/testify              v1.10+   assert + require
github.com/google/uuid                   v1.6+    UUID generation
```

**Minimum Go version:** 1.23 (consistent with service repos)

---

## Reference: Dual-Mode Pattern Source

The dual-mode pattern is adapted from two existing implementations:

- **Local mode source:** `/home/yoseforb/pkg/follow/follow-pkg/tests/integration/main_test.go`
  (service-level dual mode with exec.Command for docker — THIS plan adapts the exec.Command pattern
  for starting Go services in local mode)
- **Docker mode source:** `/home/yoseforb/pkg/follow/follow-image-gateway/tests/integration/main_test.go`
  (testcontainers-go/modules/compose pattern — adapt this for the full stack)

The key difference from follow-pkg: follow-pkg defaults to "docker" mode. THIS module defaults to
"local" mode, because developers typically have infrastructure services (PostgreSQL, MinIO, Valkey)
running via systemd. The test suite starts the Go services (follow-api, follow-image-gateway)
itself via `exec.Command`.

---

## Task 1: Module Scaffolding and Port Parameterization

### Title
Set up the `tests/integration/` Go module and parameterize `docker-compose.yml` ports

### Description

Two distinct pieces of work:

**Part A — Parameterize docker-compose.yml (docker mode only):**

The parent `docker-compose.yml` currently has all ports hardcoded (5432, 6379, 9000, 9001, 8080,
8090). In docker mode, the integration tests will spin up the full stack on non-conflicting ports to
avoid conflicts with the developer's running stack. This requires converting all host-side port
mappings to environment variable substitutions with the standard ports as defaults.

Note: Local mode does NOT use docker-compose.yml at all. Infrastructure services are assumed
running via systemd, and Go services are started directly via `exec.Command`.

Port variable mappings (these are HOST-SIDE port mappings, not service config -- services inside
containers always listen on their default ports):

| Service | Current Mapping | Parameterized |
|---------|-----------------|---------------|
| postgres | `"5432:5432"` | `"${POSTGRES_HOST_PORT:-5432}:5432"` |
| valkey | `"6379:6379"` | `"${VALKEY_HOST_PORT:-6379}:6379"` |
| minio API | `"9000:9000"` | `"${MINIO_HOST_PORT:-9000}:9000"` |
| minio Console | `"9001:9001"` | `"${MINIO_CONSOLE_HOST_PORT:-9001}:9001"` |
| follow-api | `"8080:8080"` | `"${API_HOST_PORT:-8080}:8080"` |
| follow-image-gateway | `"8090:8090"` | `"${GATEWAY_HOST_PORT:-8090}:8090"` |

Container names and network name should also be parameterized for test isolation:

```yaml
container_name: ${POSTGRES_CONTAINER_NAME:-follow-postgres}
container_name: ${VALKEY_CONTAINER_NAME:-follow-valkey}
# etc.
networks:
  internal:
    driver: bridge
    name: ${NETWORK_NAME:-follow-internal}
```

**Part B — Create `tests/integration/` module:**

Create the directory and `go.mod`:

```
/home/yoseforb/pkg/follow/tests/integration/go.mod
/home/yoseforb/pkg/follow/tests/integration/go.sum
```

`go.mod` content:
```
module follow-integration-tests

go 1.23

require (
    github.com/google/uuid v1.6.0
    github.com/stretchr/testify v1.10.0
    github.com/testcontainers/testcontainers-go v0.37.0
    github.com/testcontainers/testcontainers-go/modules/compose v0.37.0
    github.com/valkey-io/valkey-go v1.0.54
)
```

Run `go mod tidy` from the `tests/integration/` directory after creating go.mod.

**Part C — Create `main_test.go`:**

File: `/home/yoseforb/pkg/follow/tests/integration/main_test.go`
Build tag: `//go:build integration`

Package: `package integration_test`

Global variables (package-level, nolint:gochecknoglobals):

```go
var (
    apiURL         string              // follow-api base URL, e.g. http://localhost:8080
    gatewayURL     string              // follow-image-gateway URL, e.g. http://localhost:8090
    valkeyAddress  string              // Valkey addr, e.g. localhost:6379
    composeStack   compose.ComposeStack // docker mode only, nil in local mode
    apiProcess     *exec.Cmd           // local mode only: follow-api child process
    gatewayProcess *exec.Cmd           // local mode only: follow-image-gateway child process
)
```

`TestMain` logic:

```go
func TestMain(m *testing.M) {
    mode := os.Getenv("INTEGRATION_TEST_MODE")
    if mode == "" {
        mode = "local"  // LOCAL IS THE DEFAULT (unlike follow-pkg which defaults to "docker")
    }
    slog.Info("integration test mode", "mode", mode)

    exitCode := 1
    switch mode {
    case "local":
        setupLocal()
        exitCode = m.Run()
        teardownLocal()
    case "docker":
        setupDocker()
        exitCode = m.Run()
        teardownDocker()
    default:
        slog.Error("unknown INTEGRATION_TEST_MODE", "mode", mode,
            "valid", "local, docker")
    }

    os.Exit(exitCode)
}
```

`setupLocal()` function:

In local mode, infrastructure services (PostgreSQL, MinIO, Valkey) are assumed to be already
running via systemd. The test suite starts the two Go services (follow-api and follow-image-gateway)
as child processes using `exec.Command`. Services read their own config files for connecting to
infrastructure (database, MinIO, Valkey) -- the test suite does NOT pass database DSNs, MinIO
endpoints, or Valkey addresses to the services.

The ONLY extra environment variable the test suite sets on follow-api is `GATEWAY_BASE_URL`, which
tells follow-api where the image gateway is so it can generate presigned upload URLs pointing to
the correct gateway port. The gateway needs no extra env vars.

```go
func setupLocal() {
    // Read test suite addresses with defaults (systemd standard ports)
    valkeyAddress = envOrDefault("VALKEY_ADDRESS", "localhost:6379")
    apiURL = envOrDefault("API_URL", "http://localhost:8080")
    gatewayURL = envOrDefault("GATEWAY_URL", "http://localhost:8090")

    // Extract ports from URLs for CLI flags (parse host:port from URL)
    apiPort := portFromURL(apiURL, "8080")
    gatewayPort := portFromURL(gatewayURL, "8090")

    // Determine repo paths (auto-detected from project root)
    projectRoot, err := filepath.Abs(filepath.Join("..", ".."))
    if err != nil {
        slog.Error("failed to determine project root", "error", err)
        os.Exit(1)
    }
    apiDir := filepath.Join(projectRoot, "follow-api")
    gatewayDir := filepath.Join(projectRoot, "follow-image-gateway")

    // Verify infrastructure services are reachable before starting Go services
    waitForValkey(valkeyAddress)

    // Start follow-image-gateway FIRST (API needs gateway to be available)
    slog.Info("starting follow-image-gateway", "dir", gatewayDir,
        "port", gatewayPort)
    gatewayProcess = exec.Command("go", "run", "./cmd/server",
        "-host", "localhost",
        "-port", gatewayPort,
        "-log-level", "debug",
        "-runtime-timeout", "0",
    )
    gatewayProcess.Dir = gatewayDir
    // No extra env vars needed — gateway reads its own config for MinIO, Valkey, etc.
    gatewayProcess.Stdout = os.Stdout
    gatewayProcess.Stderr = os.Stderr
    if err := gatewayProcess.Start(); err != nil {
        slog.Error("failed to start follow-image-gateway", "error", err)
        os.Exit(1)
    }

    // Start follow-api with GATEWAY_BASE_URL pointing to the gateway we just started
    slog.Info("starting follow-api", "dir", apiDir, "port", apiPort)
    apiProcess = exec.Command("go", "run", "./cmd/server",
        "-host", "localhost",
        "-port", apiPort,
        "-log-level", "debug",
        "-runtime-timeout", "0",
    )
    apiProcess.Dir = apiDir
    apiProcess.Env = append(os.Environ(),
        "GATEWAY_BASE_URL=http://localhost:"+gatewayPort,
    )
    apiProcess.Stdout = os.Stdout
    apiProcess.Stderr = os.Stderr
    if err := apiProcess.Start(); err != nil {
        slog.Error("failed to start follow-api", "error", err)
        // Kill already-started gateway process before exiting
        _ = gatewayProcess.Process.Kill()
        os.Exit(1)
    }

    // Wait for both services to become healthy (60s timeout — includes Go compilation time)
    waitForService(gatewayURL + "/health/")
    waitForService(apiURL + "/health/")

    slog.Info("local mode setup complete",
        "api_url", apiURL, "gateway_url", gatewayURL,
        "valkey", valkeyAddress)
}

func envOrDefault(key, defaultVal string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return defaultVal
}

// portFromURL extracts the port from a URL string, returning defaultPort if
// the URL has no explicit port.
func portFromURL(rawURL, defaultPort string) string {
    u, err := url.Parse(rawURL)
    if err != nil {
        return defaultPort
    }
    if p := u.Port(); p != "" {
        return p
    }
    return defaultPort
}
```

Key points:
- Both services use `-host localhost -port {port}` CLI flags (not env vars) for their listen address
- follow-api receives `GATEWAY_BASE_URL` env var so it generates upload URLs pointing to the gateway
- The gateway needs NO extra env vars -- it reads its own config for MinIO, Valkey, etc.
- Services are NOT passed database DSNs, MinIO endpoints, or Valkey addresses by the test suite
- Repo paths are auto-detected from the project root (`filepath.Abs(filepath.Join("..", ".."))`)
- Services are started with `-runtime-timeout 0` (no auto-shutdown)
- `exec.Command.Start()` is used (non-blocking) so both services run concurrently
- stdout/stderr from both services are piped to test output for debugging
- The 60-second timeout on `waitForService` accounts for Go compilation time on first run
- `apiProcess` and `gatewayProcess` are stored in package-level variables for `teardownLocal()`

`setupDocker()` function:
- Navigates to project root: `filepath.Abs(filepath.Join("..", ".."))` (from tests/integration/)
- Creates compose stack: `compose.NewDockerCompose(filepath.Join(projectRoot, "docker-compose.yml"))`
- Applies environment overrides. These control HOST-SIDE port mappings in docker-compose.yml, NOT
  the services' own listen ports. Services inside containers always listen on their default ports
  (8080, 8090, etc.) -- the docker compose port mapping translates host ports to container ports:
  ```go
  composeStack = stack.WithEnv(map[string]string{
      "POSTGRES_HOST_PORT":      "15432",
      "VALKEY_HOST_PORT":        "16379",
      "MINIO_HOST_PORT":         "19000",
      "MINIO_CONSOLE_HOST_PORT": "19001",
      "API_HOST_PORT":           "18080",
      "GATEWAY_HOST_PORT":       "18090",
      "POSTGRES_CONTAINER_NAME": "follow-postgres-test",
      "VALKEY_CONTAINER_NAME":   "follow-valkey-test",
      "MINIO_CONTAINER_NAME":    "follow-minio-test",
      "API_CONTAINER_NAME":      "follow-api-test",
      "GATEWAY_CONTAINER_NAME":  "follow-gateway-test",
      "NETWORK_NAME":            "follow-test-network",
  })
  ```
- Calls `composeStack.Up(context.Background(), compose.Wait(true))` -- blocks until all
  services pass their healthchecks
- Sets global URL variables:
  - `apiURL = "http://localhost:18080"`
  - `gatewayURL = "http://localhost:18090"`
  - `valkeyAddress = "localhost:16379"`
- Calls `waitForService` and `waitForValkey` as a belt-and-suspenders check

`teardownDocker()` function:
- Calls `composeStack.Down(context.Background(), compose.RemoveVolumes(true))`
- On error: `slog.Warn("teardown failed", "error", err)` (non-fatal)

`teardownLocal()` function:

Kills both Go service processes that were started by `setupLocal()`. Uses graceful shutdown
(SIGTERM first, then SIGKILL after timeout):

```go
func teardownLocal() {
    killProcess("follow-api", apiProcess)
    killProcess("follow-image-gateway", gatewayProcess)
}

func killProcess(name string, cmd *exec.Cmd) {
    if cmd == nil || cmd.Process == nil {
        return
    }

    // Send SIGTERM for graceful shutdown
    slog.Info("stopping service", "name", name, "pid", cmd.Process.Pid)
    if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
        slog.Warn("SIGTERM failed, sending SIGKILL",
            "name", name, "error", err)
        _ = cmd.Process.Kill()
        return
    }

    // Wait up to 5 seconds for graceful exit
    done := make(chan error, 1)
    go func() { done <- cmd.Wait() }()

    select {
    case <-done:
        slog.Info("service stopped gracefully", "name", name)
    case <-time.After(5 * time.Second):
        slog.Warn("service did not stop in 5s, sending SIGKILL", "name", name)
        _ = cmd.Process.Kill()
        <-done // Wait for kill to complete
    }
}
```

Note: Requires `import "syscall"` in the import block. The `killProcess` helper is reusable for
both service processes.

`waitForService()` function:
```go
func waitForService(url string) {
    deadline := time.Now().Add(60 * time.Second)
    for time.Now().Before(deadline) {
        resp, err := http.Get(url) //nolint:noctx
        if err == nil && resp.StatusCode == http.StatusOK {
            resp.Body.Close()
            slog.Info("service ready", "url", url)
            return
        }
        if resp != nil {
            resp.Body.Close()
        }
        time.Sleep(1 * time.Second)
    }
    slog.Error("service not reachable after 60s", "url", url)
    os.Exit(1)
}
```

`waitForValkey()` function:
```go
func waitForValkey(addr string) {
    cfg := &valkey.ClientOption{
        InitAddress:  []string{addr},
        DisableCache: true,
    }
    deadline := time.Now().Add(30 * time.Second)
    for time.Now().Before(deadline) {
        client, err := valkeygo.NewClient(*cfg)
        if err == nil {
            err = client.Do(context.Background(),
                client.B().Ping().Build()).Error()
            client.Close()
            if err == nil {
                slog.Info("valkey ready", "addr", addr)
                return
            }
        }
        time.Sleep(500 * time.Millisecond)
    }
    slog.Error("valkey not reachable after 30s", "addr", addr)
    os.Exit(1)
}
```

Note: The test module uses `valkey-io/valkey-go` directly (not the follow-pkg wrapper) because it
does not import service code.

**Part D — Create `README.md`:**

File: `/home/yoseforb/pkg/follow/tests/integration/README.md`

Contents:
- Section: Prerequisites (Go 1.23+, Docker for docker mode, PostgreSQL + MinIO + Valkey running
  via systemd for local mode, Ed25519 keypair configured in both services' config files)
- Section: Running in local mode (default):
  ```bash
  # Ensure infrastructure services are running (PostgreSQL, MinIO, Valkey)
  # e.g., via systemd: systemctl status postgresql minio valkey

  # Run tests — follow-api and follow-image-gateway are started automatically
  cd /home/yoseforb/pkg/follow/tests/integration
  INTEGRATION_TEST_MODE=local go test -tags=integration -race -v .
  ```
  Note: No `docker compose up` needed. The test suite starts follow-api and follow-image-gateway
  as child processes via `exec.Command("go", "run", "./cmd/server")` and tears them down after
  tests complete.
- Section: Running in docker mode (CI/CD):
  ```bash
  cd /home/yoseforb/pkg/follow/tests/integration
  go test -tags=integration -race -v .
  ```
- Section: Environment variables table (for test suite configuration only -- these are NOT passed
  to services; services read their own config for database, MinIO, Valkey connections):
  | Variable | Default | Description |
  |----------|---------|-------------|
  | `INTEGRATION_TEST_MODE` | `local` | `local` or `docker` |
  | `API_URL` | `http://localhost:8080` | follow-api base URL (test suite connects here) |
  | `GATEWAY_URL` | `http://localhost:8090` | follow-image-gateway base URL (test suite connects here) |
  | `VALKEY_ADDRESS` | `localhost:6379` | Valkey server address (test suite connects here for inspection) |
- Section: Test cases table (one row per test function)
- Section: Troubleshooting (port conflicts, missing keypair, service not reachable,
  infrastructure not running via systemd)

### Files Affected

**Modified:**
- `/home/yoseforb/pkg/follow/docker-compose.yml` — port parameterization

**Created:**
- `/home/yoseforb/pkg/follow/tests/integration/go.mod`
- `/home/yoseforb/pkg/follow/tests/integration/go.sum` (generated by `go mod tidy`)
- `/home/yoseforb/pkg/follow/tests/integration/main_test.go`
- `/home/yoseforb/pkg/follow/tests/integration/README.md`

### Dependencies

None (first task).

### Acceptance Criteria

- `docker compose up` still works with all default ports unchanged (env vars have correct defaults)
- `API_HOST_PORT=18080 GATEWAY_HOST_PORT=18090 docker compose up` starts stack on alternate ports
- `go mod tidy` in `tests/integration/` succeeds without errors
- `go build -tags=integration ./...` in `tests/integration/` compiles successfully
- `main_test.go` compiles with build tag `integration`
- `setupLocal()` starts follow-api with `-host localhost -port {port}` and `GATEWAY_BASE_URL` env var
- `setupLocal()` starts follow-image-gateway with `-host localhost -port {port}` (no extra env vars)
- `setupLocal()` exits with exit code 1 and a helpful message when infrastructure (Valkey) is not
  running or when Go services fail to start
- `teardownLocal()` gracefully kills both Go service processes (SIGTERM, then SIGKILL after 5s)
- README accurately describes both modes (local mode does NOT require `docker compose up`)
- README environment variables table lists ONLY test suite variables (`INTEGRATION_TEST_MODE`,
  `API_URL`, `GATEWAY_URL`, `VALKEY_ADDRESS`) -- no service-internal config like database DSNs

### Story Points: 3

---

## Task 2: Infrastructure Health Check Tests

### Title
Implement `infrastructure_test.go` — verify all five services are reachable and healthy before
any business logic tests run

### Description

This is the first actual test file. Its single purpose is to validate that the test infrastructure
is working before any business logic tests run. A developer running the suite for the first time
will see this test fail fast with a clear signal if any service is misconfigured.

All tests in this file are independent and do not create any application state.

**File:** `/home/yoseforb/pkg/follow/tests/integration/infrastructure_test.go`
**Build tag:** `//go:build integration`
**Package:** `package integration_test`

**Test: `TestInfrastructure_PostgreSQLReachable`**

Since tests do not directly query PostgreSQL (state is verified via HTTP only), this test
verifies reachability indirectly via the API's database health endpoint:

```
GET {apiURL}/health/db
Expected: 200 OK
Expected body: JSON with "status" field equal to "ok"
```

Assertion detail:
```go
var result map[string]interface{}
require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
assert.Equal(t, "ok", result["status"])
```

**Test: `TestInfrastructure_ValkeyReachable`**

Creates a direct Valkey client (valkey-io/valkey-go) using `valkeyAddress` global:

```go
func TestInfrastructure_ValkeyReachable(t *testing.T) {
    client, err := valkeygo.NewClient(valkeygo.ClientOption{
        InitAddress:  []string{valkeyAddress},
        DisableCache: true,
    })
    require.NoError(t, err, "create valkey client")
    defer client.Close()

    err = client.Do(context.Background(),
        client.B().Ping().Build()).Error()
    require.NoError(t, err, "valkey PING")
}
```

**Test: `TestInfrastructure_MinIOReachable`**

Verifies MinIO via the API's storage health endpoint (avoids importing minio-go):

```
GET {apiURL}/health/storage
Expected: 200 OK
Expected body: JSON with "status" field equal to "ok"
```

**Test: `TestInfrastructure_FollowAPIHealthy`**

```
GET {apiURL}/health/
Expected: 200 OK
Expected body: JSON with "status" == "ok" AND "message" non-empty AND "timestamp" non-empty
```

Full assertion:
```go
var result map[string]interface{}
require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
assert.Equal(t, "ok", result["status"])
assert.NotEmpty(t, result["message"])
assert.NotEmpty(t, result["timestamp"])
```

**Test: `TestInfrastructure_FollowGatewayHealthy`**

```
GET {gatewayURL}/health/
Expected: 200 OK
Expected body: JSON contains "ok" anywhere (gateway returns a simple health payload)
```

The gateway's health response includes `valkey_healthy` and `minio_healthy` fields. Assert both:
```go
var result map[string]interface{}
require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
// The gateway health format uses these fields per CLAUDE.md
assert.Equal(t, true, result["valkey_healthy"],
    "gateway should report valkey as healthy")
assert.Equal(t, true, result["minio_healthy"],
    "gateway should report minio as healthy")
```

Note: If the gateway health response format differs from this assumption (the exact field names are
not in the design DSL), the implementation agent should check
`/home/yoseforb/pkg/follow/follow-image-gateway/internal/health/` for the actual response structure
and adjust accordingly.

**Test: `TestInfrastructure_APIHealthIncludesValkey`**

The follow-api health endpoint is expected to include a Valkey status field once the Valkey
integration (Phase B) is complete. This test documents that expectation:

```
GET {apiURL}/health/
Expected: 200 OK
```

Check for a `valkey` field or equivalent Valkey health signal in the response. If the field is not
present yet (Phase B incomplete), skip the assertion with `t.Skip("valkey health field not yet
implemented")` rather than failing — this allows the infrastructure test to still pass during
incremental development.

### Files Affected

**Created:**
- `/home/yoseforb/pkg/follow/tests/integration/infrastructure_test.go`

### Dependencies

- Task 1 (module and main_test.go must exist)

### Acceptance Criteria

- All 5 infrastructure tests pass when services are running (local mode or docker mode)
- Tests fail fast (within 5 seconds) when a service is down, with clear error messages
- No application state is created or modified by these tests
- `go test -tags=integration -race -run TestInfrastructure -v .` runs all 5 tests in isolation

### Story Points: 2

---

## Task 3: Full Behavioral Flow Test

### Title
Implement `behavioral_flow_test.go` — one comprehensive test exercising every API endpoint in
sequence and verifying all response fields are correct

### Description

A single test function `TestFullAPIBehavioralFlow` that exercises all API endpoints from start to
finish. This is NOT a Valkey-specific test — it is a comprehensive correctness test for the entire
API surface. It verifies that every response contains the expected fields with correct values.

The test creates isolated state (unique user per run) and cleans up after itself.

**File:** `/home/yoseforb/pkg/follow/tests/integration/behavioral_flow_test.go`
**Build tag:** `//go:build integration`
**Package:** `package integration_test`

**Step-by-step test logic for `TestFullAPIBehavioralFlow`:**

The test is a sequential state machine. Each step uses the output of the previous step. Use
`require.NoError` (not `assert.NoError`) for request errors — a failed HTTP request means the test
cannot proceed.

**Step 1: Create anonymous user**

```
POST {apiURL}/api/v1/users/anonymous
Headers: Content-Type: application/json
Body: {} (empty JSON object)
Expected: 200 OK
```

Response struct (decode into `map[string]interface{}`):
- `user_id`: non-empty string (UUID format)
- `token`: non-empty string (JWT)
- `expires_at`: non-empty string (RFC 3339)
- `created_at`: non-empty string (RFC 3339)

Extract: `userID`, `authToken` for subsequent requests.

**Step 2: Get anonymous user**

```
GET {apiURL}/api/v1/users/anonymous/{userID}
Headers: Authorization: Bearer {authToken}
Expected: 200 OK
```

Response (nested under `"user"` key):
- `id` == `userID` (matches what we created)
- `created_at`: non-empty string

**Step 3: Refresh JWT token**

```
POST {apiURL}/api/v1/auth/refresh
Headers: Authorization: Bearer {authToken}, Content-Type: application/json
Body: {} (empty)
Expected: 200 OK
```

Response: new `token` string, new `expires_at`.
Update `authToken` to the refreshed token.
Verify the refreshed token is a non-empty string.

**Step 4: Prepare route**

```
POST {apiURL}/api/v1/routes/prepare
Headers: Authorization: Bearer {authToken}, Content-Type: application/json
Body: {}
Expected: 200 OK (or 201 Created)
```

Response:
- `route_id`: non-empty UUID string
- `prepared_at`: non-empty RFC 3339 string

Extract: `routeID`.

**Step 5: Create route with 3 waypoints**

```
POST {apiURL}/api/v1/routes/{routeID}/create-waypoints
Headers: Authorization: Bearer {authToken}, Content-Type: application/json
Body:
{
  "route_id": "{routeID}",
  "name": "Test Integration Route",
  "description": "Created by TestFullAPIBehavioralFlow",
  "visibility": "private",
  "access_method": "open",
  "lifecycle_type": "permanent",
  "owner_type": "anonymous",
  "waypoints": [
    {
      "marker_x": 0.10,
      "marker_y": 0.20,
      "marker_type": "next_step",
      "description": "Waypoint 1 - turn left",
      "image_metadata": {
        "original_filename": "pexels-punttim-240223.jpg",
        "content_type": "image/jpeg",
        "file_size": 905489
      }
    },
    {
      "marker_x": 0.30,
      "marker_y": 0.40,
      "marker_type": "next_step",
      "description": "Waypoint 2 - go straight",
      "image_metadata": {
        "original_filename": "pexels-arthurbrognoli-2260838.jpg",
        "content_type": "image/jpeg",
        "file_size": 786386
      }
    },
    {
      "marker_x": 0.50,
      "marker_y": 0.60,
      "marker_type": "final_destination",
      "description": "Waypoint 3 - you have arrived",
      "image_metadata": {
        "original_filename": "pexels-hikaique-114797.jpg",
        "content_type": "image/jpeg",
        "file_size": 1159336
      }
    }
  ]
}
Expected: 200 OK (or 201 Created)
```

Response assertions (this is the most detailed response to validate):
- `route_id` == `routeID` (matches what we passed)
- `route_status` == `"pending"` (not yet active — images not uploaded)
- `waypoint_ids`: array of exactly 3 non-empty UUID strings
- `presigned_urls`: array of exactly 3 objects, each containing:
  - `image_id`: non-empty UUID string
  - `upload_url`: non-empty string (the gateway upload URL with JWT token)
  - `position`: integer 0, 1, or 2 (one per waypoint)
  - `expires_at`: non-empty RFC 3339 string
- `created_at`: non-empty RFC 3339 string
- `waypoint_count`: integer == 3 (if present)

Extract: `waypointIDs []string`, `presignedURLs []PresignedURLInfo` (where `PresignedURLInfo`
holds `image_id`, `upload_url`, `position`, `expires_at`).

Also record the original marker coordinates for later verification in Step 8:
```go
originalMarkers := []struct{ X, Y float64 }{
    {0.10, 0.20},
    {0.30, 0.40},
    {0.50, 0.60},
}
```

**Step 6: Upload images to gateway via presigned URLs**

For each `presignedURL.upload_url`, parse the JWT token from the URL query parameter `?token=...`
and upload real test image bytes as the raw body. The test images are loaded from the existing
`tests/integration/testdata/` directory using `loadTestImage()` (see Task 4).

Use these specific images for the 3 waypoints (matching the filenames in Step 5):
- Waypoint 0: `pexels-punttim-240223.jpg` (905 KB)
- Waypoint 1: `pexels-arthurbrognoli-2260838.jpg` (786 KB)
- Waypoint 2: `pexels-hikaique-114797.jpg` (1.1 MB)

```
PUT {presignedURL.upload_url}
Content-Type: application/octet-stream (or no Content-Type — gateway uses JWT claims)
Body: loadTestImage("pexels-punttim-240223.jpg") (real JPEG from testdata/)
Expected: 202 Accepted
```

Response body for 202:
```json
{"image_id": "{imageID}", "status": "processing"}
```

Assert:
- `resp.StatusCode == 202`
- Response body `image_id` matches `presignedURL.image_id`
- Response body `status` == `"processing"`

Note: `upload_url` already contains the full URL with `?token=JWT`. Parse the URL to extract just
the token if needed for JWT inspection, but for upload purposes the full URL is used as-is.

**Step 7: Wait for route to transition to ready status**

After uploading all images, poll `GET {apiURL}/api/v1/routes/{routeID}` until `route.route_status`
is `"ready"` or until 60-second timeout:

```go
waitForRouteStatus(t, routeID, "ready", 60*time.Second)
```

(Implementation of `waitForRouteStatus` is in `helpers_test.go` — see Task 4.)

This step validates the complete Valkey messaging loop:
1. Gateway processed images and published results to `image:result` stream
2. follow-api consumer group read the results
3. follow-api updated Image entities and transitioned Route to "ready"

**Step 8: Get route with images and verify marker coordinates**

```
GET {apiURL}/api/v1/routes/{routeID}?include_images=true
Headers: Authorization: Bearer {authToken}
Expected: 200 OK
```

Response assertions:
- `route.route_id` == `routeID`
- `route.route_status` == `"ready"` (confirmed from wait in Step 7)
- `route.waypoint_count` == 3
- `waypoints`: array of exactly 3 waypoints
- `can_navigate` == true
- `images_included` == true
- For each waypoint `i` in `waypoints`:
  - `waypoint_id` is in `waypointIDs`
  - `marker_x` == `originalMarkers[i].X` (coordinates unchanged — scale-invariant)
  - `marker_y` == `originalMarkers[i].Y`
  - `navigation_image_url`: non-empty string (presigned download URL)
  - `image_id`: non-empty string

**Step 9: List routes — verify route appears**

```
GET {apiURL}/api/v1/routes/?route_status=ready&navigable_only=false
Headers: Authorization: Bearer {authToken}
Expected: 200 OK
```

Response assertions:
- `pagination.count` >= 1
- At least one route in `routes` has `route_id` == `routeID`
- That route entry has `route_status` == `"ready"`

**Step 10: Update route metadata**

```
PUT {apiURL}/api/v1/routes/{routeID}
Headers: Authorization: Bearer {authToken}, Content-Type: application/json
Body: {"route_id": "{routeID}", "name": "Updated Integration Route", "visibility": "public"}
Expected: 200 OK
```

Response assertions:
- `route_id` == `routeID`
- `updated_fields`: array containing both `"name"` and `"visibility"`
- `updated_at`: non-empty RFC 3339 string

**Step 11: Update waypoint**

Update the first waypoint's description and marker:

```
PUT {apiURL}/api/v1/routes/{routeID}/waypoints/{waypointIDs[0]}
Headers: Authorization: Bearer {authToken}, Content-Type: application/json
Body:
{
  "route_id": "{routeID}",
  "waypoint_id": "{waypointIDs[0]}",
  "description": "Updated waypoint description",
  "marker_x": 0.15,
  "marker_y": 0.25
}
Expected: 200 OK
```

Response assertions:
- `waypoint_id` == `waypointIDs[0]`
- `route_id` == `routeID`
- `updated_fields`: array containing `"description"`, `"marker_x"`, `"marker_y"`
- `updated_at`: non-empty RFC 3339 string

**Step 12: Replace waypoint image (prepare + upload + confirm)**

Prepare:
```
POST {apiURL}/api/v1/routes/{routeID}/waypoints/{waypointIDs[1]}/replace-image/prepare
Headers: Authorization: Bearer {authToken}, Content-Type: application/json
Body:
{
  "route_id": "{routeID}",
  "waypoint_id": "{waypointIDs[1]}",
  "file_name": "pexels-tuurt-2954405.jpg",
  "file_size_bytes": 1400255,
  "content_type": "image/jpeg"
}
Expected: 200 OK
```

Response assertions:
- `image_id`: non-empty UUID string (the replacement image ID)
- `upload_url`: non-empty string (gateway URL with JWT token)
- `expires_at`: non-empty RFC 3339 string

Upload replacement:
```
PUT {upload_url}  (from prepare response)
Body: loadTestImage("pexels-tuurt-2954405.jpg")  (real JPEG from testdata/, 1.4 MB)
Expected: 202 Accepted
```

Confirm:
```
POST {apiURL}/api/v1/routes/{routeID}/waypoints/{waypointIDs[1]}/replace-image/confirm
Headers: Authorization: Bearer {authToken}, Content-Type: application/json
Body:
{
  "route_id": "{routeID}",
  "waypoint_id": "{waypointIDs[1]}",
  "image_id": "{image_id from prepare}",
  "file_hash": "a665a45920422f9d417e4867efdc4fb8a04a1f3fff1fa07e998e86f7f7a27ae3",
  "marker_x": 0.55,
  "marker_y": 0.65
}
Expected: 200 OK
```

Note on `file_hash`: The current architecture (pre-gateway-integration) has follow-api storing the
hash as provided by the client. The value used here is a well-formed 64-hex-character SHA256 string.
After gateway integration, the API may derive the hash from the gateway's result rather than the
client — if the confirm endpoint rejects the client-provided hash, this step should be adjusted or
the hash should be computed from the real test image bytes using `crypto/sha256`.

SHA256 of the replacement test image bytes can be computed at test time:
```go
replacementBytes := loadTestImage("pexels-tuurt-2954405.jpg")
h := sha256.Sum256(replacementBytes)
fileHash := hex.EncodeToString(h[:])
```

Response assertions:
- `waypoint_id` == `waypointIDs[1]`
- `route_id` == `routeID`
- `image_id` == `{image_id from prepare}` (new image ID)
- `old_image_id`: non-empty UUID (the previous image, if field is present)
- `marker_x` == 0.55
- `marker_y` == 0.65
- `replaced_at`: non-empty RFC 3339 string

**Step 13: Delete route**

Cleanup — delete the test route:
```
DELETE {apiURL}/api/v1/routes/{routeID}
Headers: Authorization: Bearer {authToken}
Expected: 200 OK
```

Response assertions:
- `route_id` == `routeID`
- `waypoints_deleted` == 3 (all waypoints)
- `removed_from_database` == true
- `removed_from_storage` == true (images cleaned up from MinIO)
- `deleted_at`: non-empty RFC 3339 string

Use `t.Cleanup` to guarantee deletion even if earlier steps fail:
```go
t.Cleanup(func() {
    // Best-effort delete, ignore errors
    deleteRoute(routeID, authToken)
})
```

Register the cleanup BEFORE Step 5 (after creating the route) so it runs even if Steps 6-12 fail.

### Files Affected

**Created:**
- `/home/yoseforb/pkg/follow/tests/integration/behavioral_flow_test.go`

### Dependencies

- Task 1 (main_test.go, module setup)
- Task 4 (helpers_test.go — `waitForRouteStatus`, `loadTestImage`)

Note: Task 3 and Task 4 should be implemented together since Task 3 depends on helpers defined
in Task 4.

### Acceptance Criteria

- `TestFullAPIBehavioralFlow` passes end-to-end in local mode with running services
- All 13 steps execute in order; early failures via `require` stop the test clearly
- Route is cleaned up even when intermediate steps fail (via `t.Cleanup`)
- Test verifies marker coordinates are unchanged after gateway processing (scale-invariant)
- No hardcoded sleep — all waiting uses `waitForRouteStatus` with timeout polling
- Test runs in under 90 seconds end-to-end (60s wait + 30s for other steps)

### Story Points: 5

---

## Task 4: Shared Helpers

### Title
Implement `helpers_test.go` — all shared HTTP, JWT, Valkey, image generation, and polling helpers

### Description

All helpers used across multiple test files live in `helpers_test.go`. They are not standalone
tests but `_test.go` functions that are available to all test files in the package.

**File:** `/home/yoseforb/pkg/follow/tests/integration/helpers_test.go`
**Build tag:** `//go:build integration`
**Package:** `package integration_test`

**Image Generation:**

```go
// smallJPEG returns a minimal valid JPEG byte slice (1x1 pixel white JPEG).
// Copied from follow-image-gateway/tests/integration/main_test.go.
func smallJPEG() []byte {
    return []byte{
        0xFF, 0xD8, // SOI
        0xFF, 0xE0, // APP0 marker
        0x00, 0x10, // APP0 length
        0x4A, 0x46, 0x49, 0x46, 0x00, // JFIF identifier
        0x01, 0x01, // Version 1.1
        0x00,       // Aspect ratio units: no units
        0x00, 0x01, // X density: 1
        0x00, 0x01, // Y density: 1
        0x00, 0x00, // Thumbnail: 0x0
        0xFF, 0xD9, // EOI
    }
}

// invalidImageBytes returns bytes that are not a valid image.
// Used to test failure propagation.
func invalidImageBytes() []byte {
    return []byte("this is definitely not an image file %^&*!")
}

// sha256Hex computes the SHA256 hash of data and returns it as a
// lowercase hex string (64 characters).
func sha256Hex(data []byte) string {
    h := sha256.Sum256(data)
    return hex.EncodeToString(h[:])
}
```

**HTTP API Helpers:**

All helpers take `t *testing.T` as first argument and call `t.Helper()`. They use the package-level
`apiURL` and `gatewayURL` globals.

```go
// doRequest sends an HTTP request and returns the response.
// Calls t.Fatal on transport errors.
func doRequest(
    t *testing.T,
    method, url string,
    body interface{},
    authToken string,
) *http.Response

// Encodes body as JSON if non-nil, sends request, returns response.
// Sets Content-Type: application/json and Authorization: Bearer {authToken} if non-empty.
```

```go
// createAnonymousUser calls POST /api/v1/users/anonymous.
// Returns user_id and JWT token.
func createAnonymousUser(t *testing.T) (userID, token string)
```

```go
// prepareRoute calls POST /api/v1/routes/prepare.
// Returns route_id string.
func prepareRoute(t *testing.T, authToken string) string
```

```go
// createRouteWithWaypoints calls POST /api/v1/routes/{routeID}/create-waypoints.
// Returns the full response decoded into CreateRouteResponse.
type PresignedURLEntry struct {
    ImageID   string  `json:"image_id"`
    UploadURL string  `json:"upload_url"`
    Position  int     `json:"position"`
    ExpiresAt string  `json:"expires_at"`
}
type CreateRouteResponse struct {
    RouteID      string             `json:"route_id"`
    RouteStatus  string             `json:"route_status"`
    WaypointIDs  []string           `json:"waypoint_ids"`
    PresignedURLs []PresignedURLEntry `json:"presigned_urls"`
    CreatedAt    string             `json:"created_at"`
    WaypointCount int               `json:"waypoint_count"`
}
func createRouteWithWaypoints(
    t *testing.T,
    authToken, routeID string,
    waypointCount int,
) CreateRouteResponse
// Uses marker coordinates: {0.10, 0.20}, {0.30, 0.40}, {0.50, 0.60} for first 3 waypoints.
// Uses default image_metadata: content_type=image/jpeg, file_size=2048.
```

```go
// uploadToGateway sends a PUT request to the full upload URL (which includes ?token=JWT).
// Returns the HTTP response.
func uploadToGateway(t *testing.T, uploadURL string, imageBytes []byte) *http.Response
// Uses http.MethodPut with bytes.NewReader(imageBytes) as body.
// Does NOT set Content-Type (gateway derives it from JWT claims).
```

```go
// deleteRoute calls DELETE /api/v1/routes/{routeID}.
// Best-effort — does not call t.Fatal on failure, only t.Log.
func deleteRoute(t *testing.T, routeID, authToken string)
```

**Polling Helpers:**

```go
// waitForRouteStatus polls GET /api/v1/routes/{routeID} every 1s until
// the route's route_status matches expectedStatus or timeout expires.
// Calls t.Fatalf on timeout.
func waitForRouteStatus(
    t *testing.T,
    routeID, authToken, expectedStatus string,
    timeout time.Duration,
)
// Parse response: result["route"].(map[string]interface{})["route_status"]
```

```go
// waitForImageStatus polls Valkey HGETALL image:status:{imageID} every 200ms
// until the "stage" field matches expectedStage or timeout expires.
// Calls t.Fatalf on timeout.
func waitForImageStatus(
    t *testing.T,
    valkeyClient valkeygo.Client,
    imageID, expectedStage string,
    timeout time.Duration,
)
```

```go
// newValkeyClient creates a valkey-io/valkey-go client connected to valkeyAddress.
// Registers t.Cleanup to close. Called at start of tests that need direct Valkey access.
func newValkeyClient(t *testing.T) valkeygo.Client {
    t.Helper()
    client, err := valkeygo.NewClient(valkeygo.ClientOption{
        InitAddress:  []string{valkeyAddress},
        DisableCache: true,
    })
    require.NoError(t, err)
    t.Cleanup(func() { client.Close() })
    return client
}
```

**Valkey Inspection Helpers:**

These use a passed-in `valkeygo.Client` (not the wrapper):

```go
// hGetAll reads a Valkey hash and returns all fields.
func hGetAll(
    t *testing.T,
    client valkeygo.Client,
    key string,
) map[string]string

// setNXExists checks whether a Valkey string key exists (for upload guard verification).
// Returns true if key exists.
func keyExists(
    t *testing.T,
    client valkeygo.Client,
    key string,
) bool

// xReadGroupNoAck reads messages from a stream consumer group WITHOUT acknowledging them.
// Returns the messages read. Used in recovery test (Task 8) to simulate a consumer crash.
func xReadGroupNoAck(
    t *testing.T,
    client valkeygo.Client,
    streamKey, group, consumer string,
    count int64,
) []streamMessage

// xAck acknowledges messages in a stream consumer group.
func xAck(
    t *testing.T,
    client valkeygo.Client,
    streamKey, group string,
    ids ...string,
)

// xPendingCount returns the number of pending messages in a consumer group.
func xPendingCount(
    t *testing.T,
    client valkeygo.Client,
    streamKey, group string,
) int64

// xAutoClaim reads pending messages older than minIdleTime and re-claims them
// for newConsumer. Returns the claimed messages.
func xAutoClaim(
    t *testing.T,
    client valkeygo.Client,
    streamKey, group, newConsumer string,
    minIdleTime time.Duration,
    count int64,
) []streamMessage

type streamMessage struct {
    ID     string
    Fields map[string]string
}
```

Implementation note for Valkey helpers: The test module uses `valkey-io/valkey-go` directly.
The builder pattern for commands:
```go
// HGETALL
cmd := client.B().Hgetall().Key(key).Build()
result, err := client.Do(ctx, cmd).AsStrMap()

// EXISTS
cmd := client.B().Exists().Key(key).Build()
count, err := client.Do(ctx, cmd).AsInt64()
exists := count > 0

// XREADGROUP
cmd := client.B().Xreadgroup().
    Group(group, consumer).
    Count(count).
    Streams().
    Key(streamKey).
    Id(">").
    Build()
messages, err := client.Do(ctx, cmd).AsXRead()

// XACK
cmd := client.B().Xack().
    Key(streamKey).
    Group(group).
    Id(ids...).
    Build()
client.Do(ctx, cmd)

// XPENDING (summary form)
cmd := client.B().Xpending().Key(streamKey).Group(group).Build()
info, err := client.Do(ctx, cmd).AsXPending()

// XAUTOCLAIM
cmd := client.B().Xautoclaim().
    Key(streamKey).
    Group(group).
    Consumer(newConsumer).
    MinIdleTime(minIdleTime.Milliseconds()).
    Start("0-0").
    Count(count).
    Build()
result, err := client.Do(ctx, cmd).AsXAutoClaim()
```

**SSE Helper:**

```go
type SSEEvent struct {
    Type string
    Data string
    ID   string
}

// readSSEEvents reads Server-Sent Events from an io.Reader until the context
// is cancelled or the reader returns io.EOF.
// Events are sent to the events channel.
func readSSEEvents(
    ctx context.Context,
    reader io.Reader,
    events chan<- SSEEvent,
)
// Parse SSE format:
//   event: <type>\n
//   data: <json>\n
//   \n
// Emit SSEEvent{Type, Data} for each complete event block.
// Empty event type defaults to "message".
```

### Files Affected

**Created:**
- `/home/yoseforb/pkg/follow/tests/integration/helpers_test.go`

### Dependencies

- Task 1 (module and globals in main_test.go)

### Acceptance Criteria

- All helpers compile with `//go:build integration`
- `smallJPEG()` returns exactly the same bytes as in follow-image-gateway integration tests
- `waitForRouteStatus` polls correctly and fails with a clear message on timeout
- `waitForImageStatus` polls Valkey at 200ms intervals (not 1s — stage transitions are fast)
- `newValkeyClient` registers cleanup via `t.Cleanup`
- No helpers import follow-api or follow-image-gateway packages

### Story Points: 3

---

## Task 5: Upload Guard Test

### Title
Implement `upload_guard_test.go` — verify Valkey SET NX prevents duplicate image uploads

### Description

**File:** `/home/yoseforb/pkg/follow/tests/integration/upload_guard_test.go`
**Build tag:** `//go:build integration`
**Package:** `package integration_test`

**Test: `TestValkeyUploadGuard_PreventsDuplicateUploads`**

The upload guard uses `SET NX EX` at key `image:upload:{image_id}` (TTL 1 hour) to ensure each
image is only processed once. The gateway sets this key atomically before beginning pipeline
processing. A second upload with the same token should be rejected with 409 Conflict.

```go
func TestValkeyUploadGuard_PreventsDuplicateUploads(t *testing.T) {
    // Setup
    userID, token := createAnonymousUser(t)
    _ = userID
    routeID := prepareRoute(t, token)
    route := createRouteWithWaypoints(t, token, routeID, 2)
    t.Cleanup(func() { deleteRoute(t, routeID, token) })

    // Use the first upload URL
    uploadEntry := route.PresignedURLs[0]
    vc := newValkeyClient(t)

    // First upload: should succeed
    resp1 := uploadToGateway(t, uploadEntry.UploadURL, smallJPEG())
    require.Equal(t, http.StatusAccepted, resp1.StatusCode,
        "first upload should return 202 Accepted")
    resp1.Body.Close()

    // Verify upload guard key exists in Valkey
    guardKey := "image:upload:" + uploadEntry.ImageID
    require.Eventually(t,
        func() bool { return keyExists(t, vc, guardKey) },
        5*time.Second,
        100*time.Millisecond,
        "upload guard key should exist after first upload",
    )

    // Second upload with SAME token: should be rejected
    resp2 := uploadToGateway(t, uploadEntry.UploadURL, smallJPEG())
    require.Equal(t, http.StatusConflict, resp2.StatusCode,
        "duplicate upload should return 409 Conflict")
    resp2.Body.Close()

    // Verify upload guard key still exists (not consumed by rejection)
    assert.True(t, keyExists(t, vc, guardKey),
        "upload guard key should still exist after rejected duplicate")
}
```

**Test: `TestValkeyUploadGuard_DifferentImagesAccepted`**

Verifies that a different image ID (different upload URL / different JWT) is accepted even if
another image from the same route was just uploaded:

```go
func TestValkeyUploadGuard_DifferentImagesAccepted(t *testing.T) {
    userID, token := createAnonymousUser(t)
    _ = userID
    routeID := prepareRoute(t, token)
    route := createRouteWithWaypoints(t, token, routeID, 2)
    t.Cleanup(func() { deleteRoute(t, routeID, token) })

    // Upload first image
    resp1 := uploadToGateway(t, route.PresignedURLs[0].UploadURL, smallJPEG())
    require.Equal(t, http.StatusAccepted, resp1.StatusCode)
    resp1.Body.Close()

    // Upload second image (different image_id) — should also succeed
    resp2 := uploadToGateway(t, route.PresignedURLs[1].UploadURL, smallJPEG())
    require.Equal(t, http.StatusAccepted, resp2.StatusCode,
        "uploading a different image should succeed even if another was already uploaded")
    resp2.Body.Close()
}
```

### Files Affected

**Created:**
- `/home/yoseforb/pkg/follow/tests/integration/upload_guard_test.go`

### Dependencies

- Task 1 (main_test.go)
- Task 4 (helpers_test.go)

### Acceptance Criteria

- `TestValkeyUploadGuard_PreventsDuplicateUploads` passes: first upload returns 202, second returns
  409
- `TestValkeyUploadGuard_DifferentImagesAccepted` passes: two different images both return 202
- Upload guard key `image:upload:{image_id}` is verified to exist in Valkey after first upload
- Tests clean up created routes via `t.Cleanup`

### Story Points: 2

---

## Task 6: Progress Tracking Test

### Title
Implement `progress_tracking_test.go` — verify `image:status:{id}` hash transitions through
pipeline stages

### Description

**File:** `/home/yoseforb/pkg/follow/tests/integration/progress_tracking_test.go`
**Build tag:** `//go:build integration`
**Package:** `package integration_test`

**Test: `TestValkeyProgressTracking_InitialStatusSetByAPI`**

Verifies that follow-api writes `{stage: "queued"}` to `image:status:{image_id}` immediately when
creating a route (before any upload occurs):

```go
func TestValkeyProgressTracking_InitialStatusSetByAPI(t *testing.T) {
    userID, token := createAnonymousUser(t)
    _ = userID
    routeID := prepareRoute(t, token)
    route := createRouteWithWaypoints(t, token, routeID, 2)
    t.Cleanup(func() { deleteRoute(t, routeID, token) })

    vc := newValkeyClient(t)

    // Verify each image has initial "queued" status set by API
    for _, urlEntry := range route.PresignedURLs {
        statusKey := "image:status:" + urlEntry.ImageID
        fields := hGetAll(t, vc, statusKey)
        assert.Equal(t, "queued", fields["stage"],
            "API should write stage=queued to image:status:{id} on route creation")
    }
}
```

**Test: `TestValkeyProgressTracking_StageTransitionsOnUpload`**

Verifies that the gateway updates `image:status:{image_id}` hash as the image moves through
pipeline stages:

```go
func TestValkeyProgressTracking_StageTransitionsOnUpload(t *testing.T) {
    userID, token := createAnonymousUser(t)
    _ = userID
    routeID := prepareRoute(t, token)
    route := createRouteWithWaypoints(t, token, routeID, 2)
    t.Cleanup(func() { deleteRoute(t, routeID, token) })

    imageID := route.PresignedURLs[0].ImageID
    vc := newValkeyClient(t)

    // Upload image
    resp := uploadToGateway(t, route.PresignedURLs[0].UploadURL, smallJPEG())
    require.Equal(t, http.StatusAccepted, resp.StatusCode)
    resp.Body.Close()

    // Poll hash for stage transitions
    // Expected stages in order (gateway pipeline):
    //   queued (set by API) -> validating -> analyzing -> transforming ->
    //   uploading_to_storage -> done
    // Not all intermediate stages may be observable (they transition quickly),
    // but "done" must be reached.
    seenStages := make(map[string]bool)
    statusKey := "image:status:" + imageID

    deadline := time.Now().Add(30 * time.Second)
    ticker := time.NewTicker(200 * time.Millisecond)
    defer ticker.Stop()

    for time.Now().Before(deadline) {
        <-ticker.C
        fields := hGetAll(t, vc, statusKey)
        if stage, ok := fields["stage"]; ok {
            seenStages[stage] = true
            if stage == "done" {
                break
            }
        }
    }

    // Must have reached terminal "done" state
    assert.True(t, seenStages["done"],
        "image:status hash must reach stage=done after processing")

    // Verify the hash contains meaningful completion data
    finalFields := hGetAll(t, vc, statusKey)
    assert.Equal(t, "done", finalFields["stage"])
    // The hash may also contain "updated_at" or similar — assert non-empty stage is sufficient.
}
```

Note on stage names: The exact stage names written to the hash depend on the gateway's
`ProgressTracker` implementation in follow-pkg. The stage names listed above are drawn from
the master plan's description. The implementation agent should verify the actual stage names
by checking:
- `/home/yoseforb/pkg/follow/follow-pkg/valkey/progress.go` (ProgressTracker)
- `/home/yoseforb/pkg/follow/follow-image-gateway/internal/pipeline/stages/` (stage implementations)

If the actual stage names differ, update the test's stage name strings accordingly.

**Test: `TestValkeyProgressTracking_TTLSet`**

Verifies that the `image:status:{id}` hash key has a TTL (it must expire eventually — we do not
want orphan keys):

```go
func TestValkeyProgressTracking_TTLSet(t *testing.T) {
    userID, token := createAnonymousUser(t)
    _ = userID
    routeID := prepareRoute(t, token)
    route := createRouteWithWaypoints(t, token, routeID, 2)
    t.Cleanup(func() { deleteRoute(t, routeID, token) })

    vc := newValkeyClient(t)
    imageID := route.PresignedURLs[0].ImageID
    statusKey := "image:status:" + imageID

    // Use TTL command to verify the key has an expiration
    cmd := vc.B().Ttl().Key(statusKey).Build()
    ttlSeconds, err := vc.Do(context.Background(), cmd).AsInt64()
    require.NoError(t, err)
    assert.Greater(t, ttlSeconds, int64(0),
        "image:status key should have a TTL set")
    assert.LessOrEqual(t, ttlSeconds, int64(3600),
        "image:status TTL should be at most 1 hour (3600 seconds)")
}
```

### Files Affected

**Created:**
- `/home/yoseforb/pkg/follow/tests/integration/progress_tracking_test.go`

### Dependencies

- Task 1 (main_test.go)
- Task 4 (helpers_test.go)

### Acceptance Criteria

- `TestValkeyProgressTracking_InitialStatusSetByAPI` confirms `stage=queued` is present before any
  upload
- `TestValkeyProgressTracking_StageTransitionsOnUpload` confirms `stage=done` is eventually reached
  within 30 seconds
- `TestValkeyProgressTracking_TTLSet` confirms the key has a TTL between 1 and 3600 seconds
- All tests clean up routes via `t.Cleanup`

### Story Points: 2

---

## Task 7: Failure Propagation Test

### Title
Implement `failure_propagation_test.go` — verify invalid images produce failure status that
propagates from gateway to API

### Description

**File:** `/home/yoseforb/pkg/follow/tests/integration/failure_propagation_test.go`
**Build tag:** `//go:build integration`
**Package:** `package integration_test`

**Test: `TestValkeyFailurePropagation_InvalidImageRejectedByGateway`**

Uploads bytes that are not a valid image. The gateway's validate stage should reject them, publish
a failure result to the `image:result` stream, and the API should consume the failure and mark the
image as FAILED.

The test does NOT query PostgreSQL directly — it verifies failure via the API's status response or
route status.

```go
func TestValkeyFailurePropagation_InvalidImageRejectedByGateway(t *testing.T) {
    userID, token := createAnonymousUser(t)
    _ = userID
    routeID := prepareRoute(t, token)
    route := createRouteWithWaypoints(t, token, routeID, 2)
    t.Cleanup(func() { deleteRoute(t, routeID, token) })

    imageID := route.PresignedURLs[0].ImageID
    vc := newValkeyClient(t)

    // Upload invalid bytes
    resp := uploadToGateway(t, route.PresignedURLs[0].UploadURL, invalidImageBytes())
    // Gateway may accept the upload at the HTTP level (202) and then fail during pipeline
    // processing, OR it may reject immediately (400/415). Both are valid behaviors.
    // We only care about the eventual Valkey status.
    resp.Body.Close()

    // Wait for failure status in Valkey
    statusKey := "image:status:" + imageID
    waitForImageStatus(t, vc, imageID, "failed", 30*time.Second)

    // Verify the hash has error information
    fields := hGetAll(t, vc, statusKey)
    assert.Equal(t, "failed", fields["stage"])
    // May contain "error_code" field — assert non-empty if present
    if errCode, ok := fields["error_code"]; ok {
        assert.NotEmpty(t, errCode)
    }
}
```

**Test: `TestValkeyFailurePropagation_FailureStreamMessage`**

Verifies that the gateway publishes a failure message to the `image:result` stream when processing
fails. Reads from the stream using a test consumer name that does NOT conflict with the API's
`api-workers` group (this would steal the message from the real consumer).

Important: The `api-workers` consumer group is created and managed by follow-api. The test must NOT
read from the `api-workers` group because that would steal messages from follow-api's consumer and
prevent it from processing results. Instead, this test creates a separate consumer group
`test-observers` on the `image:result` stream:

```go
func TestValkeyFailurePropagation_FailureStreamMessage(t *testing.T) {
    userID, token := createAnonymousUser(t)
    _ = userID
    routeID := prepareRoute(t, token)
    route := createRouteWithWaypoints(t, token, routeID, 2)
    t.Cleanup(func() { deleteRoute(t, routeID, token) })

    vc := newValkeyClient(t)
    imageID := route.PresignedURLs[0].ImageID
    observerGroup := "test-observers-" + imageID[:8] // Unique per test run

    // Create observer consumer group BEFORE upload (to capture the message)
    // Use "$" as start to only read messages published after group creation.
    // Actually use "0" to read all messages from beginning:
    ctx := context.Background()
    _ = vc.Do(ctx,
        vc.B().XgroupCreate().
            Key("image:result").
            Group(observerGroup).
            Id("0").
            Mkstream().Build()).Error()
    // Note: group creation may fail if stream does not exist yet — that's ok,
    // retry or use the MKSTREAM option.

    t.Cleanup(func() {
        // Delete the test observer group after the test
        vc.Do(context.Background(),
            vc.B().XgroupDestroy().
                Key("image:result").
                Group(observerGroup).Build())
    })

    // Upload invalid image
    resp := uploadToGateway(t, route.PresignedURLs[0].UploadURL, invalidImageBytes())
    resp.Body.Close()

    // Wait for failure status
    waitForImageStatus(t, vc, imageID, "failed", 30*time.Second)

    // Read from observer group to verify failure message exists in stream
    deadline := time.Now().Add(15 * time.Second)
    var failureMsg streamMessage
    found := false
    for !found && time.Now().Before(deadline) {
        messages := xReadGroupNoAck(t, vc, "image:result", observerGroup,
            "test-observer", 10)
        for _, msg := range messages {
            if msg.Fields["image_id"] == imageID &&
                msg.Fields["status"] == "failed" {
                failureMsg = msg
                found = true
                break
            }
        }
        if !found {
            time.Sleep(500 * time.Millisecond)
        }
    }

    require.True(t, found,
        "failure message for image %s should appear in image:result stream", imageID)
    assert.Equal(t, "failed", failureMsg.Fields["status"])
    assert.Equal(t, imageID, failureMsg.Fields["image_id"])
    assert.NotEmpty(t, failureMsg.Fields["error_code"],
        "failure message should include error_code")
}
```

### Files Affected

**Created:**
- `/home/yoseforb/pkg/follow/tests/integration/failure_propagation_test.go`

### Dependencies

- Task 1 (main_test.go)
- Task 4 (helpers_test.go — `invalidImageBytes`, `waitForImageStatus`, `xReadGroupNoAck`)

### Acceptance Criteria

- `TestValkeyFailurePropagation_InvalidImageRejectedByGateway` passes: `image:status` hash reaches
  `stage=failed` within 30 seconds after invalid upload
- `TestValkeyFailurePropagation_FailureStreamMessage` passes: failure message appears in
  `image:result` stream with `image_id` and `status=failed` fields
- Tests do NOT steal messages from the `api-workers` consumer group
- Test observer groups are cleaned up via `t.Cleanup`

### Story Points: 3

---

## Task 8: SSE Event Streaming Test

### Title
Implement `sse_streaming_test.go` — verify real-time Server-Sent Events from follow-api

### Description

**File:** `/home/yoseforb/pkg/follow/tests/integration/sse_streaming_test.go`
**Build tag:** `//go:build integration`
**Package:** `package integration_test`

The SSE endpoint is `GET /api/v1/routes/{route_id}/status/stream`. According to the design DSL
(`route_service.go`), it streams `RouteStatusEvent` with the following event types:
- `processing` — image is being processed
- `ready` — individual image reached ready state
- `failed` — individual image failed
- `heartbeat` — keep-alive ping
- `complete` — all images reached terminal state

The endpoint uses Server-Sent Events (SSE) format:
```
event: processing\r\n
data: {"image_id": "...", "status": "processing", "timestamp": "..."}\r\n
\r\n
```

**Test: `TestSSEStreaming_ReceivesProcessingEvents`**

```go
func TestSSEStreaming_ReceivesProcessingEvents(t *testing.T) {
    userID, token := createAnonymousUser(t)
    _ = userID
    routeID := prepareRoute(t, token)
    route := createRouteWithWaypoints(t, token, routeID, 2)
    t.Cleanup(func() { deleteRoute(t, routeID, token) })

    // Connect SSE client BEFORE uploading images
    sseURL := fmt.Sprintf("%s/api/v1/routes/%s/status/stream", apiURL, routeID)
    ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
    defer cancel()

    req, err := http.NewRequestWithContext(ctx, http.MethodGet, sseURL, nil)
    require.NoError(t, err)
    req.Header.Set("Authorization", "Bearer "+token)
    req.Header.Set("Accept", "text/event-stream")
    req.Header.Set("Cache-Control", "no-cache")

    // Use http.Client with no timeout (streaming response)
    sseClient := &http.Client{Timeout: 0}
    resp, err := sseClient.Do(req)
    require.NoError(t, err)
    defer resp.Body.Close()

    require.Equal(t, http.StatusOK, resp.StatusCode,
        "SSE endpoint should return 200 OK")
    assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"),
        "SSE response Content-Type should be text/event-stream")

    // Collect events in background goroutine
    events := make(chan SSEEvent, 20)
    go readSSEEvents(ctx, resp.Body, events)

    // Upload images to trigger processing
    for _, entry := range route.PresignedURLs {
        uploadResp := uploadToGateway(t, entry.UploadURL, smallJPEG())
        uploadResp.Body.Close()
    }

    // Collect events until we see "complete" or context times out
    seenEventTypes := make(map[string]bool)
    deadline := time.After(55 * time.Second)
    for {
        select {
        case event := <-events:
            seenEventTypes[event.Type] = true
            if event.Type == "complete" {
                goto doneCollecting
            }
        case <-deadline:
            t.Log("collected event types:", seenEventTypes)
            t.Fatal("timeout waiting for SSE complete event")
        }
    }
doneCollecting:

    // Verify we saw at least some processing events
    // "heartbeat" is optional (timing-dependent), "complete" must be seen
    assert.True(t, seenEventTypes["complete"],
        "should receive complete event when all images are processed")
    // Should see at least one of: processing, ready
    sawProgressEvent := seenEventTypes["processing"] ||
        seenEventTypes["ready"] ||
        seenEventTypes["heartbeat"]
    assert.True(t, sawProgressEvent,
        "should receive at least one progress event before complete")
}
```

**Test: `TestSSEStreaming_HeartbeatsReceived`**

Verifies that the SSE endpoint sends heartbeat events periodically (the API polls Valkey every
500ms and emits heartbeats to keep the connection alive):

```go
func TestSSEStreaming_HeartbeatsReceived(t *testing.T) {
    userID, token := createAnonymousUser(t)
    _ = userID
    routeID := prepareRoute(t, token)
    // Create a route with 2 waypoints but do NOT upload images
    _ = createRouteWithWaypoints(t, token, routeID, 2)
    t.Cleanup(func() { deleteRoute(t, routeID, token) })

    sseURL := fmt.Sprintf("%s/api/v1/routes/%s/status/stream", apiURL, routeID)
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    req, err := http.NewRequestWithContext(ctx, http.MethodGet, sseURL, nil)
    require.NoError(t, err)
    req.Header.Set("Authorization", "Bearer "+token)
    req.Header.Set("Accept", "text/event-stream")

    sseClient := &http.Client{Timeout: 0}
    resp, err := sseClient.Do(req)
    require.NoError(t, err)
    defer resp.Body.Close()

    events := make(chan SSEEvent, 10)
    go readSSEEvents(ctx, resp.Body, events)

    // Wait up to 6 seconds for a heartbeat event (API sends every ~500ms)
    heartbeatDeadline := time.After(6 * time.Second)
    for {
        select {
        case event := <-events:
            if event.Type == "heartbeat" {
                return // Heartbeat received — test passes
            }
        case <-heartbeatDeadline:
            t.Fatal("expected heartbeat event within 6 seconds")
        }
    }
}
```

### Files Affected

**Created:**
- `/home/yoseforb/pkg/follow/tests/integration/sse_streaming_test.go`

### Dependencies

- Task 1 (main_test.go)
- Task 4 (helpers_test.go — `readSSEEvents`, `SSEEvent`)

### Acceptance Criteria

- `TestSSEStreaming_ReceivesProcessingEvents` receives a `complete` event after uploading all images
- `TestSSEStreaming_HeartbeatsReceived` receives at least one `heartbeat` event within 6 seconds
- SSE response Content-Type is `text/event-stream`
- Tests do not hang indefinitely — context with timeout ensures they exit

### Story Points: 3

---

## Task 9: Recovery Test

### Title
Implement `recovery_test.go` — verify pending messages are re-read after consumer restart

### Description

**File:** `/home/yoseforb/pkg/follow/tests/integration/recovery_test.go`
**Build tag:** `//go:build integration`
**Package:** `package integration_test`

This test verifies that Valkey's Pending Entries List (PEL) correctly holds messages that were
delivered but not acknowledged. It uses a separate observer consumer group so it does NOT interfere
with the `api-workers` group that follow-api manages.

**Test: `TestValkeyRecovery_PendingMessageRetained`**

```go
func TestValkeyRecovery_PendingMessageRetained(t *testing.T) {
    userID, token := createAnonymousUser(t)
    _ = userID
    routeID := prepareRoute(t, token)
    route := createRouteWithWaypoints(t, token, routeID, 2)
    t.Cleanup(func() { deleteRoute(t, routeID, token) })

    vc := newValkeyClient(t)
    imageID := route.PresignedURLs[0].ImageID
    testGroup := "test-recovery-" + imageID[:8]

    // Create a dedicated observer group starting from "0" (read all history)
    ctx := context.Background()
    err := vc.Do(ctx,
        vc.B().XgroupCreate().
            Key("image:result").
            Group(testGroup).
            Id("0").
            Mkstream().Build()).Error()
    // Ignore BUSYGROUP error (group already exists from a previous test run)
    t.Cleanup(func() {
        vc.Do(context.Background(),
            vc.B().XgroupDestroy().
                Key("image:result").
                Group(testGroup).Build())
    })

    // Upload image and wait for gateway to process
    resp := uploadToGateway(t, route.PresignedURLs[0].UploadURL, smallJPEG())
    require.Equal(t, http.StatusAccepted, resp.StatusCode)
    resp.Body.Close()

    // Wait for gateway to publish result to stream
    waitForImageStatus(t, vc, imageID, "done", 30*time.Second)

    // Read message WITHOUT acking — simulates consumer crash
    consumer1 := "crash-consumer-1"
    messages := xReadGroupNoAck(t, vc, "image:result", testGroup, consumer1, 10)

    // Find our specific message
    var ourMsgID string
    for _, msg := range messages {
        if msg.Fields["image_id"] == imageID {
            ourMsgID = msg.ID
            break
        }
    }
    require.NotEmpty(t, ourMsgID,
        "should have read result message for image %s", imageID)

    // Verify message is in PEL (pending, not acknowledged)
    pendingCount := xPendingCount(t, vc, "image:result", testGroup)
    assert.GreaterOrEqual(t, pendingCount, int64(1),
        "unacknowledged message should be in PEL")

    // Simulate consumer restart: new consumer claims the pending message
    // after it has been idle long enough for XAUTOCLAIM threshold
    // (use minIdleTime=0 to claim immediately for testing purposes)
    time.Sleep(100 * time.Millisecond) // Small wait before autoclaim
    consumer2 := "restart-consumer-2"
    claimed := xAutoClaim(t, vc, "image:result", testGroup, consumer2,
        0, // minIdleTime: 0ms for test purposes
        10)

    // Verify our message was re-claimed
    var reclaimedMsg streamMessage
    for _, msg := range claimed {
        if msg.ID == ourMsgID {
            reclaimedMsg = msg
            break
        }
    }
    require.NotEmpty(t, reclaimedMsg.ID,
        "pending message should be re-claimed by new consumer")
    assert.Equal(t, imageID, reclaimedMsg.Fields["image_id"])

    // Ack the message to clean up
    xAck(t, vc, "image:result", testGroup, reclaimedMsg.ID)

    // Verify PEL is now empty (or reduced by 1)
    afterAckPending := xPendingCount(t, vc, "image:result", testGroup)
    assert.Equal(t, pendingCount-1, afterAckPending,
        "PEL count should decrease by 1 after ack")
}
```

Note on `minIdleTime=0`: Using 0 milliseconds for `XAUTOCLAIM` in tests means we claim messages
immediately without waiting for the idle timeout. In production, the Reclaimer in follow-pkg uses
a longer `minIdleTime` (e.g., 5 minutes). For testing, 0 is safe because we control the only
consumer in this group.

### Files Affected

**Created:**
- `/home/yoseforb/pkg/follow/tests/integration/recovery_test.go`

### Dependencies

- Task 1 (main_test.go)
- Task 4 (helpers_test.go — `xReadGroupNoAck`, `xPendingCount`, `xAutoClaim`, `xAck`)

### Acceptance Criteria

- `TestValkeyRecovery_PendingMessageRetained` passes: message remains in PEL after read-without-ack
- XAUTOCLAIM correctly transfers the message to the second consumer
- After ack, PEL count decreases
- Test uses a dedicated group that does NOT interfere with `api-workers`
- Cleanup destroys the test consumer group via `t.Cleanup`

### Story Points: 2

---

## Task 10: Service Health Valkey Test

### Title
Implement health check assertions for Valkey status in both service health endpoints

### Description

This task is an extension of Task 2 (infrastructure), split out because it is specifically about
Valkey integration and may need to be skipped if Phase B (service Valkey integration) is not yet
complete.

The Valkey-specific health assertions belong in a separate test file so they can be individually
skipped without affecting the infrastructure tests.

**File:** `/home/yoseforb/pkg/follow/tests/integration/infrastructure_test.go`

Add to the existing file from Task 2 (or keep separate as warranted by implementation progress):

**Test: `TestValkeyHealth_APIReportsValkeyHealthy`**

```go
func TestValkeyHealth_APIReportsValkeyHealthy(t *testing.T) {
    resp, err := http.Get(apiURL + "/health/") //nolint:noctx
    require.NoError(t, err)
    defer resp.Body.Close()

    require.Equal(t, http.StatusOK, resp.StatusCode)

    var result map[string]interface{}
    require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))

    // The API health response format depends on Phase B implementation.
    // The general health endpoint may or may not include a "valkey" field.
    // Check the actual API implementation and adjust accordingly.
    // If not yet implemented, skip:
    valkeyStatus, exists := result["valkey"]
    if !exists {
        t.Skip("valkey health field not yet in API health response — skip until Phase B complete")
    }
    assert.Equal(t, "ok", valkeyStatus,
        "API should report valkey as healthy")
}
```

**Test: `TestValkeyHealth_GatewayReportsValkeyHealthy`**

```go
func TestValkeyHealth_GatewayReportsValkeyHealthy(t *testing.T) {
    resp, err := http.Get(gatewayURL + "/health/") //nolint:noctx
    require.NoError(t, err)
    defer resp.Body.Close()

    require.Equal(t, http.StatusOK, resp.StatusCode)

    var result map[string]interface{}
    require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))

    // The gateway health response includes valkey_healthy per CLAUDE.md.
    // Verify the field exists and is true.
    valkeyHealthy, exists := result["valkey_healthy"]
    if !exists {
        t.Skip("valkey_healthy field not in gateway health response")
    }
    assert.Equal(t, true, valkeyHealthy,
        "gateway should report valkey as healthy")
}
```

These tests are designed to degrade gracefully: if the service does not yet expose Valkey health
status (Phase B incomplete), they skip rather than fail, allowing the rest of the test suite to
run.

### Files Affected

**Modified or created** (depending on whether Task 2 file already exists):
- `/home/yoseforb/pkg/follow/tests/integration/infrastructure_test.go`

### Dependencies

- Task 1 (main_test.go)
- Task 2 (infrastructure_test.go may already exist)

### Acceptance Criteria

- Tests skip gracefully if Valkey health field is not present in responses
- Tests pass when Phase B is complete and both services expose Valkey health status
- Tests do not fail the overall test suite when infrastructure is healthy but Valkey field absent

### Story Points: 1

---

## Valkey Client Usage in Tests

Since the test module cannot import follow-pkg, all Valkey operations use `valkey-io/valkey-go`
directly. The builder API pattern:

```go
// Import alias to avoid name collision with module name:
import valkeygo "github.com/valkey-io/valkey-go"

// Create client
client, err := valkeygo.NewClient(valkeygo.ClientOption{
    InitAddress:  []string{valkeyAddress},
    DisableCache: true,
})

// HGETALL
result, err := client.Do(ctx, client.B().Hgetall().Key(key).Build()).AsStrMap()

// EXISTS (returns count of existing keys)
n, err := client.Do(ctx, client.B().Exists().Key(key).Build()).AsInt64()

// TTL (returns seconds, -2 if not exists, -1 if no TTL)
ttl, err := client.Do(ctx, client.B().Ttl().Key(key).Build()).AsInt64()

// XREADGROUP (read new messages)
cmd := client.B().Xreadgroup().
    Group(group, consumer).
    Count(count).
    Streams().
    Key(streamKey).
    Id(">").
    Build()
result, err := client.Do(ctx, cmd).AsXRead()
// result is map[string][]valkeygo.XRangeEntry
// For each entry: entry.ID (string), entry.FieldValues (map[string]string)

// XACK
err = client.Do(ctx, client.B().Xack().
    Key(streamKey).
    Group(group).
    Id(ids...).
    Build()).Error()

// XPENDING (summary)
info, err := client.Do(ctx, client.B().Xpending().
    Key(streamKey).
    Group(group).
    Build()).AsXPending()
// info.Count (int64), info.Lower (string), info.Higher (string)

// XAUTOCLAIM
result, err := client.Do(ctx, client.B().Xautoclaim().
    Key(streamKey).
    Group(group).
    Consumer(consumer).
    MinIdleTime(minIdleMs).
    Start("0-0").
    Count(count).
    Build()).AsXAutoClaim()
// result.Messages []valkeygo.XRangeEntry, result.Next string

// XGROUP CREATE MKSTREAM
err = client.Do(ctx, client.B().XgroupCreate().
    Key(streamKey).
    Group(group).
    Id("0").
    Mkstream().
    Build()).Error()
// Ignore "BUSYGROUP" error (already exists)

// XGROUP DESTROY
err = client.Do(ctx, client.B().XgroupDestroy().
    Key(streamKey).
    Group(group).
    Build()).Error()
```

---

## Docker Compose Port Allocation (docker mode)

When running in docker mode, the following non-conflicting test ports are used:

| Service | Test Port | Default Port |
|---------|-----------|--------------|
| PostgreSQL | 15432 | 5432 |
| Valkey | 16379 | 6379 |
| MinIO API | 19000 | 9000 |
| MinIO Console | 19001 | 9001 |
| follow-api | 18080 | 8080 |
| follow-image-gateway | 18090 | 8090 |

These are set via the `WithEnv` call on the compose stack in `setupDocker()`. The actual
docker-compose.yml uses environment variable substitution (from Task 1) to respect these values.

---

## Quality Gate Commands

After implementing all tasks, run from `tests/integration/` directory:

```bash
# Install dependencies
go mod tidy

# Build check
go build -tags=integration ./...

# Run local mode (requires PostgreSQL, MinIO, Valkey running via systemd)
# The test suite starts follow-api and follow-image-gateway automatically
INTEGRATION_TEST_MODE=local go test -tags=integration -race -v -timeout=300s .

# Run specific test
INTEGRATION_TEST_MODE=local go test -tags=integration -race -v \
    -run TestInfrastructure .

# Run docker mode (CI/CD)
go test -tags=integration -race -v -timeout=600s .
```

Note: There is no gofumpt/golines requirement for this module (it is not a service module). Use
`gofmt` standard formatting. `go vet ./...` should still pass.

---

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Gateway health response format unknown | Check `/home/yoseforb/pkg/follow/follow-image-gateway/internal/health/` before finalizing assertions. Use `t.Skip()` if field absent. |
| SSE event type names may differ from plan | Verify actual event types in `RouteStatusEvent` design type in `route_types.go`. The design shows `processing`, `ready`, `failed`, `heartbeat`, `complete`. |
| Stage names in progress hash unknown | Check `follow-pkg/valkey/progress.go` for `ProgressTracker` stage constants before using stage name strings in tests. |
| `XAUTOCLAIM` minIdleTime=0 may not work in all Valkey versions | Valkey 8+ supports 0ms minIdleTime. If not, use `minIdleTime = 1` (1ms). |
| `api-workers` consumer group may not exist on test startup | All stream-reading tests use dedicated test consumer groups. Only the behavioral flow test (Task 3) relies on follow-api processing results — it does not read the stream directly. |
| Confirm replace-image: file_hash behavior post-gateway-integration | Compute SHA256 of `smallJPEG()` at test time using `crypto/sha256` rather than using a hardcoded hash. |
| testcontainers-go compose API changes | Pin to a specific version in go.mod. The API used (`NewDockerCompose`, `WithEnv`, `Up`, `Down`) is stable across 0.35+. |

---

## Implementation Order

Tasks can be implemented in this sequence (each task blocks the next where indicated):

1. **Task 1** (module scaffolding + docker-compose.yml parameterization) — blocks all others
2. **Task 4** (helpers_test.go) — blocks Tasks 3, 5, 6, 7, 8, 9
3. **Tasks 2 and 3** (infrastructure + behavioral flow) — can be done in parallel after Task 4
4. **Tasks 5–9** (Valkey-specific tests) — can be done in parallel after Task 4
5. **Task 10** (Valkey health checks) — additive, can be done any time after Task 2

---

## References

| Document | Location |
|----------|----------|
| Valkey master plan (Phase C section) | `/home/yoseforb/pkg/follow/ai-docs/planning/active/cross-repo-valkey-integration-master-plan.md` |
| follow-pkg dual-mode pattern (source) | `/home/yoseforb/pkg/follow/follow-pkg/tests/integration/main_test.go` |
| gateway testcontainers pattern (source) | `/home/yoseforb/pkg/follow/follow-image-gateway/tests/integration/main_test.go` |
| gateway test helpers | `/home/yoseforb/pkg/follow/follow-image-gateway/tests/integration/gateway_test.go` |
| Valkey Client interface | `/home/yoseforb/pkg/follow/follow-pkg/valkey/client.go` |
| Valkey types (StreamMessage, PendingInfo) | `/home/yoseforb/pkg/follow/follow-pkg/valkey/types.go` |
| API route types (response shapes) | `/home/yoseforb/pkg/follow/follow-api/design/route_types.go` |
| API route endpoints | `/home/yoseforb/pkg/follow/follow-api/design/route_service.go` |
| API user types | `/home/yoseforb/pkg/follow/follow-api/design/user_types.go` |
| Ed25519 test helpers | `/home/yoseforb/pkg/follow/follow-image-gateway/internal/auth/testhelpers.go` |
| ADR-012 (Valkey) | `/home/yoseforb/pkg/follow/ai-docs/adr/012-valkey-over-redis.md` |
| ADR-016 (Streams) | `/home/yoseforb/pkg/follow/ai-docs/adr/follow-api-016-redis-streams-inter-service-communication.md` |
| ADR-017 (Ed25519) | `/home/yoseforb/pkg/follow/ai-docs/adr/follow-api-017-ed25519-asymmetric-signing.md` |
