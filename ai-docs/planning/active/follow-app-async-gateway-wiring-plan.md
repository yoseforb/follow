# follow-app Async Gateway Wiring Plan — Phase 1: Route Creation

**Date:** 2026-02-25
**Status:** Active
**Scope:** Phase 1 — new route creation only. Route update and image replacement (Phase 2) are out of scope.
**Repo:** `follow-app` (Flutter/Dart)

---

## Overview

This plan wires the follow-app to the new async image-processing flow introduced by follow-image-gateway. Phase 1 covers only the route creation workflow — from the user tapping "Upload Route" through to a published route that can be navigated.

### Business Value

- Routes created through the gateway benefit from automated image validation and ML face/plate detection before going live.
- The synchronous confirm step is eliminated, removing a fragile "did all uploads land?" check.
- Users get real-time feedback as each image is processed, instead of a spinner that obscures what is happening.

### What Changes vs. What Stays the Same

| Concern | Current behavior | New behavior |
|---|---|---|
| Steps 1–2 (prepare + create-waypoints) | Unchanged | Unchanged |
| Upload target URL | Points to MinIO (presigned) | Points to gateway (same URL field, different server) |
| Upload HTTP response | 200 OK, no body | 202 Accepted, body contains image_id + status |
| Post-upload acknowledgment | POST confirm-waypoints (removed from API) | SSE stream + POST publish |
| Route status after upload | active | ready (SSE complete) → published (after POST publish) |
| Draft save/load | Unchanged | Unchanged |
| Offline behavior | Save draft, queue for later | Unchanged |

---

## Architecture of the New Flow

```
RouteCreationScreen
    |
    v
RouteCreationViewModel.uploadRouteToServer()
    |
    ├── Step 1: RouteRepository.prepareRoute()
    |       POST /api/v1/routes/prepare → route_id
    |
    ├── Step 2: RouteRepository.createRouteWithWaypoints()
    |       POST /api/v1/routes/{route_id}/create-waypoints
    |       → presigned_urls[] (now point to gateway)
    |
    ├── Step 3: RouteRepository.uploadImageToGateway() [MODIFIED]
    |       PUT {upload_url} → 202 Accepted
    |       Response body: { "image_id": "...", "status": "processing" }
    |
    ├── Step 4: RouteStatusStreamService.streamRouteStatus() [NEW]
    |       GET /api/v1/routes/{route_id}/status/stream
    |       SSE events: processing | ready | failed | heartbeat | complete
    |       ViewModel updates _processingProgress per event
    |
    └── Step 5: RouteRepository.publishRoute() [NEW]
            POST /api/v1/routes/{route_id}/publish → published
```

### SSE Event Format

```
Content-Type: text/event-stream

event: processing
data: {"image_id":"abc","status":"processing"}

event: ready
data: {"image_id":"abc","status":"ready"}

event: failed
data: {"image_id":"abc","status":"failed","error_reason":"..."}

event: heartbeat
data: {}

event: complete
data: {"route_id":"...","status":"ready"}
```

The stream closes after `complete` (or client disconnect). follow-api polls Valkey every 500 ms internally.

### Publish Endpoint

```
POST /api/v1/routes/{route_id}/publish
Authorization: Bearer {token}
Body: {}

Response 200:
{
  "route_id": "...",
  "route_status": "published",
  "published_at": "..."
}
```

---

## File Structure

### Files Modified

| File | Change summary |
|---|---|
| `lib/domain/models/route.dart` | Add `ready` and `published` values to `RouteStatus` enum |
| `lib/config/api_config.dart` | Add `publishRouteUrl()` and `routeStatusStreamUrl()`. Remove `confirmRouteWaypointsUrl()` |
| `lib/data/repositories/route_repository.dart` | Add `uploadImageToGateway()`, `publishRoute()` to abstract interface and `HttpRouteRepository`. Remove `confirmRouteWaypoints()` from interface and implementation. Remove `ConfirmRouteResponse` DTO if present. Add `GatewayUploadResponse` and `PublishRouteResponse` DTOs |
| `lib/ui/route_creation/route_creation_view_model.dart` | Replace `_uploadImagesWithProgress()` + `confirmRouteWaypoints()` with gateway upload + SSE wait + publish. Add processing state fields: `_processingProgress`, `_processingPhase`, `_failedImageCount`. Cancel SSE stream on dispose |
| `lib/ui/route_creation/widgets/upload_banner.dart` | Add `isProcessing` parameter and new processing phase hint text |
| `lib/ui/route_creation/widgets/upload_progress_indicator.dart` | Add `ProcessingPhase` parameter to distinguish "uploading" vs "processing on server" |
| `lib/l10n/app_en.arb` | Add new strings: processing phase labels, failed image error, publish step, SSE timeout error |
| `lib/l10n/app_he.arb` | Hebrew translations for all new strings |

