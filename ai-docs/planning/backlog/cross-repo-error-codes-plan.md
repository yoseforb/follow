# Cross-Repo API Error Codes Implementation Plan

**Status**: Backlog (extended 2026-04-09 — absorbed deleted `error-api-mapper.md` plan and added HTTP shape validation)
**Priority**: High (Developer Experience + User-Facing Quality)
**Affected Repositories**: follow-api, follow-app
**Contract Location**: `ai-docs/contracts/api-error-codes-contract.md` (coordination repo)
**Estimated Story Points**: 36 (was 30; +6 for Tasks A, B, C)

---

## Overview

### Problem Statement

The Follow API currently returns error responses with two machine-readable fields:
`name` (a Goa error category like `"invalid_input"`) and `message` (an English
human-readable string). The Flutter client has no stable, granular identifier
for individual domain errors. To display localized error messages or to branch
logic on specific conditions, the client would need fragile string matching
against English server messages.

This plan adds a `code` field — a stable, granular, SCREAMING_SNAKE_CASE string
that identifies the exact domain error — to every API error response, so the
Flutter client can:

1. Look up a pre-translated localized string keyed to the code (e.g.,
   `errorRouteTooManyWaypoints`), rather than displaying the English server
   message raw.
2. Branch logic deterministically without string matching.
3. Support future analytics, retry strategies, and user guidance per error type.

### HTTP Response Shape Validation Gap (HTML-Crash Bug)

A second, related problem exists independently of the error-code contract: when
something other than `follow-api` occupies port 8080 (e.g., another process,
a load-balancer error page, or a misconfigured `API_BASE_URL`), the server
returns an HTML page. The client calls `jsonDecode()` on this HTML body and
crashes with:

```
FormatException: Unexpected character (at character 1)
<!doctype html>
^
```

Two call sites in `auth_repository.dart` (lines 205 and 314) already catch
`FormatException` and re-wrap it as `AuthException("Invalid response format
from server - Original: ...")`. This is a workaround: it leaks raw exception
text into the error message and masks the real problem (wrong server). The
`route_repository.dart` does not catch `FormatException` at all — it propagates
uncaught to the UI layer. The `connectivity_service.dart` health probe only
checks `statusCode == 200`, so an imposter returning HTML 200 is reported as
"online".

The correct fix is upstream, at the HTTP response parsing boundary, before
`jsonDecode()` is called. Tasks A and B below add this layer. Task C hardens
the connectivity probe. Together with the error-code contract (Tasks 0–16),
these three tasks give every HTTP boundary a structured, validated parse path
that produces `ApiException` values instead of raw Dart exceptions.

### Architectural Position

This is an **API-to-Flutter contract**, not a Go-to-Go contract:

- The `code` string values are defined in `follow-api` as Go constants and used
  only when building error responses.
- The same `code` string values are defined in `follow-app` as Dart constants and
  used for comparison and localization key lookup.
- The canonical reference list lives in
  `ai-docs/contracts/api-error-codes-contract.md` in the coordination repo.
- `follow-pkg` has no role here. It is a Go-only shared library. The client is
  Dart.

### How `code` Differs from `name`

| Field | Granularity | Example | Purpose |
|-------|-------------|---------|---------|
| `name` | Goa error category (~10 values) | `"invalid_input"` | HTTP status selection, broad grouping |
| `code` | Individual domain error (~50 values) | `"ROUTE_TOO_MANY_WAYPOINTS"` | Localization lookup, logic branching |

Both fields coexist in the response. A client that only reads `name` continues to
work unchanged. The `code` field is additive.

### Approach: Goa Error Formatter Hook

Goa's `goaroutes.New()` constructor accepts a `formatter func(ctx context.Context,
err error) goahttp.Statuser` as its last argument. Currently all four service
handlers in `goa_server.go` pass `nil` for this parameter, meaning Goa uses its
default `*goa.ServiceError` serialization (producing the `{name, id, message,
temporary, timeout, fault}` shape).

A custom formatter intercepts `*goa.ServiceError` values before they are
serialized, wraps them in a custom struct that adds the `code` field, and
serializes that instead. This approach:

- Requires no Goa DSL change and no `goa gen` regeneration.
- Does not modify any generated code in `gen/`.
- Is backward-compatible: clients that do not read `code` see an extra field in
  the JSON they can ignore.
- Is scoped entirely to `internal/api/` — no domain or usecase layer changes.

The formatter is passed to all four `goaXxx.New()` calls in `mountHandlers()`.

### Client Error Display Strategy

The Flutter client uses only the `code` field for all error display decisions.
The `fault` field (which Goa adds automatically to every response) is ignored
by the client. The fallback logic is a simple two-tier decision:

```
Received error response
    |
    +---> code is present and known in ApiErrorCodes?
    |         YES: use l10n.lookupByCode(code) — pre-translated, works in Hebrew
    |
    +---> code is unknown, absent, or empty?
              Show generic localized "Something went wrong" message
