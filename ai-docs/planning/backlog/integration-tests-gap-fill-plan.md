# Integration Tests Gap-Fill Implementation Plan

## Overview

The Follow platform's Go-based integration test suite in `tests/integration/` covers the async
gateway flow end-to-end, but numerous API scenarios present in the original shell script
`follow-api/scripts/test-route-workflow.sh` have no corresponding Go test. This plan captures
every missing scenario, organises the work into four coherent phases, and provides enough
detail for an implementation agent to write each test file without further research.

### Business Value

Closing the gap eliminates blind spots in the test suite. Routes are the platform's core product;
untested listing filters, update error paths, and image-replacement boundary cases are real
regression risks as the codebase evolves toward full gateway integration and mobile client launch.

### Architecture Context

All tests in this suite exercise the **async gateway flow**:

1. `POST /api/v1/routes/{id}/create-waypoints` returns Ed25519-JWT gateway upload URLs.
2. Client `PUT`s raw bytes to `follow-image-gateway` (returns 202 Accepted).
3. Gateway processes image, publishes `done`/`failed` to `image:result` Valkey stream.
4. API consumer reads stream, transitions route to `ready`.
5. SSE endpoint streams status events to the client.

The old shell script's `confirm-waypoints` endpoint **no longer exists**. Tests that previously
verified synchronous confirmation must be adapted: any scenario that needs a fully-processed
route should set it up via the async path (upload + wait for SSE `complete` or poll Valkey
for `stage=done`) exactly as `TestFullAPIBehavioralFlow` does.

The `replace-image/confirm` endpoint from the old script **also no longer exists**. Image
replacement is now fully asynchronous: `replace-image/prepare` returns an upload URL, the
client uploads to the gateway, and the Valkey consumer atomically swaps the image. Marker
coordinates are submitted at `prepare` time (not confirm time). Tests for the replace-image
flow must use the prepare-then-upload pattern demonstrated in Step 13 of
`behavioral_flow_test.go`.

### Key Existing Helpers (do not recreate)

All helpers are defined in `tests/integration/helpers_test.go`:

| Helper | Signature | Purpose |
|--------|-----------|---------|
| `createAnonymousUser` | `(t) -> (userID, token)` | POST /users/anonymous |
| `prepareRoute` | `(t, token) -> routeID` | POST /routes/prepare |
| `createRouteWithWaypoints` | `(t, token, routeID, []waypointImageSpec) -> CreateWaypointsResponse` | POST .../create-waypoints |
| `deleteRoute` | `(t, routeID, token)` | DELETE /routes/{id}, best-effort |
| `uploadToGateway` | `(t, url, []byte) -> *http.Response` | PUT to gateway upload URL |
| `loadTestImage` | `(t, filename) -> []byte` | Read from testdata/ |
| `doRequest` | `(t, method, url, body, token) -> *http.Response` | Generic JSON HTTP helper |
| `decodeJSON` | `(t, resp) -> map[string]any` | Decode response body |
| `newValkeyClient` | `(t) -> valkeygo.Client` | Valkey client with cleanup |
| `waitForImageStatus` | `(t, client, imageID, stage, timeout)` | Poll image:status hash |
| `hGetAll` | `(t, client, key) -> map[string]string` | HGETALL |
| `keyExists` | `(t, client, key) -> bool` | EXISTS |
| `readSSEEvents` | `(ctx, reader, chan<- SSEEvent)` | Parse SSE stream |
| `invalidImageBytes` | `() -> []byte` | Non-image test bytes |
| `defaultTestImages` | `[]waypointImageSpec` | Two-waypoint standard spec |

Existing types: `CreateWaypointsResponse`, `ReplaceImagePrepareResponse`, `SSEEvent`,
`streamMessage`, `markerCoords`, `waypointImageSpec`.

Package-level vars: `apiURL`, `gatewayURL`, `valkeyAddress` (set by `TestMain`).

### Test File Placement Policy

| Scenario group | File | Reason |
|----------------|------|--------|
| Route listing filters | `route_listing_test.go` (new) | Self-contained filter variations |
| Route update errors | `route_update_errors_test.go` (new) | Error path group |
| Waypoint update errors | `waypoint_update_errors_test.go` (new) | Error path group |
| Replace-image validation | `replace_image_validation_test.go` (new) | 25-test boundary group |
| Image integrity | `image_integrity_test.go` (new) | Download + checksum group |
| Post-deletion verification | Extend `behavioral_flow_test.go` | Natural end of existing lifecycle |

---

## Phase 1: Route Listing Filter Coverage

**Priority:** HIGH
**Story Points:** 5
**New file:** `/home/yoseforb/pkg/follow/tests/integration/route_listing_test.go`

### Setup Pattern

Every test in this file follows this preamble:

```
createAnonymousUser → prepareRoute → createRouteWithWaypoints
→ upload all images via gateway → waitForImageStatus(done)
→ POST /publish (to reach "published" status for navigable tests)
→ t.Cleanup(deleteRoute)
```

Not all tests require a published route. Tests that verify `route_status=pending` only
need the create step; tests that verify `navigable_only=true` need a published route.
A shared helper `buildPublishedRoute(t) (routeID, token string)` should be added to
`helpers_test.go` so these tests do not duplicate the SSE-wait logic.

### New Helper Required: `buildPublishedRoute`

Add to `helpers_test.go`:

```go
// buildPublishedRoute creates a route with two uploaded images, waits for
// gateway processing, and publishes the route. Returns routeID and authToken.
// Registers t.Cleanup to delete the route.
func buildPublishedRoute(t *testing.T) (routeID, authToken string)
```

Implementation mirrors Steps 1-8 of `TestFullAPIBehavioralFlow` but compressed into a
helper. It must:
1. `createAnonymousUser`
2. `prepareRoute`
3. `createRouteWithWaypoints` with `defaultTestImages`
4. Upload each image with `uploadToGateway`
5. Open SSE to `GET /api/v1/routes/{id}/status/stream` with a 60-second timeout
6. Wait for `complete` event
7. `POST /publish`
8. Register `t.Cleanup(func() { deleteRoute(t, routeID, authToken) })`
9. Return `routeID`, `authToken`

### Task 1.1 — Default Query Behavior (No Parameters)

**Test function:** `TestRouteListing_DefaultBehaviorUserScoped`

**Description:** `GET /api/v1/routes` with no query parameters must return an HTTP 200
response with `routes` array and `pagination` object. The response must contain only routes
owned by the authenticated user (verified by checking that each returned `route_id` belongs
to the test user, which can be inferred by checking the route we just created appears).

**Test steps:**
1. Create a route (pending state — no need to publish).
2. `GET /api/v1/routes` with `Authorization: Bearer {token}`, no other params.
3. `require.Equal(200, resp.StatusCode)`
4. `decodeJSON` → verify `routes` array present, `pagination` object present.
5. Verify the created route appears in results.
6. `t.Cleanup(deleteRoute)`

**Files affected:** `route_listing_test.go` (new), `helpers_test.go` (new helper)

**Acceptance criteria:**
- Response is 200.
- Body contains `routes` (array) and `pagination` (object).
- Created route appears in the list.

---

### Task 1.2 — Filter by `visibility=public&access_method=open` (Discovery Mode)

**Test function:** `TestRouteListing_PublicOpenDiscovery`

**Description:** `GET /api/v1/routes?visibility=public&access_method=open` triggers discovery
mode (per `ListRoutesUseCase`: DiscoveryMode=true requires visibility=public). The response
must return HTTP 200, contain the expected structure, and every returned route must have
`visibility=public` and `access_method=open`.

Note from the use case: discovery mode uses `ExcludeOwnerID` — the authenticated user's own
routes are excluded. Create a **second user** and a public route under that user, then verify
User 1 can see User 2's route in discovery results.