### Files Created

| File | Purpose |
|---|---|
| `lib/data/services/route_status_stream_service.dart` | Pure Dart SSE client. Parses `text/event-stream` over an HTTP chunked response. Returns a `Stream<RouteStatusEvent>` |
| `test/data/services/route_status_stream_service_test.dart` | Unit tests for SSE parser using raw byte streams (no network) |
| `test/ui/route_creation/route_creation_view_model_gateway_test.dart` | Unit tests for the new async upload + SSE + publish flow in the ViewModel |

---

## Tasks

### Task 1 — Extend RouteStatus enum with `ready` and `published`

**Story Points:** 1

**Description:**
The existing `RouteStatus` enum has four values: `preparing`, `pending`, `active`, `archived`. The new server-side lifecycle uses `ready` (all images processed, awaiting publish) and `published` (live, navigable). Add these two values.

`active` remains in the enum because it may appear from legacy API responses or cached routes. The computed `Route.status` getter currently returns `active` for non-draft routes — this getter should remain unchanged for now (its logic is tied to `isDraft`, not to API status strings); the new values are for type-safe handling of SSE and publish response payloads.

**Files Affected:**
- `lib/domain/models/route.dart`

Changes:
```dart
// Add after archived('archived'):
ready('ready'),
published('published');
```

Update `fromString()` — it already uses `values.firstWhere` with a `FormatException` fallback, so no logic change is needed; the new values will be found automatically.

**Dependencies:** None

**Acceptance Criteria:**
- `RouteStatus.fromString('ready')` returns `RouteStatus.ready`
- `RouteStatus.fromString('published')` returns `RouteStatus.published`
- Existing values continue to parse correctly
- `dart analyze` reports no errors

---

### Task 2 — Add new API endpoint URLs to ApiConfig

**Story Points:** 1

**Description:**
`api_config.dart` is the single source of truth for endpoint URL construction. Add two new URL builders and delete the now-dead confirm-waypoints URL builder.

**Files Affected:**
- `lib/config/api_config.dart`

Changes:
1. Add `publishRouteUrl(String routeId)` method returning `'$apiBaseUrl/routes/$routeId/publish'`
2. Add `routeStatusStreamUrl(String routeId)` method returning `'$apiBaseUrl/routes/$routeId/status/stream'`
3. Delete `confirmRouteWaypointsUrl()` — the endpoint no longer exists in follow-api

The `isUrlSafe()` validator already allows any `$apiBaseUrl/routes/...` path, so no changes are required there.

**Dependencies:** None

**Acceptance Criteria:**
- `ApiConfig().publishRouteUrl('abc-123')` returns the correct URL string
- `ApiConfig().routeStatusStreamUrl('abc-123')` returns the correct URL string
- `confirmRouteWaypointsUrl` does not exist in the class
- `dart analyze` reports no errors

---

### Task 3 — Create RouteStatusStreamService (SSE parser)

**Story Points:** 5

**Description:**
This is a new pure-Dart service that opens an HTTP connection to the SSE endpoint and parses the chunked `text/event-stream` response into a typed Dart `Stream<RouteStatusEvent>`. No third-party SSE package is required — the `http` package is already a dependency and supports streaming responses via `http.Client.send(request)`.

The service does not hold any application state. It is a stateless factory for streams. The ViewModel manages the subscription lifecycle.

**Interface:**
```dart
/// Represents a single SSE event from the route status stream.
class RouteStatusEvent {
  const RouteStatusEvent({
    required this.type,
    required this.imageId,
    required this.routeId,
    this.status,
    this.errorReason,
  });

  // Event type: processing | ready | failed | heartbeat | complete
  final String type;
  final String? imageId;
  final String? routeId;
  final String? status;
  final String? errorReason;

  bool get isComplete => type == 'complete';
  bool get isFailed => type == 'failed';
  bool get isHeartbeat => type == 'heartbeat';
}

abstract class RouteStatusStreamService {
  /// Opens an SSE stream for route status updates.
  ///
  /// The stream emits [RouteStatusEvent]s until:
  /// - A 'complete' event is received (stream closes normally)
  /// - [cancelToken] is cancelled (stream closes with cancellation)
  /// - A network error occurs (stream closes with error)
  ///
  /// The caller must cancel the subscription when no longer needed.
  Stream<RouteStatusEvent> streamRouteStatus({
    required String routeId,
    required String authToken,
  });
}
```