```

This means every error the user sees is either a specific localized message
(for known codes) or a generic localized fallback. No raw English server
messages are ever shown to the user.

### Success Criteria

- Every domain error that reaches a client carries a stable `code` string in
  the JSON response body.
- A Flutter client on Hebrew locale sees a pre-translated Hebrew error string for
  known codes, without ever receiving Hebrew text from the server.
- A Flutter client that ignores `code` continues to work exactly as before.
- The server remains backward-compatible: old clients receive an extra JSON field
  they can ignore.
- `dart analyze` passes with no errors; `go test -race -cover ./...` passes.

---

## Contract Document (Source of Truth)

**Task 0 must be completed before Task 1. Task 1 must be completed before any
other implementation begins.**

The contract document at `ai-docs/contracts/api-error-codes-contract.md` is the
single source of truth for the `code` string values. Both the Go constants file
and the Dart constants file are derived from it. Any new code added in the future
must be added to the contract document first.

---

## Tasks

---

### Task 0 — Consolidate Duplicate Waypoint Limit Errors

**Story Points**: 1
**Repo**: `follow-api`
**Files Affected**:
- Modified: `internal/modules/route/domain/errors.go`
- Modified: All files referencing `ErrTooManyWaypoints`

**Description**:

`ErrTooManyWaypoints` and `ErrRouteTooManyWaypoints` in
`follow-api/internal/modules/route/domain/errors.go` represent the same
validation: waypoint count exceeds the maximum allowed per route. Having two
error variables for the same concept creates confusion in the error code
contract — each would need its own `code` string, but they mean the same thing.

Consolidate into a single error: `ErrRouteTooManyWaypoints`. The route domain
owns this limit, so the `Route`-prefixed name is correct. Remove
`ErrTooManyWaypoints` entirely and update all references across the codebase to
use `ErrRouteTooManyWaypoints` instead.

This must be completed before Task 1 (contract document creation) so the
contract has exactly one code (`ROUTE_TOO_MANY_WAYPOINTS`) for this validation,
not two.

**Acceptance Criteria**:
- `ErrTooManyWaypoints` no longer exists in `domain/errors.go`
- All former references to `ErrTooManyWaypoints` now use `ErrRouteTooManyWaypoints`
- `go test -race -cover ./...` passes
- `go vet ./...` passes
- `gofumpt -w . && golines -w --max-len=80 .` produces no changes

---

### Task 1 — Create API Error Codes Contract Document

**Story Points**: 2
**Repo**: Coordination repo (`/home/yoseforb/pkg/follow/`)
**Files Affected**:
- New: `ai-docs/contracts/api-error-codes-contract.md`

**Description**:

Create the canonical contract document. This document is the single source of
truth from which both the Go constants file (Task 2) and the Dart constants file
(Task 6) are derived. Follow the format of
`ai-docs/contracts/valkey-message-contract.md`.

The document must include:

**Header section** explaining:
- This is an API-to-Flutter contract (not Go-to-Go).
- `follow-pkg` has no role.
- Go constants live in `follow-api`; Dart constants live in `follow-app`.
- The `code` field is additive and backward-compatible.

**Error Code Table** grouped by domain with these columns:

| Code | Go Constant | Domain Error (Go) | Goa `name` | HTTP Status | Description |

**Groups and their codes** (complete list):

Route Validation (400):
- `ROUTE_NAME_EMPTY` — `ErrRouteNameEmpty` — `invalid_input` — 400
- `ROUTE_NAME_TOO_LONG` — `ErrRouteNameTooLong` — `invalid_input` — 400
- `ROUTE_DESCRIPTION_TOO_LONG` — `ErrRouteDescriptionTooLong` — `invalid_input` — 400
- `ROUTE_ID_EMPTY` — `ErrRouteIDEmpty` — `invalid_input` — 400
- `ROUTE_OWNER_ID_EMPTY` — `ErrRouteOwnerIDEmpty` — `invalid_input` — 400
- `ROUTE_INVALID_VISIBILITY` — `ErrRouteInvalidVisibility` — `invalid_input` — 400
- `ROUTE_INVALID_ACCESS_METHOD` — `ErrRouteInvalidAccessMethod` — `invalid_input` — 400
- `ROUTE_INVALID_LIFECYCLE_TYPE` — `ErrRouteInvalidLifecycleType` — `invalid_input` — 400
- `ROUTE_INVALID_OWNER_TYPE` — `ErrRouteInvalidOwnerType` — `invalid_input` — 400
- `ROUTE_PASSWORD_REQUIRED` — `ErrRoutePasswordRequired` — `invalid_input` — 400
- `ROUTE_EXPIRATION_REQUIRED` — `ErrRouteExpirationRequired` — `invalid_input` — 400
- `ROUTE_DISCOVERY_REQUIRES_PUBLIC` — `ErrDiscoveryModeRequiresPublicVisibility` — `invalid_input` — 400
- `ROUTE_ALREADY_EXISTS` — `ErrRouteAlreadyExists` — `invalid_input` — 400
- `ROUTE_VALIDATION_FAILED` — `ErrValidationFailed` — `invalid_input` — 400
- `ROUTE_INVALID_UUID` — `ErrInvalidUUID` — `invalid_input` — 400
- `ROUTE_NOT_PREPARED` — `ErrRouteNotPrepared` — `invalid_input` — 400

Waypoint Validation (400):
- `WAYPOINT_ID_EMPTY` — `ErrWaypointIDEmpty` — `invalid_input` — 400
- `WAYPOINT_ROUTE_ID_EMPTY` — `ErrWaypointRouteIDEmpty` — `invalid_input` — 400
- `WAYPOINT_IMAGE_ID_EMPTY` — `ErrWaypointImageIDEmpty` — `invalid_input` — 400
- `WAYPOINT_INVALID_MARKER_COORDS` — `ErrWaypointInvalidMarkerCoords` — `invalid_input` — 400
- `WAYPOINT_INVALID_MARKER_TYPE` — `ErrWaypointInvalidMarkerType` — `invalid_input` — 400
- `WAYPOINT_DESCRIPTION_TOO_LONG` — `ErrWaypointDescriptionTooLong` — `invalid_input` — 400
- `WAYPOINT_INSTRUCTIONS_TOO_LONG` — `ErrWaypointInstructionsTooLong` — `invalid_input` — 400
- `WAYPOINT_DUPLICATE_POSITION` — `ErrWaypointDuplicatePosition` — `invalid_input` — 400
- `WAYPOINT_INVALID_STATUS` — `ErrWaypointInvalidStatus` — `invalid_input` — 400
- `WAYPOINT_INVALID_STATUS_TRANSITION` — `ErrWaypointInvalidStatusTransition` — `invalid_input` — 400
- `WAYPOINT_VALIDATION_FAILED` — `ErrWaypointValidationFailed` — `invalid_input` — 400
- `WAYPOINT_POSITION_INVALID` — `ErrWaypointPositionInvalid` — `invalid_input` — 400
- `WAYPOINT_MARKER_COORDS_INVALID` — `ErrWaypointMarkerCoordsInvalid` — `invalid_input` — 400
- `WAYPOINT_PENDING_REPLACEMENT_MISMATCH` — `ErrWaypointPendingReplacementMismatch` — `invalid_input` — 400
- `WAYPOINT_NO_PENDING_REPLACEMENT` — `ErrWaypointNoPendingReplacement` — `invalid_input` — 400
- `WAYPOINT_MARKER_OUT_OF_RANGE` — `ErrWaypointMarkerOutOfRange` — `invalid_input` — 400
- `WAYPOINT_ALREADY_EXISTS` — `ErrWaypointAlreadyExists` — `invalid_input` — 400
- `WAYPOINT_ALREADY_EXISTS` — `ErrWaypointAlreadyExists` — `invalid_input` — 400

Waypoint Collection / Limits (400):
- `WAYPOINTS_MINIMUM_REQUIRED` — `ErrMinimumWaypointsRequired` — `invalid_input` — 400
- `WAYPOINTS_NONE_PROVIDED` — `ErrNoWaypointsProvided` — `invalid_input` — 400
- `ROUTE_TOO_MANY_WAYPOINTS` — `ErrRouteTooManyWaypoints` — `invalid_input` — 400
- `ROUTE_MAX_WAYPOINTS_EXCEEDED` — `ErrRouteMaxWaypointsExceeded` — `invalid_input` — 400
- `ROUTE_WAYPOINT_ORDER_INVALID` — `ErrRouteWaypointOrderInvalid` — `invalid_input` — 400
- `ROUTE_NO_WAYPOINTS` — `ErrRouteNoWaypoints` — `invalid_input` — 400

Image Validation (400):
- `IMAGE_SIZE_INVALID` — `ErrImageInvalidSize` — `invalid_input` — 400
- `IMAGE_CONTENT_TYPE_INVALID` — `ErrImageInvalidContentType` — `invalid_input` — 400
- `IMAGE_VALIDATION_FAILED` — `ErrImageValidationFailed` — `image_validation_failed` — 400

User Auth / Existence (401):
- `USER_NOT_FOUND` — `ErrUserNotFound` — `unauthorized` — 401
- `USER_ID_EMPTY` — `ErrUserIDEmpty` — `unauthorized` — 401
- `USER_VALIDATION_FAILED` — `ErrUserValidationFailed` — `user_validation_failed` — 401
- `ROUTE_PASSWORD_REQUIRED_FOR_ACCESS` — `ErrPasswordRequired` (access) — `unauthorized` — 401
- `ROUTE_PASSWORD_INCORRECT` — `ErrPasswordIncorrect` — `unauthorized` — 401

Ownership / Access Control (403):
- `ROUTE_NOT_OWNED_BY_USER` — `ErrRouteNotOwnedByUser` — `route_not_owned_by_user` — 403
- `USER_NOT_AUTHORIZED` — `ErrUserNotAuthorized` — `route_not_owned_by_user` — 403

User Limits / Quota (403):
- `USER_LIMIT_EXCEEDED` — `ErrUserLimitExceeded` — `user_limit_exceeded` — 403
- `ANONYMOUS_USER_LIMIT_EXCEEDED` — `ErrAnonymousUserLimitExceeded` — `user_limit_exceeded` — 403
- `PENDING_ROUTES_LIMIT_EXCEEDED` — `ErrPendingRoutesLimitExceeded` — `user_limit_exceeded` — 403
- `WAYPOINTS_LIMIT_EXCEEDED` — `ErrWaypointsLimitExceeded` — `user_limit_exceeded` — 403
- `MAX_PENDING_ROUTES_EXCEEDED` — `ErrMaxPendingRoutesExceeded` — `user_limit_exceeded` — 403
- `MAX_WAYPOINTS_PER_ROUTE_EXCEEDED` — `ErrMaxWaypointsPerRouteExceeded` — `user_limit_exceeded` — 403

Not Found (404):
- `ROUTE_NOT_FOUND` — `ErrRouteNotFound` — `not_found` — 404
- `WAYPOINT_NOT_FOUND` — `ErrWaypointNotFound` — `waypoint_not_found` — 404

Route State Machine (422):
- `ROUTE_NOT_PENDING` — `ErrRouteNotPending` — `route_state_error` — 422
- `ROUTE_NOT_READY` — `ErrRouteNotReady` — `route_state_error` — 422
- `ROUTE_INVALID_STATUS` — `ErrRouteInvalidStatus` — `route_state_error` — 422
- `ROUTE_INVALID_STATUS_TRANSITION` — `ErrRouteInvalidStatusTransition` — `route_state_error` — 422
- `ROUTE_ALREADY_EXPIRED` — `ErrRouteAlreadyExpired` — `route_state_error` — 422
- `ROUTE_NOT_EXPIRED` — `ErrRouteNotExpired` — `route_state_error` — 422
- `ROUTE_NOT_EMPTY` — `ErrRouteNotEmpty` — `route_state_error` — 422

**Rules section** (like the Valkey contract):
1. Never hardcode `code` string literals in Go service code — always use the
   constants from `internal/api/services/error_codes.go`.
2. Never hardcode `code` string literals in Dart UI code — always use the
   constants from `lib/data/repositories/api_error_codes.dart`.
3. The contract document is the canonical list. Adding a new code means: update
   the contract doc first, then add the Go constant, then add the Dart constant,
   then add the ARB localization key.
4. `follow-pkg` has no role in this contract. It is a Go-only library.
5. Codes that map to 5xx responses (`storage_error`, infrastructure defaults) do
   not receive a `code` — they return empty string, as those errors are not
   actionable by the client.

**Acceptance Criteria**:
- File exists at `ai-docs/contracts/api-error-codes-contract.md`
- All ~55 error codes from `domain/errors.go` and relevant `usecases/errors.go`
  entries are covered
- Format follows `valkey-message-contract.md` style (tables, groups, rules)
- Rules section clearly states `follow-pkg` has no role

---

### Task 2 — Add Go Error Code Constants to `follow-api`

**Story Points**: 2
**Repo**: `follow-api`
**Files Affected**:
- New: `internal/api/services/error_codes.go`

**Description**:

Create a Go constants file that defines every `code` string value as a named
constant. This file lives in the `services` package alongside `routes_service.go`
so it is visible to all service-layer error mapping functions without a new
import.

File structure:

```go
// Package services contains API error code constants for the Follow API.
//
// These constants define the stable, machine-readable error codes that are
// included in API error responses as the "code" field. They are the Go side
// of the API-to-Flutter error code contract.
//
// Source of truth: ai-docs/contracts/api-error-codes-contract.md
// Flutter constants: follow-app/lib/data/repositories/api_error_codes.dart
//
// Rules:
// - Never use string literals for error codes in service code.
// - Codes are SCREAMING_SNAKE_CASE strings.
// - 5xx infrastructure errors use CodeEmpty ("") — not actionable by client.
package services

