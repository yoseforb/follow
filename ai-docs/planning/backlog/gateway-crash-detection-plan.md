# Gateway Crash Detection Plan

**Status**: Backlog
**Scope**: Cross-repo — affects `follow-api`, `follow-app`, `tests/integration`
**Goal**: Detect gateway crashes within 30 seconds using a background reaper goroutine that writes real `failed` status to Valkey, and fix the Flutter app to parse `all_done`, show retry UI, and use the existing retry-upload endpoint instead of blindly publishing.

---

## Context

### Current State (what exists)

- `follow-api/internal/infrastructure/streaming/sse_streamer.go`: `SSEStreamer` polls Valkey every 500ms. `DefaultSSEStreamerConfig()` has `MaxDuration: 5 * time.Minute`. `RouteStatusEventPayload.AllDone` is already written correctly in `sendCompleteAndClose`. The `buildSSEStreamer` function in `internal/api/server/goa_server.go` constructs the streamer with `DefaultSSEStreamerConfig()` and `valkeyClient`; it does not currently receive `cfg.Gateway.BaseURL`.
- `follow-api/cmd/server/app.go`: `initConsumer` starts a `ConsumerSupervisor` goroutine that keeps the Valkey stream consumer alive. This is the exact pattern to follow for the Stale Image Reaper.
- `follow-pkg/valkey/contracts.go`: All key patterns (`KeyPrefixImageStatus`), field names (`FieldStage`, `FieldError`), and stage values (`StageFailed`, `StageQueued`, `StageValidating`, etc.) are constants. No string literals allowed.
- `follow-app/lib/data/services/route_status_stream_service.dart`: `RouteStatusEvent` does not have an `allDone` field. `_parseMessage()` constructs `RouteStatusEvent` without reading `all_done` from JSON.
- `follow-app/lib/ui/route_creation/route_creation_view_model.dart`: `_waitForProcessingComplete` returns `Future<void>`, uses `Completer<void>`, and completes without returning the `all_done` result. The upload flow (Step 4-6, around line 558-582) calls `_waitForProcessingComplete` then unconditionally calls `publishRoute`.
- **Retry-upload endpoint**: `POST /api/v1/routes/{route_id}/waypoints/{waypoint_id}/retry-upload` — already implemented in follow-api (Task 10 from edge-case-resilience-plan). Returns `{image_id, upload_url, expires_at}`.

### The Problem

When the image-gateway crashes mid-processing:
1. The Valkey status hash for in-flight images stays at an intermediate stage (e.g., `stage=validating`) forever. Nobody writes `stage=failed`.
2. The SSEStreamer keeps emitting `processing` events for 5 minutes until `MaxDuration` expires.
3. `sendCompleteAndClose` is called with `allDone=false` — but Flutter ignores this.
4. Flutter calls `publishRoute`, gets HTTP 422 (route still in PENDING state).

### Approved Fix: Stale Image Reaper

A background goroutine in follow-api that periodically scans Valkey for images stuck in non-terminal stages for more than 30 seconds and writes `stage=failed` to their status hashes. The SSEStreamer then reads the real `failed` state on its next poll tick — zero SSE logic changes needed. Data is correct everywhere including reconnecting clients.

This approach fixes the root cause at the data layer, not the presentation layer.

---

## Implementation Order

| Task | Repo | Blocks |
|------|------|--------|
| A: Add `Scan` method to follow-pkg `Client` | follow-pkg | B |
| B: Stale Image Reaper implementation | follow-api | C |
| C: Wire reaper in app.go alongside consumer supervisor | follow-api | K |
| D: Reduce MaxDuration from 5 min to 2 min | follow-api | none (can merge with C) |
| E: Parse `all_done` in Flutter RouteStatusEvent | follow-app | F |
| F: Update `_waitForProcessingComplete` return type to Future<bool> | follow-app | I |
| G: Add RetryUploadResult model class | follow-app | H |
| H: Add retry-upload API call to RouteRepository | follow-app | I |
| I: Update ViewModel upload flow for failure handling | follow-app | J |
| J: Add retry UI in route creation screen | follow-app | K |
| K: Integration test — stale reaper + SSE + retry flow | tests/integration | none |

Task A (follow-pkg) must land first. Tasks B-D can be implemented and merged as a unit after A. Tasks E-J follow as a unit. Task K requires both units to be complete.

---

## Tasks

---

### Task A: Add `Scan` Method to follow-pkg `Client`

**Story Points**: 2

**Repo**: follow-pkg

**Description**

The `Client` interface in `follow-pkg/valkey/client.go` has no method for iterating keys. The `StaleImageReaper` (Task B) needs SCAN to discover active `image:status:*` hashes. Add a `Scan` method to the `Client` interface and implement it in `ValkeyClient`. All callers use the wrapper interface — no raw valkey-go `Do()` calls are permitted outside the wrapper.

**Interface Addition**

Add to the `// Key Operations` section of `follow-pkg/valkey/client.go`:

```go
// Scan iterates keys matching pattern using the SCAN command.
// Returns the next cursor and matching keys. Cursor "0" starts
// a new iteration; cursor "0" in the return signals completion.
Scan(
    ctx context.Context,
    cursor string,
    pattern string,
    count int64,
) (nextCursor string, keys []string, err error)
```

**Implementation**

Add `Scan` to `ValkeyClient` in `follow-pkg/valkey/valkey_client.go` (or a dedicated `key_ops.go` file following the existing file-per-operation-group convention):