**Implementation (`HttpRouteStatusStreamService`):**
1. Build a `http.Request` with method GET, URL from `ApiConfig().routeStatusStreamUrl(routeId)`, headers `Authorization: Bearer $token` and `Accept: text/event-stream`
2. Call `_httpClient.send(request)` to get a `http.StreamedResponse`
3. Verify status code 200; if not, throw `RouteException` with status code
4. Pipe `response.stream` (a `Stream<List<int>>`) through:
   - `utf8.decoder` transformer (converts bytes to String chunks)
   - A stateful transformer that accumulates incomplete lines and emits complete SSE `event:`/`data:` pairs
5. Parse each complete SSE message block into a `RouteStatusEvent`
6. Yield the event downstream
7. The stream naturally completes when the server closes the connection (after `complete` event)

**SSE parsing rules:**
- SSE messages are separated by double newlines (`\n\n`)
- Each message has zero or more `event:`, `data:`, `id:`, `retry:` lines
- Parse `event:` as the event type string
- Parse `data:` as JSON; extract `image_id`, `route_id`, `status`, `error_reason`
- Ignore `id:` and `retry:` lines
- Discard messages with no `event:` or `data:` (pure comments, empty lines)
- Heartbeat events have `event: heartbeat` and `data: {}`

**Error handling:**
- Network timeout during stream: emit error on the stream (ViewModel catches it)
- Invalid JSON in data field: log warning, skip the event, continue stream
- Non-200 status from server: close stream with a `RouteException`

**Files Affected:**
- `lib/data/services/route_status_stream_service.dart` (new file)

**Dependencies:** Task 2 (needs `routeStatusStreamUrl`)

**Acceptance Criteria:**
- `streamRouteStatus()` returns a `Stream<RouteStatusEvent>`
- Cancelling the subscription causes the underlying HTTP connection to be aborted
- Parses `processing`, `ready`, `failed`, `heartbeat`, `complete` event types correctly from raw SSE bytes
- Invalid JSON data lines are skipped without crashing
- Non-200 response causes stream to emit an error

---

### Task 4 — Unit tests for RouteStatusStreamService

**Story Points:** 3

**Description:**
Write classical/Detroit style unit tests for `HttpRouteStatusStreamService`. Tests use hand-written fakes — no mock framework. A fake HTTP client replaces the real one, feeding pre-canned byte sequences into the service.

**Test scenarios to cover:**

1. Single `ready` event followed by `complete` — stream emits two events then closes
2. `processing` then `ready` then `complete` — stream emits three events then closes
3. `failed` event — stream emits the failed event with `errorReason` populated
4. `heartbeat` event — emitted as `RouteStatusEvent` with `type == 'heartbeat'`
5. Partial line chunks — bytes arrive in mid-line fragments; parser reassembles them correctly
6. Invalid JSON in `data:` line — event is skipped, stream continues to next message
7. HTTP 401 response — stream emits a `RouteException` error
8. HTTP 404 response — stream emits a `RouteException` error

**Fake HTTP client pattern** (matches existing project test style):
```dart
class FakeHttpClient implements http.Client {
  FakeHttpClient({required this.streamedResponse});
  final http.StreamedResponse streamedResponse;

  @override
  Future<http.StreamedResponse> send(http.BaseRequest request) async =>
      streamedResponse;

  // All other methods throw UnimplementedError
}
```

**Files Affected:**
- `test/data/services/route_status_stream_service_test.dart` (new file)

**Dependencies:** Task 3

**Acceptance Criteria:**
- All 8 test scenarios pass
- `flutter test test/data/services/route_status_stream_service_test.dart` exits 0
- `dart analyze` reports no errors on test file

---

### Task 5 — Add gateway upload and publish methods to RouteRepository

**Story Points:** 3

**Description:**
The `RouteRepository` abstract interface and `HttpRouteRepository` implementation need two new methods. The existing `uploadImageToStorage` is kept to avoid breakage of any code referencing it and because it may still be used during Phase 2 transition.

**New DTOs to add inside `route_repository.dart`:**

