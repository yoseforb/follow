# Async Image Pipeline: Edge Case Resilience Plan

**Status**: Backlog
**Scope**: Cross-repo — affects `follow-pkg`, `follow-api`, `follow-image-gateway`, `tests/integration`
**Goal**: Make the async image processing pipeline production-ready by fixing nine identified design gaps.

---

## Context

The async pipeline (follow-image-gateway -> Valkey Streams -> follow-api) is functionally complete but has edge cases that leave routes stuck, messages unackable, or the consumer non-recoverable. All nine decisions have been approved. No backward compatibility is required.

### Current State (what exists)

- `follow-pkg/valkey/`: `Consumer` (single-use, sync.Once, dies after MaxRetries), `Reclaimer` (single-use, sync.Once), `processAndAck` (binary: nil=ACK, error=PEL), `Producer` (no retry), `UploadGuard` (SET NX only, no delete method)
- `follow-api`: consumer starts once in goroutine, no supervisor; `handleMessage` treats all errors as transient; SSE polls Valkey blindly (no PostgreSQL fallback at startup)
- `follow-image-gateway`: `PublishResult` is fire-and-forget for both progress update and stream publish

### Approved Fixes Summary

| Decision | Fix |
|----------|-----|
| 1: Route stuck on image failure | New `POST /routes/{id}/waypoints/{wid}/retry-upload` endpoint; clears upload guard |
| 2: Poison pill messages | Permanent vs transient error classification; DLQ after 10 deliveries |
| 3: Consumer resilience | NOGROUP auto-recovery; remove sync.Once; supervisor loop in app.go |
| 4: Gateway publish retry | Exponential backoff (3 attempts) on `Producer.Publish` and `Producer.PublishWithTrim` |
| 5: Gateway crash mid-pipeline | Solved by Decision 1 (retry-upload clears guard) |
| 6: Route deletion during processing | Solved by Decision 2 (ErrImageNotFound -> permanent ACK) |
| 7: Rapid image replacement | Block second `replace-image/prepare` if pending exists; 409 Conflict |
| 8: SSE and replacement images | No change needed |
| 9: SSE with expired Valkey hashes | Check PostgreSQL route status at SSE startup; emit complete immediately if ready/published |

---

## Planning Order

Tasks are ordered per the required implementation sequence:
1. Contract changes (`ai-docs/contracts/`)
2. Shared library changes (`follow-pkg/valkey/`)
3. API service changes (`follow-api`)
4. Gateway service changes (`follow-image-gateway`)
5. Integration tests (`tests/integration/`) — written as failing tests first (TDD)

---

## Tasks

---

### Task 1: Update Valkey Message Contract Documentation

**Story Points**: 1

**Description**

Add the DLQ stream key to the contract document. The DLQ (`image:result:dlq`) is new cross-service state written by `follow-api` when a message exceeds 10 delivery attempts. Document it so all engineers know it exists and what it contains.

**Files Affected**

- `ai-docs/contracts/valkey-message-contract.md` — add new section

**Contract Addition**

Add a new section documenting:

```
Key Pattern 4: image:result:dlq (Dead Letter Queue Stream)

| Aspect | Value |
| Type | Redis Stream |
| Stream Key | image:result:dlq (valkey.StreamImageResultDLQ) |
| Writer | follow-api consumer (when delivery_count >= 10) |
| Reader | Operator inspection only (no automated consumer) |
| Trimming | MAXLEN ~1000 |

Fields written (copied from original message plus metadata):
  - All original fields from image:result message
  - dlq_reason: string — why it was dead-lettered ("max_deliveries_exceeded")
  - dlq_delivery_count: string — delivery count at time of DLQ
  - dlq_at: RFC3339 timestamp
```

**Acceptance Criteria**

- `StreamImageResultDLQ = "image:result:dlq"` constant is documented as source of truth
- DLQ field names (`dlq_reason`, `dlq_delivery_count`, `dlq_at`) are documented
- Rules section updated to state: "After 10 delivery attempts, a message is moved to the DLQ stream and ACKed from the main stream"

**Dependencies**: None

---

### Task 2: Add DLQ Stream Constant to `follow-pkg/valkey/contracts.go`

**Story Points**: 1

**Description**

Add the `StreamImageResultDLQ` constant and the three DLQ field name constants to `contracts.go`. Both `follow-api` (which writes to DLQ) and any future reader reference these constants. This must come before any code changes in other repos.

**Files Affected**

- `follow-pkg/valkey/contracts.go` — add constants

**Constants to Add**

```go
// ---- Dead Letter Queue ----.
const (
    // StreamImageResultDLQ is the stream key for messages that
    // exceeded MaxDeliveries. Operator-inspected only.
    StreamImageResultDLQ = "image:result:dlq"

    // DLQFieldReason is the reason a message was dead-lettered.
    DLQFieldReason = "dlq_reason"

    // DLQFieldDeliveryCount is the delivery count at DLQ time.
    DLQFieldDeliveryCount = "dlq_delivery_count"

    // DLQFieldAt is the RFC3339 timestamp when the message was dead-lettered.
    DLQFieldAt = "dlq_at"
)
```

**Acceptance Criteria**

- `go vet ./...` passes
- `go test -race -cover ./...` passes
- Constants are exported and documented with godoc comments

**Dependencies**: Task 1 (contract documented before constants added)

---

### Task 3: Add `ClearUpload` Method to `UploadGuard`

**Story Points**: 1

**Description**

`UploadGuard` currently only has `ClaimUpload`. The retry-upload endpoint (Decision 1) needs to clear a failed image's upload guard so the gateway accepts a fresh upload. Add `ClearUpload(ctx, imageID)` which calls `client.Del` on the guard key.

**Files Affected**

- `follow-pkg/valkey/upload_guard.go` — add `ClearUpload` method
- `follow-pkg/valkey/upload_guard_test.go` or equivalent — add tests

**Implementation**

```go
// ClearUpload removes the upload guard for the given imageID,
// allowing a fresh upload attempt. Used by retry-upload endpoint
// after a failed image processing result.
func (u *UploadGuard) ClearUpload(
    ctx context.Context,
    imageID string,
) error {
    key := fmt.Sprintf("%s:%s", u.prefix, imageID)
    if err := u.client.Del(ctx, key); err != nil {
        return fmt.Errorf("upload guard clear failed: %w", err)
    }
    return nil
}
```

**Acceptance Criteria**

- `ClearUpload` deletes the key `{prefix}:{imageID}` via `client.Del`
- After `ClaimUpload` then `ClearUpload`, `ClaimUpload` succeeds again
- Unit test covers: clear on existing key succeeds, clear on non-existent key succeeds (idempotent), `Del` error propagates
- `go test -race -cover ./...` passes in `follow-pkg`