```go
// Scan executes a SCAN command with the given cursor, pattern,
// and count hint. Returns the next cursor and matching keys.
// Cursor "0" in the return signals that a full iteration is done.
func (v *ValkeyClient) Scan(
    ctx context.Context,
    cursor string,
    pattern string,
    count int64,
) (string, []string, error) {
    cmd := v.client.B().Scan().
        Cursor(parseCursor(cursor)).
        Match(pattern).
        Count(count).
        Build()

    result := v.client.Do(ctx, cmd)
    if err := result.Error(); err != nil {
        return "0", nil, fmt.Errorf(
            "scan command failed: %w", err)
    }

    scanResult, err := result.AsScanEntry()
    if err != nil {
        return "0", nil, fmt.Errorf(
            "parse scan response: %w", err)
    }

    return fmt.Sprintf("%d", scanResult.Cursor),
        scanResult.Elements, nil
}

// parseCursor converts a string cursor ("0", "12345") to uint64
// for valkey-go's typed SCAN builder.
func parseCursor(cursor string) uint64 {
    if cursor == "" || cursor == "0" {
        return 0
    }
    var n uint64
    _, _ = fmt.Sscanf(cursor, "%d", &n)
    return n
}
```

**Contracts Addition**

Add to `follow-pkg/valkey/contracts.go` in the `// ---- Progress Hash Field Names ----` section or as a new `// ---- Reaper Error Messages ----` group:

```go
// ---- Reaper Error Messages ----

// ErrorProcessingTimeout is the error message written by the
// StaleImageReaper when an image has not progressed for longer
// than the configured stale threshold.
ErrorProcessingTimeout = "image processing timed out"
```

This constant is used by the reaper in follow-api when writing `stage=failed` — no string literals.

**Tests**

Add integration tests in `follow-pkg/tests/integration/valkey_client_test.go` (appended to the existing file, following the `//go:build integration` pattern). All SCAN tests run against a real Valkey instance — writing a fake for the Valkey client is equivalent to reimplementing Valkey from scratch.

- `TestIntegration_Scan_EmptyKeyspace`: SCAN on a fresh keyspace — assert no keys match a unique test pattern.
- `TestIntegration_Scan_SinglePage`: Write several `image:status:{uuid}` hashes, SCAN with `image:status:*` pattern and a large count — assert all keys returned in a single iteration (cursor reaches "0").
- `TestIntegration_Scan_FindsKeys`: Write 5 `image:status:{uuid}` hashes, SCAN with `image:status:*` pattern — assert all 5 keys found across iteration.
- `TestIntegration_Scan_CursorPagination`: Write 50 keys, SCAN with `count=5` — assert full iteration (looping until cursor "0") collects all 50 keys.
- `TestIntegration_Scan_PatternFiltering`: Write keys with two distinct prefixes, SCAN with one prefix pattern — assert only the matching keys are returned.

**Quality Gates**

```bash
gofumpt -w . && golines -w --max-len=80 .
go vet ./...
golangci-lint run -c .golangci.yml ./... --fix
go test -race -cover ./...
INTEGRATION_TEST_MODE=local go test -tags=integration -race ./tests/integration/
go mod tidy
```

**Files Affected**

- `follow-pkg/valkey/client.go` — MODIFY (add `Scan` to interface)
- `follow-pkg/valkey/valkey_client.go` — MODIFY (add `Scan` implementation and `parseCursor` helper)
- `follow-pkg/valkey/contracts.go` — MODIFY (add `ErrorProcessingTimeout` constant)
- `follow-pkg/tests/integration/valkey_client_test.go` — MODIFY (append 5 integration tests)

**Dependencies**

None. This task has no blocking prerequisites.

**Acceptance Criteria**

- `Scan` method added to `Client` interface and `ValkeyClient` implementation.
- `ErrorProcessingTimeout` constant added to `contracts.go`.
- `var _ Client = (*ValkeyClient)(nil)` compile-time assertion continues to pass.
- Integration tests in `follow-pkg/tests/integration/` verify SCAN against a real Valkey instance, covering: empty keyspace, single-page result, multi-key retrieval, cursor pagination, and pattern filtering.
- All quality gates pass with zero warnings.

---

### Task B: Stale Image Reaper

**Story Points**: 5

**Description**

Create a `StaleImageReaper` background goroutine in a new package `internal/infrastructure/reaper/`. The reaper periodically scans Valkey for image status hashes stuck in non-terminal stages for longer than a configurable threshold and writes `stage=failed, error=image processing timed out` to those hashes. The SSEStreamer picks up the `failed` state on its next regular poll — no changes to SSE logic.

The reaper uses only `valkey.Client` wrapper methods (`client.Scan`, `client.HGetAll`, `client.HSet`) — never raw valkey-go `Do()` or builder calls directly. It does NOT publish to the `image:result` stream; it writes only to the Valkey hash.

**Package Location**

Create `internal/infrastructure/reaper/` as a new package. It is separate from `internal/infrastructure/streaming/` because its responsibility is Valkey data correction, not SSE event dispatch. It depends only on the Valkey client and the constants from `follow-pkg/valkey`.

**StaleImageReaper Struct**

```go
// StaleImageReaper scans Valkey for image status hashes stuck in
// non-terminal stages and marks them as failed.
type StaleImageReaper struct {
    client valkey.Client
    cfg    StaleImageReaperConfig
    log    zerolog.Logger
}

type StaleImageReaperConfig struct {
    // ScanInterval is how often the reaper scans Valkey.
    // Default: 10s.
    ScanInterval time.Duration

    // StaleThreshold is how long an image can remain in a
    // non-terminal stage before being marked failed.
    // Default: 30s.
    StaleThreshold time.Duration

    // ScanBatchSize is the SCAN count hint per SCAN call.
    // Default: 100.
    ScanBatchSize int64
}

func DefaultStaleImageReaperConfig() StaleImageReaperConfig {
    return StaleImageReaperConfig{
        ScanInterval:   10 * time.Second,
        StaleThreshold: 30 * time.Second,
        ScanBatchSize:  100,
    }
}
```

Add named constants for default values (`defaultScanIntervalSeconds = 10`, `defaultStaleThresholdSeconds = 30`, `defaultScanBatchSize = 100`) to satisfy the `mnd` linter.

**Constructor**

```go
func NewStaleImageReaper(
    client valkey.Client,
    cfg StaleImageReaperConfig,
    log zerolog.Logger,
) (*StaleImageReaper, error) {
    if client == nil {
        return nil, ErrClientNil
    }
    return &StaleImageReaper{
        client: client,
        cfg:    cfg,
        log:    log,
    }, nil
}
```