**Test steps:**
1. Create user2 and a public+open route under user2 (must be published).
2. Create user1 (the test's authenticated user).
3. `GET /api/v1/routes?visibility=public&access_method=open` as user1.
4. `require.Equal(200, resp.StatusCode)`
5. Verify response has `routes` array and `pagination` object.
6. Verify at least one route in the results has `visibility=public`.
7. (Optional) Verify user2's route appears.
8. `t.Cleanup` deletes both routes.

**Files affected:** `route_listing_test.go`

**Acceptance criteria:**
- 200 response with correct structure.
- All returned routes have `visibility=public`.

---

### Task 1.3 — Filter by `navigable_only=true`

**Test function:** `TestRouteListing_NavigableOnly`

**Description:** `GET /api/v1/routes?navigable_only=true` must return only routes that are
navigable (published, not expired). A pending route created in the same test must NOT appear.

**Test steps:**
1. Use `buildPublishedRoute` to create one navigable route.
2. Create a second route but leave it in `pending` state (do not upload images).
3. `GET /api/v1/routes?navigable_only=true` as the same user.
4. Verify the published route appears; verify the pending route does NOT appear.
5. `t.Cleanup` deletes both routes.

**Files affected:** `route_listing_test.go`

**Acceptance criteria:**
- Published route present in results.
- Pending route absent from results.
- Every route in results has a navigable status.

---

### Task 1.4 — Filter by `route_status=pending`

**Test function:** `TestRouteListing_FilterByStatusPending`

**Description:** `GET /api/v1/routes?route_status=pending` must return HTTP 200 and include
the user's pending route. A published route must not appear in the results.

**Test steps:**
1. Create a route and leave it pending (do not upload images).
2. Use `buildPublishedRoute` for a second route.
3. `GET /api/v1/routes?route_status=pending`.
4. Verify the pending route appears.
5. Verify the published route does NOT appear.
6. All returned routes should have `route_status=pending`.
7. `t.Cleanup` deletes both routes.

**Files affected:** `route_listing_test.go`

**Acceptance criteria:**
- Pending route found in results.
- Published route not found.
- All results have `route_status=pending`.

---

### Task 1.5 — User-Scoped Public Routes (`visibility=public` only)

**Test function:** `TestRouteListing_UserScopedPublicVisibility`

**Description:** `GET /api/v1/routes?visibility=public` with no `access_method` filter must
return the user's own public routes (my-routes mode, visibility filter applied). The API
routes this through the non-discovery path (`UserID` filter, not `ExcludeOwnerID`).

**Test steps:**
1. Create a public route under user1 (route uses `visibility=public`).
2. Create a private route under user1 (`visibility=private`).
3. `GET /api/v1/routes?visibility=public`.
4. Verify the public route appears; the private route must not appear.
5. `t.Cleanup` deletes both routes.

Note: The `createRouteWithWaypoints` helper currently hardcodes `"visibility": "private"`.
This test needs a variant that overrides `visibility=public`. Either call `doRequest` directly
(building the payload manually) or add a `buildWaypointPayload` helper that accepts route-level
options. The simplest approach is to call `doRequest` directly for the create-waypoints step
in this test.

**Files affected:** `route_listing_test.go`

**Acceptance criteria:**
- Public route found.
- Private route not found.

---

### Task 1.6 — Pagination Parameters

**Test function:** `TestRouteListing_Pagination`

**Description:** `GET /api/v1/routes?page=1&page_size=1` must return exactly one route per
page and correctly populate the `pagination` object. A second request with `page=2` should
return the second route (or an empty list if only one exists).

**Confirmed:** The API uses `page` (default 1, min 1) and `page_size` (default 20, min 1,
max 100) query parameters. Offset is computed internally as `max((page-1) * page_size, 0)`.

**Test steps:**
1. Create two routes for the same user.
2. `GET /api/v1/routes?page=1&page_size=1`.
3. Verify exactly 1 route returned.
4. Verify `pagination` object is present and accurate.
5. `GET /api/v1/routes?page=2&page_size=1`.
6. Verify the second route is returned.
7. `t.Cleanup` deletes both routes.

**Files affected:** `route_listing_test.go`

**Acceptance criteria:**
- First page returns exactly 1 route.
- Second page returns the other route.
- `pagination` object reflects correct counts.

---

## Phase 2: Route and Waypoint Update Error Cases

**Priority:** HIGH
**Story Points:** 3
**New files:**
- `/home/yoseforb/pkg/follow/tests/integration/route_update_errors_test.go`
- `/home/yoseforb/pkg/follow/tests/integration/waypoint_update_errors_test.go`

### Setup Pattern

Both files need a live route with waypoints. Create one route per `TestXxx` function using
the standard helpers. All these tests only need a `pending` route (no publishing required).

```go
_, token := createAnonymousUser(t)
routeID := prepareRoute(t, token)
createResp := createRouteWithWaypoints(t, token, routeID, defaultTestImages)
t.Cleanup(func() { deleteRoute(t, routeID, token) })
waypointID := createResp.WaypointIDs[0]
```

### Task 2.1 — Route Update: Empty Payload Returns 400

**Test function:** `TestRouteUpdate_EmptyPayloadReturns400`

**Description:** `PUT /api/v1/routes/{routeID}` with an empty JSON body `{}` must return
HTTP 400. The route update use case rejects updates with no fields set.

**Test steps:**
1. Set up a pending route.
2. `PUT /api/v1/routes/{routeID}` with body `{}` and valid auth token.
3. `require.Equal(400, resp.StatusCode)`.
4. Decode body and assert some error indication is present.

**Files affected:** `route_update_errors_test.go`

**Acceptance criteria:**
- HTTP 400 returned.
- Response body is a valid JSON object.

---

### Task 2.2 — Route Update: Non-Existent Route Returns 404

**Test function:** `TestRouteUpdate_NonExistentRouteReturns404`

**Description:** `PUT /api/v1/routes/f47ac10b-58cc-4372-a567-0e02b2c3d479` with a valid auth
token and a non-empty payload must return HTTP 404.

**Test steps:**
1. Create a user (token needed for auth, no route needed).
2. `PUT /api/v1/routes/f47ac10b-58cc-4372-a567-0e02b2c3d479` with body
   `{"name": "Updated"}` and valid token.
3. `require.Equal(404, resp.StatusCode)`.

**Files affected:** `route_update_errors_test.go`

**Acceptance criteria:**
- HTTP 404 returned.

---

### Task 2.3 — Route Update: No Authentication Returns 401

**Test function:** `TestRouteUpdate_NoAuthReturns401`

**Description:** `PUT /api/v1/routes/{routeID}` without an Authorization header must return
HTTP 401.

**Test steps:**
1. Set up a pending route.
2. `PUT /api/v1/routes/{routeID}` with body `{"name": "Updated"}` and **no** auth token
   (pass `""` as authToken to `doRequest`).
3. `require.Equal(401, resp.StatusCode)`.

**Files affected:** `route_update_errors_test.go`

**Acceptance criteria:**
- HTTP 401 returned.

---

### Task 2.4 — Waypoint Update: Empty Payload Returns 400

**Test function:** `TestWaypointUpdate_EmptyPayloadReturns400`

**Description:** `PUT /api/v1/routes/{routeID}/waypoints/{waypointID}` with body `{}` must
return HTTP 400.

**Test steps:**
1. Set up a pending route, extract `waypointIDs[0]`.
2. `PUT` to waypoint URL with body `{}` and valid token.
3. `require.Equal(400, resp.StatusCode)`.

**Files affected:** `waypoint_update_errors_test.go`

**Acceptance criteria:**
- HTTP 400 returned.

---

### Task 2.5 — Waypoint Update: Non-Existent Waypoint Returns 404

**Test function:** `TestWaypointUpdate_NonExistentWaypointReturns404`

**Description:** `PUT /api/v1/routes/{routeID}/waypoints/f47ac10b-58cc-4372-a567-0e02b2c3d479`
with a valid token and non-empty payload must return HTTP 404.

**Test steps:**
1. Set up a pending route.
2. `PUT /api/v1/routes/{routeID}/waypoints/f47ac10b-58cc-4372-a567-0e02b2c3d479` with body
   `{"description": "Test"}` and valid token.
3. `require.Equal(404, resp.StatusCode)`.

**Files affected:** `waypoint_update_errors_test.go`

**Acceptance criteria:**
- HTTP 404 returned.

---

### Task 2.6 — Waypoint Update: No Authentication Returns 401

**Test function:** `TestWaypointUpdate_NoAuthReturns401`

**Description:** `PUT .../waypoints/{waypointID}` with no Authorization header must return
HTTP 401.

**Test steps:**
1. Set up a pending route, extract `waypointIDs[0]`.
2. `PUT` to waypoint URL with body `{"description": "Test"}` and **no** auth token.
3. `require.Equal(401, resp.StatusCode)`.

**Files affected:** `waypoint_update_errors_test.go`

**Acceptance criteria:**
- HTTP 401 returned.

---

### Task 2.7 — Waypoint Update: Update Marker Coordinates

**Test function:** `TestWaypointUpdate_MarkerCoordinatesOnly`

**Description:** `PUT .../waypoints/{waypointID}` with only `marker_x` and `marker_y` fields
must return HTTP 200 and report those fields in `updated_fields`.

**Test steps:**
1. Set up a pending route, extract `waypointIDs[0]`.
2. `PUT` with body `{"marker_x": 0.35, "marker_y": 0.45}`.
3. `require.Equal(200, resp.StatusCode)`.
4. Decode response; verify `updated_fields` contains `"marker_x"` and `"marker_y"`.
5. Verify `waypoint_id` in response matches.

**Files affected:** `waypoint_update_errors_test.go`

**Acceptance criteria:**
- HTTP 200 returned.
- `updated_fields` contains `marker_x` and `marker_y`.
- `waypoint_id` and `route_id` in response match expected values.

---

### Task 2.8 — Waypoint Update: Update Marker Type

**Test function:** `TestWaypointUpdate_MarkerTypeOnly`

**Description:** `PUT .../waypoints/{waypointID}` with only `marker_type` must return HTTP 200
and report `marker_type` in `updated_fields`.

**Test steps:**
1. Set up a pending route with `marker_type=next_step`, extract `waypointIDs[0]`.
2. `PUT` with body `{"marker_type": "final_destination"}`.
3. `require.Equal(200, resp.StatusCode)`.
4. Verify `updated_fields` contains `"marker_type"`.

**Files affected:** `waypoint_update_errors_test.go`

**Acceptance criteria:**
- HTTP 200 returned.
- `updated_fields` contains `marker_type`.

---

### Task 2.9 — Waypoint Update: Multiple Fields Simultaneously

**Test function:** `TestWaypointUpdate_MultipleFieldsSimultaneous`

**Description:** `PUT .../waypoints/{waypointID}` with `description`, `marker_x`, `marker_y`,
and `marker_type` all set must return HTTP 200 and report all four fields in `updated_fields`.

**Test steps:**
1. Set up a pending route, extract `waypointIDs[0]`.
2. `PUT` with body:
   ```json
   {
     "description": "All fields updated",
     "marker_x": 0.20,
     "marker_y": 0.30,
     "marker_type": "next_step"
   }
   ```
3. `require.Equal(200, resp.StatusCode)`.
4. Decode and verify all four fields appear in `updated_fields`.
5. Verify `updated_at` is non-empty.

**Files affected:** `waypoint_update_errors_test.go`

**Acceptance criteria:**
- HTTP 200 returned.
- `updated_fields` contains `description`, `marker_x`, `marker_y`, `marker_type`.

---

## Phase 3: Replace-Image Validation (25 Test Scenarios)

**Priority:** HIGH
**Story Points:** 8
**New file:** `/home/yoseforb/pkg/follow/tests/integration/replace_image_validation_test.go`

### Architecture Note for This Phase

The old shell script tested `replace-image/confirm` as a synchronous endpoint where the client
submitted a `file_hash` and `marker_x`/`marker_y`. That endpoint **no longer exists**. In the
current architecture:

- Marker coordinates are submitted at **prepare** time (`replace-image/prepare` body).
- The image swap is performed asynchronously by the Valkey consumer after gateway processing.
- There is no client-facing confirm step.

Consequently:
- Tests 13-21 from the shell script (confirm flow with marker coordinates) must be replaced
  with boundary validation tests against the `replace-image/prepare` endpoint itself, since
  that is where marker coordinates are now validated.
- Tests 22-25 (wrong hash, non-existent image_id, response fields, DB persistence) become
  async-flow tests using the pattern from Step 13 of `behavioral_flow_test.go`.

### Setup Helper: `buildRouteWithWaypoint`

Add to `helpers_test.go` (or inline in the test file):

```go
// buildRouteWithWaypoint creates a route in pending state and returns the
// routeID, first waypointID, and authToken. Does NOT upload images or wait
// for processing. Registers t.Cleanup for deletion.
func buildRouteWithWaypoint(t *testing.T) (routeID, waypointID, token string)
```

Most tests in this phase only need `replace-image/prepare` to validate server-side input
rules; they do not need a fully processed route.

### Task 3.1 — File Size Boundary Tests (Table-Driven)

**Test function:** `TestReplaceImagePrepare_FileSizeBoundaries`

**Description:** A table-driven test covering the five file-size boundary cases from the old
script. Uses `t.Run` sub-tests. All cases target the same waypoint.

**Table cases:**

| Name | `file_size_bytes` | `content_type` | Expected HTTP |
|------|-------------------|----------------|---------------|
| `zero_bytes` | 0 | `image/jpeg` | 400 |
| `below_minimum_1023` | 1023 | `image/jpeg` | 400 |
| `minimum_valid_1024` | 1024 | `image/jpeg` | 200 |
| `maximum_valid_10MB` | 10485760 | `image/jpeg` | 200 |
| `above_maximum` | 10485761 | `image/jpeg` | 400 |

**Implementation note:** The `PrepareReplaceWaypointImageUseCase` validates
`input.FileSizeBytes <= 0 || input.FileSizeBytes > 10485760`. The code shows 1 byte is the
minimum (the error string says "1 byte to 10MB"), but the old shell script treated 1023 as
invalid and 1024 as the minimum. Confirm the actual minimum by reading the usecase source.
The source at line 161 shows `input.FileSizeBytes <= 0` rejects 0 and negative values, but
does NOT enforce a 1KB minimum — only the 10MB upper bound. Adjust the table accordingly:

| Name | `file_size_bytes` | Expected HTTP |
|------|-------------------|---------------|
| `zero_bytes` | 0 | 400 |
| `one_byte_valid` | 1 | 200 |
| `typical_valid` | 512000 | 200 |
| `maximum_valid_10MB` | 10485760 | 200 |
| `above_maximum` | 10485761 | 400 |

**Files affected:** `replace_image_validation_test.go`

**Acceptance criteria:**
- Zero bytes returns 400.
- 1 byte returns 200.
- 10 MB returns 200.
- 10 MB + 1 byte returns 400.

---

### Task 3.2 — Content-Type Validation (Table-Driven)

**Test function:** `TestReplaceImagePrepare_ContentTypeValidation`

**Description:** A table-driven test for valid and invalid content types.

**Table cases:**

| Name | `content_type` | Expected HTTP |
|------|----------------|---------------|
| `jpeg_accepted` | `image/jpeg` | 200 |
| `png_accepted` | `image/png` | 200 |
| `pdf_rejected` | `application/pdf` | 400 |
| `octet_stream_rejected` | `application/octet-stream` | 400 |

The usecase validates `content_type != "image/jpeg" && content_type != "image/png"`. Note
that `image/heic` and `image/webp` are listed as valid in `ImageMetadata.ContentType`
validation (the create-waypoints endpoint), but the `PrepareReplaceWaypointImageUseCase`
only allows jpeg and png. The test should use jpeg and png as the valid cases.

**Files affected:** `replace_image_validation_test.go`

**Acceptance criteria:**
- jpeg returns 200.
- png returns 200.
- pdf returns 400.
- octet-stream returns 400.

---

### Task 3.3 — Non-Existent Route Returns 404

**Test function:** `TestReplaceImagePrepare_NonExistentRouteReturns404`

**Description:** `POST /api/v1/routes/f47ac10b-58cc-4372-a567-0e02b2c3d479/waypoints/{wp}/replace-image/prepare`
with a valid token and valid payload must return HTTP 404.

**Test steps:**
1. Create user and route (for a real waypointID).
2. Call prepare on a **fake route ID** (`f47ac10b-58cc-4372-a567-0e02b2c3d479`).
3. `require.Equal(404, resp.StatusCode)`.

**Files affected:** `replace_image_validation_test.go`

**Acceptance criteria:** HTTP 404.

---

### Task 3.4 — Non-Existent Waypoint Returns 404

**Test function:** `TestReplaceImagePrepare_NonExistentWaypointReturns404`

**Description:** `POST /api/v1/routes/{routeID}/waypoints/f47ac10b-58cc-4372-a567-0e02b2c3d479/replace-image/prepare`
with valid route ID and valid token must return HTTP 404.

**Test steps:**
1. Create user and route.
2. Call prepare with the real route ID but a **fake waypoint ID**.
3. `require.Equal(404, resp.StatusCode)`.

**Files affected:** `replace_image_validation_test.go`

**Acceptance criteria:** HTTP 404.

---

### Task 3.5 — No Authentication Returns 401

**Test function:** `TestReplaceImagePrepare_NoAuthReturns401`

**Description:** `POST .../replace-image/prepare` without Authorization header must return
HTTP 401.

**Test steps:**
1. Create user and route.
2. Call prepare with **no auth token** and a valid payload.
3. `require.Equal(401, resp.StatusCode)`.

**Files affected:** `replace_image_validation_test.go`

**Acceptance criteria:** HTTP 401.

---

### Task 3.6 — Marker Coordinate Boundary Tests at Prepare Time (Table-Driven)

**Test function:** `TestReplaceImagePrepare_MarkerCoordinateBoundaries`

**Description:** Since marker coordinates are now submitted at prepare time, boundary
validation (0.0 valid, 1.0 valid, -0.0001 invalid, 1.0001 invalid) must be tested against
the `replace-image/prepare` endpoint.

The `PrepareReplaceWaypointImageInput` struct uses:
```
MarkerX float64 `validate:"min=0,max=1"`
MarkerY float64 `validate:"min=0,max=1"`
```

**Table cases:**

| Name | `marker_x` | `marker_y` | Expected HTTP |
|------|------------|------------|---------------|
| `x_zero_valid` | 0.0 | 0.5 | 200 |
| `x_one_valid` | 1.0 | 0.5 | 200 |
| `x_negative_invalid` | -0.0001 | 0.5 | 400 |
| `x_over_one_invalid` | 1.0001 | 0.5 | 400 |
| `y_zero_valid` | 0.5 | 0.0 | 200 |
| `y_one_valid` | 0.5 | 1.0 | 200 |
| `y_negative_invalid` | 0.5 | -0.0001 | 400 |
| `y_over_one_invalid` | 0.5 | 1.0001 | 400 |

Note: `file_size_bytes` must be a valid value (e.g. 100000) in all cases so the only
varying variable is the coordinate.

**Files affected:** `replace_image_validation_test.go`

**Acceptance criteria:**
- 0.0 and 1.0 for both axes return 200.
- Values below 0 or above 1 return 400.

---

### Task 3.7 — Full Async Replace Flow with Marker Verification

**Test function:** `TestReplaceImage_AsyncFlowWithMarkerPersistence`

**Description:** End-to-end async image replacement: prepare → upload to gateway → wait for
Valkey swap → verify new image ID and marker coordinates in GET route response. This mirrors
Step 13 of `TestFullAPIBehavioralFlow` but as a standalone test.

**Test steps:**
1. Use `buildPublishedRoute` to get a live route with waypointID.
2. `POST .../replace-image/prepare` with `marker_x=0.55`, `marker_y=0.65`, valid file size.
3. Decode response to get `image_id` and `upload_url`.
4. Upload replacement image with `uploadToGateway`.
5. Expect 202 from gateway.
6. Poll `GET /api/v1/routes/{routeID}?include_images=true` every 1 second for up to 15
   seconds until `waypoints[waypointID].image_id == newImageID`.
7. Verify `marker_x ≈ 0.55` and `marker_y ≈ 0.65` (within 0.001 tolerance).
8. Verify route remains `published` after swap.

**Files affected:** `replace_image_validation_test.go`

**Acceptance criteria:**
- Gateway returns 202.
- Within 15 seconds, waypoint image_id is the new image_id.
- Marker coordinates updated to (0.55, 0.65).
- Route status remains `published`.

---

### Task 3.8 — Prepare Response Includes All Required Fields

**Test function:** `TestReplaceImagePrepare_ResponseFields`

**Description:** A successful `replace-image/prepare` response must include `image_id`,
`upload_url`, and `expires_at`. Verifies the `ReplaceImagePrepareResponse` contract.

**Test steps:**
1. Create user and route.
2. Call prepare with valid payload.
3. `require.Equal(200, resp.StatusCode)`.
4. Decode into `ReplaceImagePrepareResponse`.
5. `assert.NotEmpty(t, resp.ImageID)`.
6. `assert.NotEmpty(t, resp.UploadURL)`.
7. `assert.NotEmpty(t, resp.ExpiresAt)`.

**Files affected:** `replace_image_validation_test.go`

**Acceptance criteria:**
- All three fields non-empty.

---

## Phase 4: Image Integrity and Post-Deletion Verification

**Priority:** HIGH / MEDIUM
**Story Points:** 5
**New file:** `/home/yoseforb/pkg/follow/tests/integration/image_integrity_test.go`
**Extend:** `behavioral_flow_test.go`

### Task 4.1 — Image Download Integrity Verification

**Test function:** `TestImageIntegrity_DownloadedImageValid`
**File:** `image_integrity_test.go` (new)

**Description:** After uploading a known test image and waiting for the gateway to process
it, download the image via the presigned `navigation_image_url` and verify the downloaded
image is valid.

**Confirmed:** The gateway transforms images — it re-encodes to WebP (quality=85), strips
all metadata, resizes to max 1920px, and blurs detected faces/plates. SHA256 equality
between uploaded and downloaded bytes is **NOT possible**. The SHA256 stored in the database
and Valkey `image:result` stream is the hash of the processed WebP output.

**Implementation approach:**

```go
func TestImageIntegrity_DownloadedImageValid(t *testing.T) {
    // 1. Build published route, get presigned download URLs from
    //    GET /routes/{id}?include_images=true.
    // 2. For each waypoint's navigation_image_url:
    //    a. HTTP GET → downloadBytes.
    //    b. Verify HTTP 200.
    //    c. Verify downloaded bytes are non-empty (size > 0).
    //    d. Verify Content-Type header is "image/webp" (gateway re-encodes).
    //    e. Verify downloaded size < original JPEG size (WebP is smaller).
    // 3. Verify all waypoints have download URLs (none missing).
}
```

**Files affected:** `image_integrity_test.go` (new)

**New helper needed:**
```go
// downloadBytes performs an HTTP GET on rawURL and returns the response body.
// Calls t.Fatal on errors or non-200 response.
func downloadBytes(t *testing.T, rawURL string) []byte
```

Add `downloadBytes` to `helpers_test.go`.

**Acceptance criteria:**
- Download returns HTTP 200.
- Downloaded bytes are non-empty (size > 0).
- Content-Type is `image/webp` (confirming gateway transformation).
- Downloaded size is smaller than original JPEG (WebP compression).
- All waypoints have valid download URLs.

---

### Task 4.2 — Post-Deletion: Fetch Deleted Route Returns 404

**Test function:** `TestPostDeletion_FetchDeletedRouteReturns404`
**File:** `image_integrity_test.go` (or extend `behavioral_flow_test.go`)

**Description:** After deleting a route, `GET /api/v1/routes/{routeID}` must return HTTP 404.
The current `TestFullAPIBehavioralFlow` deletes the route at Step 14 but does NOT verify the
404. This test adds that verification as a standalone test to avoid coupling with the
14-step behavioral flow.

**Test steps:**
1. Create a user and a route (pending state, no need to publish).
2. `DELETE /api/v1/routes/{routeID}` — expect 200.
3. `GET /api/v1/routes/{routeID}` — `require.Equal(404, resp.StatusCode)`.

**Files affected:** `image_integrity_test.go`

**Acceptance criteria:**
- DELETE returns 200.
- Subsequent GET returns 404.

---

### Task 4.3 — Post-Deletion: Deleted Route Absent from Listing

**Test function:** `TestPostDeletion_RouteAbsentFromListing`
**File:** `image_integrity_test.go`

**Description:** After deleting a route, `GET /api/v1/routes` must not include the deleted
route in the `routes` array.

**Test steps:**
1. Create a user and a route.
2. Record the `routeID`.
3. `DELETE /api/v1/routes/{routeID}`.
4. `GET /api/v1/routes` with the same auth token.
5. Iterate `routes` array; assert no entry has `route_id == deletedRouteID`.

**Files affected:** `image_integrity_test.go`

**Acceptance criteria:**
- Deleted route not present in route listing response.

---

### Task 4.4 — Extend `TestFullAPIBehavioralFlow` with Post-Deletion Verification

**File:** `behavioral_flow_test.go` (existing)

**Description:** Add a Step 15 after the existing Step 14 (Delete route) to verify:
1. `GET /api/v1/routes/{routeID}` returns 404.
2. The deleted route does not appear in `GET /api/v1/routes?route_status=published`.

This makes the behavioral flow test fully self-contained as a lifecycle test.

**Implementation:**

After the existing step14 assertions, add:

```go
// ------------------------------------------------------------------ //
// Step 15: Verify route is no longer accessible after deletion        //
// ------------------------------------------------------------------ //
t.Log("Step 15: Verify route is gone after deletion")

step15Resp := doRequest(t, http.MethodGet,
    apiURL+"/api/v1/routes/"+routeID, nil, authToken)
require.Equal(t, http.StatusNotFound, step15Resp.StatusCode,
    "Step 15: deleted route must return 404")
step15Resp.Body.Close()

step15ListResp := doRequest(t, http.MethodGet,
    apiURL+"/api/v1/routes?route_status=published", nil, authToken)
require.Equal(t, http.StatusOK, step15ListResp.StatusCode,
    "Step 15: list after deletion must return 200")
step15ListBody := decodeJSON(t, step15ListResp)
step15Routes, _ := step15ListBody["routes"].([]any)
for _, rRaw := range step15Routes {
    r, ok := rRaw.(map[string]any)
    if !ok {
        continue
    }
    assert.NotEqual(t, routeID, r["route_id"],
        "Step 15: deleted route must not appear in listing")
}
```

**Files affected:** `behavioral_flow_test.go`

**Acceptance criteria:**
- GET deleted route returns 404.
- Listing does not contain the deleted route ID.

---

## New Helpers Summary

The following helpers must be added to `helpers_test.go`:

### `buildPublishedRoute`

```go
// buildPublishedRoute creates a route with two waypoints, uploads both images
// to the gateway, waits for SSE complete event, and publishes the route.
// Registers t.Cleanup to delete the route on test completion.
// Returns routeID and authToken for use in the calling test.
func buildPublishedRoute(t *testing.T) (routeID, authToken string) {
    t.Helper()
    _, token := createAnonymousUser(t)
    routeID = prepareRoute(t, token)
    route := createRouteWithWaypoints(t, token, routeID, defaultTestImages)
    t.Cleanup(func() { deleteRoute(t, routeID, token) })

    // Upload images.
    for _, entry := range route.PresignedURLs {
        img := loadTestImage(t, defaultTestImages[entry.Position].Filename)
        resp := uploadToGateway(t, entry.UploadURL, img)
        require.Equal(t, http.StatusAccepted, resp.StatusCode)
        resp.Body.Close()
    }

    // Wait for SSE complete.
    waitForRouteComplete(t, routeID, token, 60*time.Second)

    // Publish route.
    publishResp := doRequest(t, http.MethodPost,
        apiURL+"/api/v1/routes/"+routeID+"/publish", nil, token)
    require.Equal(t, http.StatusOK, publishResp.StatusCode)
    publishResp.Body.Close()

    return routeID, token
}
```

### `waitForRouteComplete`

```go
// waitForRouteComplete opens an SSE connection to the route status stream and
// blocks until a "complete" event is received or timeout elapses.
// Calls t.Fatalf if timeout elapses before "complete".
func waitForRouteComplete(
    t *testing.T,
    routeID, authToken string,
    timeout time.Duration,
) {
    t.Helper()
    // Implementation mirrors Step 7 of TestFullAPIBehavioralFlow.
}
```

### `downloadBytes`

```go
// downloadBytes performs an HTTP GET on rawURL and returns the response body.
// Calls t.Fatal if the GET fails or returns non-200.
func downloadBytes(t *testing.T, rawURL string) []byte {
    t.Helper()
    resp, err := http.Get(rawURL)
    require.NoError(t, err, "downloadBytes: GET failed")
    defer resp.Body.Close()
    require.Equal(t, http.StatusOK, resp.StatusCode,
        "downloadBytes: expected 200, got %d", resp.StatusCode)
    data, err := io.ReadAll(resp.Body)
    require.NoError(t, err, "downloadBytes: failed to read body")
    return data
}
```

---

## Implementation Order

The phases are largely independent, but the helpers in `helpers_test.go` must be added first
since Phases 1 and 3 depend on `buildPublishedRoute`.

1. **Add new helpers to `helpers_test.go`** (`buildPublishedRoute`, `waitForRouteComplete`,
   `downloadBytes`) — no tests depend on passing tests in order.
2. **Phase 2** (route/waypoint update errors) — no published route needed, fastest to
   implement.
3. **Phase 3** (replace-image validation) — depends on `buildPublishedRoute` for Task 3.7
   only; other tasks just need a pending route.
4. **Phase 1** (route listing filters) — depends on `buildPublishedRoute`.
5. **Phase 4** (integrity + post-deletion) — depends on `buildPublishedRoute` and
   `downloadBytes`.
6. **Extend `behavioral_flow_test.go`** (Task 4.4) — isolated edit to existing file.

---

## Quality Gates After Implementation

The implementation agent must run the following after each file is written:

```bash
cd /home/yoseforb/pkg/follow/tests/integration

# Format
gofumpt -w . && golines -w --max-len=80 .

# Lint (integration build tag required)
golangci-lint run --build-tags integration -c .golangci.yml ./...

# Module hygiene
go mod tidy
```

The integration tests themselves are not run as part of quality gates (they require live
services). The lint check is sufficient to confirm compile-correctness before committing.

---

## Pre-Implementation Research — RESOLVED

All open questions have been researched and resolved:

### 1. Pagination Parameters (RESOLVED)

The API uses **page-based pagination** (not offset/limit):

| Parameter | Type | Default | Min | Max | Query Name |
|-----------|------|---------|-----|-----|-----------|
| `page` | Int | `1` | `1` | — | `page` |
| `page_size` | Int | `20` | `1` | `100` | `page_size` |

Offset is computed internally: `offset = max((page-1) * page_size, 0)`.

Additional query parameters on `GET /api/v1/routes`:

| Parameter | Type | Values | Default |
|-----------|------|--------|---------|
| `discovery_mode` | Bool | `true`/`false` | `false` |
| `visibility` | String | `"public"`, `"private"` | — |
| `access_method` | String | `"open"`, `"password_protected"` | — |
| `route_status` | String | `"preparing"`, `"pending"`, `"ready"`, `"published"`, `"archived"`, `"deleted"` | `"published"` |
| `navigable_only` | Bool | `true`/`false` | `true` |
| `name` | String | any | — |
| `description` | String | any | — |

**Impact on Task 1.6:** Use `page=1&page_size=1` (confirmed correct).
**Impact on Task 1.2:** The old script's `visibility=public&access_method=open` maps to
`discovery_mode=true` in the current API.

Source: `follow-api/design/route_service.go` and `gen/http/routes/server/encode_decode.go`.

### 2. Gateway Image Transformation (RESOLVED)

**The gateway TRANSFORMS image bytes.** SHA256 equality between uploaded and downloaded
images is **NOT possible**. The pipeline applies:

1. **Decode + resize** to max 1920px (AnalyzeStage)
2. **Gaussian blur** on detected faces/license plates (TransformStage)
3. **Re-encode to WebP** with quality=85, all metadata stripped (`Keep: vips.KeepNone`)
4. **SHA256 computed on the WebP output**, not the original bytes

The stored SHA256 (in Valkey `image:result` stream and database) is the hash of the
**processed WebP bytes**, not the original upload.

**Impact on Task 4.1:** The test MUST NOT assert SHA256 equality between uploaded and
downloaded bytes. Instead verify:
- Downloaded image is non-empty (size > 0)
- Downloaded image is valid (HTTP 200)
- Downloaded content-type is `image/webp` (not original format)
- Optionally: SHA256 of downloaded bytes matches the SHA256 from the Valkey `image:result`
  stream message (both are the processed WebP hash)

Source: `follow-image-gateway/internal/pipeline/stages/transform.go` lines 203-220.

### 3. File Size Minimum (Previously Confirmed)

Minimum file size is 1 byte (not 1 KB). The `PrepareReplaceWaypointImageUseCase` validates
`input.FileSizeBytes <= 0` (rejects 0 and negative) with no 1KB floor.

Source: `follow-api/internal/domains/route/usecases/prepare_replace_waypoint_image_usecase.go`
line 161.

---

## Risk Factors

1. ~~**Gateway image transformation**~~ — **RESOLVED**: Gateway transforms images (WebP
   re-encode, blur, metadata strip). Task 4.1 updated to verify non-empty download, valid
   HTTP 200, and WebP content-type instead of SHA256 equality.

2. ~~**Pagination parameter names**~~ — **RESOLVED**: API uses `page`/`page_size` (not
   `limit`/`offset`). Task 1.6 confirmed correct.

3. **Discovery mode test isolation**: Task 1.2 requires two users. Tests running in parallel
   could interfere if they share public routes. Use unique route names and filter by routeID
   to avoid false positives from other test runs.

4. **`buildPublishedRoute` duration**: Building a published route requires gateway processing
   (10-30 seconds). Tests using this helper will be slow. Mark them with a descriptive log
   statement and ensure the SSE timeout is at least 60 seconds.

5. **`t.Parallel()` safety**: Error-case tests (Phases 2 and 3 input validation) are safe
   to parallelise because they create their own users and routes. Do NOT mark tests that
   share Valkey observer groups as parallel without unique group names per test run.

---

## File Creation Checklist

| File | Status |
|------|--------|
| `helpers_test.go` — add `buildPublishedRoute`, `waitForRouteComplete`, `downloadBytes` | Pending |
| `route_listing_test.go` | Pending |
| `route_update_errors_test.go` | Pending |
| `waypoint_update_errors_test.go` | Pending |
| `replace_image_validation_test.go` | Pending |
| `image_integrity_test.go` | Pending |
| `behavioral_flow_test.go` — extend Step 14 with Step 15 | Pending |
