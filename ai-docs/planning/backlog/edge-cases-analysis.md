

# Exhaustive Edge Case and Failure Scenario Analysis

## Follow Platform Image Processing Pipeline

**Scope**: Client (Flutter) -> SSE -> follow-api -> Valkey -> follow-image-gateway -> MinIO

**Analysis based on**: Direct reading of all source files listed at the end of this document.

---

## CATEGORY 1: SERVICE CRASHES AND RESTARTS

---

### 1.1 Gateway Crash Mid-Pipeline (Job Dropped During Shutdown)

**Scenario**: Gateway process dies while a job is between pipeline stages.

**What happens step by step**:
1. Client uploads image. Gateway returns 202 Accepted.
2. Job enters pipeline, passes validate stage, is mid-decode.
3. Gateway process crashes (OOM, SIGKILL, host failure).
4. In `worker()` (worker.go lines 300-322), the `select { case outputCh <- job; case <-p.ctx.Done() }` path fires on context cancellation. `releaseJobMemory(job)` is called, `OnJobDropped` observer fires.
5. The `mergeChannels` goroutines (worker.go lines 109-163) similarly drop jobs via `p.ctx.Done()` during shutdown.
6. `resultPublisher()` never processes this job. `safePublisherCall()` is never called.
7. `ResultPublisher.PublishResult()` is never invoked -- no message published to `image:result` stream and no final status written to `image:status:{id}` hash.
8. The hash remains at whatever intermediate stage the gateway last wrote via the per-stage `ProgressTracker` (e.g., "validating").
9. The `image:upload:{id}` guard key persists (TTL 1 hour).
10. SSE poller reads the stale intermediate stage and keeps sending "processing" events indefinitely.
11. After SSE MaxDuration (5 min), SSE sends `complete(all_done=false)`.
12. Route stays PENDING in PostgreSQL forever because no `image:result` message was published and the API consumer never runs `ConfirmWaypointImageUseCase`.

**Expected behavior**: The system should detect stuck images (no progress update for N minutes) and either (a) mark them as failed, or (b) allow the upload guard to expire so the client can retry. A watchdog timer or a reconciliation process should exist.

**Current risk**: **UNHANDLED**. There is no timeout or watchdog. The `image:status:{id}` hash has a 1-hour TTL, so it will eventually expire. The `image:upload:{id}` guard also expires after 1 hour. After both expire, the SSE poller would get `ErrImageStatusNotFound` (treated as transient retry). The route remains permanently stuck in PENDING unless the user manually replaces the image -- but `replace-image/prepare` requires READY/PUBLISHED status, which is impossible for a PENDING route. Dead end.

**Testability**: Partially testable. Upload an image, kill the gateway process before pipeline completes (using `docker stop` or `kill -9`), then verify: (a) `image:status:{id}` hash shows a non-terminal stage, (b) SSE eventually sends `complete(all_done=false)`, (c) route remains PENDING. The precise timing of the kill is difficult to control, but a slow-processing image (large file) widens the window.

**Priority**: **Critical**

---

### 1.2 Gateway Crash After MinIO Upload But Before Stream Publish

**Scenario**: Upload stage writes to MinIO successfully, but gateway crashes before `ResultPublisher.publishToStream()` writes the result to the `image:result` stream.

**What happens step by step**:
1. Pipeline upload stage calls MinIO PutObject -- succeeds. Image bytes are in object storage.
2. Job reaches `resultPublisher()` (worker.go line 378). `safePublisherCall(job)` calls `publisher.PublishResult(p.ctx, job)`.
3. Inside `ResultPublisher.publishResult()` (result_publisher.go line 72): `updateFinalProgress()` is called FIRST (line 83). This writes `stage=done, progress=100` to the `image:status:{id}` hash. Succeeds.
4. Gateway crashes BEFORE `publishToStream()` (line 86) executes.
5. Image is in MinIO. Status hash says "done". But no stream message exists in `image:result`.
6. API consumer (`ImageResultConsumer`) blocks on `XREADGROUP ">"` -- no message arrives, nothing to process.
7. `MarkImageProcessedUseCase` is never called. Image stays "pending" in PostgreSQL.
8. `ConfirmWaypointImageUseCase` is never called. Waypoint stays "pending". Route stays PENDING.
9. SSE poller reads `stage=done` from Valkey hash and sends "ready" events to the client for that image.
10. **Split brain**: Client sees "ready" via SSE. PostgreSQL says "pending". Route is PENDING even though SSE says all images are done.

**Expected behavior**: The system should have a reconciliation mechanism. Either (a) the API should periodically cross-check Valkey status hashes against PostgreSQL state, or (b) the gateway should retry stream publishing, or (c) the `image:result` publish should happen before the status hash update (or atomically with it).

**Current risk**: **UNHANDLED**. This is a split-brain scenario. The ordering in `publishResult()` (status hash first, stream second) is particularly dangerous because it creates a window where Valkey says "done" but the stream message is missing. There is no reconciliation process to detect this.

**Testability**: Difficult to test precisely (requires crash between two specific operations). Could be approximated by: (a) directly writing `stage=done` to a status hash without publishing to the stream, then verifying SSE says "ready" but the route stays PENDING, or (b) wrapping the publisher with a failing mock that succeeds on `updateFinalProgress` but fails on `publishToStream`.

**Priority**: **Critical**

---

### 1.3 API Crashes While Consuming from image:result Stream (Mid-Processing)

**Scenario**: API consumer reads a message, begins processing, and crashes between the Image module call and the Route module call.

**What happens step by step**:
1. `ImageResultConsumer.handleMessage()` (image_result_consumer.go line 93) parses the message.
2. `ImageResultProcessor.Process()` calls `imageModule.ExecuteCommand()` (image_result_processor.go line 90) -- `MarkImageProcessedUseCase` marks image as "uploaded" in PostgreSQL. **Succeeds and commits**.
3. API process crashes BEFORE `routeModule.ExecuteCommand()` (line 114) runs `ConfirmWaypointImageCommand`.
4. The message was NOT acknowledged (`processAndAck` in message_processor.go line 23 called the handler, but handler never returned nil).
5. Message stays in PEL (Pending Entries List).
6. On API restart, Reclaimer (reclaimer.go) eventually runs XAUTOCLAIM after `IdleTimeout=5min`.
7. Message is redelivered. `Process()` runs again.
8. `MarkImageProcessedUseCase` is called again: image is already "uploaded" -- idempotency check at lines 81-87 of mark_image_processed_usecase.go returns early. No error.
9. `ConfirmWaypointImageUseCase` runs: waypoint is confirmed, route transitions if all waypoints are done.

**Expected behavior**: Retry should succeed due to idempotent design.

**Current risk**: **HANDLED with delay**. The PEL + Reclaimer mechanism works correctly. The idempotency of both use cases ensures safe retry. The delay is `IdleTimeout=5min` (reclaimer.go line 27 default) before the message is reclaimed. During this window, the route is stuck in PENDING.

**Testability**: Already partially covered by `TestValkeyRecovery_PendingMessageRetained`. Could add a test that verifies the full retry path: first processing succeeds at image level but "fails" at route level, message stays in PEL, reclaimer picks it up, second attempt completes both levels.

**Priority**: **Medium** -- Handled but with up to 5-minute delay.

---

### 1.4 API Crashes Mid-SSE Stream

**Scenario**: API is streaming SSE events and the process crashes.

**What happens step by step**:
1. Client has an open SSE connection. SSE streamer is polling Valkey every 500ms.
2. API process crashes.
3. TCP connection drops. Client receives EOF or connection reset.
4. Client reconnects (new HTTP request to SSE endpoint).
5. New `StreamRouteStatus()` call resolves image IDs from PostgreSQL.
6. New `SSEStreamer.Stream()` starts polling Valkey for current state.
7. Images that reached terminal state during the crash are immediately detected.

**Expected behavior**: Client should reconnect and see current state.

**Current risk**: **HANDLED**. The polling-based SSE design is inherently resilient to server crashes. Reconnection reads current Valkey state, not historical events. No `Last-Event-ID` is needed. Images that completed during the outage are immediately reported.

**Testability**: Connect to SSE, kill the API, restart it, reconnect. Verify images completed during outage appear as "ready" immediately.

**Priority**: **Low**

---

### 1.5 API Restart -- Consumer Resumes from Last Position

**Scenario**: API is cleanly stopped and restarted. Does the consumer resume correctly?