// CodeEmpty is returned for infrastructure/server faults where no
// client-actionable code is appropriate.
const CodeEmpty = ""

// Route validation codes (400 invalid_input)
const (
    CodeRouteNameEmpty             = "ROUTE_NAME_EMPTY"
    CodeRouteNameTooLong           = "ROUTE_NAME_TOO_LONG"
    CodeRouteDescriptionTooLong    = "ROUTE_DESCRIPTION_TOO_LONG"
    CodeRouteIDEmpty               = "ROUTE_ID_EMPTY"
    CodeRouteOwnerIDEmpty          = "ROUTE_OWNER_ID_EMPTY"
    CodeRouteInvalidVisibility     = "ROUTE_INVALID_VISIBILITY"
    CodeRouteInvalidAccessMethod   = "ROUTE_INVALID_ACCESS_METHOD"
    CodeRouteInvalidLifecycleType  = "ROUTE_INVALID_LIFECYCLE_TYPE"
    CodeRouteInvalidOwnerType      = "ROUTE_INVALID_OWNER_TYPE"
    CodeRoutePasswordRequired      = "ROUTE_PASSWORD_REQUIRED"
    CodeRouteExpirationRequired    = "ROUTE_EXPIRATION_REQUIRED"
    CodeRouteDiscoveryRequiresPublic = "ROUTE_DISCOVERY_REQUIRES_PUBLIC"
    CodeRouteAlreadyExists         = "ROUTE_ALREADY_EXISTS"
    CodeRouteValidationFailed      = "ROUTE_VALIDATION_FAILED"
    CodeRouteInvalidUUID           = "ROUTE_INVALID_UUID"
    CodeRouteNotPrepared           = "ROUTE_NOT_PREPARED"
)

// Waypoint validation codes (400 invalid_input)
const (
    CodeWaypointIDEmpty                    = "WAYPOINT_ID_EMPTY"
    // ... (all waypoint codes from contract doc)
)

// Waypoint collection / limit codes (400 invalid_input)
const (
    CodeWaypointsMinimumRequired   = "WAYPOINTS_MINIMUM_REQUIRED"
    // ... (all collection codes from contract doc)
)

// Image validation codes (400)
const (
    CodeImageSizeInvalid        = "IMAGE_SIZE_INVALID"
    CodeImageContentTypeInvalid = "IMAGE_CONTENT_TYPE_INVALID"
    CodeImageValidationFailed   = "IMAGE_VALIDATION_FAILED"
)

// User auth / existence codes (401)
const (
    CodeUserNotFound                  = "USER_NOT_FOUND"
    CodeUserIDEmpty                   = "USER_ID_EMPTY"
    CodeUserValidationFailed          = "USER_VALIDATION_FAILED"
    CodeRoutePasswordRequiredForAccess = "ROUTE_PASSWORD_REQUIRED_FOR_ACCESS"
    CodeRoutePasswordIncorrect        = "ROUTE_PASSWORD_INCORRECT"
)

// Ownership / access control codes (403)
const (
    CodeRouteNotOwnedByUser = "ROUTE_NOT_OWNED_BY_USER"
    CodeUserNotAuthorized   = "USER_NOT_AUTHORIZED"
)

// User limits / quota codes (403)
const (
    CodeUserLimitExceeded          = "USER_LIMIT_EXCEEDED"
    CodeAnonymousUserLimitExceeded = "ANONYMOUS_USER_LIMIT_EXCEEDED"
    CodePendingRoutesLimitExceeded = "PENDING_ROUTES_LIMIT_EXCEEDED"
    CodeWaypointsLimitExceeded     = "WAYPOINTS_LIMIT_EXCEEDED"
    CodeMaxPendingRoutesExceeded   = "MAX_PENDING_ROUTES_EXCEEDED"
    CodeMaxWaypointsPerRouteExceeded = "MAX_WAYPOINTS_PER_ROUTE_EXCEEDED"
)

// Not found codes (404)
const (
    CodeRouteNotFound    = "ROUTE_NOT_FOUND"
    CodeWaypointNotFound = "WAYPOINT_NOT_FOUND"
)

// Route state machine codes (422)
const (
    CodeRouteNotPending              = "ROUTE_NOT_PENDING"
    CodeRouteNotReady                = "ROUTE_NOT_READY"
    CodeRouteInvalidStatus           = "ROUTE_INVALID_STATUS"
    CodeRouteInvalidStatusTransition = "ROUTE_INVALID_STATUS_TRANSITION"
    CodeRouteAlreadyExpired          = "ROUTE_ALREADY_EXPIRED"
    CodeRouteNotExpired              = "ROUTE_NOT_EXPIRED"
    CodeRouteNotEmpty                = "ROUTE_NOT_EMPTY"
)
```

**Acceptance Criteria**:
- File compiles with `go build ./...`
- Every code value in the contract document has a corresponding Go constant
- No string literals for codes exist anywhere else in `internal/api/services/`
- `go vet ./...` passes

---

### Task 3 — Add Goa Error Formatter with `code` Field

**Story Points**: 3
**Repo**: `follow-api`
**Files Affected**:
- New: `internal/api/server/error_formatter.go`
- Modified: `internal/api/server/goa_server.go`

**Description**:

Implement a custom Goa error formatter that wraps `*goa.ServiceError` with a
`code` field and passes it to all four `goaXxx.New()` handler constructors.

**Step 1**: Create `internal/api/server/error_formatter.go`.

The formatter must implement `func(ctx context.Context, err error) goahttp.Statuser`.
`goahttp.Statuser` requires a `StatusCode() int` method.

```go
// errorResponseBody is the JSON shape of all API error responses.
// It extends Goa's default ServiceError shape with a "code" field.
// The code field is empty string for 5xx infrastructure errors.
type errorResponseBody struct {
    Name      string `json:"name"`
    ID        string `json:"id"`
    Message   string `json:"message"`
    Temporary bool   `json:"temporary"`
    Timeout   bool   `json:"timeout"`
    Fault     bool   `json:"fault"`
    Code      string `json:"code"`
}

// statusBody wraps errorResponseBody so it satisfies goahttp.Statuser.
type statusBody struct {
    body       errorResponseBody
    statusCode int
}

func (s *statusBody) StatusCode() int { return s.statusCode }