**Run Method**

```go
// Run starts the reaper loop. It blocks until ctx is cancelled.
// Run is designed to be called in a goroutine. It logs each
// scan iteration and each stale image it marks as failed.
func (r *StaleImageReaper) Run(ctx context.Context) {
    ticker := time.NewTicker(r.cfg.ScanInterval)
    defer ticker.Stop()

    // firstSeenStaleAt tracks when each key was first observed
    // in a non-terminal stage. Keyed by the full Valkey hash key
    // (e.g., "image:status:uuid") to avoid redundant key assembly.
    firstSeenStaleAt := make(map[string]time.Time)

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            r.runScan(ctx, firstSeenStaleAt)
        }
    }
}
```

**runScan Method**

Use `client.Scan` (added in Task A) to iterate all `image:status:*` keys, then `client.HGetAll` to read each key's fields. No raw valkey-go calls.

```go
func (r *StaleImageReaper) runScan(
    ctx context.Context,
    firstSeenStaleAt map[string]time.Time,
) {
    // Collect all active image status keys via SCAN.
    pattern := valkey.KeyPrefixImageStatus + ":*"
    activeKeys := make(map[string]struct{})
    cursor := "0"
    for {
        nextCursor, keys, err := r.client.Scan(
            ctx, cursor, pattern, r.cfg.ScanBatchSize,
        )
        if err != nil {
            r.log.Error().Err(err).
                Msg("reaper: SCAN failed")
            return
        }
        for _, key := range keys {
            activeKeys[key] = struct{}{}
        }
        cursor = nextCursor
        if cursor == "0" {
            break
        }
    }

    // Inspect each key using HGetAll; extract stage field.
    for key := range activeKeys {
        fields, err := r.client.HGetAll(ctx, key)
        if err != nil || len(fields) == 0 {
            // Transient error or key expired between SCAN
            // and HGetAll — skip.
            continue
        }

        stage := fields[valkey.FieldStage]

        // Terminal stages: clean up tracking and skip.
        if stage == valkey.StageDone ||
            stage == valkey.StageFailed {
            delete(firstSeenStaleAt, key)
            continue
        }

        // Non-terminal: track and check threshold.
        first, tracked := firstSeenStaleAt[key]
        if !tracked {
            firstSeenStaleAt[key] = time.Now()
            continue
        }
        if time.Since(first) >= r.cfg.StaleThreshold {
            r.markFailed(ctx, key, firstSeenStaleAt)
        }
    }

    // Prune tracking entries for keys no longer in Valkey
    // (expired or deleted since last SCAN).
    for key := range firstSeenStaleAt {
        if _, found := activeKeys[key]; !found {
            delete(firstSeenStaleAt, key)
        }
    }
}
```

**markFailed Method**

Uses `client.HSet` from the wrapper. Uses `valkey.ErrorProcessingTimeout` constant from `follow-pkg/valkey/contracts.go` — no string literals. Does NOT publish to the `image:result` stream.

```go
func (r *StaleImageReaper) markFailed(
    ctx context.Context,
    key string,
    firstSeenStaleAt map[string]time.Time,
) {
    fields := map[string]string{
        valkey.FieldStage:     valkey.StageFailed,
        valkey.FieldError:     valkey.ErrorProcessingTimeout,
        valkey.FieldUpdatedAt: time.Now().UTC().Format(time.RFC3339),
    }
    err := r.client.HSet(ctx, key, fields)
    if err != nil {
        r.log.Error().Err(err).Str("key", key).
            Msg("reaper: failed to mark image as failed")
        return
    }
    r.log.Warn().Str("key", key).
        Msg("reaper: marked stale image as failed")
    delete(firstSeenStaleAt, key)
}
```

**Errors**

Create `internal/infrastructure/reaper/errors.go`:

```go
var ErrClientNil = errors.New("reaper: valkey client is nil")
```

**Tests**

Create `internal/infrastructure/reaper/stale_image_reaper_test.go` in `package reaper_test`. Use a `FakeValkeyClient` (hand-written fake implementing the `valkey.Client` interface, not a mock framework, consistent with project testing philosophy). The fake must implement `Scan` and `HGetAll` and `HSet` at minimum.

- `TestStaleImageReaper_Run_MarksStaleImageFailed`: Set up a fake Valkey with one hash `image:status:{uuid}` at `stage=validating`. Configure `StaleThreshold=50ms, ScanInterval=10ms`. Run reaper in goroutine. After 100ms, assert the hash has `stage=failed` and `error=image processing timed out`.
- `TestStaleImageReaper_Run_DoesNotMarkFreshImage`: Same setup but check after only 20ms (before threshold). Assert `stage` is still `validating`.
- `TestStaleImageReaper_Run_SkipsTerminalImages`: Hash at `stage=done`. After 100ms, assert stage remains `done` (not overwritten).
- `TestStaleImageReaper_Run_SkipsAlreadyFailedImages`: Hash at `stage=failed`. Assert `HSet` not called (no re-write).
- `TestStaleImageReaper_Run_CleansTrackingMapOnTerminal`: Image first seen as `validating` (added to tracking), then transitions to `done`. Assert it is not marked failed.
- `TestStaleImageReaper_Run_StopsOnContextCancel`: Context cancelled — assert goroutine exits promptly.
- `TestStaleImageReaper_Constructor_NilClient`: Assert `NewStaleImageReaper(nil, ...)` returns `ErrClientNil`.

**Files Affected**

- `internal/infrastructure/reaper/stale_image_reaper.go` — NEW
- `internal/infrastructure/reaper/errors.go` — NEW
- `internal/infrastructure/reaper/stale_image_reaper_test.go` — NEW

**Dependencies**

Task A must be complete (`valkey.Client.Scan` and `valkey.ErrorProcessingTimeout` must exist in follow-pkg).

**Acceptance Criteria**