**Dependencies**: None (independent of Task 2)

---

### Task 4: Redesign `MessageHandler` Contract for Permanent vs Transient Errors

**Story Points**: 3

**Description**

The current `MessageHandler = func(ctx, msg) error` contract is binary: nil = ACK, error = keep in PEL. This cannot distinguish poison pills from transient failures.

Replace the handler contract with a `HandlerResult` type that explicitly classifies the outcome. Redesign `processAndAck` to act on the classification. Add DLQ logic using delivery count from `XAutoClaim`/`XPending` or a delivery count field embedded in the message context.

**Design Decision — Delivery Count**

Valkey tracks delivery count per message in the PEL. The `StreamMessage` struct must expose it. Check `client.XPending` for delivery count or embed it in `StreamMessage.DeliveryCount` (populated by the client wrapper from XREADGROUP response). The simpler approach: add `DeliveryCount int64` to `StreamMessage` populated from the XREADGROUP result. XREADGROUP returns the delivery count in the message header when using the pending entries format; with `">"` (new messages), delivery count is 1. The Reclaimer via XAUTOCLAIM gets the count from the claimed message metadata.

Given the valkey-io/valkey-go client library, the approach is: after XAUTOCLAIM, get delivery count via `XPENDING stream group - + 1 consumer` for claimed messages, or use the count field from the XAUTOCLAIM response which includes it in extended format. For simplicity: add `DeliveryCount` to `StreamMessage`, populate it from the XAUTOCLAIM response when available (the valkey-io library's `XAutoClaim` returns `XRangeEntry` which may not include delivery count). Use `XPENDING` to query count when needed.

**Simpler approach**: After a handler returns `HandlerResultPermanent` or after N `HandlerResultTransient` returns, the message is checked: query `XPENDING stream group - + 1 consumer` filtered by message ID to get delivery count. If `>= MaxDeliveries`, move to DLQ + ACK. This keeps `StreamMessage` unchanged and avoids parsing XAUTOCLAIM extended format.

Actually, the cleanest approach for the DLQ threshold: add `DeliveryCount int64` to `StreamMessage`. Populate it:
- For XREADGROUP new messages (`>`): delivery count = 1 (first delivery)
- For Reclaimer XAUTOCLAIM messages: query `XPENDING stream group msgID msgID 1` to get delivery count per message

Wait — the simpler correct approach: expose delivery count from `XPENDING` range query inside `processAndAck`. Given that `processAndAck` already has the client and message ID, it can query `XPENDING stream group <msgID> <msgID> 1` after a transient failure to check if threshold is reached. This avoids changing `StreamMessage` at all.

**Final Design**

```go
// HandlerResult classifies how the consumer should respond to
// a message after the handler runs.
type HandlerResult int

const (
    // HandlerResultACK — processing succeeded; XACK the message.
    HandlerResultACK HandlerResult = iota

    // HandlerResultPermanent — message is invalid and cannot be
    // retried (e.g., unknown image_id, invalid UUID). WARN log + XACK.
    HandlerResultPermanent

    // HandlerResultTransient — recoverable failure (e.g., DB
    // timeout). Keep message in PEL for redelivery.
    HandlerResultTransient
)

// MessageHandler is a callback function that processes messages
// from a stream. It returns a HandlerResult to indicate whether
// the message should be ACKed, dead-lettered (permanent error),
// or kept in PEL (transient error).
//
// Handlers MUST be idempotent (at-least-once delivery).
type MessageHandler func(
    ctx context.Context,
    msg StreamMessage,
) HandlerResult
```

`processAndAck` becomes `processAndAckV2`:
- `HandlerResultACK` -> XACK
- `HandlerResultPermanent` -> WARN log + XACK
- `HandlerResultTransient` -> query PEL for delivery count; if `>= MaxDeliveries` -> move to DLQ stream + XACK; else -> keep in PEL (no ACK)

DLQ write: `XADD image:result:dlq * {all_original_fields} dlq_reason "max_deliveries_exceeded" dlq_delivery_count "{count}" dlq_at "{now}"`

**Files Affected**

- `follow-pkg/valkey/consumer.go` — update `MessageHandler` type, `ConsumerConfig` add `MaxDeliveries int`, update `readAndProcessMessages`
- `follow-pkg/valkey/reclaimer.go` — update `claimIdleMessages` to use new handler contract
- `follow-pkg/valkey/message_processor.go` — replace `processAndAck` with new logic; add DLQ write helper
- `follow-pkg/valkey/errors.go` — add `ErrPendingQueryFailed` if needed
- All existing tests for `Consumer` and `Reclaimer` in `follow-pkg` — update handler signatures

**Config Addition to `ConsumerConfig`**

```go
// MaxDeliveries is the maximum number of times a message can be
// delivered before it is moved to the DLQ stream. Default: 10.
// Set to 0 to disable DLQ behavior.
MaxDeliveries int
```

Default: 10 (applied in `NewConsumer` if <= 0).

**DLQ Producer**

The `processAndAck` helper needs a way to publish to the DLQ stream. Pass a `*Producer` (or just the `Client`) into `processAndAck`. Since `processAndAck` already receives `client Client`, it can call `client.XAdd` directly for DLQ publishing without needing a `Producer` wrapper. Keep it simple.

**Acceptance Criteria**

- `MessageHandler` returns `HandlerResult` (not `error`)
- `processAndAck` (renamed or updated in-place) implements the three-case logic
- Permanent result: message is ACKed + WARN logged; no retry
- Transient result: delivery count checked; if `>= MaxDeliveries` -> DLQ write + ACK; else -> PEL retained
- ACK result: message is ACKed
- `MaxDeliveries` defaults to 10
- DLQ message contains all original fields plus three DLQ fields using constants from `contracts.go`
- `Consumer` and `Reclaimer` both use new handler contract
- All existing `follow-pkg` tests updated and passing
- `go test -race -cover ./...` passes in `follow-pkg`

**Dependencies**: Task 2 (DLQ stream constant), Task 3 (independent)

---

### Task 5: Remove `sync.Once` from `Consumer`, Make It Restartable

**Story Points**: 3

**Description**

`Consumer.Start()` is currently single-use: `sync.Once` prevents calling `Start` a second time, returning `ErrConsumerAlreadyStarted`. The supervisor loop in `app.go` (Task 8) needs to create a new `Consumer` instance and call `Start` fresh after failure — this is fine since `NewConsumer` is cheap. However, `ErrConsumerAlreadyStarted` is confusing when the supervisor creates a brand new instance. The real problem is `sync.Once` preventing any kind of recovery from `readWithRetry` failures.

Current flow: `readWithRetry` exhausts `MaxRetries` (default 3) -> returns error -> `Start` returns error -> goroutine exits -> no recovery. The supervisor in Task 8 will call `NewConsumer` + `Start` again.

Additionally, `Consumer` must detect `NOGROUP` errors (the consumer group was deleted externally) and recreate the group rather than treating it as a fatal error.

**Changes**

1. Remove `sync.Once` from `Consumer`. Replace with a simple `started bool` protected by the existing `mu` mutex. Return `ErrConsumerAlreadyStarted` if `Start` called on same instance twice — this protects against accidental double-start of the same instance, while a fresh `NewConsumer` instance is fully restartable.

2. In `readAndProcessMessages`, detect `ErrGroupNotFound` (the NOGROUP error mapped from Valkey) returned by `XReadGroup`. When detected: call `createGroup(ctx)` to recreate the consumer group, then retry the read. This is a one-shot recovery within the same iteration; if the group still cannot be created, return an error to trigger `readWithRetry`'s backoff.

3. In `readWithRetry`, raise the cap from `10s` to `30s` (per Decision 3) to match the requirement of "capped exponential backoff (cap 30s)".

**Files Affected**

- `follow-pkg/valkey/consumer.go` — replace `startOnce sync.Once` with mutex-protected bool; NOGROUP detection and recovery in `readAndProcessMessages`; adjust backoff cap to 30s
- `follow-pkg/valkey/consumer_test.go` — add test: consumer recovers from NOGROUP, consumer errors on double-start of same instance

**Acceptance Criteria**

- `NewConsumer(client, cfg, log)` followed immediately by a second `NewConsumer(client, cfg, log)` produces two independent instances, both startable
- Calling `Start` twice on the same `Consumer` instance returns `ErrConsumerAlreadyStarted`
- When Valkey returns a NOGROUP error during `XReadGroup`, the consumer calls `createGroup` and retries within the same `readWithRetry` attempt (does not count as a retry against `MaxRetries`)
- `readWithRetry` backoff cap is 30s
- `go test -race -cover ./...` passes in `follow-pkg`

**Dependencies**: Task 4 (handler contract changed, tests must use new `HandlerResult` type)

---

### Task 6: Remove `sync.Once` from `Reclaimer`, Make It Restartable

**Story Points**: 2

**Description**

Same pattern as Task 5 but for `Reclaimer`. The Reclaimer is also single-use due to `sync.Once`. The supervisor (Task 8) will create a fresh instance, so the primary concern is removing the `sync.Once` confusion and ensuring a fresh instance starts cleanly.

Additionally, the Reclaimer has no DLQ awareness today: it calls `processAndAck` with the old binary handler contract. After Task 4, it will use the new `HandlerResult`-based `processAndAck`. Ensure the Reclaimer passes `MaxDeliveries` through so PEL messages that have been reclaimed many times are eventually dead-lettered.

**Files Affected**

- `follow-pkg/valkey/reclaimer.go` — replace `startOnce sync.Once` with mutex-protected bool

**Acceptance Criteria**

- Same restartability guarantees as `Consumer` (Task 5)
- Reclaimer uses the `HandlerResult`-based `processAndAck` (from Task 4)
- Double-start on same instance returns `ErrReclaimerAlreadyStarted`
- `go test -race -cover ./...` passes in `follow-pkg`

**Dependencies**: Task 4, Task 5

---

### Task 7: Add Retry with Exponential Backoff to `Producer`

**Story Points**: 2

**Description**

`Producer.Publish` and `Producer.PublishWithTrim` are single-attempt, fire-and-forget in caller code. When Valkey is briefly unavailable (e.g., rolling restart), a one-shot publish loses the result permanently.

Add internal retry with exponential backoff directly into `Producer`: 3 attempts, starting at 100ms (100ms -> 200ms -> 400ms). Context cancellation aborts the retry loop. On all attempts failing, return the last error.

This is pure `follow-pkg` behavior and benefits all consumers of `Producer` (both services).

**Files Affected**

- `follow-pkg/valkey/producer.go` — add retry loop to `Publish` and `PublishWithTrim`
- `follow-pkg/valkey/producer_test.go` — add tests for retry behavior

**Implementation Sketch**

```go
func (p *Producer) publishWithRetry(
    ctx context.Context,
    fn func() (string, error),
) (string, error) {
    const maxAttempts = 3
    backoff := 100 * time.Millisecond
    var lastErr error
    for attempt := 0; attempt < maxAttempts; attempt++ {
        id, err := fn()
        if err == nil {
            return id, nil
        }
        lastErr = err
        if attempt < maxAttempts-1 {
            timer := time.NewTimer(backoff)
            select {
            case <-timer.C:
                backoff *= 2
            case <-ctx.Done():
                timer.Stop()
                return "", fmt.Errorf(
                    "publish aborted: %w", ctx.Err(),
                )
            }
        }
    }
    return "", fmt.Errorf("publish failed after retries: %w", lastErr)
}
```

**Acceptance Criteria**

- `Publish` retries up to 3 times (100ms, 200ms delays) before returning error
- `PublishWithTrim` retries identically
- Context cancellation during backoff returns immediately with context error
- On first success, no further retries
- `go test -race -cover ./...` passes in `follow-pkg`
- `ProgressTracker.SetProgress` is NOT changed here (it lives in `progress.go`; the gateway calls it separately — gateway change is in Task 11)

**Dependencies**: None (independent library change)

---

### Task 8: Implement Consumer Supervisor Loop in `follow-api/cmd/server/app.go`

**Story Points**: 2

**Description**

Currently `initConsumer` starts one goroutine and if it exits with an error, it is logged but the consumer is dead forever. Implement a supervisor loop that restarts the consumer (creates a new instance and starts it) indefinitely on error, with capped exponential backoff (100ms -> 200ms -> ... -> 30s cap).

The supervisor runs for the lifetime of the application context. When the context is cancelled (shutdown), the supervisor exits cleanly.

**Files Affected**

- `follow-api/cmd/server/app.go` — replace single goroutine launch with supervisor goroutine; update `initConsumer` to create consumer factory function

**Implementation Sketch**

In `initConsumer`, extract a `newConsumer` factory function (closure over modules and config) that creates a fresh `ImageResultConsumer`. Then start the supervisor goroutine:

```go
go func() {
    backoff := 100 * time.Millisecond
    for {
        consumer := newConsumer()
        startErr := consumer.Start(ctx)
        if ctx.Err() != nil {
            // App is shutting down — not an error.
            return
        }
        if startErr != nil {
            a.log.Error().
                Err(startErr).
                Dur("backoff", backoff).
                Msg("image result consumer exited, restarting")
        }
        timer := time.NewTimer(backoff)
        select {
        case <-timer.C:
            backoff = min(backoff*2, 30*time.Second)
        case <-ctx.Done():
            if !timer.Stop() {
                <-timer.C
            }
            return
        }
    }
}()
```

Wait for `Ready()` on the first consumer instance before proceeding with server startup (as today). The supervisor's subsequent restarts do not block startup.

The `a.imageResultConsumer` field becomes a pointer to the most recent consumer, updated by the supervisor. Since `stopConsumer()` in Shutdown calls `a.imageResultConsumer.Stop()`, the supervisor must be stopped by context cancellation (not via the consumer field).

**Design Note**: The supervisor goroutine is context-driven. The app context is cancelled by the signal handler during shutdown, which causes the running consumer to return, the supervisor to exit, and no further restarts to be attempted.

**Files Affected**

- `follow-api/cmd/server/app.go` — supervisor goroutine in `initConsumer`, remove the single `stopConsumer` field dependency (the supervisor owns consumer lifecycle)

**Acceptance Criteria**

- After a consumer exits with error (e.g., simulated by XGROUP DESTROY in EC-9 integration test), a new consumer is created and starts reading
- Backoff starts at 100ms, caps at 30s
- On application shutdown (context cancel), supervisor exits without starting another consumer
- Server startup still waits for the first consumer to be ready before accepting requests
- `go run ./cmd/server -runtime-timeout 10s` succeeds

**Dependencies**: Task 5 (Consumer restartability), Task 4 (handler contract)

---

### Task 9: Update `ImageResultConsumer.handleMessage` for Permanent vs Transient Classification

**Story Points**: 2

**Description**

`handleMessage` currently returns `error` (old contract). After Task 4, it must return `HandlerResult`. Implement the classification logic:

**Permanent errors (ACK immediately + WARN):**
- `ErrMissingImageID` — malformed message, will never succeed
- `ErrInvalidImageID` — malformed message, will never succeed
- `ErrMissingStatus` — malformed message, will never succeed
- `ErrImageModuleFailed` wrapping `imageDomain.ErrImageNotFound` — image deleted from DB; pointless to retry
- `ErrRouteModuleFailed` wrapping `routeDomain.ErrWaypointNotFound` — waypoint deleted from DB; pointless to retry
- `ErrRouteModuleFailed` wrapping `routeDomain.ErrRouteNotFound` — route deleted from DB; pointless to retry

**Transient errors (keep in PEL):**
- `ErrImageModuleFailed` with any other underlying error (e.g., DB timeout)
- `ErrRouteModuleFailed` with any other underlying error
- `ErrMissingImageResult`, `ErrMissingRouteResult` — internal inconsistency, retry may help

**ACK (success):**
- `processor.Process` returns nil

**Implementation Note**: To classify `ErrImageModuleFailed` vs not-found, use `errors.Is(err, imageDomain.ErrImageNotFound)`. The processor wraps domain errors with `fmt.Errorf("%w: %w", ErrImageModuleFailed, err)`, so `errors.Is(procErr, imageDomain.ErrImageNotFound)` works through the wrap chain.

**Files Affected**

- `follow-api/internal/infrastructure/consumers/image_result_consumer.go` — change `handleMessage` return type from `error` to `valkey.HandlerResult`; implement classification logic
- `follow-api/internal/infrastructure/consumers/errors.go` — no new errors needed
- `follow-api/internal/infrastructure/consumers/image_result_consumer_test.go` — update tests for new return type

**Acceptance Criteria**

- `handleMessage` returns `valkey.HandlerResult`, not `error`
- Malformed messages (missing field, invalid UUID) return `HandlerResultPermanent`
- `ErrImageNotFound`, `ErrWaypointNotFound`, `ErrRouteNotFound` errors return `HandlerResultPermanent`
- DB/infrastructure errors return `HandlerResultTransient`
- Success returns `HandlerResultACK`
- Permanent results are logged at WARN level with message ID and reason
- `go test -race -cover ./...` passes in `follow-api`

**Dependencies**: Task 4 (new `HandlerResult` type and updated `processAndAck`)

---

### Task 10: Add `RetryUploadWaypointImage` Use Case to `follow-api`

**Story Points**: 3

**Description**

Implement the full stack for `POST /routes/{route_id}/waypoints/{waypoint_id}/retry-upload`:
- Validates route ownership and waypoint membership
- Validates the waypoint's primary image is in `failed` status
- Clears the old upload guard via `UploadGuard.ClearUpload` (from Task 3)
- Generates a fresh presigned upload URL (new image record created, or reuse existing failed image record — see design note)
- Returns the new upload URL

**Design Note — Image Record Strategy**

Two options:
1. Reuse the failed image ID: reset its status from `failed` back to `pending`, generate a new upload URL with the same image ID. Requires an `image.ResetImageStatus` command.
2. Create a new image ID: expire the old failed image record, create a new image record with new ID, update the waypoint to point to new image ID.

**Decision**: Create a new image ID. This is cleaner — the old failed image record stays in history as `failed`, the waypoint's image ID is updated to the new `pending` image record. The gateway JWT encodes the image ID, so a fresh ID means a fresh token. The Valkey guard key is keyed by image ID, so clearing the old guard is still needed to allow the gateway to accept the new upload URL (which will have the new image ID — so no guard exists yet for the new ID anyway). Therefore, the `ClearUpload` on the old image ID guard is still needed only if the gateway's guard TTL is still active for the old ID.

**Revised Decision**: Keep it simple — the gateway's upload guard is keyed by `image_id`. A new image record gets a new UUID, so its guard doesn't exist yet. The old guard (for the failed image ID) will expire naturally within 1 hour. No explicit `ClearUpload` call is needed here. The old image record is marked `expired`.

**Wait — Decision 1 explicitly says**: "The endpoint must clear the old upload guard (`image:upload:{id}`) so re-upload is allowed." But if we assign a new image ID, the new ID has no guard, so there is nothing to clear. The `ClearUpload` is needed only when reusing the same image ID.

**Final Decision**: Create a new image ID (cleaner). No explicit guard clear needed (new ID has no guard). Include `UploadGuard.ClearUpload` on the old image ID as a best-effort cleanup step anyway (belt-and-suspenders), since the old guard may still be active.

**Use Case Steps**

1. Verify user exists
2. Load route by ID + user ID (ownership check)
3. Load waypoint by ID; verify it belongs to the route
4. Load the waypoint's primary image; verify `status == failed`
5. Mark old image record as `expired` via image domain command
6. Clear old upload guard: `uploadGuard.ClearUpload(ctx, oldImageID.String())`
7. Create new image record via image domain (same metadata as original): returns new `image_id` + presigned upload URL
8. Update waypoint's `image_id` to new image ID (waypoint status stays `pending`)
9. Reset Valkey progress hash for new image to `stage=queued`
10. Return `{ image_id, upload_url, expires_at }`

**New Command/Query Needed**

- `imageModule.ExpireImageCommand{ImageID}` — mark image as `expired` (or reuse `RemoveImages` if it marks expired)
- Reuse existing `imageService.PrepareImageUploads` for step 7
- New waypoint repository method or use existing `Update`

Check existing `imageService.RemoveImages(ctx, []uuid.UUID, false)` — if it marks as `expired`, it can be reused. Verify in the image domain code.

**Goa DSL Addition**

```go
Method("retry_upload_waypoint_image", func() {
    Description(
        "Retry upload for a failed waypoint image. "+
        "Clears the failed state and returns a fresh upload URL.",
    )
    Security(JWTAuth, func() {
        Scope("api:write")
        Scope("images:upload")
    })

    Payload(func() {
        Token("token", String)
        Field(1, "route_id", String, func() {
            Format("uuid")
        })
        Field(2, "waypoint_id", String, func() {
            Format("uuid")
        })
        Required("route_id", "waypoint_id")
    })

    Result(RetryUploadWaypointImageResult)

    HTTP(func() {
        POST(
            "/{route_id}/waypoints/{waypoint_id}/retry-upload",
        )
    })

    Error("user_validation_failed")
    Error("route_not_owned_by_user")
    Error("not_found")
    Error("invalid_input")
})
```

Result type:

```go
var RetryUploadWaypointImageResult = Type(
    "RetryUploadWaypointImageResult", func() {
    Field(1, "image_id", String)
    Field(2, "upload_url", String)
    Field(3, "expires_at", String)
    Required("image_id", "upload_url", "expires_at")
})
```

**Files Affected**

- `follow-api/design/route_service.go` — add `retry_upload_waypoint_image` method
- `follow-api/design/route_types.go` — add `RetryUploadWaypointImageResult` type
- `follow-api/gen/` — regenerate Goa code (`goa gen follow-api/design`)
- `follow-api/internal/domains/route/interfaces/` — add `RetryUploadWaypointImageInput`, `RetryUploadWaypointImageOutput`, `RetryUploadWaypointImageUseCaseInterface`
- `follow-api/internal/domains/route/usecases/retry_upload_waypoint_image_usecase.go` — new use case
- `follow-api/internal/domains/route/usecases/retry_upload_waypoint_image_usecase_test.go` — unit tests
- `follow-api/internal/domains/route/module/commands.go` — add `RetryUploadWaypointImageCommand`
- `follow-api/internal/domains/route/module/route_module.go` — add handler for new command
- `follow-api/internal/api/services/routes_service.go` — add `RetryUploadWaypointImage` method
- `follow-api/internal/infrastructure/gateway/upload_guard_client.go` (new or existing) — inject `UploadGuard` into the use case

**Acceptance Criteria**

- `POST /routes/{id}/waypoints/{wid}/retry-upload` returns 200 with `{image_id, upload_url, expires_at}`
- Returns 404 if route or waypoint not found
- Returns 403 if route not owned by user
- Returns 422 if waypoint's image is NOT in `failed` state (not applicable for retry)
- Returns 401 if JWT missing/invalid
- After calling retry-upload, uploading the returned URL results in the route eventually transitioning to `ready`
- Old upload guard for the failed image ID is cleared (best-effort)
- `go test -race -cover ./...` passes in `follow-api`
- `go run ./cmd/server -runtime-timeout 10s` succeeds

**Dependencies**: Task 3 (`ClearUpload` exists), Task 9 (`handleMessage` classification done so that creation-flow failures are treated correctly)

---

### Task 11: Block Second `replace-image/prepare` When Pending Replacement Exists

**Story Points**: 2

**Description**

`PrepareReplaceWaypointImageUseCase` currently silently overwrites an existing pending replacement: it expires the old pending image and creates a new one. Decision 7 says to instead return 409 Conflict if a pending replacement already exists.

This is a simple precondition check before the existing logic.

**Files Affected**

- `follow-api/internal/domains/route/usecases/prepare_replace_waypoint_image_usecase.go` — add precondition check before the `if waypoint.HasPendingReplacement()` block
- `follow-api/internal/domains/route/usecases/errors.go` — add `ErrWaypointReplacementInProgress`
- `follow-api/design/route_service.go` — add `conflict` error to `replace_waypoint_image_prepare` method
- `follow-api/internal/api/services/routes_service.go` — map `ErrWaypointReplacementInProgress` to 409
- `follow-api/internal/domains/route/usecases/prepare_replace_waypoint_image_usecase_test.go` — add test for 409 case

**New Error**

```go
// ErrWaypointReplacementInProgress indicates a replacement is already
// in progress for this waypoint. Client must wait for current
// replacement to complete or fail before starting another.
ErrWaypointReplacementInProgress = errors.New(
    "waypoint image replacement already in progress",
)
```

**Use Case Change**

Replace the current "silently expire + overwrite" logic:

```go
// Before (current):
if waypoint.HasPendingReplacement() {
    oldPendingID := *waypoint.PendingReplacementImageID()
    _ = uc.imageService.RemoveImages(ctx, []uuid.UUID{oldPendingID}, false)
}

// After (new):
if waypoint.HasPendingReplacement() {
    return nil, fmt.Errorf(
        "%w: waypoint %s already has a pending replacement",
        ErrWaypointReplacementInProgress,
        input.WaypointID,
    )
}
```

**HTTP Mapping**

409 Conflict for `ErrWaypointReplacementInProgress`.

**Acceptance Criteria**

- First `replace-image/prepare` call succeeds (200)
- Second `replace-image/prepare` call on the same waypoint before the first completes returns 409
- After the first replacement completes (image processed), a second `replace-image/prepare` succeeds (because `HasPendingReplacement()` is false once committed)
- `go test -race -cover ./...` passes in `follow-api`

**Dependencies**: None (independent domain change)

---

### Task 12: Fix SSE to Check PostgreSQL Route Status at Startup

**Story Points**: 2

**Description**

When a client connects to `GET /routes/{id}/status/stream` for a route that is already `ready` or `published`, the current SSE implementation blindly starts polling Valkey for up to 5 minutes before timing out with `all_done=false`. This is wrong — the route is done.

Fix: Before starting the Valkey polling loop, check the route's status in PostgreSQL. If `ready` or `published`, emit `complete(all_done=true)` immediately and close the stream.

**Implementation Location**

The check belongs in `routes_service.go`'s `StreamRouteStatus` method, after `resolveImageIDs` returns (which already queries PostgreSQL for image IDs). Add a route status query before delegating to `sseStreamer.Stream`.

The `GetRouteStatusQuery` already exists in the route module and returns the route's current status. Use it.

**Files Affected**

- `follow-api/internal/api/services/routes_service.go` — add route status check in `StreamRouteStatus` before delegating to SSEStreamer
- `follow-api/internal/infrastructure/streaming/sse_streamer.go` — no change needed (the early-exit logic lives in the service layer)

**Implementation in `StreamRouteStatus`**

```go
// 3a. Check if route is already complete (ready or published).
//     If so, emit complete(all_done=true) immediately.
statusResult, statusErr := s.routeModule.ExecuteQuery(
    ctx,
    &routeModule.GetRouteStatusQuery{
        RouteID: routeID,
        UserID:  userID,
    },
)
if statusErr == nil {
    if rs, ok := statusResult.(*routeModule.GetRouteStatusResult); ok {
        if rs.Status != nil &&
            (rs.Status.IsReady() || rs.Status.IsPublished()) {
            adapter := NewGoaStreamAdapter(stream)
            allDone := true
            _ = adapter.Send(&streaming.RouteStatusEventPayload{
                EventType: "complete",
                AllDone:   &allDone,
                Timestamp: time.Now().UTC(),
            })
            return adapter.Close()
        }
    }
}
// statusErr is non-fatal for SSE: if we can't get route status,
// proceed with normal Valkey polling.
```

**Acceptance Criteria**

- Connecting SSE to an already-`ready` route emits `complete(all_done=true)` within 1 second (no 5-minute wait)
- Connecting SSE to a `published` route also emits `complete(all_done=true)` immediately
- Normal SSE flow (route still `pending`) works exactly as before
- `go test -race -cover ./...` passes in `follow-api`

**Dependencies**: None (independent API service change)

---

### Task 13: Add Retry to `ResultPublisher.updateFinalProgress` in `follow-image-gateway`

**Story Points**: 1

**Description**

`updateFinalProgress` calls `progressTracker.SetProgress` once. If Valkey is briefly unavailable at the moment a pipeline job completes, the progress hash is not updated and the SSE poller may never see `stage=done`. Since `Producer` now has retry (Task 7), and `ProgressTracker.SetProgress` does not, add equivalent retry to `updateFinalProgress`.

The retry is local to the gateway's `ResultPublisher` — it is NOT added to `ProgressTracker` itself (which is a shared library that should stay simple). The retry is a 3-attempt loop with 100ms->200ms->400ms backoff, matching the `Producer` pattern.

**Files Affected**

- `follow-image-gateway/internal/messaging/result_publisher.go` — add retry loop around `progressTracker.SetProgress` in `updateFinalProgress`

**Implementation Sketch**

```go
func (rp *ResultPublisher) updateFinalProgress(
    ctx context.Context,
    job *pipeline.ImageJob,
) {
    // ... build fields ...
    const maxAttempts = 3
    backoff := 100 * time.Millisecond
    for attempt := 0; attempt < maxAttempts; attempt++ {
        err := rp.progressTracker.SetProgress(ctx, job.ID, fields)
        if err == nil {
            return
        }
        if attempt < maxAttempts-1 {
            timer := time.NewTimer(backoff)
            select {
            case <-timer.C:
                backoff *= 2
            case <-ctx.Done():
                timer.Stop()
                rp.log.Warn().Err(ctx.Err()).
                    Str("image_id", job.ID).
                    Msg("updateFinalProgress aborted by context")
                return
            }
        }
        rp.log.Warn().
            Err(err).
            Int("attempt", attempt+1).
            Str("image_id", job.ID).
            Msg("Failed to update final progress status")
    }
}
```

**Acceptance Criteria**

- `updateFinalProgress` retries up to 3 times on failure
- `publishToStream` already benefits from `Producer` retry (Task 7) — no additional change needed
- `go test -race -cover ./...` passes in `follow-image-gateway`
- `go run ./cmd/server -runtime-timeout 10s` succeeds

**Dependencies**: Task 7 (Producer retry is a separate change but motivates the pattern)

---

### Task 14: Write Edge Case Integration Tests (TDD — Phase 1, Parallel-Safe)

**Story Points**: 5

**Description**

Write the Phase 1 integration tests. These tests assert correct final behavior. They will FAIL until the implementation tasks above are complete. Tests must be independent of each other (parallel-safe, self-cleaning, no shared state).

**Files Affected**

- `tests/integration/edge_cases_phase1_test.go` — new file

**Build Tag**

```go
//go:build integration
```

**Tests to Implement**

**EC-1: `TestEdgeCase_RouteStuckPendingOnImageFailure`**

Setup:
1. Create user + auth token
2. Prepare route, create 3 waypoints: waypoints 0 and 1 get valid JPEG images, waypoint 2 gets `invalidImageBytes()` (bytes that fail gateway validation)
3. Upload all three images to their presigned URLs
4. Poll Valkey for all three image IDs to reach terminal state (`done` or `failed`), timeout 60s

Assert intermediate state:
- Images 0 and 1 reach `stage=done`
- Image 2 reaches `stage=failed`
- Route status is still `pending` (GET /routes/{id} -> `route_status=pending`)

Retry:
5. Call `POST /routes/{id}/waypoints/{wid2}/retry-upload` (waypoint 2)
6. Assert: 200 response with `{image_id, upload_url, expires_at}`
7. Upload a valid JPEG to the new `upload_url`
8. Poll Valkey for new image_id to reach `stage=done`, timeout 60s

Assert final state:
- GET /routes/{id} -> `route_status=ready`

Cleanup: `t.Cleanup(func() { deleteRoute(t, routeID, authToken) })`

**EC-2: `TestEdgeCase_SSEReportsMixedSuccessAndFailure`**

Setup:
1. Create user + auth token
2. Prepare route, create 2 waypoints: waypoint 0 gets valid image, waypoint 1 gets invalid bytes
3. Upload both images

SSE connection:
4. Open `GET /routes/{id}/status/stream` in a goroutine, collect SSE events into a slice, context timeout 60s

5. Upload images (done before or after SSE connection — race is acceptable; SSE will see terminal states)

Assert collected events:
- At least one `ready` event with an `image_id` matching waypoint 0's image
- At least one `failed` event with an `image_id` matching waypoint 1's image and a non-empty `error_reason`
- Exactly one `complete` event with `all_done=true`
- No `complete(all_done=false)` event

Cleanup: close SSE connection, delete route.

**EC-3: `TestEdgeCase_SSEForAlreadyReadyRoute`**

Setup:
1. Create 1-waypoint route, upload valid image
2. Wait for `GET /routes/{id}` to return `route_status=ready` (poll every 500ms, timeout 60s)

SSE connection (AFTER route is ready):
3. Connect SSE, set context timeout of 5s

Assert:
- First meaningful event is `complete` with `all_done=true`
- Event received within 2s (not 5min timeout)

**EC-4: `TestEdgeCase_SSEDuringImageReplacement`**

Setup:
1. Create 1-waypoint route, upload valid image, wait for `route_status=ready`
2. Publish route: `POST /routes/{id}/publish` -> `route_status=published`
3. Call `POST /routes/{id}/waypoints/{wid}/replace-image/prepare`
4. Connect SSE: `GET /routes/{id}/status/stream`

Upload replacement:
5. Upload valid image to replacement URL
6. Wait for SSE to close (timeout 30s)

Assert SSE events:
- `complete(all_done=true)` received (SSE sees route as published/complete at startup via PostgreSQL check)
- No `failed` events for the original (already-ready) image
- Stream closes within 5s of connecting (fast path, not 5min wait)

**EC-8: `TestEdgeCase_UploadGuardBlocksDuplicate`**

Note: EC-8 is listed here because it's parallel-safe. It tests the existing upload guard behavior (Decision 5/6 already rely on it).

Setup:
1. Create 1-waypoint route
2. Get the presigned upload URL from create-waypoints

Assert:
- First PUT to upload URL -> 202 Accepted
- Second PUT to same upload URL (immediately, same bytes) -> 409 Conflict

Note: This test may already be covered in `upload_guard_test.go`. If it is, skip writing it here and add a comment referencing the existing test.

**Acceptance Criteria**

- All Phase 1 tests compile under `//go:build integration`
- Tests use `t.Parallel()` where safe (EC-1, EC-2, EC-3 are parallel-safe)
- Tests call `t.Cleanup` to delete created routes
- Tests fail at the correct assertion when run before implementation is complete
- Tests pass after all implementation tasks are done

**Dependencies**: Tasks 10 (retry-upload endpoint), 12 (SSE PostgreSQL check) — tests FAIL until these are implemented

---

### Task 15: Write Edge Case Integration Tests (TDD — Phase 2, PEL Tests, Sequential)

**Story Points**: 3

**Description**

Write Phase 2 integration tests that interact with the Valkey PEL directly. These must run sequentially (cannot use `t.Parallel()`) because they inject raw messages into the stream or manipulate consumer state that the running API consumer also watches.

**Files Affected**

- `tests/integration/edge_cases_phase2_test.go` — new file

**Tests to Implement**

**EC-5: `TestEdgeCase_PermanentErrorAckedImmediately`**

Setup:
1. Get a Valkey client via `newValkeyClient(t)`
2. Generate a random UUID that does NOT correspond to any image in the DB
3. XADD a message to `image:result` stream: `{image_id: <random-uuid>, status: "processed"}`
4. Wait up to 5s for the pending count on the `api-workers` group to return to 0

Assert:
- `xPendingCount(t, client, "image:result", "api-workers")` == 0 (message was ACKed, not stuck in PEL)
- No new messages in PEL

Note: This test requires the API consumer to be running (it is, as `TestMain` starts the API server). The consumer must classify `ErrImageNotFound` (returned when the image_id is not in the DB) as `HandlerResultPermanent` and ACK.

**EC-6: `TestEdgeCase_DeletedRouteResultAcked`**

Setup:
1. Create a route (1 waypoint), save the presigned upload URL and waypoint's image_id
2. Delete the route: `DELETE /routes/{id}`
3. Upload to the saved presigned URL (gateway processes and publishes result to stream)
4. Wait up to 10s for `xPendingCount` to return to 0

Assert:
- Consumer ACKed the orphaned result (route deleted, so `ErrRouteNotFound` or `ErrWaypointNotFound` -> `HandlerResultPermanent`)
- No new messages stuck in PEL

**EC-7: `TestEdgeCase_ConcurrentReplacementBlocked`**

Setup:
1. Create 1-waypoint route, upload valid image, wait for `route_status=ready`, publish route
2. Save the waypoint_id

Replace attempt 1:
3. Call `POST /routes/{id}/waypoints/{wid}/replace-image/prepare` -> 200 (save the replacement image_id)

Replace attempt 2 (before first completes):
4. Call `POST /routes/{id}/waypoints/{wid}/replace-image/prepare` again

Assert:
- Second call returns 409 Conflict
- First replacement's image_id is still the pending replacement (waypoint state unchanged)

Cleanup: delete route.

**Acceptance Criteria**

- EC-5 and EC-6 tests require the API consumer to process the injected/uploaded message; they poll with a 5-10s timeout
- EC-7 is a simple synchronous API call test
- Tests FAIL until Task 9 (permanent error classification) and Task 11 (replacement blocking) are implemented
- Tests are sequential (no `t.Parallel()` for EC-5 and EC-6 since they depend on global stream state)
- `golangci-lint run --build-tags integration` passes

**Dependencies**: Task 9 (EC-5, EC-6), Task 11 (EC-7)

---

### Task 16: Write Edge Case Integration Tests (TDD — Phase 3, Destructive, Sequential, Last)

**Story Points**: 3

**Description**

Write Phase 3 destructive tests. These tests kill processes, destroy consumer groups, and disrupt infrastructure. They MUST run last (or be isolated). Use `t.Cleanup` to restore state.

**Files Affected**

- `tests/integration/edge_cases_phase3_test.go` — new file

**Tests to Implement**

**EC-9: `TestEdgeCase_ConsumerGroupDestroy_ConsumerRecovers`**

Setup:
1. Verify consumer works: create 1-waypoint route, upload valid image, wait for `route_status=ready`

Destroy consumer group:
2. Get Valkey client, run `XGROUP DESTROY image:result api-workers`

Create new route (consumer must recover):
3. Create another 1-waypoint route, upload valid image
4. Wait up to 60s for `route_status=ready` (consumer must auto-recreate group and process result)

Assert:
- Route eventually reaches `ready` status
- No manual intervention required

Restore state (handled automatically by consumer's NOGROUP recovery in Task 5).

**EC-10: `TestEdgeCase_GatewayCrashMidProcessing_RetryUpload`**

This test requires process control (SIGKILL on the gateway process). In the integration test framework, `gatewayProcess` is a package-level variable (`*exec.Cmd`) set by `setupLocal`. The test can SIGKILL the gateway process group and restart it.

Setup:
1. Create 2-waypoint route
2. Upload waypoint 0's image (wait for `stage=done`)
3. Upload waypoint 1's image
4. Immediately SIGKILL the gateway process group: `syscall.Kill(-gatewayProcess.Process.Pid, syscall.SIGKILL)`
5. Wait 1s for gateway to die

Assert intermediate state:
- Route is still `pending`
- Waypoint 1's image is stuck (no terminal Valkey state within 5s)

Restart gateway:
6. Start a new gateway process (same setup as `setupLocal` does) and wait for `/health/ready`

Retry upload:
7. Call `POST /routes/{id}/waypoints/{wid1}/retry-upload` -> 200 with new image_id and upload_url
8. Upload a valid image to the new URL
9. Wait up to 60s for `route_status=ready`

Assert final state:
- Route reaches `ready`

Cleanup: delete route, ensure gateway is running for subsequent tests (this test must be the LAST one or run in isolation).

**Design Note on Test Ordering**: Go's test framework runs tests in source order within a file. Phase 3 tests in `edge_cases_phase3_test.go` are isolated from Phase 1 and Phase 2 files. EC-10 must be the last test that touches the gateway process. Use a package-level `sync.Once` or a `TestMain`-style guard if needed.

**Acceptance Criteria**

- EC-9 verifies NOGROUP auto-recovery within 60s
- EC-10 verifies the full retry-upload flow after a gateway crash
- Tests FAIL until Tasks 5 (NOGROUP recovery), 8 (supervisor), and 10 (retry-upload) are implemented
- Tests restore service health after running (gateway is restarted, group is recreated)
- `golangci-lint run --build-tags integration` passes

**Dependencies**: Task 5 (NOGROUP recovery), Task 8 (supervisor), Task 10 (retry-upload endpoint)

---

## Quality Gates (Per Repo)

After every task, run:

### `follow-pkg`
```bash
cd /home/yoseforb/pkg/follow/follow-pkg
gofumpt -w . && golines -w --max-len=80 .
go vet ./... && ./custom-gcl run -c .golangci-custom.yml ./... --fix
go test -race -cover ./...
go mod tidy
```

### `follow-api`
```bash
cd /home/yoseforb/pkg/follow/follow-api
gofumpt -w . && golines -w --max-len=80 .
go vet ./... && ./custom-gcl run -c .golangci-custom.yml ./...
go test -race -cover ./...
go mod tidy
go run ./cmd/server -runtime-timeout 10s
```

### `follow-image-gateway`
```bash
cd /home/yoseforb/pkg/follow/follow-image-gateway
gofumpt -w . && golines -w --max-len=80 .
go vet ./... && ./custom-gcl run -c .golangci-custom.yml ./... --fix
go test -race -cover ./...
go mod tidy
go run ./cmd/server -runtime-timeout 10s
```

### `tests/integration`
```bash
cd /home/yoseforb/pkg/follow/tests/integration
gofumpt -w . && golines -w --max-len=80 .
golangci-lint run --build-tags integration -c .golangci-custom.yml ./...
go mod tidy
```

---

## Implementation Order

The dependency graph dictates this sequence:

```
Task 1 (contract doc)
    └── Task 2 (DLQ constants)
            └── Task 4 (handler redesign)
                    ├── Task 5 (Consumer restartable)
                    │       └── Task 6 (Reclaimer restartable)
                    │               └── Task 8 (supervisor loop)
                    └── Task 9 (handleMessage classification)

Task 3 (ClearUpload)
    └── Task 10 (retry-upload endpoint) ──── (also needs Task 9)

Task 7 (Producer retry) — independent
Task 11 (block double replace) — independent
Task 12 (SSE PostgreSQL check) — independent
Task 13 (gateway updateFinalProgress retry) — independent

Tasks 14-16 (integration tests) — depend on above implementations,
           written first as failing tests (TDD), implemented last
```

**Recommended Implementation Sprint Order**:

1. Tasks 1-3 (contracts + ClearUpload) — foundation, 1 day
2. Tasks 7, 11, 12, 13 — independent fixes, can be parallelized, 1 day
3. Task 4 — handler redesign, requires careful testing, 1 day
4. Tasks 5, 6, 8, 9 — consumer resilience + classification, 1.5 days
5. Task 10 — retry-upload endpoint (full stack), 1.5 days
6. Tasks 14-16 — integration tests, 1 day

**Total**: ~7 development days, ~27 story points

---

## Commit Message Guide

Follow the project convention: `type(scope): description` in imperative mood.

| Task | Suggested Commit |
|------|-----------------|
| 1 | `docs(contracts): document DLQ stream key and fields` |
| 2 | `feat(follow-pkg): add StreamImageResultDLQ and DLQ field constants` |
| 3 | `feat(follow-pkg): add ClearUpload to UploadGuard` |
| 4 | `refactor(follow-pkg): redesign MessageHandler to return HandlerResult with DLQ support` |
| 5 | `improve(follow-pkg): make Consumer restartable and add NOGROUP auto-recovery` |
| 6 | `improve(follow-pkg): make Reclaimer restartable` |
| 7 | `feat(follow-pkg): add exponential backoff retry to Producer` |
| 8 | `feat(follow-api): add consumer supervisor loop with backoff restart` |
| 9 | `feat(follow-api): classify permanent vs transient errors in image result consumer` |
| 10 | `feat(follow-api): add retry-upload endpoint for failed waypoint images` |
| 11 | `fix(follow-api): return 409 when waypoint replacement already in progress` |
| 12 | `fix(follow-api): check route status in PostgreSQL at SSE startup` |
| 13 | `fix(follow-image-gateway): add retry to updateFinalProgress` |
| 14 | `test(integration): add Phase 1 edge case tests (parallel-safe)` |
| 15 | `test(integration): add Phase 2 edge case tests (PEL, sequential)` |
| 16 | `test(integration): add Phase 3 edge case tests (destructive)` |

---

## Risk Factors

**Risk 1: `HandlerResult` type change breaks existing consumers**
All callers of `MessageHandler` must update their handler functions. In `follow-api`, only `ImageResultConsumer.handleMessage` is a handler. After Task 4, this is the only file to update. Risk is low but requires careful coordination between Tasks 4 and 9.

**Risk 2: `processAndAck` DLQ delivery count query adds latency**
Querying `XPENDING` to check delivery count on every transient failure adds one round trip per failed message. This is acceptable for the recovery path (not the happy path). If it becomes an issue, delivery count can be embedded in the message at claim time.

**Risk 3: EC-10 gateway process restart is fragile**
Killing and restarting the gateway process in a test is inherently racy. The test must include generous timeouts and verify the gateway is healthy before proceeding. If the test environment does not support process control (e.g., Docker mode), EC-10 may need to be skipped via a build tag or environment variable.

**Risk 4: `GetRouteStatusQuery` in SSE startup adds one DB round trip per SSE connection**
This is acceptable — SSE connections are long-lived and the query is cheap. The early-exit optimization far outweighs the cost.

**Risk 5: Task 10 new image ID changes the Valkey progress hash key**
The `retry-upload` endpoint creates a new image record with a new UUID. The SSE poller (in `resolveImageIDs`) queries PostgreSQL for the current image IDs of the route's waypoints. After `retry-upload`, the waypoint's `image_id` is updated to the new UUID, so `resolveImageIDs` will return the new UUID. If the client reconnects SSE after calling retry-upload, it will poll the correct new image. If the client's SSE connection was open during the retry, the old UUID will be polled (which may show `stage=failed`) and the new UUID will not be visible until reconnect. This is acceptable behavior — the client is expected to reconnect SSE after calling retry-upload.