// NewErrorFormatter returns a Goa error formatter that adds the "code" field
// to every error response. The formatter is registered with all service
// handler constructors in mountHandlers.
func NewErrorFormatter() func(context.Context, error) goahttp.Statuser {
    return func(ctx context.Context, err error) goahttp.Statuser {
        var se *goa.ServiceError
        if !errors.As(err, &se) {
            // Non-ServiceError — use Goa default behavior by returning nil.
            // Goa's encodeError handles it.
            return nil
        }

        code := errorCodeFor(se)
        body := errorResponseBody{
            Name:      se.Name,
            ID:        se.ID,
            Message:   se.Message,
            Temporary: se.Temporary,
            Timeout:   se.Timeout,
            Fault:     se.Fault,
            Code:      code,
        }

        return &statusBody{
            body:       body,
            statusCode: goahttp.ResponseStatus(se),
        }
    }
}
```

**Step 2**: Create `errorCodeFor(se *goa.ServiceError) string` in the same file.
This function maps a `*goa.ServiceError` to a `code` constant. It uses
`errors.Is()` against the wrapped error chain to identify the originating domain
error, using the same switch structure as `mapRouteError()` but returning code
constants instead of Goa errors:

```go
func errorCodeFor(se *goa.ServiceError) string {
    err := se.Unwrap()
    if err == nil {
        return services.CodeEmpty
    }
    switch {
    case errors.Is(err, routeDomain.ErrRouteNotFound):
        return services.CodeRouteNotFound
    case errors.Is(err, routeDomain.ErrWaypointNotFound):
        return services.CodeWaypointNotFound
    // ... all mappings following the same structure as mapRouteError()
    // Infrastructure/storage errors:
    default:
        return services.CodeEmpty
    }
}
```

**Step 3**: Wire the formatter into `mountHandlers()` in `goa_server.go`.
Currently all `goaXxx.New()` calls pass `nil` as the last (formatter) argument.
Replace with `formatter`:

```go
func mountHandlers(
    mux goahttp.Muxer,
    routeEndpoints *routes.Endpoints,
    userEndpoints *users.Endpoints,
    authEndpoints *genauth.Endpoints,
    healthEndpoints *health.Endpoints,
) {
    formatter := NewErrorFormatter()

    routeHandler := goaroutes.New(
        routeEndpoints,
        mux,
        goahttp.RequestDecoder,
        goahttp.ResponseEncoder,
        nil,
        formatter,  // was nil
    )
    userHandler := goausers.New(
        userEndpoints,
        mux,
        goahttp.RequestDecoder,
        goahttp.ResponseEncoder,
        nil,
        formatter,  // was nil
    )
    // ... same for auth and health handlers
}
```

**Important**: The `goa.ServiceError` struct embeds the original error via its
`Unwrap()` method. Verify this during implementation by checking
`goa.NewServiceError` in the Goa source — the error is stored as the `err`
field and exposed via `Unwrap()`. If `Unwrap()` is unavailable, use
`se.Message` string matching as a last-resort fallback, but `Unwrap()` is
the correct and expected path.

**Acceptance Criteria**:
- A request that triggers `ErrRouteTooManyWaypoints` returns JSON with
  `"code": "ROUTE_TOO_MANY_WAYPOINTS"` alongside existing `name`, `message`,
  `fault` fields
- A request that triggers a storage/infrastructure 500 error returns JSON with
  `"code": ""` (empty, not absent)
- All existing error response fields (`name`, `id`, `message`, `temporary`,
  `timeout`, `fault`) continue to appear with correct values
- `go test -race -cover ./...` passes
- `gofumpt -w . && golines -w --max-len=80 .` passes

---

### Task 4 — Unit Tests for Error Formatter

**Story Points**: 2
**Repo**: `follow-api`
**Files Affected**:
- New: `internal/api/server/error_formatter_test.go`

**Description**:

Unit tests for `NewErrorFormatter()` and `errorCodeFor()`.

Test structure follows the project's classical/Detroit style: table-driven,
`t.Parallel()`, no mock frameworks, verify outcomes not interactions.

**`errorCodeFor` test table** (representative, not exhaustive — include at least
one entry per HTTP-status group):

```go
tests := []struct {
    name     string
    domainErr error
    wantCode  string
}{
    {
        name:     "route not found -> ROUTE_NOT_FOUND",
        domainErr: routeDomain.ErrRouteNotFound,
        wantCode:  services.CodeRouteNotFound,
    },
    {
        name:     "too many waypoints -> ROUTE_TOO_MANY_WAYPOINTS",
        domainErr: routeDomain.ErrRouteTooManyWaypoints,
        wantCode:  services.CodeRouteTooManyWaypoints,
    },
    {
        name:     "user limit exceeded -> USER_LIMIT_EXCEEDED",
        domainErr: routeDomain.ErrUserLimitExceeded,
        wantCode:  services.CodeUserLimitExceeded,
    },
    {
        name:     "route not pending -> ROUTE_NOT_PENDING",
        domainErr: routeDomain.ErrRouteNotPending,
        wantCode:  services.CodeRouteNotPending,
    },
    {
        name:     "wrapped domain error still resolves",
        domainErr: fmt.Errorf("context: %w", routeDomain.ErrRouteNotFound),
        wantCode:  services.CodeRouteNotFound,
    },
    {
        name:     "infrastructure error -> empty code",
        domainErr: routeUsecases.ErrRepositoryOperationFailed,
        wantCode:  services.CodeEmpty,
    },
    {
        name:     "nil wrapped error -> empty code",
        domainErr: nil,
        wantCode:  services.CodeEmpty,
    },
}
```

**`NewErrorFormatter` integration test**:
- Call the formatter with a `*goa.ServiceError` wrapping `ErrRouteTooManyWaypoints`
- Assert the returned `goahttp.Statuser` is non-nil
- Assert that JSON-encoding the returned value produces a body with `"code":
  "ROUTE_TOO_MANY_WAYPOINTS"` and correct `"name"`, `"fault"` values

**Acceptance Criteria**:
- `go test -race -cover ./internal/api/server/...` passes
- Coverage on `error_formatter.go` is >= 85%
- All table-driven cases pass

---

### Task 5 — Integration Test: Error Code in HTTP Response

**Story Points**: 2
**Repo**: `follow-api`
**Files Affected**:
- Modified: Relevant existing API integration test file, or new file in
  `internal/api/server/` or `tests/integration/`

**Description**:

End-to-end test confirming that the `code` field appears in actual HTTP
responses from a running server. This test starts the server in test mode and
makes real HTTP requests.

Use the existing integration test infrastructure in the project (the
`-runtime-timeout` flag pattern and the HTTP test client established in
`internal/api/server/auth_integration_test.go`).

**Scenarios to test**:

1. POST `/api/v1/routes/{id}/create-waypoints` with 11 waypoints (exceeds limit
   of 10) — expect 400 with `"code": "ROUTE_TOO_MANY_WAYPOINTS"` in body.
2. POST `/api/v1/routes/prepare` with an expired/missing JWT — expect 401 with
   `"code": ""` (auth middleware fires before route service, produces a non-domain
   error).
3. DELETE `/api/v1/routes/{id}` for a route owned by a different user — expect
   403 with `"code": "ROUTE_NOT_OWNED_BY_USER"`.
4. GET `/api/v1/routes/{nonexistent_id}` — expect 404 with `"code":
   "ROUTE_NOT_FOUND"`.

For each scenario, assert:
- HTTP status code is correct
- Response body is valid JSON
- `code` field is present
- `code` value matches expected constant
- `name`, `message`, `fault` fields are still present with correct values

**Acceptance Criteria**:
- All 4 scenarios pass
- `go test -race -cover ./...` passes

---

### Task 6 — Add Dart Error Code Constants to `follow-app`

**Story Points**: 1
**Repo**: `follow-app`
**Files Affected**:
- New: `lib/data/repositories/api_error_codes.dart`

**Description**:

Create a pure-Dart file containing every `code` string as a Dart constant.
This is the Dart mirror of `internal/api/services/error_codes.go` in `follow-api`.

```dart
/// API error code constants for the Follow app.
///
/// These constants are the Dart side of the API-to-Flutter error code contract.
/// They correspond exactly to the Go constants in follow-api's
/// `internal/api/services/error_codes.go`.
///
/// Source of truth: ai-docs/contracts/api-error-codes-contract.md
/// Go constants: follow-api/internal/api/services/error_codes.go
///
/// Rules:
/// - Never use string literals for error codes in Dart code.
/// - Use these constants for comparison and localization key lookup.
/// - follow-pkg has no role in this contract.
abstract final class ApiErrorCodes {
  /// Infrastructure / server faults — not actionable by the client.
  static const String empty = '';

  // ── Route validation (400) ─────────────────────────────────────────────
  static const String routeNameEmpty          = 'ROUTE_NAME_EMPTY';
  static const String routeNameTooLong        = 'ROUTE_NAME_TOO_LONG';
  static const String routeDescriptionTooLong = 'ROUTE_DESCRIPTION_TOO_LONG';
  // ... (all codes from contract doc)

  // ── Waypoint validation (400) ──────────────────────────────────────────
  static const String waypointIdEmpty              = 'WAYPOINT_ID_EMPTY';
  // ... (all waypoint codes)

  // ── Waypoint collection / limits (400) ────────────────────────────────
  static const String waypointsMinimumRequired = 'WAYPOINTS_MINIMUM_REQUIRED';
  static const String routeTooManyWaypoints     = 'ROUTE_TOO_MANY_WAYPOINTS';
  // ... (all collection codes)

  // ── Image validation (400) ────────────────────────────────────────────
  static const String imageSizeInvalid       = 'IMAGE_SIZE_INVALID';
  static const String imageContentTypeInvalid = 'IMAGE_CONTENT_TYPE_INVALID';
  static const String imageValidationFailed  = 'IMAGE_VALIDATION_FAILED';

  // ── User auth / existence (401) ───────────────────────────────────────
  static const String userNotFound            = 'USER_NOT_FOUND';
  static const String userIdEmpty             = 'USER_ID_EMPTY';
  static const String userValidationFailed    = 'USER_VALIDATION_FAILED';
  static const String routePasswordRequired   = 'ROUTE_PASSWORD_REQUIRED_FOR_ACCESS';
  static const String routePasswordIncorrect  = 'ROUTE_PASSWORD_INCORRECT';

  // ── Ownership / access control (403) ─────────────────────────────────
  static const String routeNotOwnedByUser = 'ROUTE_NOT_OWNED_BY_USER';
  static const String userNotAuthorized   = 'USER_NOT_AUTHORIZED';

  // ── User limits / quota (403) ─────────────────────────────────────────
  static const String userLimitExceeded          = 'USER_LIMIT_EXCEEDED';
  static const String anonymousUserLimitExceeded = 'ANONYMOUS_USER_LIMIT_EXCEEDED';
  static const String pendingRoutesLimitExceeded = 'PENDING_ROUTES_LIMIT_EXCEEDED';
  static const String waypointsLimitExceeded     = 'WAYPOINTS_LIMIT_EXCEEDED';
  static const String maxPendingRoutesExceeded   = 'MAX_PENDING_ROUTES_EXCEEDED';
  static const String maxWaypointsPerRouteExceeded = 'MAX_WAYPOINTS_PER_ROUTE_EXCEEDED';

  // ── Not found (404) ───────────────────────────────────────────────────
  static const String routeNotFound    = 'ROUTE_NOT_FOUND';
  static const String waypointNotFound = 'WAYPOINT_NOT_FOUND';