- `StaleImageReaper` uses only `valkey.Client` wrapper methods (`client.Scan`, `client.HGetAll`, `client.HSet`) — no raw valkey-go `Do()` or builder calls.
- `markFailed` writes `valkey.ErrorProcessingTimeout` — no string literals in the reaper package.
- The reaper does NOT publish to the `image:result` stream.
- Images at `stage=done` or `stage=failed` are never overwritten.
- Context cancellation exits `Run` cleanly with no goroutine leak.
- All seven unit tests pass with `go test -race ./internal/infrastructure/reaper/...`.
- `go vet`, `golangci-lint`, `gofumpt`, `golines` all pass with zero warnings.
- No import of the `streaming` package (reaper is independent).

---

### Task C: Wire Stale Image Reaper in app.go

**Story Points**: 2

**Description**

Start the `StaleImageReaper` goroutine in `cmd/server/app.go` alongside the consumer supervisor. Add a new `initReaper` step to the `App.Init` sequence. The reaper runs for the server's lifetime and stops when `ctx` is cancelled (same lifecycle as the consumer supervisor).

**Changes to App Struct**

No new fields needed on `App` — the reaper goroutine is fire-and-forget, started in `initReaper`. It holds no state on `App` that needs to be shut down (the context cancellation stops it).

**New initReaper Method**

```go
// initReaper creates a StaleImageReaper and starts it in a
// goroutine. The reaper runs for the server lifetime; context
// cancellation stops it. Must run after initValkey.
func (a *App) initReaper(ctx context.Context) error {
    a.log.Info().Msg("Initializing stale image reaper")

    reaper, err := reaper.NewStaleImageReaper(
        a.valkeyClient,
        reaper.DefaultStaleImageReaperConfig(),
        logger.GetLoggerForComponent("stale-image-reaper"),
    )
    if err != nil {
        return fmt.Errorf(
            "failed to create stale image reaper: %w",
            err,
        )
    }

    go reaper.Run(ctx)

    a.log.Info().
        Dur("scan_interval", reaper.DefaultStaleImageReaperConfig().ScanInterval).
        Dur("stale_threshold", reaper.DefaultStaleImageReaperConfig().StaleThreshold).
        Msg("Stale image reaper started")

    return nil
}
```

**Init Steps Update**

Add `{"reaper", a.initReaper}` to the `steps` slice in `Init`, after `{"valkey", a.initValkey}` and before `{"jwt service", a.initJWTService}`. Valkey must be initialized first; the reaper needs the client.

**Shutdown**

The reaper goroutine stops when the `ctx` passed to `Init` is cancelled, which happens before `Shutdown` is called. No additional cleanup required. Add a log line in `stopConsumer` or add a separate `stopReaper` noop method for symmetry with the shutdown log sequence. Follow the same pattern as `stopConsumer`.

**Files Affected**

- `cmd/server/app.go` — MODIFY (new `initReaper` method, add step to `Init`, add import for `reaper` package)

**Dependencies**

Task B must be complete (`StaleImageReaper` must exist).

**Acceptance Criteria**

- Server starts without error: `go run ./cmd/server -runtime-timeout 10s`.
- Log output at startup includes `"Stale image reaper started"` with `scan_interval` and `stale_threshold` fields.
- All existing tests pass (`go test -race -cover ./...`).

---

### Task C: Reduce MaxDuration from 5 Minutes to 2 Minutes

**Story Points**: 1

**Description**

This task is intentionally minimal and must be merged with Task B. Change the default `MaxDuration` in `DefaultSSEStreamerConfig()` in `internal/infrastructure/streaming/sse_streamer.go` from `5 * time.Minute` to `2 * time.Minute`. This is a safety net for cases where the reaper has not yet fired (first 30 seconds) or Valkey itself is unavailable.

Add a named constant `defaultMaxDurationMinutes = 2` alongside the existing `defaultHeartbeatSeconds = 30` to satisfy the `mnd` linter.

Update `DefaultSSEStreamerConfig()`:

```go
func DefaultSSEStreamerConfig() SSEStreamerConfig {
    return SSEStreamerConfig{
        PollInterval:   500 * time.Millisecond,
        HeartbeatEvery: defaultHeartbeatSeconds * time.Second,
        MaxDuration:    defaultMaxDurationMinutes * time.Minute,
    }
}
```

Add a new test to `internal/infrastructure/streaming/sse_streamer_test.go`:

```go
func TestSSEStreamer_DefaultConfig_MaxDuration(t *testing.T) {
    t.Parallel()
    cfg := streaming.DefaultSSEStreamerConfig()
    assert.Equal(t, 2*time.Minute, cfg.MaxDuration)
}
```

The existing `TestSSEStreamer_Stream_MaxDurationExpiry` test uses a custom config with `MaxDuration=10ms` and is unaffected.

**Files Affected**

- `internal/infrastructure/streaming/sse_streamer.go` — MODIFY (constant + DefaultSSEStreamerConfig)
- `internal/infrastructure/streaming/sse_streamer_test.go` — MODIFY (new default assertion test)

**Dependencies**

None (independent of Tasks A and B, but merge together for a clean diff).

**Acceptance Criteria**

- `DefaultSSEStreamerConfig().MaxDuration` equals `2 * time.Minute`.
- New `defaultMaxDurationMinutes` constant exists; no bare `2` in the expression.
- `TestSSEStreamer_DefaultConfig_MaxDuration` passes.
- `mnd` linter passes.

---

### Task D: Parse `all_done` in Flutter RouteStatusEvent

**Story Points**: 1

**Description**

The `RouteStatusEvent` class in `lib/data/services/route_status_stream_service.dart` does not parse the `all_done` JSON field from `complete` events. The field is already sent by follow-api but silently dropped.

**Changes to RouteStatusEvent**

Add a nullable boolean field and a computed getter:

```dart
/// Whether all images completed successfully.
/// Only present on [isComplete] events.
/// null when the field is absent from the JSON payload.
final bool? allDone;

/// Whether the complete event signals full success.
///
/// Returns true only when [allDone] is explicitly true.
/// Returns false when [allDone] is false or absent.
/// Absent is treated as incomplete for safety.
bool get isAllDone => allDone == true;
```