```dart
/// Response from uploading an image to the gateway.
/// HTTP 202 Accepted — processing is async.
class GatewayUploadResponse {
  const GatewayUploadResponse({
    required this.imageId,
    required this.status,
  });

  factory GatewayUploadResponse.fromJson(Map<String, dynamic> json) {
    return GatewayUploadResponse(
      imageId: json['image_id'] as String,
      status: json['status'] as String,
    );
  }

  final String imageId;
  final String status; // typically 'processing'
}

/// Response from publishing a route.
class PublishRouteResponse {
  const PublishRouteResponse({
    required this.routeId,
    required this.routeStatus,
    required this.publishedAt,
  });

  factory PublishRouteResponse.fromJson(Map<String, dynamic> json) {
    return PublishRouteResponse(
      routeId: json['route_id'] as String,
      routeStatus: json['route_status'] as String,
      publishedAt: DateTime.parse(json['published_at'] as String),
    );
  }

  final String routeId;
  final String routeStatus;
  final DateTime publishedAt;
}
```

**New abstract interface methods:**

```dart
/// Uploads an image to the gateway and returns the async processing response.
///
/// The gateway processes images asynchronously. This method returns after
/// receiving HTTP 202 Accepted. Monitor processing status via
/// [RouteStatusStreamService.streamRouteStatus].
///
/// Throws [RouteException] if the upload request itself fails (network
/// error, 4xx/5xx response, etc.).
Future<GatewayUploadResponse> uploadImageToGateway({
  required File imageFile,
  required String uploadUrl,
  required String contentType,
  Uint8List? imageBytes,
});

/// Publishes a route after all images have been processed.
///
/// Transitions the route from 'ready' to 'published'. The route must be
/// in 'ready' state (i.e., SSE 'complete' event has been received).
///
/// Throws [RouteException] if publication fails.
Future<PublishRouteResponse> publishRoute({required String routeId});
```

**HttpRouteRepository implementation of `uploadImageToGateway`:**
- Reuse the same byte-reading logic from `uploadImageToStorage` (imageBytes-first, then imageFile fallback)
- PUT to `uploadUrl` with `Content-Type` and `Content-Length` headers
- Accept **202** as the success status code (change from current 200/204 check)
- Parse response body as JSON and return `GatewayUploadResponse`
- On non-202: throw `RouteException` with status code

**HttpRouteRepository implementation of `publishRoute`:**
- POST to `_apiConfig.publishRouteUrl(routeId)` with auth headers and empty JSON body `{}`
- Accept 200 as success
- Parse and return `PublishRouteResponse`
- On non-200: throw `RouteException`

**Removal of `confirmRouteWaypoints`:**
Delete `confirmRouteWaypoints` from the abstract interface and its implementation in `HttpRouteRepository`. Also delete `ConfirmRouteResponse` DTO if it exists. The confirm-waypoints endpoint has been removed from follow-api; there is nothing to call.

**Files Affected:**
- `lib/data/repositories/route_repository.dart`

**Dependencies:** Task 2

**Acceptance Criteria:**
- `RouteRepository` abstract class declares `uploadImageToGateway` and `publishRoute`
- `HttpRouteRepository` implements both methods
- `GatewayUploadResponse` and `PublishRouteResponse` DTOs exist with working `fromJson` factories
- `confirmRouteWaypoints` and `ConfirmRouteResponse` do not exist anywhere in the file
- `dart analyze` reports no errors

---

### Task 6 — Update RouteCreationViewModel: replace confirm flow with SSE + publish

**Story Points:** 8

**Description:**
This is the core behavioral change. `uploadRouteToServer()` in `RouteCreationViewModel` currently calls:
1. `prepareRoute()`
2. `createRouteWithWaypoints()`
3. `_uploadImagesWithProgress()` (calls `uploadImageToStorage()`)

It must be updated to:
1. `prepareRoute()` — unchanged
2. `createRouteWithWaypoints()` — unchanged
3. `_uploadImagesToGateway()` — new private method, uses `uploadImageToGateway()`
4. `_waitForProcessingComplete()` — new private method, subscribes to SSE stream
5. `publishRoute()` — new repository call

**New ViewModel state fields to add:**

```dart
// Processing state (server-side, after images are uploaded to gateway)
int _processingCompleted = 0;
int _processingFailed = 0;
bool _isProcessing = false; // true while SSE stream is open

// SSE subscription lifecycle
StreamSubscription<RouteStatusEvent>? _statusStreamSubscription;
```

**New getters to expose:**

```dart
/// True while waiting for server-side image processing via SSE.
bool get isProcessing => _isProcessing;

/// Number of images confirmed processed by the gateway.
int get processingCompleted => _processingCompleted;

/// Number of images that failed processing.
int get processingFailed => _processingFailed;
```