  // ── Route state machine (422) ─────────────────────────────────────────
  static const String routeNotPending              = 'ROUTE_NOT_PENDING';
  static const String routeNotReady                = 'ROUTE_NOT_READY';
  static const String routeInvalidStatus           = 'ROUTE_INVALID_STATUS';
  static const String routeInvalidStatusTransition = 'ROUTE_INVALID_STATUS_TRANSITION';
  static const String routeAlreadyExpired          = 'ROUTE_ALREADY_EXPIRED';
  static const String routeNotExpired              = 'ROUTE_NOT_EXPIRED';
  static const String routeNotEmpty                = 'ROUTE_NOT_EMPTY';
}
```

**Acceptance Criteria**:
- File compiles with `dart analyze` showing no errors
- Every code in the contract document has a corresponding Dart constant
- No string literals for codes appear anywhere else in `lib/data/`
- String values exactly match the Go constants (case-sensitive)

---

### Task 7 — Extend `ApiErrorResponse` Model with `code` Field

**Story Points**: 1
**Repo**: `follow-app`
**Files Affected**:
- New: `lib/data/models/api_error_response.dart`

**Description**:

Create `ApiErrorResponse` model to parse error responses from the API, including
the `code` field for domain error identification.

Add to `ApiErrorResponse`:

```dart
/// The machine-readable error code identifying the specific domain error.
///
/// Empty string when the error is an infrastructure fault (5xx) or when
/// the server does not support the code field (backward-compatibility).
/// Use [ApiErrorCodes] constants for comparison — never compare to string
/// literals.
final String code;
```

Update `fromJson`:
```dart
code: (json['code'] as String?) ?? '',
```

Add a `hasCode` getter:
```dart
/// Whether this error has a specific domain code that can be looked up
/// in [ApiErrorCodes].
bool get hasCode => code.isNotEmpty;
```

The `isDomainError` getter is unchanged.

**Acceptance Criteria**:
- `ApiErrorResponse.fromJson({'name':'invalid_input','id':'x','message':'m','timeout':false,'fault':false,'code':'ROUTE_TOO_MANY_WAYPOINTS'}).code` equals `'ROUTE_TOO_MANY_WAYPOINTS'`
- `ApiErrorResponse.fromJson({'name':'invalid_input','id':'x','message':'m','timeout':false,'fault':false}).code` equals `''` (missing field defaults to empty)
- `hasCode` is `true` for non-empty codes, `false` for empty string
- `dart analyze` passes
- Unit tests updated to cover the `code` field

---

### Task 8 — Extend `ApiErrorParser` to Surface `code`

**Story Points**: 1
**Repo**: `follow-app`
**Files Affected**:
- New: `lib/data/repositories/api_error_parser.dart`

**Description**:

Extend (or create) `ApiErrorParser` so that `DomainErrorResult` carries the
`code` field alongside `message` and `name`.

```dart
/// A domain error where the server message is actionable and a specific
/// error code is available for localization lookup.
final class DomainErrorResult extends ApiErrorParseResult {
  const DomainErrorResult({
    required this.message,
    required this.name,
    required this.code,   // NEW
  });
  final String message;
  final String name;
  final String code;      // NEW — may be empty for older server versions
}
```

The `parseApiError` function populates `code` from `ApiErrorResponse.code` when
`hasCode` is true, otherwise sets it to empty string:

```dart
return DomainErrorResult(
  message: parsed.message,
  name: parsed.name,
  code: parsed.code,   // empty string if server does not send code
);
```

No change to `InfraErrorResult`.

**Acceptance Criteria**:
- `parseApiError(400, '{"name":"invalid_input","message":"...","fault":false,"timeout":false,"id":"x","code":"ROUTE_TOO_MANY_WAYPOINTS"}')` returns `DomainErrorResult` with `code == 'ROUTE_TOO_MANY_WAYPOINTS'`
- `parseApiError(400, '{"name":"invalid_input","message":"...","fault":false,"timeout":false,"id":"x"}')` returns `DomainErrorResult` with `code == ''` (backward-compatible)
- Unit tests updated

---

### Task 9 — Wire `RouteException` Constructor to `ApiException` Base

**Story Points**: 1
**Repo**: `follow-app`
**Files Affected**:
- Modified: `lib/data/repositories/route_repository_models.dart` (constructor delegates to `ApiException`)

**Description**:

**Scope change from original**: `code` is NOT added directly to `RouteException`
here — it is already present on `ApiException` (Task A, which is a prerequisite
for this task). This task only ensures `RouteException`'s constructor delegates
`code`, `isDomainError`, and `isFormatError` correctly to the base class, and
that all existing `throw RouteException(...)` call sites continue to compile
unchanged.

After Task A introduces `ApiException`, update `RouteException` to:

```dart
class RouteException extends ApiException {
  const RouteException(
    super.message, {
    super.statusCode,
    super.responseBody,
    super.originalError,
    super.isDomainError = false,
    super.code = '',
    super.isFormatError = false,
  });

  // Domain-specific getters remain here unchanged:
  bool get isNotFound => statusCode == 404;
  // ... (all existing domain-specific getters)
}
```

The default `code = ''` keeps all existing `throw RouteException(...)` call
sites unchanged. Cross-reference: see Task A for the base class definition.

**Acceptance Criteria**:
- `RouteException extends ApiException` (not `implements Exception`)
- `RouteException` constructor accepts `code` and `isFormatError` with defaults
- All existing `throw RouteException(...)` call sites compile without changes
- `dart analyze` passes

---

### Task A — Shared `ApiException` Base Class

**Story Points**: 2
**Repo**: `follow-app`
**Files Affected**:
- New: `lib/data/repositories/api_exception.dart`
- Modified: `lib/data/repositories/auth_repository.dart` (make `AuthException` extend `ApiException`)
- Modified: `lib/data/repositories/route_repository_models.dart` (make `RouteException` extend `ApiException`)

**Description**:

`AuthException` (currently at `auth_repository.dart:604-650`) and `RouteException`
(at `route_repository_models.dart:466-514`) have identical shapes — `message`,
`statusCode`, `responseBody`, `originalError` — but no common ancestor. Task 9
was scoped to add `code` only to `RouteException`, which would cement the drift.
This task moves `code` onto a shared base class so both exception types benefit.

Create `lib/data/repositories/api_exception.dart` with:

```dart
/// Base class for all API-layer exceptions in the Follow app.
///
/// Provides common fields shared by [AuthException] and [RouteException].
/// Domain-specific getters (e.g. [AuthException.isUserNotFound],
/// [RouteException.isNotFound]) remain on the subclasses.
abstract class ApiException implements Exception {
  const ApiException(
    this.message, {
    this.statusCode,
    this.responseBody,
    this.originalError,
    this.isDomainError = false,
    this.code = '',
    this.isFormatError = false,
  });

  /// Human-readable error message (may be server-supplied or client-generated).
  final String message;

  /// HTTP status code from the response, if available.
  final int? statusCode;

  /// Raw response body, for debugging.
  final String? responseBody;

  /// The original exception that caused this error, if any.
  final Object? originalError;

  /// True when the server returned a structured domain error (4xx with a
  /// recognised [code] value).
  final bool isDomainError;

  /// SCREAMING_SNAKE_CASE error code from the API response `code` field.
  /// Empty string when: infrastructure fault, server too old, or parse error.
  /// Use [ApiErrorCodes] constants for comparison — never string literals.
  final String code;

  /// True when the response body was not valid JSON (e.g. an HTML page was
  /// returned by an imposter server or a proxy). When true, [code] is always
  /// empty and the caller must NOT retry.
  final bool isFormatError;