Update the constructor to include `this.allDone`.

Update `_parseMessage()` to parse the field:

```dart
return RouteStatusEvent(
  type: eventType,
  imageId: jsonData['image_id'] as String?,
  routeId: jsonData['route_id'] as String?,
  status: jsonData['status'] as String?,
  errorReason: jsonData['error_reason'] as String?,
  allDone: jsonData['all_done'] as bool?,
);
```

Update `toString()` to include `allDone`.

**Tests**

Add unit tests (create `test/data/services/route_status_stream_service_test.dart` if it does not exist):

- `parseMessage_complete_allDoneTrue`: SSE data with `"all_done": true` — assert `event.allDone == true`, `event.isAllDone == true`.
- `parseMessage_complete_allDoneFalse`: SSE data with `"all_done": false` — assert `event.allDone == false`, `event.isAllDone == false`.
- `parseMessage_complete_allDoneMissing`: No `all_done` key in JSON — assert `event.allDone == null`, `event.isAllDone == false`.

**Files Affected**

- `lib/data/services/route_status_stream_service.dart` — MODIFY

**Dependencies**

None. Pure data model change.

**Acceptance Criteria**

- `dart analyze` returns "No errors".
- All three unit tests pass.
- `toString()` output includes `allDone`.
- No breaking changes to existing code using `RouteStatusEvent` (field is nullable).
- No localized strings in this task.

---

### Task E: Update `_waitForProcessingComplete` Return Type to Future<bool>

**Story Points**: 2

**Description**

`_waitForProcessingComplete` in `RouteCreationViewModel` currently returns `Future<void>` and uses `Completer<void>`. Change it to return `Future<bool>` where `true` means `all_done=true` and `false` means incomplete/failed/unexpected close.

**Changes**

1. Change return type from `Future<void>` to `Future<bool>`.
2. Change `Completer<void>` to `Completer<bool>`.
3. In the `isComplete` event handler: complete with `event.isAllDone` (true or false).
4. In `onDone` (stream closed without `complete` event): complete with `false`.
5. In `onError`: `completer.completeError(error)` (unchanged — caller handles via try-catch).
6. Return the awaited completer future result.

**Changes to Upload Flow Caller**

In `uploadRouteToServer`, replace:

```dart
await _waitForProcessingComplete(routeId);
```

With:

```dart
final bool processingAllDone = await _waitForProcessingComplete(routeId);
```

Store `processingAllDone` for use in Step 5 (Task H extends this).

**Files Affected**

- `lib/ui/route_creation/route_creation_view_model.dart` — MODIFY

**Dependencies**

Task D must be complete (`RouteStatusEvent.isAllDone` must exist).

**Acceptance Criteria**

- `dart analyze` returns "No errors".
- `_waitForProcessingComplete` returns `true` when SSE sends `complete(all_done=true)`.
- `_waitForProcessingComplete` returns `false` when SSE sends `complete(all_done=false)` or closes unexpectedly.
- Existing upload flow compiles and runs (Task H will use the return value).

---

### Task F: Add RetryUploadResult Model Class

**Story Points**: 1

**Description**

Create the Dart model class for the retry-upload API response. The endpoint `POST /api/v1/routes/{routeId}/waypoints/{waypointId}/retry-upload` returns:

```json
{
  "image_id": "uuid",
  "upload_url": "https://...",
  "expires_at": "2026-03-01T00:00:00Z"
}
```

Create `lib/data/models/retry_upload_result.dart`:

```dart
/// Response from the retry-upload endpoint.
///
/// Contains a fresh presigned upload URL for re-uploading a failed
/// or stuck waypoint image to the gateway.
class RetryUploadResult {
  const RetryUploadResult({
    required this.imageId,
    required this.uploadUrl,
    required this.expiresAt,
  });

  factory RetryUploadResult.fromJson(Map<String, dynamic> json) {
    return RetryUploadResult(
      imageId: json['image_id'] as String,
      uploadUrl: json['upload_url'] as String,
      expiresAt: DateTime.parse(json['expires_at'] as String),
    );
  }

  final String imageId;
  final String uploadUrl;
  final DateTime expiresAt;

  bool get isExpired => DateTime.now().isAfter(expiresAt);
}
```

Add unit tests in `test/data/models/retry_upload_result_test.dart`:
- `fromJson_validPayload_parsesCorrectly`: assert all three fields parsed correctly.
- `isExpired_pastDate_returnsTrue`: assert `isExpired` returns true for past `expiresAt`.
- `isExpired_futureDate_returnsFalse`: assert `isExpired` returns false for future `expiresAt`.

**Files Affected**

- `lib/data/models/retry_upload_result.dart` — NEW
- `test/data/models/retry_upload_result_test.dart` — NEW

**Dependencies**

None.

**Acceptance Criteria**

- `dart analyze` returns "No errors".
- All three unit tests pass.
- Model follows the existing `fromJson` pattern in `lib/data/models/`.

---

### Task G: Add Retry-Upload API Call to RouteRepository

**Story Points**: 2

**Description**

Add `retryUploadWaypointImage` to the `RouteRepository` abstract class and its `HttpRouteRepository` implementation.

**Abstract Interface Addition**

```dart
/// Requests a fresh upload URL for a failed or stuck waypoint image.
///
/// Calls POST /api/v1/routes/{routeId}/waypoints/{waypointId}/retry-upload.
/// The existing upload guard is cleared server-side before the new URL is
/// issued, allowing the gateway to accept a fresh upload.
///
/// Returns a [RetryUploadResult] with a new presigned upload URL.
///
/// Throws [RouteException] if the request fails.
Future<RetryUploadResult> retryUploadWaypointImage({
  required String routeId,
  required String waypointId,
});
```

**HTTP Implementation**