**Updated `uploadRouteToServer()` logic (simplified pseudocode):**

```
Step 1: prepareRoute() — unchanged, get routeId
Step 2: createRouteWithWaypoints() — unchanged, get presignedUrls

Step 3: _uploadImagesToGateway()
  - For each waypoint with a presignedUrl:
    - call uploadImageToGateway(uploadUrl: url, ...)
    - accept GatewayUploadResponse (202 Accepted)
    - update _uploadProgress based on count
    - update waypoint.uploadProgress in WaypointManager

Step 4: _waitForProcessingComplete(routeId)
  - Set _isProcessing = true, _processingCompleted = 0
  - Subscribe to RouteStatusStreamService.streamRouteStatus(routeId)
  - On 'ready' event: _processingCompleted++, notifyListeners()
  - On 'failed' event: _processingFailed++, notifyListeners()
  - On 'heartbeat': no-op
  - On 'complete' event: close subscription, set _isProcessing = false
  - On error: set _uploadError, set _isProcessing = false, rethrow to caller
  - Timeout: after 5 minutes with no 'complete', close and throw RouteException

Step 5: Check _processingFailed > 0
  - If ALL waypoints failed: throw RouteException (abort)
  - If SOME failed: log warning, continue to publish (partial routes allowed)

Step 6: publishRoute(routeId)
  - Set _isRouteConfirmed = true after success

Step 7-onwards: cache route, transition images, delete draft
  - Unchanged from current code
```

**Dispose method update:**
Cancel `_statusStreamSubscription` in `dispose()` to prevent memory leaks and dangling connections.

**SSE timeout implementation:**
Use `Stream.timeout(Duration(minutes: 5))` on the status stream before subscribing. The timeout duration should be a named constant `static const Duration _sseTimeout = Duration(minutes: 5)`.

**Error handling for partial failures:**
If `_processingFailed > 0` but `_processingCompleted > 0`, set `_uploadError` to a non-null warning string (localized key) but do NOT return false — proceed to publish. Only abort if `_processingCompleted == 0` (total failure).

**Inject `RouteStatusStreamService` via constructor:**
Add an optional `RouteStatusStreamService? routeStatusStreamService` parameter to the constructor. If null, create `HttpRouteStatusStreamService()`. This pattern matches how `RouteRepository` is injected and enables testability without a mock framework.

**Files Affected:**
- `lib/ui/route_creation/route_creation_view_model.dart`

**Dependencies:** Tasks 3, 5

**Acceptance Criteria:**
- `uploadRouteToServer()` calls `uploadImageToGateway()`, then awaits SSE `complete`, then calls `publishRoute()`
- `confirmRouteWaypoints()` is not called anywhere in the ViewModel
- `_statusStreamSubscription?.cancel()` is called in `dispose()`
- `isProcessing` getter returns true while SSE stream is open
- Draft save/load behavior is unchanged
- Offline/connectivity check behavior is unchanged
- `dart analyze` reports no errors

---

### Task 7 — Update upload UI: distinguish upload phase from processing phase

**Story Points:** 3

**Description:**
The current UI shows a single phase: "uploading." With the gateway flow there are now two distinct server-side phases that need different UI treatment:

- **Upload phase** (Steps 1–3): Sending bytes to the gateway. Progress is 0.0–0.5 (first half of the progress bar).
- **Processing phase** (Step 4): Gateway is processing images. Progress moves from 0.5–1.0 as SSE `ready` events arrive.

This approach maps naturally onto the existing `_uploadProgress` float that drives `UploadProgressIndicator`. No new progress field is needed in the ViewModel — the processing progress can be derived from `processingCompleted / waypointCount` and then blended with the upload half.

Alternative simpler approach (recommended): Keep upload progress as-is (0.0–1.0) for the upload phase, then replace it with a separate processing progress (0.0–1.0) during the processing phase. The ViewModel exposes `isProcessing` already. The UI can branch on `isProcessing` to show different content.

**Changes to `UploadBanner`:**
Add optional `isProcessing` parameter (defaults to `false`). When `isProcessing == true`:
- Change the button label to the new l10n key `routeCreationProcessingImages` (e.g., "Processing images...")
- Disable the upload button (cannot re-trigger during processing)
- Show a different hint text from new l10n key `routeCreationProcessingHint` (e.g., "Checking images on server...")

**Changes to `UploadProgressIndicator`:**
Add optional `isProcessing` parameter. When `isProcessing == true`:
- Change the title from "Uploading" to `routeCreationProcessingImages`
- The `uploadedCount` label changes to `processingCompleted` (same widget, different semantics; the count variable is already passed as a parameter)