  @override
  String toString() => 'ApiException($statusCode): $message';
}
```

Modify `AuthException` and `RouteException` to `extend ApiException` instead of
`implements Exception`. Their existing domain-specific getters (`isUserNotFound`,
`isNotFound`, etc.) stay on the subclasses unchanged. Their constructors delegate
to `super` for the shared fields.

**Relationship to Task 9**: Task 9's scope is reduced. `code` is now added on
`ApiException` (here), not per-subclass. Task 9 only wires the `RouteException`
constructor call sites. See the updated Task 9 description.

**Acceptance Criteria**:
- `AuthException extends ApiException` compiles
- `RouteException extends ApiException` compiles
- All existing `throw AuthException(...)` and `throw RouteException(...)` call
  sites compile without changes
- `ApiException.isFormatError` defaults to `false`
- `dart analyze` passes with no errors

---

### Task B — Content-Type & Body-Sniff Validation in `ApiErrorParser`

**Story Points**: 3
**Repo**: `follow-app`
**Files Affected**:
- Modified: `lib/data/repositories/api_error_parser.dart` (add shape validation before `jsonDecode`)
- Modified: `lib/data/models/api_error_response.dart` (no structural change needed; validation is in the parser)
- Modified: `lib/data/repositories/auth_repository.dart` (remove `FormatException` workaround at lines 205 and 314)
- Modified: `lib/data/repositories/route_repository.dart` (no explicit `FormatException` catch needed after this)
- Modified: `lib/l10n/app_en.arb` (add `apiErrorFormatInvalid`)
- Modified: `lib/l10n/app_he.arb` (add `apiErrorFormatInvalid`)
- New: `test/data/repositories/api_error_parser_shape_validation_test.dart`

**Description**:

Before `jsonDecode()` is called anywhere in the repository layer, validate the
HTTP response shape. This task extends `ApiErrorParser` (Task 8's file) with a
pre-decode guard that runs on every non-2xx response branch.

**Validation logic** (add as a private helper `_validateResponseShape` in
`api_error_parser.dart`):

```dart
/// Returns a [FormatErrorResult] if the response cannot be JSON, null otherwise.
FormatErrorResult? _validateResponseShape(
  http.Response response,
  String method,
  Uri uri,
) {
  // 1. Content-type check (case-insensitive).
  final contentType = (response.headers['content-type'] ?? '').toLowerCase();
  final isJson = contentType.contains('application/json') ||
      contentType.contains('+json');

  // 2. Body sniff: if content-type is wrong OR absent, check first byte.
  final body = response.body.trimLeft();
  final looksLikeHtml = body.startsWith('<');

  if (!isJson || looksLikeHtml) {
    final hint = 'The server at ${uri.host}:${uri.port} may not be '
        'follow-api — check API_BASE_URL.';
    return FormatErrorResult(
      message: 'Non-JSON response from $method ${uri.path} '
          '(status ${response.statusCode}, '
          'content-type: "${response.headers['content-type'] ?? 'absent'}"). '
          '$hint',
    );
  }
  return null;
}
```

Add `FormatErrorResult` as a new sealed subtype of `ApiErrorParseResult`:

```dart
/// The response body was not valid JSON. The server may be an imposter
/// (wrong port, proxy error page, misconfigured API_BASE_URL).
/// Callers must NOT retry on this result.
final class FormatErrorResult extends ApiErrorParseResult {
  const FormatErrorResult({required this.message});
  final String message;
}
```

Call `_validateResponseShape` at the top of `parseApiError` (or equivalent
entry point), before any `jsonDecode`. If it returns non-null, return that
result immediately.

Repositories catching `ApiException` must check `isFormatError` and surface
`l10n.apiErrorFormatInvalid` — they must NOT retry.

**CRITICAL — remove the existing workaround**: The `FormatException` catch
blocks in `auth_repository.dart` at lines 205 and 314 that wrap as
`AuthException("Invalid response format from server - Original: ...")` must be
**deleted entirely**. That logic becomes dead code once validation runs upstream.
Do not keep them "for safety" — removing dead workarounds is required by the
project's engineering principles.

**Add ARB keys** (also coordinate with Task 11 agent to avoid duplication):
```
"apiErrorFormatInvalid": "Cannot connect to the server. Check your network and API_BASE_URL."
"@apiErrorFormatInvalid": { "description": "Shown when the server returns non-JSON (e.g. HTML from an imposter server)." }
```
Hebrew: `"apiErrorFormatInvalid": "לא ניתן להתחבר לשרת. בדוק את החיבור לרשת ואת כתובת ה-API."`

**Acceptance Criteria**:
- An HTML body with HTTP 200 status produces `FormatErrorResult` (not a thrown
  `FormatException`) with the structured hint message
- An HTML body with `text/html` content-type header produces `FormatErrorResult`
- A missing `content-type` header where body starts with `<` produces
  `FormatErrorResult`
- A response with `content-type: application/json` and a valid JSON body
  proceeds to normal parsing (no false positive)
- Empty body produces `FormatErrorResult` (no content-type → sniff → empty
  string does not start with `<` → proceed, but `jsonDecode('')` throws → the
  existing `FormatException` catch in the parser handles it and returns an
  `InfraErrorResult`; document this edge case)
- The `FormatException` catch blocks removed from `auth_repository.dart:205,314`
- Unit tests cover: `text/html` content-type, missing content-type, content-type
  says JSON but body starts with `<`, empty body, truncated JSON
- `dart analyze` passes with no errors
- `flutter test --coverage` passes

---

### Task C — Harden `ConnectivityService` Health Probe

**Story Points**: 1
**Repo**: `follow-app`
**Files Affected**:
- Modified: `lib/data/services/connectivity_service.dart` (lines 120–165)
- Possibly modified: `lib/ui/connectivity/connectivity_view_model.dart` (if a MISCONFIGURED state is added — investigate during implementation)

**Description**:

The current health probe at `connectivity_service.dart:120-165` only checks
`statusCode == 200`. An imposter server returning HTML with status 200 is
incorrectly reported as "online", masking the real connectivity problem.

**Before implementing**, check the actual `/health` response shape from
`follow-api` source at `/home/yoseforb/pkg/follow/follow-api/` to confirm the
exact JSON body (expected: something like `{"status":"ok"}` or similar). Use
whatever the real server returns as the authoritative shape.

Extend the probe to:
1. Validate `content-type` contains `application/json` or `+json`
   (case-insensitive).
2. As a body sniff fallback: reject if `response.body.trimLeft().startsWith('<')`.
3. Optionally: attempt `jsonDecode` and verify the expected key is present
   (e.g. `body['status'] == 'ok'`). Only add this if the follow-api `/health`
   endpoint reliably returns a fixed shape — do not guess.

On any validation failure (wrong content-type, HTML body, JSON parse error),
treat the result the same as a non-200 status: report connectivity as NOT
connected (or a new `MISCONFIGURED` state if the existing connectivity state
machine supports it cleanly — investigate `connectivity_view_model.dart` before
deciding; if adding a new state requires significant refactoring, report NOT
connected and add a log line instead).

**Acceptance Criteria**:
- A health probe returning HTML `200 OK` causes the app to report NOT connected
  (not "online")
- A health probe returning valid JSON `200 OK` from follow-api continues to
  report connected correctly
- No regression in the happy-path connectivity detection
- `dart analyze` passes with no errors

---

### Task 10 — Update Repository Error Paths to Propagate `code`

**Story Points**: 2
**Repo**: `follow-app`
**Files Affected**:
- Modified: `lib/data/repositories/route_repository.dart`

**Description**:

Update all non-success response branches in `HttpRouteRepository` to propagate
the `code` field from `DomainErrorResult` into `RouteException`.

The pattern in each error branch:

```dart
final ApiErrorParseResult parsed = parseApiError(
  response.statusCode,
  response.body,
);
if (parsed is DomainErrorResult) {
  throw RouteException(
    parsed.message,
    statusCode: response.statusCode,
    responseBody: response.body,
    isDomainError: true,
    code: parsed.code,    // NEW: propagate the code
  );
} else {
  throw RouteException(
    'Failed to [operation]',
    statusCode: response.statusCode,
    responseBody: response.body,
  );
}
```

Affected methods: all non-success branches in `HttpRouteRepository`
(`prepareRoute()`, `createRouteWithWaypoints()`, `getRoute()`, `listRoutes()`,
`listDiscoveryRoutes()`, `deleteRoute()`, `publishRoute()`).

**Acceptance Criteria**:
- `RouteException` thrown from `createRouteWithWaypoints()` on a 400 response
  with `"code": "ROUTE_TOO_MANY_WAYPOINTS"` has `code == 'ROUTE_TOO_MANY_WAYPOINTS'`
- `RouteException` thrown on a 500 response has `code == ''`
- `dart analyze` passes

---

### Task 11 — Add Localization Strings for Known Error Codes

**Story Points**: 3
**Repo**: `follow-app`
**Files Affected**:
- Modified: `lib/l10n/app_en.arb`
- Modified: `lib/l10n/app_he.arb`

**Description**:

Add ARB localization keys for each known error code. These are the pre-translated
strings that the client shows instead of the raw server English message when a
code is recognized.

The naming convention for ARB keys is `error` + PascalCase(code). Examples:
- `ROUTE_TOO_MANY_WAYPOINTS` → `errorRouteTooManyWaypoints`
- `MAX_PENDING_ROUTES_EXCEEDED` → `errorMaxPendingRoutesExceeded`
- `ROUTE_NOT_FOUND` → `errorRouteNotFound`

**Priority subset for MVP** (implement these first; the rest can be added
incrementally as they become visible to users):

User-facing during route creation:
- `errorRouteTooManyWaypoints` — "Route cannot have more than 10 waypoints"
- `errorWaypointsTooMany` — "Too many waypoints — maximum is 10"
- `errorWaypointsMinimumRequired` — "At least 2 waypoints are required"
- `errorMaxPendingRoutesExceeded` — "You have reached the limit of 3 saved routes"
- `errorMaxWaypointsPerRouteExceeded` — "This route is at the waypoint limit (10)"
- `errorRouteNameEmpty` — "Route name cannot be empty"
- `errorRouteNameTooLong` — "Route name is too long"
- `errorRouteDescriptionTooLong` — "Route description is too long"
- `errorAnonymousUserLimitExceeded` — "You have reached your route limit"
- `errorPendingRoutesLimitExceeded` — "Too many pending routes"
- `errorWaypointsLimitExceeded` — "You have reached your waypoint limit"

User-facing during route management:
- `errorRouteNotFound` — "This route no longer exists"
- `errorRouteNotOwnedByUser` — "You do not have permission to modify this route"
- `errorRouteNotPending` — "This route is not in the right state for this action"
- `errorRouteNotReady` — "Route processing is not complete yet"
- `errorRouteAlreadyExpired` — "This route has expired"

User-facing during image replacement:
- `errorWaypointNotFound` — "This waypoint no longer exists"
- `errorImageSizeInvalid` — "Image file size is invalid"
- `errorImageContentTypeInvalid` — "Image format is not supported"
- `errorImageValidationFailed` — "Image validation failed"

Add each key to `app_en.arb` with `@` metadata, and to `app_he.arb` with a
Hebrew translation.

Hebrew translations must follow the 20 mandatory rules in
`ai-docs/infrastructure/hebrew-translation-guidelines.md`. All Hebrew strings
require human review before production.

After editing both ARB files, run `flutter gen-l10n` to regenerate
`lib/l10n/app_localizations.dart`.

**Acceptance Criteria**:
- All MVP-priority ARB keys are present in both `app_en.arb` and `app_he.arb`
- `flutter gen-l10n` succeeds
- `dart analyze` passes
- Hebrew strings follow the translation guidelines

---

### Task 12 — Add Code-Based Localization Lookup

**Story Points**: 2
**Repo**: `follow-app`
**Files Affected**:
- New: `lib/data/repositories/api_error_localizer.dart`

**Description**:

Create a pure-Dart utility that maps a `code` string to a localized error
message string using the ARB-generated `AppLocalizations` instance.

```dart
/// Returns a localized error message for the given [code], or null if the
/// code is unknown or empty.
///
/// Returns null (not a fallback string) so the caller can decide what
/// fallback to apply (server message, generic string, etc.).
///
/// Requires a valid [AppLocalizations] instance — typically obtained from
/// the widget context. In ViewModels that lack BuildContext, pass the
/// localizations instance from the UI layer when the error is displayed.
String? localizedMessageForCode(String code, AppLocalizations l10n) {
  switch (code) {
    case ApiErrorCodes.routeTooManyWaypoints:
      return l10n.errorRouteTooManyWaypoints;
    case ApiErrorCodes.waypointsTooMany:
      return l10n.errorWaypointsTooMany;
    case ApiErrorCodes.waypointsMinimumRequired:
      return l10n.errorWaypointsMinimumRequired;
    case ApiErrorCodes.maxPendingRoutesExceeded:
      return l10n.errorMaxPendingRoutesExceeded;
    // ... all MVP codes from Task 11 ...
    default:
      return null; // unknown code — let caller apply fallback
  }
}
```

This function is pure: no side effects, no Flutter Widget dependency (it
only uses `AppLocalizations` which is a generated Dart class).

**Acceptance Criteria**:
- `localizedMessageForCode(ApiErrorCodes.routeTooManyWaypoints, l10n)` returns
  the non-null English string when invoked with the English `AppLocalizations`
- `localizedMessageForCode('UNKNOWN_CODE', l10n)` returns `null`
- `localizedMessageForCode('', l10n)` returns `null`
- `dart analyze` passes

---

### Task 13 — Update Error Display in `RouteCreationScreen` to Use Code Lookup

**Story Points**: 2
**Repo**: `follow-app`
**Files Affected**:
- Modified: `lib/ui/route_creation/route_creation_screen.dart`
- Modified: `lib/ui/route_creation/route_creation_view_model.dart`

**Description**:

Update the `domainError` case in `_localizeUploadError()` to first attempt a
code-based localized string before falling back to the server `message` field.

In `route_creation_screen.dart`, the `_localizeUploadError()` method for the
`domainError` case becomes:

```dart
UploadErrorType.domainError =>
  // Prefer a pre-translated localized string for the specific code.
  // Falls back to the server's English message, then the generic string.
  localizedMessageForCode(viewModel.domainErrorCode ?? '', l10n) ??
  viewModel.domainErrorMessage ??
  l10n.uploadErrorClientRequest,