- Endpoint: `POST /api/v1/routes/{routeId}/waypoints/{waypointId}/retry-upload`
- Auth: Bearer token (same pattern as existing repository methods).
- Body: empty (`{}`).
- Parse response using `RetryUploadResult.fromJson`.
- Map HTTP errors to `RouteException` (404 = waypoint not found, 409 = image not retryable, 503 = server error).

Follow the exact pattern of existing methods in the file (token retrieval via `_secureStorageService`, error mapping).

**Tests**

Add repository tests:
- `retryUploadWaypointImage_success_returnsResult`: mock HTTP 200 — assert `RetryUploadResult` fields parsed correctly.
- `retryUploadWaypointImage_404_throwsRouteException`: mock HTTP 404 — assert throws `RouteException`.
- `retryUploadWaypointImage_409_throwsRouteException`: mock HTTP 409 — assert throws `RouteException`.

**Files Affected**

- `lib/data/repositories/route_repository.dart` — MODIFY (abstract + implementation)

**Dependencies**

Task F must be complete (`RetryUploadResult` model must exist).

**Acceptance Criteria**

- `dart analyze` returns "No errors".
- All three repository tests pass.
- Method follows existing authentication and error handling patterns in the file.

---

### Task H: Update ViewModel Upload Flow for Failure Handling

**Story Points**: 3

**Description**

Update `RouteCreationViewModel` to correctly handle `all_done=false` and provide the retry-upload flow. This is the core business logic change on the Flutter side.

**New ViewModel State**

Add fields to track per-waypoint failure for the retry UI:

```dart
// IDs of waypoints whose images are stuck/failed after SSE complete.
List<String> _failedWaypointIds = [];
List<String> get failedWaypointIds => List.unmodifiable(_failedWaypointIds);

// Whether processing completed with all_done=false.
bool _processingIncomplete = false;
bool get processingIncomplete => _processingIncomplete;
```

**Upload Flow Step 5 Revision**

Replace the current Step 5 logic in `uploadRouteToServer`:

```dart
// Current (broken): publishes even when all_done=false.
// New:
if (!processingAllDone || _processingFailed > 0) {
  _processingIncomplete = true;
  _failedWaypointIds = _collectFailedWaypointIds();
  notifyListeners();
  return; // Do NOT publish. Task I's UI will handle recovery.
}
// Only publish when explicitly all_done=true AND no failed images.
await _routeRepository.publishRoute(routeId: routeId);
```

**Tracking Processed Image IDs**

Add `Set<String> _processedImageIds = {}` to ViewModel state. Clear it at the start of each `_waitForProcessingComplete` call. In the `ready` event handler inside `_waitForProcessingComplete`, record `_processedImageIds.add(event.imageId!)` when `event.imageId != null`.

**_collectFailedWaypointIds Helper**

Iterate `_waypointManager`'s waypoints, compare each waypoint's associated image ID against `_processedImageIds`. Return the list of waypoint IDs whose image IDs are NOT in `_processedImageIds`. This identifies exactly which waypoints need retry.

**Retry Method**

```dart
/// Retries upload for a single failed waypoint.
///
/// 1. Calls retry-upload endpoint to get a fresh presigned URL.
/// 2. Re-uploads the image bytes to the gateway.
/// 3. Re-enters SSE polling to await the re-processed image.
///
/// On success: removes [waypointId] from [_failedWaypointIds].
/// If [_failedWaypointIds] becomes empty, sets [_processingIncomplete]
/// to false.
///
/// On failure: sets [_errorMessage] and leaves [waypointId] in
/// [_failedWaypointIds].
Future<void> retryWaypointUpload({
  required String routeId,
  required String waypointId,
}) async { ... }
```

The method must:
1. Call `_routeRepository.retryUploadWaypointImage(routeId: routeId, waypointId: waypointId)` to get a fresh URL.
2. Find the waypoint in `_waypointManager` by `waypointId`, retrieve its captured image bytes.
3. Upload the image bytes using the new presigned URL (same upload mechanism as the initial upload).
4. Open a new SSE stream via `_waitForProcessingComplete(routeId)` to await the re-processed image. The SSE stream returns status for all non-terminal images, so a single retry call may resolve multiple failures simultaneously.
5. On `all_done=true` or `_failedWaypointIds.isEmpty` after the stream completes: remove `waypointId` from `_failedWaypointIds`, update `_processingIncomplete`.
6. `notifyListeners()` after state changes.

**Publish After All Retries**

Add `publishAfterRetry`:

```dart
/// Publishes the route after all retry uploads succeed.
///
/// Only callable when [failedWaypointIds] is empty.
/// Returns true on success, false on failure.
Future<bool> publishAfterRetry(String routeId) async {
  if (failedWaypointIds.isNotEmpty) return false;
  try {
    await _routeRepository.publishRoute(routeId: routeId);
    _isRouteConfirmed = true;
    _processingIncomplete = false;
    notifyListeners();
    return true;
  } on Exception catch (e) {
    _errorMessage = e.toString();
    notifyListeners();
    return false;
  }
}
```

**Files Affected**

- `lib/ui/route_creation/route_creation_view_model.dart` — MODIFY

**Dependencies**

Tasks D, E, and G must be complete.

**Acceptance Criteria**

- `dart analyze` returns "No errors".
- When `processingAllDone=false`: `uploadRouteToServer` does NOT call `publishRoute`, `processingIncomplete=true`, `failedWaypointIds` is populated.
- When `processingAllDone=true` and no failures: `publishRoute` is called (unchanged behavior).
- `retryWaypointUpload` correctly re-uploads and updates state.
- Unit tests cover: all success (publish fires), all failure (publish blocked), partial failure with retry (publish fires after retry).

---

### Task I: Add Retry UI in Route Creation Screen

**Story Points**: 5

**Description**

Add the processing failure UI to `route_creation_screen.dart`. When `viewModel.processingIncomplete == true`, render a "Processing Failed" recovery panel (not a dialog — an inline panel below the upload progress indicator) that lists failed waypoints and provides per-waypoint retry buttons.

**UX Flow**