**Changes to `RouteCreationScreen`:**
Pass `isProcessing: viewModel.isProcessing` and `processingCompleted: viewModel.processingCompleted` to both banner and indicator widgets. The screen already uses `Consumer<RouteCreationViewModel>` so no structural change is needed.

**Files Affected:**
- `lib/ui/route_creation/widgets/upload_banner.dart`
- `lib/ui/route_creation/widgets/upload_progress_indicator.dart`
- `lib/ui/route_creation/route_creation_screen.dart` (pass new parameters)

**Dependencies:** Task 6

**Acceptance Criteria:**
- During the upload phase, the UI shows the existing upload progress behavior
- During the processing phase (`isProcessing == true`), the upload button is disabled and the label changes
- RTL layout is preserved (no new directional EdgeInsets introduced)
- `dart analyze` reports no errors

---

### Task 8 — Add l10n strings for the new async flow

**Story Points:** 2

**Description:**
All user-facing strings introduced by Tasks 6 and 7 must be localized in English and Hebrew. No UI string may be hardcoded in Dart source.

**New strings required in `app_en.arb`:**

| Key | English value | Usage |
|---|---|---|
| `routeCreationProcessingImages` | "Processing images..." | Button label and progress title while SSE is open |
| `routeCreationProcessingHint` | "Checking images on server. This takes a few seconds." | Hint text below processing button |
| `routeCreationPublishingRoute` | "Publishing route..." | Loading state label during publishRoute call |
| `routeCreationPublishSuccess` | "Route published successfully!" | Replaces old `routeCreationUploadSuccess` in success path |
| `routeCreationSomeImagesFailed` | "Some images could not be processed. Route published with {count} image(s)." | Warning snackbar when partial failure occurs |
| `routeCreationAllImagesFailed` | "All images failed to process. Please try again." | Error message when every image fails |
| `routeCreationProcessingTimeout` | "Server took too long to process images. Please try again." | SSE timeout error |
| `routeCreationProcessingFailed` | "Image processing failed. Please try again." | Generic SSE stream error |
| `routeCreationProcessedCount` | "{processedCount} of {totalCount} processed" | Progress label during processing phase |

Each entry requires both `@key` metadata blocks with `description` and `note` fields. Parameterized strings use `{placeholderName}` with `@placeholders` metadata.

**Hebrew translations in `app_he.arb`:**
Follow all 20 rules from `ai-docs/infrastructure/hebrew-translation-guidelines.md`. Key rules applicable here:
- Use masculine singular for technical actions unless context demands plural
- Present tense for ongoing states ("מעבד תמונות..." not "מעבד תמונות")
- No nikud
- Brand name "Follow" is never translated

**After adding strings:**
Run `flutter gen-l10n` to regenerate `lib/l10n/app_localizations_en.dart` and `lib/l10n/app_localizations_he.dart`.

**Files Affected:**
- `lib/l10n/app_en.arb`
- `lib/l10n/app_he.arb`

**Dependencies:** None (can be done in parallel with Tasks 6–7)

**Acceptance Criteria:**
- All 9 keys exist in both `app_en.arb` and `app_he.arb` with valid ARB structure
- `flutter gen-l10n` runs without errors
- `dart analyze` reports no errors on the generated localizations files
- Hebrew translations reviewed against translation guidelines

---

### Task 9 — Unit tests for RouteCreationViewModel gateway flow

**Story Points:** 5