```

In `route_creation_view_model.dart`, add the domain error fields:

```dart
String? _domainErrorCode;

String? get domainErrorCode => _domainErrorCode;
```

Populate it in the catch block:

```dart
} on Exception catch (e) {
  _uploadErrorType = classifyUploadError(e);
  if (_uploadErrorType == UploadErrorType.domainError && e is RouteException) {
    _domainErrorMessage = e.message;
    _domainErrorCode = e.code.isNotEmpty ? e.code : null;  // NEW
  } else {
    _domainErrorMessage = null;
    _domainErrorCode = null;                               // NEW
  }
  logError('Route upload failed', e);
  return false;
}
```

Clear `_domainErrorCode` wherever `_domainErrorMessage` is cleared
(`initializeRoute()`, `clearError()`, `reset()`, before upload starts).

**Acceptance Criteria**:
- When upload fails with code `ROUTE_TOO_MANY_WAYPOINTS`, the error banner shows
  the localized string for that code (not the raw server message)
- When upload fails with an unknown code (or no code), falls back to server
  `message` → generic fallback chain
- `clearError()` resets both `domainErrorMessage` and `domainErrorCode` to null
- `dart analyze` passes

---

### Task 14 — Update `BaseViewModel.executeOperation` to Use Code Lookup

**Story Points**: 1
**Repo**: `follow-app`
**Files Affected**:
- Modified: `lib/ui/base/base_view_model.dart`

**Description**:

Update `executeOperation()` and `executeOperationWithResult()` to attempt a
code-based localized string lookup before using the raw server message.

Because `BaseViewModel` has no `BuildContext`, the `AppLocalizations` instance
must be passed as a parameter or the ViewModel must store a reference. Follow
the project's existing pattern for this (check if `AppLocalizations? _l10n` is
already used in the codebase; if so, follow that pattern).

If the project's ViewModels do not yet have an `AppLocalizations` reference,
implement the simpler fallback: when `isDomainError == true` and `code` is
non-empty, store the code in `_errorCode` and let the UI layer perform the
code-to-localized-string translation when rendering `errorMessage`. Document
this clearly so the UI layer knows to call `localizedMessageForCode()`.

This task may be deferred after Task 13 ships if the simpler server-message
approach already provides sufficient quality.

**Acceptance Criteria**:
- Errors from `RouteListViewModel.deleteRoute()` with code
  `ROUTE_NOT_OWNED_BY_USER` show a localized string (or the server message as
  fallback if localization is deferred)
- `dart analyze` passes

---

### Task 15 — Unit Tests for Dart Error Code Layer

**Story Points**: 2
**Repo**: `follow-app`
**Files Affected**:
- New: `test/data/repositories/api_error_codes_test.dart`
- New: `test/data/repositories/api_error_localizer_test.dart`
- Modified: `test/data/models/api_error_response_test.dart` (add `code` cases)
- Modified: `test/data/repositories/api_error_parser_test.dart` (add `code` cases)
- Modified: `test/ui/route_creation/route_creation_view_model_test.dart`
  (add `domainErrorCode` cases)

**Description**:

Unit tests for all new Dart behavior introduced in Tasks 6–14.

**`api_error_codes_test.dart`** — sanity check that constants have expected values:
```dart
test('routeNotFound code matches contract', () {
  expect(ApiErrorCodes.routeNotFound, equals('ROUTE_NOT_FOUND'));
});
// One test per group is sufficient; full exhaustive testing is unnecessary
// for string constants.
```

**`api_error_localizer_test.dart`** — table-driven tests:
- Known code → non-null localized string
- Empty string → null
- Unknown string → null
- All MVP codes from Task 11 have a non-null result

**`api_error_response_test.dart` additions**:
- `fromJson` with `code` present → `code` is populated
- `fromJson` without `code` → `code` defaults to `''`
- `hasCode == true` when code is non-empty
- `hasCode == false` when code is empty

**`api_error_parser_test.dart` additions**:
- Response with `code` field → `DomainErrorResult.code` matches
- Response without `code` field → `DomainErrorResult.code` is `''`

**`route_creation_view_model_test.dart` additions**:
- Upload failure with code-carrying `RouteException` → `domainErrorCode` is non-null
- Upload failure without code → `domainErrorCode` is null
- `clearError()` resets `domainErrorCode`

**Acceptance Criteria**:
- `flutter test --coverage` passes
- Coverage on new files >= 90%
- All pre-existing tests continue to pass

---

### Task 16 — Manual QA Verification

**Story Points**: 1
**Files Affected**: None (testing only)

**Description**:

Manual testing confirming end-to-end behavior after all tasks complete.
Requires follow-api and follow-app running locally together.

**Scenario 1 — Too many waypoints (400 + code, localized)**:
1. Attempt to upload a route with 11 waypoints
2. Confirm HTTP response body contains `"code": "ROUTE_TOO_MANY_WAYPOINTS"`
3. In Flutter client (English): error banner shows pre-translated English string
   from ARB, not the raw server message
4. In Flutter client (Hebrew): error banner shows the Hebrew ARB string

**Scenario 2 — Anonymous user at route limit (403 + code, localized)**:
1. Create an anonymous user and reach the 3-route pending limit
2. Attempt to upload a 4th route
3. Confirm HTTP response body contains `"code": "MAX_PENDING_ROUTES_EXCEEDED"`
4. Flutter client shows the localized string for that code

**Scenario 3 — Route not found (404 + code, localized)**:
1. Navigate to a non-existent route ID
2. Confirm HTTP response body contains `"code": "ROUTE_NOT_FOUND"`
3. Flutter client shows the localized route not found string

**Scenario 4 — Server fault (500, empty code)**:
1. Simulate a 500 error
2. Confirm HTTP response body contains `"code": ""` (not absent, not a code)
3. Flutter client shows generic "Something went wrong" message (not a domain string)

**Scenario 5 — Older server version (no `code` field)**:
1. Simulate a response body that lacks the `code` field entirely
2. Flutter client shows server `message` as fallback (existing behavior)
3. No crash or null error

**Scenario 6 — RTL (Hebrew) layout**:
1. Switch app to Hebrew
2. Reproduce Scenario 1 or 2
3. Error message displays correctly in RTL layout

**Acceptance Criteria**:
- All 6 scenarios behave as described
- No technical strings (class names, stack traces) visible in any scenario
- Both English and Hebrew locales function correctly

---

## Implementation Order

### Dependency Chain

```
Task C (ConnectivityService hardening) — independent, ships first
                                          (fixes HTML-crash bug immediately)