When the upload flow returns with `processingIncomplete=true`:
1. Render the "Processing Failed" panel in the route creation screen. The panel replaces the upload progress indicators, not the whole screen.
2. The panel shows:
   - Localized title: `l10n.processingFailedTitle`.
   - Localized subtitle: `l10n.processingFailedSubtitle`.
   - For each failed waypoint: a `ListTile` with the waypoint position number, a "Retry" button (`l10n.processingFailedRetryButton`), and a status indicator (pending / retrying / done).
   - A "Cancel Route" button (`l10n.processingFailedCancelButton`) at the bottom.
   - A "Publish Route" button (`l10n.processingFailedPublishButton`) enabled only when `viewModel.failedWaypointIds.isEmpty`.

**Retry Button Behavior**

Each retry button:
1. Shows a `CircularProgressIndicator` while retrying.
2. Calls `viewModel.retryWaypointUpload(routeId: routeId, waypointId: waypointId)`.
3. On success: shows a checkmark; "Publish Route" becomes enabled if all retries done.
4. On failure: shows an error icon; retry button becomes active again.

**Navigation**

- "Cancel Route": calls a ViewModel method to delete the route (which internally calls the delete endpoint), then `context.go(RoutePaths.root)`. Do NOT use `Navigator.pop()`.
- "Publish Route" (after all retries succeed): calls `viewModel.publishAfterRetry(routeId)`, then on success `context.go(RoutePaths.root)`.

**Localization**

All UI strings in both `app_en.arb` and `app_he.arb`. Run `flutter gen-l10n` after updating.

English strings to add:
```
"processingFailedTitle": "Some images could not be processed",
"processingFailedSubtitle": "Tap Retry next to each failed image to re-upload",
"processingFailedRetryButton": "Retry",
"processingFailedCancelButton": "Cancel Route",
"processingFailedPublishButton": "Publish Route",
"processingFailedWaypointLabel": "Waypoint {position}",
"processingFailedAllRetriedSuccess": "All images processed. Ready to publish!"
```

Hebrew translations must follow `ai-docs/infrastructure/hebrew-translation-guidelines.md`.

**RTL Requirements**

All layout must use `EdgeInsetsDirectional`, `AlignmentDirectional`, and `PositionedDirectional`. No `EdgeInsets.only(left:...)` or `EdgeInsets.fromLTRB`. No `Alignment.centerLeft` / `Alignment.centerRight`.

**Files Affected**

- `lib/ui/route_creation/route_creation_screen.dart` — MODIFY
- `lib/l10n/app_en.arb` — MODIFY (7 new strings)
- `lib/l10n/app_he.arb` — MODIFY (7 Hebrew translations)

Run `flutter gen-l10n` after updating `.arb` files.

**Dependencies**

Task H must be complete (`viewModel.processingIncomplete`, `viewModel.failedWaypointIds`, `viewModel.retryWaypointUpload`, `viewModel.publishAfterRetry` must exist).

**Acceptance Criteria**

- `dart analyze` returns "No errors".
- `flutter test --coverage` passes (>80% coverage maintained).
- When `processingIncomplete=true`: retry panel is visible, no error dialog shown.
- When `processingIncomplete=false`: no behavior change from current flow.
- All 7 strings present in both `app_en.arb` and `app_he.arb`.
- No `Navigator.pop()` in new code.
- No `EdgeInsets.only(left:...)` or other LTR-only layout widgets.
- Widget test covering retry panel render when `processingIncomplete=true`.

---

### Task J: Integration Test — Stale Reaper, SSE, and Retry Flow

**Story Points**: 3

**Description**

Add an end-to-end integration test in `tests/integration/` that exercises the full crash detection and recovery flow without a running gateway. The test simulates a gateway crash by writing intermediate Valkey progress state directly (as the gateway would), then stopping updates to simulate a crash, and verifying the reaper marks the image as failed and SSE emits the correct events.

**Test Name**

`TestStaleImageReaper_MarksFailedAndSSEEmitsFailedEvent`

**Test Steps**

1. Set up a real Valkey client (integration test mode — real Valkey required).
2. Write `image:status:{uuid}` with `stage=validating` to Valkey (simulating gateway mid-processing).
3. Configure a `StaleImageReaper` with `StaleThreshold=2s, ScanInterval=500ms`.
4. Start the reaper in a goroutine with a cancellable context.
5. Assert: after 3 seconds, `HGET image:status:{uuid} stage` returns `"failed"`.
6. Assert: `HGET image:status:{uuid} error` returns `"image processing timed out"`.
7. Cancel the reaper context; assert goroutine exits within 1 second.

Add a second test `TestRetryUploadEndpoint_ClearsGuardAndReturnsNewURL` verifying the retry-upload API endpoint works end-to-end:
1. Create an anonymous user and a route in PENDING state with one image.
2. Call `POST /routes/{route_id}/waypoints/{waypoint_id}/retry-upload`.
3. Assert HTTP 200 with `image_id`, `upload_url`, `expires_at` in response.
4. Assert the upload guard for the original image ID was cleared (the endpoint is already implemented; the test validates the contract).

**Build Tag**

Both tests must have `//go:build integration` at the top.

**Files Affected**

- `tests/integration/stale_reaper_test.go` — NEW

**Dependencies**

Tasks A-C must be complete and the SSEStreamer changes merged (Task C reduces `MaxDuration`). Task G (retry-upload in follow-api) must be deployed — it is already implemented per edge-case-resilience-plan Task 10.

**Acceptance Criteria**

- Tests tagged `//go:build integration`.
- `TestStaleImageReaper_MarksFailedAndSSEEmitsFailedEvent` passes with `INTEGRATION_TEST_MODE=local go test -tags=integration -v ./tests/integration/ -run TestStaleImageReaper -timeout 30s`.
- Test fails if `stage=failed` does not appear within 5 seconds (proving the reaper fired in time, not just eventually).
- `golangci-lint --build-tags integration` passes on the new file.

---

## Cross-Repo Dependencies