**Description:**
Write unit tests that cover the new async upload + SSE wait + publish flow in `RouteCreationViewModel`. Use classical/Detroit style: hand-written fakes only, no `mockito` (which is listed as a dev dependency but the project's testing philosophy is classical fakes, not mock frameworks).

**Fakes needed:**

```dart
/// Fake RouteRepository that records calls and returns configurable responses.
class FakeRouteRepository implements RouteRepository {
  // Configurable responses
  PrepareRouteResponse? prepareResponse;
  CreateRouteResponse? createResponse;
  GatewayUploadResponse? gatewayUploadResponse;
  PublishRouteResponse? publishResponse;

  // Call recorders
  List<String> calledMethods = [];

  @override
  Future<GatewayUploadResponse> uploadImageToGateway({...}) async {
    calledMethods.add('uploadImageToGateway');
    return gatewayUploadResponse!;
  }

  @override
  Future<PublishRouteResponse> publishRoute({required String routeId}) async {
    calledMethods.add('publishRoute');
    return publishResponse!;
  }

  // ... other methods with UnimplementedError or configurable stubs
}

/// Fake RouteStatusStreamService that emits pre-configured events.
class FakeRouteStatusStreamService implements RouteStatusStreamService {
  FakeRouteStatusStreamService({required this.events});
  final List<RouteStatusEvent> events;

  @override
  Stream<RouteStatusEvent> streamRouteStatus({
    required String routeId,
    required String authToken,
  }) => Stream.fromIterable(events);
}
```

**Test scenarios:**

1. **Happy path — all images succeed:** Fake emits `ready` (x N) then `complete`. ViewModel ends with `_isRouteConfirmed == true`, `publishRoute` was called, `confirmRouteWaypoints` was NOT called.

2. **Partial failure — some images fail:** Fake emits some `failed` and some `ready` then `complete`. ViewModel calls `publishRoute`, sets warning `_uploadError`, but still returns `true`.

3. **Total failure — all images fail:** Fake emits N `failed` then `complete`. ViewModel does NOT call `publishRoute`, returns `false`, `_uploadError` is non-null.

4. **SSE stream error (network drop):** Fake emits an error event. ViewModel catches it, sets `_uploadError`, returns `false`, `_isProcessing == false` after.

5. **Upload to gateway returns non-202:** `FakeRouteRepository.uploadImageToGateway` throws `RouteException`. ViewModel returns `false` without reaching SSE step.

6. **Publish fails after SSE complete:** `FakeRouteRepository.publishRoute` throws `RouteException`. ViewModel returns `false`, `_uploadError` is non-null.

7. **Offline check still blocks upload:** Set `isOffline = true` on `ConnectivityViewModel` fake. `uploadRouteToServer()` returns `false` before any repo calls.

8. **`dispose()` during processing:** Start the SSE subscription then call `dispose()`. Verify no `StateError` (ChangeNotifier use after dispose) is thrown.

**Files Affected:**
- `test/ui/route_creation/route_creation_view_model_gateway_test.dart` (new file)

**Dependencies:** Tasks 5, 6

**Acceptance Criteria:**
- All 8 test scenarios pass
- `flutter test test/ui/route_creation/route_creation_view_model_gateway_test.dart` exits 0
- No `mockito` imports in the test file
- `dart analyze` reports no errors

---

### Task 10 — Manual QA and integration smoke test

**Story Points:** 2

**Description:**
End-to-end manual verification of the complete Phase 1 flow against a running local stack (`docker-compose.yml` in the coordination repo, with follow-api + follow-image-gateway + Valkey + MinIO + PostgreSQL active).

**Prerequisites:**
- All Tasks 1–9 complete and passing quality gates
- `docker-compose up -d` in `/home/yoseforb/pkg/follow/`
- follow-image-gateway Valkey integration (Phase 5 of the gateway plan) is deployed

**Manual test script:**

1. **Happy path (2 waypoints):**
   - Launch app, authenticate as anonymous user
   - Create a new route with name "QA Test Route"
   - Add waypoint 1: capture photo, place marker
   - Add waypoint 2: capture photo, place marker
   - Tap "Upload Route"
   - Verify: upload phase shows progress (0 → 2 uploaded)
   - Verify: processing phase UI appears with "Processing images..." label
   - Verify: each `ready` SSE event increments the processed count
   - Verify: after `complete` SSE, the app calls publish and shows "Route published successfully!"
   - Navigate to My Routes and verify the route appears without "Draft" badge
   - Navigate the route and verify images load

2. **Offline path (unchanged behavior):**
   - Disable network on device
   - Attempt to upload a draft route
   - Verify: "You're offline. Route saved as draft" message appears
   - Re-enable network
   - Verify: draft persists in list

3. **SSE timeout simulation:**
   - Block the SSE endpoint (e.g., via proxy or by stopping follow-api mid-stream)
   - Verify: after 5 minutes, or after simulated disconnect, `routeCreationProcessingTimeout` error message appears

4. **Single image failure:**
   - Simulate gateway returning `failed` for one image (can be done by uploading a deliberately corrupt image if gateway has format validation)
   - Verify: partial failure warning appears but route is still published if at least one image succeeded

**Quality gates to run before marking complete:**
```bash
cd /home/yoseforb/pkg/follow/follow-app
dart format .
dart analyze   # Must show "No errors"
dart fix --apply
flutter test --coverage
```

**Files Affected:** None (manual test only)

**Dependencies:** Tasks 1–9 all complete

**Acceptance Criteria:**
- Happy path completes successfully end-to-end
- Route appears in My Routes after publish
- Offline path behaves identically to pre-migration behavior
- All quality gates pass with zero lint errors

---

## Task Dependency Graph

```
Task 1 (RouteStatus enum)
    |
    └──→ Task 6 (ViewModel)

Task 2 (ApiConfig URLs)
    |
    ├──→ Task 3 (SSE service)
    |       |
    |       ├──→ Task 4 (SSE tests)
    |       └──→ Task 6 (ViewModel)
    |
    └──→ Task 5 (Repository methods)
            |
            ├──→ Task 6 (ViewModel)
            └──→ Task 9 (ViewModel tests)

Task 6 (ViewModel)
    |
    ├──→ Task 7 (UI updates)
    └──→ Task 9 (ViewModel tests)

Task 8 (l10n strings) — independent, can run in parallel with Tasks 3–7

Tasks 1–9 all complete
    |
    └──→ Task 10 (QA)
```

**Recommended implementation sequence:**
1. Tasks 1, 2, 8 in parallel (foundations, no dependencies between them)
2. Tasks 3, 5 in parallel (both depend only on Task 2)
3. Task 4 (depends on Task 3)
4. Task 6 (depends on Tasks 1, 3, 5)
5. Tasks 7, 9 in parallel (both depend on Task 6)
6. Task 10 (depends on all)

---

## Story Point Summary

| Task | Description | Story Points |
|---|---|---|
| 1 | Extend RouteStatus enum | 1 |
| 2 | Add API endpoint URLs | 1 |
| 3 | Create RouteStatusStreamService | 5 |
| 4 | Unit tests for SSE service | 3 |
| 5 | Gateway upload + publish in RouteRepository | 3 |
| 6 | Update RouteCreationViewModel | 8 |
| 7 | Update upload UI for processing phase | 3 |
| 8 | Add l10n strings | 2 |
| 9 | Unit tests for ViewModel gateway flow | 5 |
| 10 | Manual QA | 2 |
| **Total** | | **33** |

---

## Constraints and Non-Goals

### In-Scope (Phase 1)
- New route creation: prepare → create-waypoints → gateway upload → SSE → publish
- RouteStatus enum extension
- SSE stream parsing service
- ViewModel orchestration of the async flow
- UI phases: upload vs. processing
- l10n strings for all new states (English + Hebrew)
- Unit tests for the new service and ViewModel flows

### Out-of-Scope (Phase 2, planned separately)
- Image replacement (replace-image/prepare and replace-image/confirm endpoints)
- Waypoint update flow
- Route update flow
- Removal of `confirmRouteWaypoints` dead code
- Removal of the `@Deprecated` annotations added in this phase
- SSE reconnection with exponential backoff (treat initial implementation as single-connection; if stream drops, it is an error)

### Technical Constraints
- No new pub.dev packages. SSE parsing is implemented manually using the existing `http` package's streaming support.
- No mock frameworks in tests. Hand-written fakes only.
- All user-facing strings must be localized (English + Hebrew with RTL support).
- Use `context.go()` / `context.push()` for navigation. NEVER `Navigator.pop()`.
- RTL-compatible layout widgets: `EdgeInsetsDirectional`, `PositionedDirectional`, `AlignmentDirectional`.
- `dart analyze` must show zero errors before any commit.

---

## Definition of Done

The Phase 1 implementation is complete when ALL of the following are true:

- [ ] `RouteStatus.ready` and `RouteStatus.published` exist and parse correctly
- [ ] `ApiConfig` has `publishRouteUrl()` and `routeStatusStreamUrl()`
- [ ] `RouteStatusStreamService` parses SSE events from a chunked HTTP stream
- [ ] `RouteRepository` has `uploadImageToGateway()` and `publishRoute()` on both abstract interface and `HttpRouteRepository`
- [ ] `RouteCreationViewModel.uploadRouteToServer()` no longer calls `confirmRouteWaypoints()` in the new path
- [ ] `RouteCreationViewModel` disposes SSE subscription in `dispose()`
- [ ] Upload phase UI and processing phase UI are visually distinct
- [ ] All new user-facing strings exist in both `app_en.arb` and `app_he.arb`
- [ ] `flutter gen-l10n` succeeds
- [ ] `dart analyze` reports "No errors"
- [ ] `flutter test --coverage` passes for all new and existing tests
- [ ] Manual QA happy path succeeds against live local stack
- [ ] Draft save/load is unaffected (existing draft tests still pass)
- [ ] Offline path behavior is unaffected