**What happens step by step**:
1. Consumer calls `Stop()` (consumer.go line 339) which cancels its context.
2. `Start()` loop exits via `ctx.Err() != nil` (line 309).
3. Messages that arrived during downtime sit in the stream.
4. On restart, `NewImageResultConsumer` creates a new `Consumer` instance (new `sync.Once`).
5. `Start()` calls `createGroup()` (line 279). `XGROUP CREATE ... "0"` returns "already exists" error, which is ignored (line 135).
6. Consumer reads `">"` which delivers undelivered messages (messages added after the group's last-delivered-ID).
7. Messages in PEL (delivered before crash but not ACKed) are handled by the Reclaimer.

**Expected behavior**: All messages should eventually be processed.

**Current risk**: **HANDLED**. Redis Streams consumer groups correctly track delivery state. The `"0"` in `createGroup` only applies to initial creation. Subsequent calls fail with "already exists" and the existing group state is preserved.

**Testability**: Can test by publishing messages while no consumer runs, starting the consumer, verifying all messages are processed.

**Priority**: **Low**

---

### 1.6 Valkey Crashes and Restarts (Data Loss)

**Scenario**: Valkey process crashes or restarts. All in-memory data is lost (no persistence configured for local dev).

**What happens step by step**:
1. Valkey crashes. All keys destroyed: `image:status:{id}` hashes, `image:upload:{id}` guards, `image:result` stream, and `api-workers` consumer group.
2. Valkey restarts with empty state.
3. **Gateway side**: New uploads succeed (no guard exists, SET NX returns true even for duplicates). Progress writes create new hashes. Stream publishes create new stream entries. But there is no consumer group.
4. **API consumer side**: `XREADGROUP` calls fail with NOGROUP error because the `api-workers` group was destroyed.
5. `readAndProcessMessages()` (consumer.go line 193) receives an error (not `ErrNoMessages`, not context cancellation).
6. `readWithRetry()` retries 3 times with exponential backoff (100ms, 200ms, 400ms), then returns fatal error.
7. `Start()` loop (line 309-317) receives the error, logs "xreadgroup failed after retries, terminating consumer", and returns.
8. Consumer goroutine dies permanently. The `Consumer` is single-use (`sync.Once`).
9. **SSE side**: `PollImageStatus()` calls `HGetAll()` on non-existent keys, returns empty map, which maps to `ErrImageStatusNotFound`. The poller treats this as transient and `continue`s (sse_streamer.go line 180). SSE keeps polling but never sees terminal states for pre-existing images.
10. After MaxDuration (5 min), SSE sends `complete(all_done=false)` for pre-Valkey-crash images.
11. New images uploaded AFTER Valkey restart: gateway creates new status hashes and publishes to stream, but API consumer is dead and never processes them.

**Expected behavior**: API should detect the consumer group destruction and recreate it. Consumer should have restart logic.

**Current risk**: **UNHANDLED**. Valkey restart permanently breaks the API consumer. `createGroup()` is only called once at `Start()` (line 279). There is no reconnection or group recreation after start. The code comment says "Higher-level services MUST implement restart logic with backoff" (consumer.go line 49) but no such logic exists in follow-api.

**Testability**: Stop Valkey, restart it, verify API consumer behavior. Publish a message to the new empty stream. Verify the consumer is dead and the message is never processed.

**Priority**: **High** -- Valkey restart breaks pipeline permanently until API restart.

---

### 1.7 MinIO Unavailable During Gateway Upload Stage

**Scenario**: MinIO is unreachable when the gateway pipeline's upload stage tries to write the processed image.

**What happens step by step**:
1. Pipeline reaches upload stage. `UploadStage.Process()` calls MinIO PutObject, which fails.
2. Stage returns an error. Worker (worker.go line 265) sets `job.Error = err`, `job.ErrorCode = errorCodeFor(err)`.
3. Job is routed to error channel (line 302).
4. Error channel merges into result channel. `resultPublisher()` calls `safePublisherCall(job)`.
5. `ResultPublisher.publishResult()` writes `stage=failed` to status hash (via `updateFinalProgress`), then publishes failure message to `image:result` stream (via `publishToStream`).
6. API consumer receives failure message. `MarkImageProcessedUseCase` marks image as failed. `ConfirmWaypointImageUseCase.handleImageFailure()` clears pending replacement if applicable.
7. SSE poller reads `stage=failed` and sends "failed" event to client.

**Expected behavior**: Clean failure propagation.

**Current risk**: **HANDLED**. The pipeline's error routing correctly handles stage failures. The `errorCodeFor()` function maps the error to a code. The failure propagates through Valkey to the API and SSE.

**Testability**: Can test by making MinIO unreachable (stop container, block port) before uploading. Verify the failure propagates to SSE.

**Priority**: **Low**

---

### 1.8 PostgreSQL Unavailable When API Processes Result

**Scenario**: API consumer receives a result message but PostgreSQL is down.

**What happens step by step**:
1. `ImageResultProcessor.Process()` calls `imageModule.ExecuteCommand()` (image_result_processor.go line 90).
2. `MarkImageProcessedUseCase.Execute()` calls `imageRepo.FindByID()` which fails (PostgreSQL unreachable).
3. Error: `ErrImageModuleFailed` wrapping the DB error (line 93).
4. `handleMessage()` returns the error (image_result_consumer.go line 140-144).
5. `processAndAck()` (message_processor.go) does NOT call XACK. Message stays in PEL.
6. Consumer continues reading `">"` for new messages. The failed message sits in PEL.
7. Reclaimer runs after `IdleTimeout=5min` and retries via XAUTOCLAIM.
8. If PostgreSQL is back, retry succeeds. If still down, message goes back to PEL.

**Expected behavior**: PEL-based retry should handle transient PostgreSQL outages.

**Current risk**: **HANDLED with delay**. The PEL mechanism correctly preserves the message. Idempotent use cases ensure safe retry. The 5-minute reclaim delay is the main cost. If PostgreSQL is down for an extended period, messages accumulate in PEL but are not lost.

**Testability**: Stop PostgreSQL, upload images (they will process successfully in the gateway), verify PEL grows, restart PostgreSQL, verify Reclaimer eventually processes the backlog.

**Priority**: **Medium** -- Handled but with delay.

---

## CATEGORY 2: TIMING AND RACE CONDITIONS

---

### 2.1 Client Connects to SSE AFTER All Images Already Processed (Late Subscriber)

**Scenario**: Client uploads images, delays connecting to SSE. By the time SSE connects, all images are done.

**What happens step by step**:
1. All images processed. All `image:status:{id}` hashes show `stage=done`.
2. Client connects to SSE. `StreamRouteStatus()` (routes_service.go line 1554) resolves image IDs.
3. `SSEStreamer.Stream()` starts polling.
4. First tick: `pollAllImages()` calls `PollImageStatus()` for each image. All return `stage=done`.
5. `handleImageStatus()` (sse_streamer.go line 199): each image matches `valkey.StageDone` (line 208). Sends "ready" event with image_id. Marks as terminal.
6. After processing all images: `len(terminal) == len(imageIDs)` (line 132). Calls `sendCompleteAndClose(sender, true)`.
7. Client receives N "ready" events followed by `complete(all_done=true)`.

**Expected behavior**: Immediate delivery of final state.

**Current risk**: **HANDLED**. The polling design reads current state, not historical events. Late subscribers immediately see terminal states.

**Testability**: Already implicitly tested in the behavioral flow test (Step 7 SSE connects after Step 6 uploads).

**Priority**: **Low**

---

### 2.2 Client Connects to SSE BEFORE API Writes Initial "queued" Status

**Scenario**: Race between `create-waypoints` writing `stage=queued` to Valkey and the client connecting to SSE.

**What happens step by step**:
1. Client calls `create-waypoints`. API begins creating route, waypoints, and writing `stage=queued` to Valkey per image. The `stage=queued` write is fire-and-forget (prepare_image_upload.go line 331: "Log error but don't fail").
2. API returns 200 with presigned URLs.
3. Client IMMEDIATELY connects to SSE.
4. SSE calls `resolveImageIDs()` -- queries PostgreSQL for image IDs. These exist (committed in step 1).
5. Poller calls `PollImageStatus()` for each image_id.
6. If hash doesn't exist yet: `HGetAll` returns empty map. `PollImageStatus()` returns `ErrImageStatusNotFound`.
7. `pollAllImages()` (sse_streamer.go line 180): `if pollErr != nil || imgStatus == nil { continue }`. Skips this image for this tick.
8. Next tick (500ms): hash likely exists now. Sends "processing" event.

**Expected behavior**: Brief delay (up to one poll interval) before first event, then normal operation.

**Current risk**: **HANDLED**. The `continue` on error in `pollAllImages` is the correct retry-on-next-tick behavior. The write in `prepare_image_upload.go` is fast (single Valkey HSetWithExpire), so the window is tiny.

**Testability**: Connect to SSE immediately after `create-waypoints` returns, before uploading. Verify that events eventually arrive (within a few poll intervals).

**Priority**: **Low**

---

### 2.3 Two Images Finish Simultaneously -- Route Transition Race (Single Consumer)

**Scenario**: Route has 2 remaining pending waypoints. Both images finish processing at the same time.

**What happens step by step**:
1. Gateway publishes two messages to `image:result` stream in rapid succession.
2. API consumer reads both in a single `XREADGROUP` batch (count=10 default).
3. `readAndProcessMessages()` (consumer.go line 226-240) processes them sequentially in a for loop.
4. First message: `ConfirmWaypointImageUseCase` confirms waypoint A, calls `CountPendingByRouteID()` which returns 1 (waypoint B still pending). No route transition.
5. Second message: `ConfirmWaypointImageUseCase` confirms waypoint B, calls `CountPendingByRouteID()` which returns 0. Calls `transitionRouteToReady()`. Route transitions PENDING -> READY.

**Expected behavior**: Route transitions exactly once.

**Current risk**: **HANDLED** (single consumer). Sequential processing within the for loop ensures correct counting. The PostgreSQL write from step 4 commits before step 5 reads, so the count is accurate.

**Note**: If multiple consumer instances were introduced (scaling `api-workers` group), this would become a race condition. Both could read `pending=1`, both confirm, both see `pending=0`, both try to transition. The idempotency check in `transitionRouteToReady()` (line 281: `if route.Status().IsReady() || route.Status().IsPublished()`) would prevent double-write, but this depends on read-your-writes consistency in PostgreSQL. With statement-level isolation, the second reader might see stale data.

**Testability**: Upload multiple images for the same route simultaneously. Verify route transitions exactly once to READY.

**Priority**: **Low** (single consumer) / **High** (if multi-consumer scaling is introduced)

---

### 2.4 Image Replacement: New Image Not Tracked in SSE Stream

**Scenario**: User initiates image replacement. The SSE stream does not include the pending replacement image.

**What happens step by step**:
1. Route is PUBLISHED. User calls `replace-image/prepare` for waypoint 1. API creates a new image record (ID=B) and sets waypoint's `pending_replacement_image_id = B`.
2. User opens SSE stream for the route.
3. `resolveImageIDs()` (routes_service.go line 1597-1627) executes `GetImageIDsForRouteQuery` which retrieves image IDs from the route's waypoints. The waypoint's primary `image_id` still points to the OLD image (ID=A) -- it won't be swapped until the consumer runs `SwapWaypointImageUseCase`.
4. SSE polls status for image A (which is already "done"). Immediately sends "ready", then "complete".
5. Image B is being processed by the gateway, but SSE never polls it.
6. Client has no real-time visibility into replacement image processing.

**Expected behavior**: The SSE stream should either include pending replacement image IDs or provide a separate mechanism for tracking replacement progress.

**Current risk**: **UNHANDLED**. `GetImageIDsForRouteQuery` only returns the primary `image_id` per waypoint, not `pending_replacement_image_id`. The client must use the single-image polling endpoint (`GET /routes/{id}/images/{image_id}/status`) or poll the route GET endpoint to detect when the swap completes. No real-time SSE feedback for replacements.

**Testability**: Initiate replacement, connect to SSE, verify the replacement image_id is NOT polled. Verify SSE completes immediately (because original images are already "done").

**Priority**: **Medium** -- UX gap but not data loss.

---

### 2.5 Gateway Processes Image Faster Than SSE Poll Interval (500ms)

**Scenario**: Small image processes in under 500ms. Client misses all intermediate stages.

**What happens step by step**:
1. Image uploaded. `image:status:{id}` starts at `stage=queued`.
2. Gateway processes in 200ms: hash transitions queued -> validating -> decoding -> processing -> encoding -> uploading -> done.
3. SSE first tick (500ms later): reads `stage=done`.
4. Client receives "ready" event directly. Never sees "processing".

**Expected behavior**: Client should handle receiving "ready" without prior "processing". Intermediate stages are informational, not guaranteed.

**Current risk**: **HANDLED BY DESIGN**. The SSE contract delivers current state, not state history. The Flutter client should treat "processing" as optional. The critical events are "ready", "failed", and "complete".

**Testability**: Upload a small image. Verify SSE may send "ready" without prior "processing".

**Priority**: **Low**

---

### 2.6 Route Deleted While Images Still Processing in Gateway

**Scenario**: User deletes a route while the gateway is still processing its images.

**What happens step by step**:
1. User creates route with 3 waypoints, uploads images to gateway.
2. Gateway starts processing images.
3. User calls `DELETE /routes/{route_id}`.
4. `DeleteRouteUseCase.Execute()` (delete_route_usecase.go): calls `deleteWaypoints()` which removes images from Image domain (calls `imageService.RemoveImages()`), then deletes waypoints from Route domain, then deletes the route.
5. Image records deleted from PostgreSQL. Route and waypoints deleted.
6. Gateway finishes processing, publishes result to `image:result` stream.
7. API consumer receives the result. `ImageResultProcessor.Process()` calls `imageModule.ExecuteCommand()`.
8. `MarkImageProcessedUseCase.Execute()` calls `imageRepo.FindByID()` -- image was deleted.
9. Returns `domain.ErrImageNotFound` (mark_image_processed_usecase.go line 65-67).
10. Error propagates: `ErrImageModuleFailed`.
11. `handleMessage()` returns error (image_result_consumer.go line 140-144).
12. Message NOT ACKed. Stays in PEL.
13. Reclaimer retries. Same error. **Infinite loop**.

**Expected behavior**: Consumer should detect "image not found" as a permanent condition and ACK the message (skip it).

**Current risk**: **UNHANDLED**. This is a poison pill. The message will be retried forever. The PEL grows with each retry cycle. The Reclaimer wastes resources processing it every scan interval.

**Testability**: Create a route, upload images, immediately delete the route, wait for gateway to publish results. Verify the consumer handles the orphaned messages (currently it won't -- they'll sit in PEL).

**Priority**: **High**

---

### 2.7 Image Replacement Initiated While Original Is Still Processing

**Scenario**: Route is PENDING (original images still being processed by gateway). User tries to replace an image.

**What happens step by step**:
1. Route is PENDING.
2. User calls `replace-image/prepare` for a waypoint.
3. `PrepareReplaceWaypointImageUseCase` validates route status. Route must be READY or PUBLISHED.
4. Route is PENDING -- request is rejected with appropriate error.

**Expected behavior**: Replacement rejected until route is READY.

**Current risk**: **HANDLED**. Route status validation prevents replacement on PENDING routes.

**Testability**: Call `replace-image/prepare` on a PENDING route. Verify rejection.

**Priority**: **Low**

---

### 2.8 Multiple Rapid Image Replacements on Same Waypoint

**Scenario**: User calls `replace-image/prepare` twice in quick succession for the same waypoint. The second call overwrites the first pending replacement.

**What happens step by step**:
1. Route is PUBLISHED. User calls `replace-image/prepare` for waypoint 1. API creates image B, sets `pending_replacement_image_id = B`, `pending_marker_x`, `pending_marker_y` on the waypoint.
2. User calls `replace-image/prepare` AGAIN for waypoint 1 before image B finishes. API creates image C, overwrites `pending_replacement_image_id = C`, `pending_marker_x`, `pending_marker_y`.
3. User uploads image B to gateway. Gateway processes B, publishes to `image:result`.
4. API consumer receives result for image B.
5. `ImageResultProcessor.Process()`: `MarkImageProcessedUseCase` marks image B as "uploaded" in PostgreSQL. Returns status "uploaded".
6. `ConfirmWaypointImageUseCase.Execute()`: tries `FindByImageID(B)` -- not found (B is not the primary image_id on any waypoint). Tries `FindByPendingReplacementImageID(B)` -- not found (pending was overwritten to C).
7. Returns error: `"no waypoint references image B as primary or pending replacement: waypoint not found"` (confirm_waypoint_image_usecase.go line 228-231).
8. Error propagates: `ErrRouteModuleFailed`.
9. `handleMessage()` returns error. Message NOT ACKed. Stays in PEL.
10. **Poison pill**: message retried forever.

**Expected behavior**: The consumer should handle "waypoint not found for this image" as a permanent condition and ACK the message.

**Current risk**: **UNHANDLED**. Same poison pill pattern as 2.6. The `ConfirmWaypointImageUseCase` does not distinguish between "temporary not found" and "permanently orphaned image". The error `domain.ErrWaypointNotFound` causes the consumer to retry forever.

**Testability**: Prepare two replacements rapidly for the same waypoint. Upload only the first image. Verify the consumer handles the orphaned result. (Currently fails -- poison pill.)

**Priority**: **High**

---

### 2.9 Concurrent Uploads to Same Presigned URL (Duplicate Guard)

**Scenario**: Two clients (or retrying client) both PUT to the same presigned upload URL.

**What happens step by step**:
1. Client A sends PUT. Gateway validates JWT (`JWTAuth`), then calls `uploadGuard.ClaimUpload(imageID)` (upload_service.go line 249).
2. `ClaimUpload` calls `SetNX(key, "claimed", TTL=1h)` (upload_guard.go line 55). Returns true. Upload proceeds.
3. Client B sends PUT. JWT validates. `ClaimUpload` calls `SetNX` -- key already exists. Returns `ErrUploadAlreadyExists`.
4. Upload service maps this to 409 Conflict (upload_service.go line 263-266).

**Expected behavior**: Only one upload processed.

**Current risk**: **HANDLED**. The SET NX guard is atomic and correct.

**Testability**: Already tested in `TestValkeyUploadGuard_PreventsDuplicateUploads`.

**Priority**: **Low**

---

### 2.10 Upload Guard Fails (Valkey Unreachable from Gateway) -- Graceful Degradation

**Scenario**: Gateway cannot reach Valkey for the upload guard check.

**What happens step by step**:
1. Client uploads image. JWT validates.
2. `uploadGuard.ClaimUpload()` fails with a connection error (not `ErrUploadAlreadyExists`).
3. Upload service (upload_service.go line 268-276): "Other Valkey errors: graceful degradation". Logs the error and continues processing.
4. Image is processed without deduplication protection.

**Expected behavior**: Upload should proceed (graceful degradation) with a warning log.

**Current risk**: **HANDLED**. The code explicitly implements graceful degradation for non-duplicate Valkey errors. Additionally, the upload guard is optional (can be nil, lines 242-247).

**Testability**: Block gateway's Valkey connection. Upload an image. Verify it processes successfully with a warning log.

**Priority**: **Low**

---

## CATEGORY 3: NETWORK AND CONNECTIVITY

---

### 3.1 Valkey Connection Drops -- API Consumer Dies Permanently

**Scenario**: Network partition between API and Valkey while the consumer is running.

**What happens step by step**:
1. Consumer is in `readAndProcessMessages()`. Calls `client.XReadGroup()` (consumer.go line 196).
2. Network drops. XReadGroup returns a connection error.
3. Error is not `ErrNoMessages` and not `context.Canceled` (line 206-222). Falls to default case, returns error.
4. `readWithRetry()` (line 151) catches error, retries with backoff: 100ms, 200ms, 400ms.
5. After `MaxRetries=3` (line 89 default), returns error: `"xreadgroup failed after retries"`.
6. `Start()` main loop (line 311-317): receives error, logs "xreadgroup failed after retries, terminating consumer", **returns the error**.
7. Consumer goroutine exits permanently.
8. The `Consumer` is single-use (`sync.Once` at line 255-258). It cannot be restarted.
9. **No restart logic exists** in the application layer.

**Expected behavior**: The consumer should reconnect when Valkey becomes available. Either the Consumer should have internal reconnection logic, or the application layer should restart it.

**Current risk**: **UNHANDLED**. Total failure time before consumer dies: ~0.7 seconds (100+200+400ms backoff). A Valkey blip lasting less than 1 second permanently kills the consumer. The documentation explicitly states "Higher-level services MUST implement restart logic with backoff" (consumer.go line 48-49), but this is not implemented in follow-api.

**Testability**: Block API -> Valkey traffic for 2 seconds. Verify consumer exits. Unblock traffic. Verify consumer does NOT restart. Publish a message to the stream. Verify it is never consumed.

**Priority**: **Critical**

---

### 3.2 Valkey Connection Drops -- Gateway Cannot Publish Result

**Scenario**: Gateway finishes processing an image but Valkey is unreachable when trying to publish.

**What happens step by step**:
1. Pipeline completes all stages. Job reaches `resultPublisher()`.
2. `safePublisherCall(job)` calls `publisher.PublishResult(p.ctx, job)` (worker.go line 437).
3. Inside `ResultPublisher.publishResult()` (result_publisher.go line 72):
   - `updateFinalProgress()`: calls `progressTracker.SetProgress()`. Fails (Valkey unreachable). Error is logged (line 123-127) but **not propagated**: "Errors here are logged but don't prevent publishing to the result stream" (line 116-117).
   - `publishToStream()`: calls `producer.PublishWithTrim()`. Fails (Valkey unreachable). Error is logged (line 150-154). Method returns without publishing.
4. `PublishResult()` returns (void return, no error to propagate).
5. `safePublisherCall()` completes without error.
6. Job is silently dropped. No result in stream. No status hash update.
7. Image is in MinIO but API never learns about it. Route stays PENDING.

**Expected behavior**: The gateway should retry publishing or queue the result for later delivery.

**Current risk**: **UNHANDLED**. The `JobPublisher` interface (job_publisher.go line 46) has a void return: `PublishResult(ctx context.Context, job *ImageJob)`. There is no mechanism to signal failure back to the pipeline. The `ResultPublisher` (result_publisher.go) logs errors but cannot retry. No persistent queue exists for failed publishes.

**Testability**: Block gateway -> Valkey traffic AFTER an image finishes processing (tricky timing). Verify the result message is missing from the stream. Verify the route stays PENDING.

**Priority**: **Critical** -- Silent data loss.

---

### 3.3 Client SSE Connection Drops and Reconnects

**Scenario**: Mobile network switch drops the SSE connection.

**What happens step by step**:
1. SSE streamer is sending events. `sender.Send()` returns error (broken pipe / connection reset).
2. `handleImageStatus()` (sse_streamer.go line 218-219): detects send error, calls `sender.Close()`, returns `true` (exited flag).
3. `pollAllImages()` returns `true`. `Stream()` returns `nil` (line 128-130).
4. Server-side cleanup complete.
5. Client reconnects. New HTTP request.
6. New `StreamRouteStatus()` resolves image IDs, starts fresh polling.
7. Current Valkey state is immediately reflected.

**Expected behavior**: Clean reconnection with current state.

**Current risk**: **HANDLED**. The code correctly detects sender errors and cleans up. The polling design makes reconnection seamless.

**Testability**: Connect to SSE, kill client connection (close socket), verify server cleans up. Reconnect, verify current state.

**Priority**: **Low**

---

### 3.4 Presigned URL Expires Before Upload Completes

**Scenario**: The Ed25519 JWT in the upload URL expires during a slow upload over a poor connection.

**What happens step by step**:
1. Client receives presigned URL with JWT (e.g., 15-minute expiry).
2. Client starts uploading over slow connection.
3. Gateway's HTTP server receives the full request body.
4. `JWTAuth()` (upload_service.go line 96-103) validates the JWT. Token is expired.
5. Gateway returns 401 Unauthorized.
6. No upload guard is claimed (guard check happens after JWT auth, line 242).
7. No pipeline processing occurs.
8. `image:status:{id}` hash still says "queued" (set by API at route creation).

**Expected behavior**: The client should detect 401 and request a new URL.

**Current risk**: **PARTIALLY HANDLED**. For image REPLACEMENT, the client can call `replace-image/prepare` again to get a new URL. For the CREATION flow, there is no endpoint to regenerate a presigned URL for an existing waypoint. The user must delete the route and start over.

**Testability**: Use a very short JWT expiry (e.g., 5 seconds). Start a slow upload. Verify 401 response.

**Priority**: **Medium** -- Creation flow has no URL regeneration.

---

### 3.5 Presigned URL Used After Route Is Deleted

**Scenario**: Route is deleted, but the client still has valid presigned upload URLs (JWTs haven't expired).

**What happens step by step**:
1. Client has presigned URLs from `create-waypoints`.
2. Route is deleted. Image records removed from PostgreSQL.
3. Client uploads to a presigned URL. JWT validates (JWT contains `image_id`, `storage_key`, not route_id -- no route existence check in JWT).
4. Upload guard claims the upload (if guard key doesn't already exist).
5. Gateway processes image, publishes to `image:result` stream.
6. API consumer receives message. `MarkImageProcessedUseCase` calls `imageRepo.FindByID()`.
7. Image not found (deleted with route). Returns `domain.ErrImageNotFound`.
8. Error propagates. Message NOT ACKed. **Poison pill**.

**Expected behavior**: Consumer should ACK messages for non-existent images.

**Current risk**: **UNHANDLED**. Same poison pill as 2.6 and 2.8.

**Testability**: Create route, get presigned URLs, delete route, upload to old URL. Verify consumer handles the result (currently fails -- poison pill).

**Priority**: **High**

---

### 3.6 Valkey Connection Drop During SSE Polling

**Scenario**: Valkey becomes unreachable while the SSE poller is polling image statuses.

**What happens step by step**:
1. SSE poller is running. Calls `PollImageStatus()` for each non-terminal image.
2. `ValkeyStatusPollerImpl.PollImageStatus()` (valkey_poller_impl.go line 35) calls `client.HGetAll()`.
3. HGetAll fails (Valkey unreachable). Returns error.
4. `PollImageStatus()` returns `(nil, error)`.
5. `pollAllImages()` (sse_streamer.go line 180): `if pollErr != nil || imgStatus == nil { continue }`. Skips this image for this tick.
6. Next tick: same error. Continues skipping.
7. All images remain non-terminal in the SSE's view. Heartbeats keep the connection alive.
8. After MaxDuration (5 min): sends `complete(all_done=false)`.

**Expected behavior**: SSE should be resilient to transient Valkey outages.

**Current risk**: **HANDLED**. The `continue` on poll error is correct. SSE gracefully degrades to heartbeat-only mode during Valkey outages. The MaxDuration timeout prevents infinite hanging.

**Testability**: Block API -> Valkey traffic during SSE polling. Verify SSE sends heartbeats but no status events. Unblock, verify status events resume.

**Priority**: **Low**

---

## CATEGORY 4: DATA CONSISTENCY

---

### 4.1 Partial Route Failure: Some Images Succeed, Others Fail

**Scenario**: Route has 3 waypoints. 2 images process successfully, 1 fails (invalid image data).

**What happens step by step**:
1. Images A and B process successfully. `ConfirmWaypointImageUseCase` confirms waypoints A and B. `CountPendingByRouteID()` returns 1 (waypoint C still pending).
2. Image C fails validation in gateway. Gateway publishes failure to `image:result`.
3. API consumer receives failure for C. `MarkImageProcessedUseCase` marks image C as "failed".
4. `ConfirmWaypointImageUseCase.Execute()` with `status=failed` (line 90): calls `handleImageFailure()`.
5. `handleImageFailure()` (line 119): calls `FindByPendingReplacementImageID(C)`. Not found (this is creation flow, not replacement). Returns `ErrWaypointNotFound`.
6. Returns `image_failed_no_pending_replacement` with `RouteID=uuid.Nil, WaypointID=uuid.Nil`.
7. **No waypoint status change**. Waypoint C remains "pending" in PostgreSQL.
8. Route stays PENDING because `CountPendingByRouteID` still returns 1.
9. SSE: polls all 3 images. A and B return "done" -> "ready" events. C returns "failed" -> "failed" event. All 3 are terminal in Valkey.
10. `len(terminal) == len(imageIDs)` -> SSE sends `complete(all_done=true)`.
11. **Client sees**: all images terminal (2 ready, 1 failed), SSE complete. But route is still PENDING in PostgreSQL.

**Expected behavior**: There should be a mechanism to either (a) retry the failed image, (b) remove the failed waypoint, or (c) transition the route to a "partially failed" state that allows remediation.

**Current risk**: **UNHANDLED**. The route is permanently stuck in PENDING. The client knows image C failed (via SSE "failed" event) but has no API endpoint to fix it. `replace-image/prepare` requires READY/PUBLISHED. There is no "retry image" endpoint for creation flow. The user must delete the entire route and start over.

**Testability**: Create a 3-waypoint route. Upload 2 valid images and 1 invalid image (e.g., random bytes). Verify: (a) SSE reports 2 "ready" and 1 "failed", (b) SSE sends `complete(all_done=true)`, (c) GET route shows `route_status=pending`, (d) there is no API path to fix the route.

**Priority**: **Critical** -- Fundamental product-level gap.

---

### 4.2 All Images Fail During Route Creation

**Scenario**: Every image in a route fails processing.

**What happens step by step**:
Same as 4.1 but for all images. All waypoints remain "pending". Route stays PENDING. SSE sends `complete(all_done=true)` with all images "failed".

**Expected behavior**: Client should know route creation failed entirely and be able to retry or delete.

**Current risk**: **UNHANDLED**. Same as 4.1. Delete-and-recreate is the only option.

**Testability**: Upload all invalid images. Verify route stuck in PENDING.

**Priority**: **Critical** -- Same root cause as 4.1.

---

### 4.3 Status Hash TTL Expires Before SSE Reads It

**Scenario**: An image is processed, the status hash has 1-hour TTL, but no SSE connection is made for over 1 hour.

**What happens step by step**:
1. Image processed. `image:status:{id}` set to `stage=done` with TTL=1h (DefaultProgressTTL in progress.go line 10).
2. No SSE connection for > 1 hour. Hash expires.
3. Client connects to SSE.
4. Poller calls `HGetAll()` for expired key. Empty map returned.
5. `PollImageStatus()` returns `ErrImageStatusNotFound`.
6. `pollAllImages()`: `continue` (transient retry).
7. Hash never reappears. Image is stuck as non-terminal in SSE's view.
8. After MaxDuration (5 min): `complete(all_done=false)`.

**Expected behavior**: SSE should fall back to PostgreSQL for image status when Valkey data is missing.

**Current risk**: **VERY LOW RISK IN PRACTICE**. The 1-hour TTL far exceeds the 5-minute SSE MaxDuration. The route transition in PostgreSQL has already happened (via the API consumer). The route is READY or PUBLISHED in PostgreSQL. The SSE stream shows `all_done=false`, but the client can verify the actual route status via `GET /routes/{id}`.

**Testability**: Set very short TTL (5 seconds). Upload, wait for processing, wait for TTL expiry, connect SSE. Verify `complete(all_done=false)`.

**Priority**: **Low** -- Extremely unlikely in production.

---

### 4.4 Stream Message with Non-Existent image_id in PostgreSQL

**Scenario**: A message appears in `image:result` stream with an image_id that doesn't exist in the database.

**What happens step by step**:
1. Message arrives (could be from: deleted route, orphaned replacement, or corrupted data).
2. `ImageResultProcessor.Process()` calls `MarkImageProcessedUseCase.Execute()`.
3. `imageRepo.FindByID()` returns `domain.ErrImageNotFound`.
4. Error propagates: `ErrImageModuleFailed`.
5. `handleMessage()` returns error. Message NOT ACKed.
6. **Poison pill**: infinite retry.

**Expected behavior**: Consumer should recognize permanent failures (image not found, waypoint not found) and ACK the message with an error log.

**Current risk**: **UNHANDLED**. This is the systemic root cause behind scenarios 2.6, 2.8, and 3.5. The consumer has no dead letter mechanism and no distinction between transient and permanent errors.

**Testability**: Directly publish a message with a random UUID to `image:result`. Verify it becomes a poison pill in the PEL.

**Priority**: **Critical** -- Root cause of multiple poison pill scenarios.

---

### 4.5 Duplicate Stream Messages (Idempotency)

**Scenario**: Same image result message delivered twice (Reclaimer redelivery, or network-level duplicate).

**What happens step by step**:
1. First delivery: `MarkImageProcessedUseCase` marks image as "uploaded". `ConfirmWaypointImageUseCase` confirms waypoint, transitions route.
2. Second delivery: `MarkImageProcessedUseCase` (line 81-87): image already "uploaded", returns early (idempotent). `ConfirmWaypointImageUseCase` (line 168): waypoint already confirmed, skips. Route already READY (line 281): returns `already_ready`.
3. Both steps succeed. Second ACK is also successful.

**Expected behavior**: No-op on second delivery.

**Current risk**: **HANDLED**. Both use cases have explicit idempotency checks.

**Testability**: Process same message twice. Verify no errors and no double transition.

**Priority**: **Low**

---

### 4.6 Consumer Group Not Created Before First Message

**Scenario**: Gateway publishes before API has started and created the `api-workers` consumer group.

**What happens step by step**:
1. Gateway publishes to `image:result` stream (XADD creates the stream if needed).
2. API starts. Consumer calls `createGroup()` (consumer.go line 127) with `XGROUP CREATE ... "0"`.
3. `"0"` means: start reading from the beginning of the stream.
4. Consumer calls `XREADGROUP ... ">"` which delivers all messages not yet delivered to the group.
5. Pre-existing message is delivered and processed.

**Expected behavior**: All pre-existing messages consumed.

**Current risk**: **HANDLED**. The `"0"` starting position ensures all historical messages are visible.

**Testability**: Publish to stream before starting consumer. Start consumer. Verify message is processed.

**Priority**: **Low**

---

### 4.7 XACK Fails After Successful Processing

**Scenario**: Handler succeeds but XACK fails (Valkey hiccup at the exact moment of ack).

**What happens step by step**:
1. `processAndAck()` (message_processor.go line 22): handler returns nil (success).
2. `client.XAck()` fails (line 27). Error logged (line 33-39).
3. Message stays in PEL despite successful processing.
4. Reclaimer eventually re-claims and redelivers.
5. Second processing: idempotent (both use cases return early).
6. Second XACK attempt (hopefully succeeds). Message finally removed from PEL.

**Expected behavior**: Idempotent retry handles this.

**Current risk**: **HANDLED**. The `processAndAck` function logs the XACK failure but doesn't crash. The PEL + Reclaimer + idempotent handlers handle this correctly.

**Testability**: Difficult to reproduce (requires Valkey failure at exact XACK moment). Conceptually covered by idempotency tests.

**Priority**: **Low**

---

### 4.8 Image Module Succeeds But Route Module Returns Unexpected Result Type

**Scenario**: `ConfirmWaypointImageCommand` executes but doesn't set a result (programming error).

**What happens step by step**:
1. `ImageResultProcessor.Process()` (image_result_processor.go): imageModule succeeds.
2. `routeModule.ExecuteCommand()` succeeds.
3. `routeCmd.GetResult()` returns something that is not `*routeModule.ConfirmWaypointImageResult`.
4. Line 123-129: `ErrMissingRouteResult` is returned.
5. `handleMessage()` returns error. Message NOT ACKed.
6. Retried forever. Poison pill (unless it was a transient issue that self-corrects, which it wouldn't be since it's a programming error).

**Expected behavior**: This is a programming error that should be caught during development/testing. In production, should ACK with error log after N retries.

**Current risk**: **LOW RISK IN PRACTICE** but follows the same poison pill pattern if it occurs. This is a programming error, not a runtime failure.

**Testability**: Unit test level, not integration test.

**Priority**: **Low**

---

## CATEGORY 5: RESOURCE EXHAUSTION

---

### 5.1 Stream MAXLEN Trim Removes Unprocessed Messages

**Scenario**: Stream is trimmed past messages that are still in the PEL.

**What happens step by step**:
1. Gateway publishes with `PublishWithTrim()` using approximate MAXLEN.
2. If MAXLEN is small and the consumer is behind (e.g., dead consumer, slow processing), old messages are trimmed from the stream.
3. Messages in PEL that reference trimmed stream entries: XAUTOCLAIM returns them in the "deleted entries" list rather than the "claimed" list.
4. Those messages are effectively lost.

**Expected behavior**: PEL messages should be processed before trimming.

**Current risk**: **VERY LOW RISK**. Each image produces exactly 1 stream message. The default MAXLEN should be configured to handle peak load. If MAXLEN = 10000 and the consumer is processing, it would need 10000+ unprocessed messages to hit this. If the consumer is dead, this is a secondary concern (the primary issue is the dead consumer itself).

**Testability**: Set MAXLEN very low (e.g., 5). Publish many messages with consumer paused. Resume consumer. Verify some messages are lost.

**Priority**: **Low**

---

### 5.2 Many Concurrent SSE Connections Overwhelm Valkey

**Scenario**: Thousands of SSE connections each polling Valkey every 500ms.

**What happens step by step**:
1. Each SSE connection runs `SSEStreamer.Stream()` which creates a 500ms ticker.
2. Each tick calls `PollImageStatus()` for EACH non-terminal image in the route.
3. With 1000 connections averaging 3 images each: 6000 HGetAll calls per second.
4. Valkey latency increases. Consumer reads slow down. Progress updates slow down.
5. SSE poll errors increase (timeouts), causing skipped ticks.
6. Degraded experience for all users.

**Expected behavior**: Connection limits or polling optimization (batching, adaptive intervals).

**Current risk**: **UNHANDLED at scale**. No connection limit. No rate limiting. No batched polling (each image polled individually). No adaptive polling interval.

**Testability**: Load test: open N concurrent SSE connections, monitor Valkey latency and error rates.

**Priority**: **Medium** -- Not a problem at MVP scale.

---

### 5.3 Large Image Causes Pipeline Backpressure

**Scenario**: A very large image (10MB) occupies pipeline workers while other images wait.

**What happens step by step**:
1. Large image enters pipeline. Worker is busy for extended time (decode, ML detection, encode).
2. Other jobs queue behind in channel buffers.
3. If buffer is full, `Submit()` blocks (with timeout context) or `TrySubmit()` returns `ErrPipelineFull`.
4. Gateway returns 503 Service Unavailable for new uploads if pipeline is full.

**Expected behavior**: Backpressure should be communicated to clients.

**Current risk**: **HANDLED**. The `TrySubmit()` (pipeline.go line 376) returns `ErrPipelineFull`. The `Submit()` (line 326) has a timeout context. The upload service (upload_service.go line 141-145) creates a timeout context for submission. If pipeline is full/slow, gateway returns 503.

**Testability**: Upload many large images simultaneously. Verify 503 responses when pipeline is saturated. Backpressure test exists: `tests/integration/backpressure_test.go`.

**Priority**: **Low**

---

### 5.4 Valkey Memory Bounded by TTLs and MAXLEN

**Scenario**: Does Valkey memory grow unboundedly?

**What happens step by step**:
1. Per image: `image:status:{id}` hash (TTL 1h), `image:upload:{id}` guard (TTL 1h), 1 stream message.
2. Hashes and guards expire after 1 hour.
3. Stream is bounded by MAXLEN.
4. Consumer group metadata is small and bounded.

**Expected behavior**: Memory should be bounded.

**Current risk**: **HANDLED**. TTLs + MAXLEN bound memory usage.

**Testability**: Monitoring concern, not integration test.

**Priority**: **Low**

---

## CATEGORY 6: BUSINESS LOGIC EDGE CASES

---

### 6.1 Route with Single Waypoint -- Single Image Determines Route Fate

**Scenario**: Route has exactly 1 waypoint. If the single image fails, the route is permanently stuck.

**What happens step by step**:
1. Route created with 1 waypoint.
2. Image fails processing (invalid data, ML detection finds a face, etc.).
3. `handleImageFailure()`: returns `image_failed_no_pending_replacement`. No waypoint change.
4. Route stays PENDING with 1 pending waypoint. No recovery path.

**Expected behavior**: Same issue as 4.1 but amplified -- single point of failure for the entire route.

**Current risk**: **UNHANDLED**. Same root cause as 4.1/4.2.

**Testability**: Create 1-waypoint route. Upload invalid image. Verify stuck.

**Priority**: **Critical** -- Same as 4.1.

---

### 6.2 Route with Zero Waypoints

**Scenario**: User creates a route with empty waypoints array.

**What happens step by step**:
1. `POST /routes/{id}/create-waypoints` with `"waypoints": []`.
2. API validation should reject (Goa DSL likely has MinLength validation on the waypoints array).
3. If validation passes: route transitions to PENDING with 0 waypoints.
4. SSE: `resolveImageIDs()` returns empty list.
5. `SSEStreamer.Stream()` (sse_streamer.go line 83-85): `len(imageIDs) == 0`, immediately sends `complete(all_done=true)`.
6. But route is PENDING with no waypoints to confirm -- it can never transition to READY.

**Expected behavior**: API should reject empty waypoint lists.

**Current risk**: **LIKELY HANDLED** (by Goa DSL validation). Would need to check the design DSL to confirm.

**Testability**: Call `create-waypoints` with empty array. Verify 400 Bad Request.

**Priority**: **Low** -- Likely validated.

---

### 6.3 Image Replacement: Failed Replacement Clears Pending Fields

**Scenario**: User initiates image replacement. New image fails processing. Pending fields are cleared.

**What happens step by step**:
1. Route is PUBLISHED. Waypoint 1 has image A (confirmed). User prepares replacement with image B.
2. Waypoint: `image_id=A, pending_replacement_image_id=B, pending_marker_x=0.5, pending_marker_y=0.6`.
3. Image B fails in gateway. API consumer receives failure.
4. `ConfirmWaypointImageUseCase.handleImageFailure()` (line 119-159):
   - `FindByPendingReplacementImageID(B)` -- found.
   - `waypoint.ClearPendingReplacement()` -- clears `pending_replacement_image_id`, `pending_marker_x`, `pending_marker_y`.
   - Saves waypoint. Returns `pending_replacement_cleared_on_failure`.
5. Waypoint reverts to original state: `image_id=A` with original markers.
6. Route stays PUBLISHED. User can try replacement again.

**Expected behavior**: Clean rollback of pending replacement.

**Current risk**: **HANDLED**. The `handleImageFailure` path correctly clears pending fields and allows retry.

**Testability**: Prepare replacement, upload invalid image, verify pending fields are cleared and waypoint retains original image.

**Priority**: **Low**

---

### 6.4 Route Status Transition: PENDING Can Only Go to READY

**Scenario**: Something tries to transition a route from PENDING to a non-READY state.

**What happens step by step**:
1. `route.UpdateStatus(target)` is called internally.
2. `CanTransitionTo()` (route_status.go line 95-119): PENDING can only transition to READY.
3. Any other transition returns `ErrRouteInvalidStatus`.

**Expected behavior**: State machine enforces valid transitions.

**Current risk**: **HANDLED**. State machine is explicit and tested.

**Testability**: Already covered by unit tests in `route_status_test.go`.

**Priority**: **Low**

---

### 6.5 Route Published -- Image Replacement Does Not Affect Route Status

**Scenario**: Route is PUBLISHED. Image replacement occurs. Route should stay PUBLISHED.

**What happens step by step**:
1. Route is PUBLISHED. Waypoint image replaced.
2. `ConfirmWaypointImageUseCase.handleReplacementFlow()` (line 219-261): delegates to `SwapWaypointImageUseCase`.
3. Returns `RouteTransitioned: false, Reason: "image_replaced"`.
4. Route stays PUBLISHED.

**Expected behavior**: Replacement does not change route status.

**Current risk**: **HANDLED**. Tested in behavioral flow test Step 13d.

**Testability**: Already tested.

**Priority**: **Low**

---

## CATEGORY 7: SSE-SPECIFIC EDGE CASES

---

### 7.1 SSE MaxDuration Expires While Images Still Processing

**Scenario**: Images take longer than 5 minutes. SSE stream times out.

**What happens step by step**:
1. `SSEStreamer.Stream()`: `deadline := time.Now().Add(streamer.cfg.MaxDuration)` (sse_streamer.go line 87).
2. On each tick: `if tickTime.After(deadline)` (line 116).
3. After 5 minutes: `sendCompleteAndClose(sender, false)` (line 117-120).
4. Client receives `complete(all_done=false)`.
5. Stream closes.

**Expected behavior**: Client should be informed that processing is ongoing. Client should reconnect or use polling endpoint.

**Current risk**: **HANDLED**. The `all_done=false` flag clearly communicates that not all images completed. Client responsibility to reconnect or poll.

**Testability**: Already tested in `TestSSEStreamer_Stream_MaxDurationExpiry`.

**Priority**: **Low**

---

### 7.2 Heartbeat Keeps Connection Alive During Idle Periods

**Scenario**: All images are in intermediate states with no changes. No status events to send. Connection might be killed by proxy.

**What happens step by step**:
1. All images are at "processing" stage. Poller sends "processing" events on each tick, but only if the status is different from... actually, there is NO deduplication of events. The SSE streamer sends "processing" on EVERY tick for every non-terminal image.
2. Wait -- re-reading the code: `handleImageStatus()` (sse_streamer.go line 199-261) sends an event for every non-terminal image on every tick. It DOES send repeated "processing" events. This means the connection is very active, not idle.
3. Additionally, heartbeats fire every 30 seconds (line 102-113).

**Expected behavior**: Connection should stay alive via regular events.

**Current risk**: **HANDLED**. In fact, the SSE stream is MORE active than needed because it sends "processing" events on every poll tick (every 500ms), not just on state changes. This keeps the connection very active but generates more traffic than necessary.

**Testability**: Already tested in `TestSSEStreamer_Stream_HeartbeatSent`.

**Priority**: **Low**

---

### 7.3 SSE Sends Repeated "processing" Events (No Deduplication)

**Scenario**: The SSE streamer sends "processing" for the same image on every poll tick, not just on state changes.

**What happens step by step**:
1. Image is at "validating" stage. First tick: sends "processing" event.
2. Image still at "validating" stage. Second tick: sends "processing" event again.
3. Image moves to "decoding". Third tick: sends "processing" event (same SSE event type, different internal stage).
4. Client receives many duplicate "processing" events.

**Expected behavior**: The streamer should either (a) track previous state and only send on change, or (b) include the actual stage in the event so clients can deduplicate.

**Current risk**: **LOW IMPACT BUT SUBOPTIMAL**. The client receives redundant "processing" events (every 500ms per non-terminal image). This wastes bandwidth and client processing, but doesn't cause incorrect behavior. The event payload does NOT include the actual stage (only "processing" / "ready" / "failed") -- lines 243-257 collapse all non-terminal stages to "processing".

**Testability**: Connect to SSE while image is processing. Count how many "processing" events are received for the same image.

**Priority**: **Low** -- Wasteful but not broken. Future optimization.

---

### 7.4 Client Disconnects Immediately After Connecting

**Scenario**: Client opens SSE and immediately closes the connection.

**What happens step by step**:
1. Client connects. `StreamRouteStatus()` starts.
2. `resolveImageIDs()` queries PostgreSQL. Returns image IDs.
3. `SSEStreamer.Stream()` starts. First poll tick fires.
4. `sender.Send()` returns error (connection already closed).
5. `handleImageStatus()` detects error, calls `sender.Close()`, returns `exited=true`.
6. `Stream()` returns nil. Clean exit.

**Expected behavior**: No resource leak, no error propagation.

**Current risk**: **HANDLED**. Code correctly handles sender errors at every Send call point.

**Testability**: Connect and immediately disconnect. Verify server logs show clean cleanup.

**Priority**: **Low**

---

### 7.5 No Last-Event-ID Support

**Scenario**: Client reconnects with `Last-Event-ID` header.

**What happens step by step**:
1. SSE events have no ID field set (the `RouteStatusEventPayload` struct has no ID field -- sse_streamer.go line 20-27).
2. Client reconnects with `Last-Event-ID`. Server ignores it (Goa may not even parse it).
3. SSE starts fresh, polling current Valkey state.

**Expected behavior**: The polling design makes `Last-Event-ID` unnecessary.

**Current risk**: **ACCEPTABLE**. The SSE design is intentionally polling-based, not event-sourced. Reconnection reads current state, which is correct behavior.

**Testability**: Send `Last-Event-ID` header. Verify no effect.

**Priority**: **Low**

---

### 7.6 SSE for Route That Doesn't Exist

**Scenario**: Client connects to SSE for a non-existent or unauthorized route.

**What happens step by step**:
1. `StreamRouteStatus()` calls `resolveImageIDs()` (routes_service.go line 1577-1581).
2. `GetImageIDsForRouteQuery` fails with `ErrRouteNotFound` or authorization error.
3. `resolveImageIDs` returns error (line 1617-1619).
4. `StreamRouteStatus` calls `closeSSEStream(stream)` (line 1581).
5. Stream is closed. Client receives EOF.

**Expected behavior**: Stream closes immediately. Client should handle the connection close.

**Current risk**: **PARTIALLY HANDLED**. The stream closes without sending an error event. The client just sees a connection close with no explanation. Ideally, an error event should be sent before closing. The `closeSSEStream` function (line 1537-1547) just calls `stream.Close()` without sending any event.

**Testability**: Connect to SSE with invalid route_id. Verify connection closes immediately.

**Priority**: **Low** -- Works but UX could be better.

---

### 7.7 SSE Write Deadline Middleware

**Scenario**: HTTP server's write deadline kills SSE connections.

**What happens step by step**:
1. Go's HTTP server has a write deadline that would kill long-lived SSE connections.
2. `sseWriteDeadlineMiddleware()` (tested in sse_middleware_test.go) detects SSE paths (matching `/status/stream` suffix).
3. Sets `SetWriteDeadline(time.Time{})` -- zero time means no deadline.
4. SSE connections are exempt from write deadlines.

**Expected behavior**: SSE connections should not be killed by write deadlines.

**Current risk**: **HANDLED**. The middleware correctly disables write deadlines for SSE paths.

**Testability**: Already tested in `TestSSEWriteDeadlineMiddleware`.

**Priority**: **Low**

---

## CATEGORY 8: SYSTEMIC / CROSS-CUTTING ISSUES

---

### 8.1 No Dead Letter Queue -- Poison Pill Messages Retry Forever

**Scenario**: A message in `image:result` cannot be processed permanently (image deleted, waypoint deleted, orphaned replacement, invalid data).

**What happens step by step**:
1. `handleMessage()` returns an error. `processAndAck()` does NOT call XACK.
2. Message stays in PEL.
3. Reclaimer (reclaimer.go) runs every `ScanInterval=1min` and calls XAUTOCLAIM for messages idle > `IdleTimeout=5min`.
4. `claimIdleMessages()` claims the message. `processAndAck()` runs the handler again.
5. Same error. Handler fails. Message NOT ACKed. Back to PEL.
6. Next Reclaimer cycle: same thing. **Infinite loop**.

**Root cause analysis**: The `processAndAck()` function (message_processor.go) has a binary outcome: handler returns nil -> ACK, handler returns error -> no ACK. There is NO mechanism for:
- Tracking delivery count (how many times a message has been retried).
- Dead letter queue (moving permanently-failed messages out of the main stream).
- Distinguishing transient errors (DB timeout) from permanent errors (image not found).
- Maximum retry count before forced ACK with error log.

**Affected scenarios**: 2.6 (route deletion during processing), 2.8 (rapid replacement), 3.5 (upload to deleted route), 4.4 (non-existent image_id).

**Expected behavior**: Either (a) the handler should classify errors as transient vs permanent and ACK permanent failures, or (b) a message should have a max delivery count after which it's moved to a dead letter stream, or (c) the Reclaimer should check XPENDING delivery counts and skip messages exceeding a threshold.

**Current risk**: **UNHANDLED**. This is the most impactful systemic issue. The PEL grows with each poison pill message. The Reclaimer wastes cycles retrying them. At sufficient scale, the Reclaimer's XAUTOCLAIM will always return poison pill messages, starving legitimate retry messages.

**Testability**: Publish a message with a random (non-existent) image_id to `image:result`. Wait for Reclaimer cycle. Verify the message is in PEL with increasing delivery count. Verify it is retried forever.

**Priority**: **Critical**

---

### 8.2 No Consumer Restart Logic in Application Layer

**Scenario**: The consumer goroutine exits due to any unrecoverable error (Valkey disconnect, configuration issue, etc.). The API continues running without stream processing.

**What happens step by step**:
1. `Consumer.Start()` returns an error (e.g., after `MaxRetries=3` XREADGROUP failures).
2. The goroutine that called `Start()` exits.
3. The API HTTP server continues running. All other endpoints work.
4. No more `image:result` messages are consumed.
5. Images get processed by the gateway, results published to stream, but never consumed by the API.
6. Routes stay in PENDING forever. Waypoints never confirmed. Route transitions never happen.
7. SSE works (reads from Valkey hashes, not from the consumer) -- shows "done" for processed images.
8. **Split brain**: SSE says images are "ready", but route status is "pending" in PostgreSQL.

**Expected behavior**: Application should detect consumer exit and restart it with exponential backoff.

**Current risk**: **UNHANDLED**. The `Consumer` struct uses `sync.Once` (consumer.go line 252-258) -- `Start()` can only be called once. To restart, a completely new `ImageResultConsumer` (and underlying `Consumer`) must be created. There is no supervisor, circuit breaker, or restart logic in the application layer.

**Testability**: Cause consumer to exit (Valkey disconnect). Verify API continues serving HTTP. Verify no stream messages are consumed. Verify routes stay PENDING even though SSE shows images as "ready".

**Priority**: **Critical**

---

### 8.3 No Recovery Path for Failed Creation-Flow Images

**Scenario**: An image fails during the creation flow. The route cannot progress to READY.

**What happens step by step**:
1. Image fails (validation error, ML detection, timeout, etc.).
2. Route stays PENDING (see 4.1).
3. User wants to fix the failed image. Options:
   - `replace-image/prepare`: requires READY or PUBLISHED. **Blocked** -- route is PENDING.
   - No "retry image upload" endpoint exists for creation flow.
   - Only option: `DELETE /routes/{id}` and start over.

**Root cause**: The `replace-image/prepare` endpoint (based on the code reading) validates that the route is in READY or PUBLISHED state before allowing replacement. This is a deliberate design choice to prevent replacement during the initial creation flow. However, it means there is no remediation path when creation-flow images fail.

**Expected behavior**: At minimum one of: (a) allow replacement on PENDING routes, (b) add a "retry-upload" endpoint for creation-flow images, (c) auto-detect and transition stuck routes to a "failed" status with notification.

**Current risk**: **UNHANDLED**. This is a product-level gap that directly impacts user experience. A single bad image (or a transient gateway failure) during route creation means the user loses all their work and must start over.

**Testability**: Create a multi-waypoint route. Upload invalid image for one waypoint. Verify: (a) route stuck in PENDING, (b) `replace-image/prepare` returns error, (c) no other fix API exists.

**Priority**: **Critical**

---

### 8.4 Fire-and-Forget Progress Updates Create Inconsistency Window

**Scenario**: The gateway's per-stage progress updates to `image:status:{id}` are fire-and-forget. If one fails silently, SSE shows stale data.

**What happens step by step**:
1. Pipeline worker processes a job through stages. At each stage, the job's `ProgressTracker.SetProgress()` is called (via pipeline observer or per-stage logic).
2. A Valkey hiccup causes one update to fail silently.
3. SSE poller reads the stale stage. Client sees the image stuck at an old stage.
4. Next successful update corrects the data.

**Expected behavior**: Progress updates are best-effort. Brief staleness is acceptable.

**Current risk**: **ACCEPTABLE**. Progress updates are informational. The critical writes are the FINAL status update and the stream publish. Brief staleness in intermediate stages is a non-issue.

**Testability**: Not worth testing -- by design.

**Priority**: **Low**

---

### 8.5 Initial "queued" Status Write Failure (API-Side, Fire-and-Forget)

**Scenario**: API creates route/waypoints but the initial `stage=queued` write to Valkey fails.

**What happens step by step**:
1. `create-waypoints` processes. For each image, `SetProgress()` is called with `stage=queued` (prepare_image_upload.go line 324-329).
2. Write fails (Valkey unreachable). Error is logged but not propagated (line 330-332: "fire-and-forget").
3. API returns 200 with presigned URLs.
4. SSE: poller tries to read `image:status:{id}`. Key doesn't exist. Returns `ErrImageStatusNotFound`. Treated as transient.
5. Gateway receives the upload. Sets progress as it processes. Eventually the hash is created by the gateway.
6. SSE eventually picks up the gateway-written status.

**Expected behavior**: SSE should work even without the initial API-written "queued" status.

**Current risk**: **HANDLED**. The fire-and-forget design is intentional. SSE gracefully handles missing keys. Gateway writes overwrite the initial "queued" status anyway.

**Testability**: Block API -> Valkey before route creation. Verify SSE still works (may show brief "no status" period before gateway updates).

**Priority**: **Low**

---

## SUMMARY: PRIORITIZED ACTION ITEMS

### Critical (8 scenarios -- immediate action required)

| ID | Scenario | Core Issue |
|----|----------|-----------|
| **8.1** | No dead letter queue / max retry | Poison pill messages retry forever in PEL |
| **8.2** | No consumer restart logic | Consumer dies permanently on any Valkey hiccup |
| **8.3** | No recovery for failed creation images | Route permanently stuck in PENDING |
| **4.1** | Partial route failure | Some images fail, route stuck in PENDING |
| **4.2** | All images fail | Same as 4.1 for all images |
| **3.1** | Valkey connection drop kills consumer | 0.7 seconds of Valkey downtime = permanent consumer death |
| **3.2** | Gateway-Valkey disconnect loses result | Silent data loss, image processed but API never knows |
| **1.2** | Gateway crash after MinIO but before stream | Split brain: Valkey says done, PostgreSQL says pending |

### High (4 scenarios -- fix in near term)

| ID | Scenario | Core Issue |
|----|----------|-----------|
| **1.1** | Gateway crash mid-pipeline | No result published, route stuck |
| **1.6** | Valkey restart destroys consumer group | Consumer gets NOGROUP, dies, no restart |
| **2.6** | Route deletion while processing | Poison pill (4.4 root cause) |
| **2.8** | Rapid replacement overwrites pending | Poison pill (4.4 root cause) |
| **3.5** | Upload to deleted route URL | Poison pill (4.4 root cause) |

### Medium (5 scenarios -- plan for next iteration)

| ID | Scenario | Core Issue |
|----|----------|-----------|
| **1.3** | API crash mid-consume | 5-minute delay before retry |
| **1.8** | PostgreSQL down during processing | 5-minute delay before retry |
| **2.4** | Replacement not tracked in SSE | No real-time replacement feedback |
| **3.4** | Presigned URL expires (creation) | No URL regeneration for creation flow |
| **5.2** | SSE connection flood | No rate limiting at scale |

### Low (remaining scenarios -- acceptable risk)

All other scenarios (1.4, 1.5, 1.7, 2.1, 2.2, 2.3, 2.5, 2.7, 2.9, 2.10, 3.3, 3.6, 4.3, 4.5, 4.6, 4.7, 4.8, 5.1, 5.3, 5.4, 6.2, 6.3, 6.4, 6.5, 7.1-7.7, 8.4, 8.5) are handled by the existing design or represent extremely unlikely conditions.

---

## RECOMMENDED FIXES (Architectural)

### Fix 1: Dead Letter / Max Retry Mechanism (Fixes 8.1, 2.6, 2.8, 3.5, 4.4)
In `processAndAck()` or the Reclaimer, check message delivery count via `XPENDING`. After N retries (e.g., 5), force ACK with error log. Optionally publish to a `image:result:dead-letter` stream for investigation.

Alternatively, modify `ImageResultProcessor.Process()` to classify `ErrImageNotFound` and `ErrWaypointNotFound` as permanent failures and return nil (causing ACK).

### Fix 2: Consumer Supervisor / Restart Logic (Fixes 8.2, 3.1)
In `follow-api/cmd/server/app.go`, wrap the consumer start in a restart loop with exponential backoff. Create new `ImageResultConsumer` instances on each restart (since `Consumer` is single-use). Cap backoff at 30 seconds.

### Fix 3: Creation-Flow Image Retry (Fixes 8.3, 4.1, 4.2, 6.1)
Either (a) add a `POST /routes/{id}/waypoints/{wid}/retry-upload` endpoint that generates a new presigned URL for a failed creation-flow image, or (b) allow `replace-image/prepare` to work on PENDING routes for waypoints with failed images.

### Fix 4: Gateway Publish Retry (Fixes 3.2, 1.2)
Add retry logic to `ResultPublisher.publishToStream()` with exponential backoff (3 attempts). If all retries fail, write to a local fallback file or in-memory queue. Consider making `PublishResult` return an error so the pipeline can handle publish failures.

### Fix 5: Consumer Group Recreation (Fixes 1.6)
In the consumer's error handling path, detect NOGROUP errors and call `createGroup()` again before retrying.