| From | To | What |
|------|----|------|
| follow-api reaper (Task A-B) | follow-pkg | Uses `valkey.KeyPrefixImageStatus`, `valkey.FieldStage`, `valkey.StageFailed`, `valkey.FieldError` constants — no changes to follow-pkg needed |
| follow-app (Task G) | follow-api | `POST /routes/{id}/waypoints/{wid}/retry-upload` — already implemented (edge-case-resilience-plan Task 10), no API changes needed |
| follow-app (Task D) | follow-api SSE | `all_done` field in `complete` event — already sent by follow-api `sendCompleteAndClose`, no API changes needed |
| tests/integration (Task J) | follow-api Tasks A-B | StaleImageReaper must be deployed and running |

---

## Files Affected Summary

### follow-api

| File | Change |
|------|--------|
| `internal/infrastructure/reaper/stale_image_reaper.go` | NEW — reaper struct, Run, runScan, markFailed |
| `internal/infrastructure/reaper/errors.go` | NEW — ErrClientNil |
| `internal/infrastructure/reaper/stale_image_reaper_test.go` | NEW — 7 unit tests |
| `cmd/server/app.go` | MODIFY — new `initReaper` method, add step to Init sequence |
| `internal/infrastructure/streaming/sse_streamer.go` | MODIFY — reduce MaxDuration default to 2 min, add constant |
| `internal/infrastructure/streaming/sse_streamer_test.go` | MODIFY — add default MaxDuration assertion test |

### follow-app

| File | Change |
|------|--------|
| `lib/data/services/route_status_stream_service.dart` | MODIFY — add `allDone` field + `isAllDone` getter |
| `lib/data/models/retry_upload_result.dart` | NEW — RetryUploadResult model |
| `test/data/models/retry_upload_result_test.dart` | NEW — 3 unit tests |
| `lib/data/repositories/route_repository.dart` | MODIFY — add `retryUploadWaypointImage` |
| `lib/ui/route_creation/route_creation_view_model.dart` | MODIFY — `all_done` handling, retry logic, new state fields |
| `lib/ui/route_creation/route_creation_screen.dart` | MODIFY — retry UI panel |
| `lib/l10n/app_en.arb` | MODIFY — 7 new strings |
| `lib/l10n/app_he.arb` | MODIFY — 7 Hebrew translations |

### tests/integration

| File | Change |
|------|--------|
| `tests/integration/stale_reaper_test.go` | NEW — reaper marks stale images failed, retry-upload endpoint |

---

## End-to-End Acceptance Criteria

1. **Gateway crash scenario**: When the gateway crashes mid-processing, within 30-45 seconds (reaper threshold 30s + one scan interval 10s), the image's Valkey hash shows `stage=failed`. The SSEStreamer picks this up on the next 500ms poll tick and emits a `failed` event. The Flutter app shows the "Processing Failed" panel with retry buttons.

2. **Normal flow unchanged**: When all images process normally (`all_done=true`, zero failures), the publish step fires automatically without user interaction. No behavior change.

3. **Retry succeeds**: User taps "Retry" for a failed waypoint. The app calls `retryUploadWaypointImage`, re-uploads the image to the (restarted) gateway, SSE emits `ready`. When all retries succeed, "Publish Route" becomes enabled.

4. **Publish after retry**: User taps "Publish Route". Route transitions to PUBLISHED. App navigates home. No 422 error.

5. **2-minute safety net**: If images are stuck for any reason and the reaper has not yet fired (first 30 seconds) or fails transiently, `MaxDuration=2min` terminates the SSE stream and `complete(all_done=false)` is sent, triggering the same retry UI.

6. **Terminal images never overwritten**: The reaper never writes `stage=failed` to images that are already `stage=done` or `stage=failed`.

7. **Context cancellation**: The reaper goroutine exits cleanly when the server context is cancelled during shutdown.

---

## Risk Factors

### SCAN performance with many keys

SCAN with pattern `image:status:*` is non-blocking but iterates all keyspace. With the default TTL of 1 hour and typical upload volumes (1-20 images per upload session), the number of active `image:status:*` keys is always tiny. This is not a concern for MVP. If key volume grows significantly in production, a registration-based approach (Task A Option B from the problem description) can replace SCAN as a follow-up.

### Reaper fires before gateway finishes (false positive)

If the gateway is alive but processing slowly (longer than 30 seconds), the reaper will mark images as `failed` even though the gateway would have succeeded. The 30-second threshold is calibrated to be significantly longer than normal gateway processing time (typically 2-10 seconds). If slow images become a production concern, `StaleThreshold` can be increased via config without a code change (add `REAPER_STALE_THRESHOLD_SECONDS` env var as a follow-up).

### Race: gateway writes after reaper marks failed

If the gateway writes `stage=done` to the hash after the reaper writes `stage=failed`, the hash will show `stage=done`. The SSEStreamer will then emit `ready` for that image. This is correct behavior — the gateway won. The client will proceed to publish normally. No special handling needed.

### Flutter: SSE re-polling resolves multiple failures at once

When `retryWaypointUpload` re-opens the SSE stream, it polls for all non-terminal images, not just the retried one. If multiple waypoints failed, a single retry call may resolve multiple failures simultaneously. This is desirable, not a bug.

### Import cycle risk

`internal/infrastructure/reaper` imports `follow-pkg/valkey` (for constants) and `zerolog` (for logging). It does not import `internal/infrastructure/streaming` or any other internal package. No import cycle risk.

---

## Story Points Summary

| Task | Points | Complexity |
|------|--------|------------|
| A: Stale Image Reaper implementation | 5 | High |
| B: Wire reaper in app.go | 2 | Medium |
| C: Reduce MaxDuration to 2 min | 1 | Low |
| D: Parse `all_done` in Flutter | 1 | Low |
| E: `_waitForProcessingComplete` return type | 2 | Medium |
| F: RetryUploadResult model | 1 | Low |
| G: Retry-upload in RouteRepository | 2 | Medium |
| H: ViewModel failure handling | 3 | High |
| I: Retry UI | 5 | High |
| J: Integration test | 3 | High |
| **Total** | **25** | |