Task B (Content-type/body-sniff validation) — independent of error-code contract
  └─> Task A (ApiException base class) — prerequisite for Task B's isFormatError
        └─> [integrate into Dart chain below]

Task 0 (Consolidate errors) — must come first
  └─> Task 1 (Contract doc)
        ├─> Task 2 (Go constants)
        │     └─> Task 3 (Go formatter)
        │           └─> Task 4 (Go formatter tests)
        │                 └─> Task 5 (Go integration tests)
        │
        └─> Task 6 (Dart constants)
              ├─> Task 7 (ApiErrorResponse.code)
              │     └─> Task B (shape validation — folds here; removes FormatException workaround)
              │           └─> Task 8 (ApiErrorParser.code)
              │                 └─> Task A (ApiException base class)
              │                       └─> Task 9 (RouteException → ApiException constructor wire-up)
              │                             └─> Task 10 (Repository error paths)
              │
              └─> Task 11 (ARB localization strings — includes apiErrorFormatInvalid from Task B)
                    └─> Task 12 (Localizer utility)
                          ├─> Task 13 (RouteCreationScreen display)
                          └─> Task 14 (BaseViewModel display)
                                └─> Task 15 (Dart tests)
                                      └─> Task 16 (Manual QA)
```

### Recommended Sequential Order for Single Agent

**IMMEDIATE — ship first, independent of full contract rollout**:
- Task C (ConnectivityService hardening) — 1sp, self-contained, fixes false
  "online" from imposter server. No dependencies on other tasks.
- Task B (Shape validation + FormatException workaround removal) — 3sp, requires
  Task A for the `isFormatError` field. Together Tasks A+B fix the HTML-crash
  bug. **Ship Tasks A → B before starting the main contract chain.**

**Go work (Tasks 0–5)**: 0 → 1 → 2 → 3 → 4 → 5

**Dart work (Tasks 6–16)**: 6 → 7 → B → 8 → A → 9 → 10 → 11 → 12 → 13 → 14 → 15 → 16

Tasks 6–10 are prerequisites for Tasks 11–15. Tasks 11–12 can be done in
parallel with Tasks 13–14 if using two agents.

### Parallel Agent Split

If splitting across agents:
- **backend-api-engineer**: Tasks 0, 1, 2, 3, 4, 5
- **frontend-flutter-engineer**: Tasks C, A, B (first, immediately), then
  6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16

The backend work (Tasks 0–5) is a prerequisite for fully testing the Flutter
work, but the Flutter implementation (Tasks 6–14) can be written and unit-tested
against mock responses before the backend ships. Tasks C, A, B are fully
independent of the backend and should ship first.

---

## Relationship to Existing Plans

### `route-domain-error-mapping-plan.md`

The Go `mapRouteError()` refactor (replacing string matching with `errors.Is()`)
is already complete per the commit history. The error formatter in Task 3 of this
plan depends on `goa.ServiceError.Unwrap()` returning the domain error — which
requires the `errors.Is()` pattern to be in place. This dependency is already
satisfied.

### Supersedes: `error-api-mapper.md`

The plan `ai-docs/planning/backlog/error-api-mapper.md` was deleted (`git rm`)
on 2026-04-09. It proposed reading a lowercase `body['error']` field (e.g.
`route_state_error`) from today's server shape and mapping it to 13 localized
messages. That approach was a workaround against the current server's lack of a
stable code field. This plan obsoletes it entirely: the proper SCREAMING_SNAKE_CASE
`code` field (added via the Goa formatter in Task 3) is the correct mechanism,
and the ~55-code contract with Dart constants and `api_error_localizer.dart`
(Tasks 6, 12) replaces the ad-hoc 13-message mapping.

---

## Out of Scope

- **Server-side message translation**: The server continues to return English
  `message` fields. Translated messages come from the Flutter ARB files, not
  the server.
- **User domain error codes**: User-domain errors (`/api/v1/users/*`) are not
  covered by this plan. The formatter will produce `code: ""` for user service
  errors. User domain codes can be added in a follow-on plan when user-facing
  error quality in those screens becomes a priority.
- **Image gateway error codes**: Gateway errors flowing through SSE (`"failed"`
  events) use a separate `error_code` field already defined in the Valkey
  contract. That is a different contract.
- **Analytics / error reporting**: Instrumenting specific error codes for
  analytics is a separate concern.
- **Retry logic per error code**: Some codes (e.g. `ROUTE_NOT_READY`) might
  benefit from automatic retry. That is a separate concern.
- **Goa DSL regeneration**: This plan deliberately avoids modifying the Goa
  DSL and regenerating `gen/`. The formatter approach achieves the goal without
  touching generated code.
- **Migrating to `dio` or adding an interceptor stack**: The `http` package
  with a single `ApiErrorParser` choke point is sufficient. No HTTP client
  migration is in scope.
- **Full interceptor / middleware stack**: A shared `ApiException` base class
  plus centralized pre-decode validation in `ApiErrorParser` is the correct
  scope. Additional abstraction layers are not justified by current needs.

---

## Files Summary

### New Files (follow-api)

| File | Purpose |
|------|---------|
| `ai-docs/contracts/api-error-codes-contract.md` | Contract source of truth |
| `internal/api/services/error_codes.go` | Go error code constants |
| `internal/api/server/error_formatter.go` | Goa error formatter (adds `code` field) |
| `internal/api/server/error_formatter_test.go` | Unit tests for formatter |

### Modified Files (follow-api)

| File | Change |
|------|--------|
| `internal/api/server/goa_server.go` | Pass `NewErrorFormatter()` to all `goaXxx.New()` calls |

### New Files (follow-app)

| File | Purpose |
|------|---------| 
| `lib/data/repositories/api_exception.dart` | Shared `ApiException` base class (Task A) |
| `lib/data/repositories/api_error_codes.dart` | Dart error code constants (Task 6) |
| `lib/data/repositories/api_error_localizer.dart` | Code → localized string lookup (Task 12) |
| `test/data/repositories/api_error_codes_test.dart` | Constant value tests (Task 15) |
| `test/data/repositories/api_error_localizer_test.dart` | Localizer logic tests (Task 15) |
| `test/data/repositories/api_error_parser_shape_validation_test.dart` | Shape validation edge cases (Task B) |

### Modified Files (follow-app)

| File | Change |
|------|--------|
| `lib/data/models/api_error_response.dart` | Add `code` field and `hasCode` getter (Task 7) |
| `lib/data/repositories/api_error_parser.dart` | Add shape validation + `FormatErrorResult`; surface `code` in `DomainErrorResult` (Tasks B, 8) |
| `lib/data/repositories/auth_repository.dart` | Extend `AuthException` from `ApiException`; remove `FormatException` workaround at lines 205, 314 (Tasks A, B) |
| `lib/data/repositories/route_repository_models.dart` | Extend `RouteException` from `ApiException` (Tasks A, 9) |
| `lib/data/repositories/route_repository.dart` | Propagate `code` and `isFormatError` in error paths (Task 10) |
| `lib/data/services/connectivity_service.dart` | Validate content-type + body shape in health probe (Task C) |
| `lib/ui/connectivity/connectivity_view_model.dart` | Possibly add MISCONFIGURED state (Task C — investigate during implementation) |
| `lib/ui/route_creation/route_creation_view_model.dart` | Add `_domainErrorCode` field (Task 13) |
| `lib/ui/route_creation/route_creation_screen.dart` | Code-based lookup before message fallback (Task 13) |
| `lib/ui/base/base_view_model.dart` | Code-aware error message in `executeOperation` (Task 14) |
| `lib/l10n/app_en.arb` | ~20 error code keys + `apiErrorFormatInvalid` (Tasks B, 11) |
| `lib/l10n/app_he.arb` | ~20 error code keys + `apiErrorFormatInvalid` in Hebrew (Tasks B, 11) |
| `test/data/models/api_error_response_test.dart` | Add `code` field test cases (Task 15) |
| `test/data/repositories/api_error_parser_test.dart` | Add `code` propagation + shape validation cases (Tasks B, 15) |
| `test/ui/route_creation/route_creation_view_model_test.dart` | Add `domainErrorCode` cases (Task 15) |

---

## Quality Gates

### follow-api (after every task)

```bash
gofumpt -w . && golines -w --max-len=80 .
go vet ./...
./custom-gcl run -c .golangci-custom.yml ./... --fix
go test -race -cover ./...
go mod tidy
go run ./cmd/server -runtime-timeout 10s
```

### follow-app (after every task)

```bash
dart format .
dart analyze          # MUST show "No errors"
dart fix --apply
flutter test --coverage   # Must pass, coverage >= 80%
flutter gen-l10n          # After any ARB file change
```
