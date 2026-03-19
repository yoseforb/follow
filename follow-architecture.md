# Follow Platform — System Architecture Reference

This document describes the Follow platform architecture in detail. It is derived from
integration test observations and codebase analysis. A new developer should be able to
read this document end-to-end and understand how every piece fits together.

---

## Table of Contents

1. [System Overview](#1-system-overview)
2. [Service Map](#2-service-map)
3. [API Endpoints](#3-api-endpoints)
4. [follow-api Architecture](#4-follow-api-architecture)
5. [follow-image-gateway Architecture](#5-follow-image-gateway-architecture)
6. [follow-pkg Shared Contracts](#6-follow-pkg-shared-contracts)
7. [Inter-Service Communication](#7-inter-service-communication)
8. [Entity Lifecycle & Aggregates](#8-entity-lifecycle--aggregates)
9. [Data Flows](#9-data-flows)
10. [Event Bus & Cascade Patterns](#10-event-bus--cascade-patterns)
11. [Background Systems](#11-background-systems)
12. [SSE Real-Time Streaming](#12-sse-real-time-streaming)
13. [Error Handling Patterns](#13-error-handling-patterns)
14. [Timing Reference](#14-timing-reference)

---

## 1. System Overview

Follow is an image-based navigation platform. Users create routes composed of
sequential waypoint photographs with directional markers. Other users navigate
by following the photo sequence — no GPS required.

The platform consists of two Go backend services, a Flutter mobile app, and
shared Go utilities:

```
┌─────────────┐       REST/SSE        ┌──────────────┐
│  follow-app  │◄────────────────────►│  follow-api   │
│  (Flutter)   │                      │  (port 8080)  │
└──────┬───────┘                      └──────┬────────┘
       │                                     │
       │  HTTP PUT + Bearer JWT              │  Ed25519 JWT signing
       │                                     │  Valkey consumer
       ▼                                     │  PostgreSQL
┌──────────────────────┐                     │  MinIO (presigned URLs)
│ follow-image-gateway │                     │
│     (port 8090)      │◄────────────────────┘
└──────────┬───────────┘   Valkey Streams
           │                (image:result)
           │
           ▼
      MinIO (upload processed images)
```

**Infrastructure dependencies**: PostgreSQL, MinIO (S3-compatible object store),
Valkey (Redis-compatible, BSD-3-Clause).

---

## 2. Service Map

### 2.1 follow-api (Backend API Server)

| Aspect        | Detail |
|---------------|--------|
| Language      | Go |
| Port          | 8080 (configurable) |
| Framework     | Goa-Design (HTTP API DSL) |
| Architecture  | Modular-monolithic, DDD, Clean/Hexagonal |
| Database      | PostgreSQL (domain-separated schemas: `route`, `user`, `images`) |
| Storage       | MinIO — presigned download URLs, object verification |
| Messaging     | Valkey Redis Streams consumer (reads `image:result`) |
| Event Bus     | Watermill GoChannel (in-memory, non-persistent) |
| Auth          | Symmetric JWT (user auth), Ed25519 JWT signing (upload tokens) |

### 2.2 follow-image-gateway (Image Processing Microservice)

| Aspect        | Detail |
|---------------|--------|
| Language      | Go |
| Port          | 8090 (configurable) |
| Framework     | Goa-Design |
| Architecture  | Pipes and Filters (4-stage pipeline) |
| Storage       | MinIO — uploads processed WebP images |
| Messaging     | Valkey — publishes to `image:result` stream |
| Auth          | Ed25519 JWT verification (public key only) |
| ML            | ONNX Runtime — SCRFD face detection, YOLOv11 license plate detection |
| Image         | vipsgen (libvips bindings) — 4-8x faster than Go stdlib |

### 2.3 follow-pkg (Shared Go Utilities)

Imported by both Go services. Contains:

- Valkey contract constants (key patterns, field names, status values)
- Consumer, Reclaimer, MessageProcessor implementations
- UploadGuard, ProgressTracker, Producer utilities
- Structured logger configuration

### 2.4 follow-app (Flutter Mobile App)

| Aspect        | Detail |
|---------------|--------|
| Language      | Dart/Flutter |
| Architecture  | MVVM with Provider + ChangeNotifier |
| Navigation    | go_router (declarative) |
| Storage       | Hive (local), FlutterSecureStorage (credentials) |
| Platforms     | Android (primary), iOS, Web Mobile |

---

## 3. API Endpoints

### 3.1 follow-api (port 8080)

All API endpoints (except health) require JWT authentication via `Authorization: Bearer <token>` header.

#### Authentication Service

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `POST` | `/api/v1/auth/refresh` | JWT | Refresh JWT token. Generates a new access token with the same user information. |

**Responses**: 200 (new token), 401 (unauthorized)

#### User Service

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `POST` | `/api/v1/users/anonymous` | None | Create a new anonymous user. Returns user_id + JWT token. |
| `GET` | `/api/v1/users/anonymous/{user_id}` | JWT | Get anonymous user details by ID. |
| `DELETE` | `/api/v1/users/anonymous/{user_id}` | JWT | Delete an anonymous user. User can only delete their own account. Triggers async cascade (routes → images → storage cleanup). |

**Responses**: 200 (success), 400 (invalid input), 401 (unauthorized), 403 (forbidden), 404 (not found), 500 (creation failed)

#### Admin Service (development only)

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/api/v1/users/admin/stats` | JWT | Get user statistics and metrics. |
| `GET` | `/api/v1/users/admin/anonymous` | JWT | List anonymous users with pagination. Query params: `limit`, `offset`, `created_after`. |

**Responses**: 200 (success), 401 (unauthorized), 403 (forbidden), 500 (internal error)

#### Route Service — Lifecycle

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `POST` | `/api/v1/routes/prepare` | JWT | Prepare route creation. Validates user, checks pending route limit, allocates route UUID. Returns `route_id`. |
| `POST` | `/api/v1/routes/{route_id}/create-waypoints` | JWT | Create route with waypoints in PENDING state. Creates image entities, signs Ed25519 upload tokens, sets initial Valkey status. Returns `waypoint_ids[]` + `presigned_urls[]` (each with `upload_url` + `upload_token`). |
| `POST` | `/api/v1/routes/{route_id}/publish` | JWT | Publish route (READY → PUBLISHED). Route must have completed all image processing. Makes route navigable. |
| `GET` | `/api/v1/routes/{route_id}` | JWT | Get route details. Query params: `include_images` (boolean, adds presigned download URLs), `password` (for password-protected routes). |
| `PUT` | `/api/v1/routes/{route_id}` | JWT | Update route metadata (location name, description, visibility, access method). |
| `DELETE` | `/api/v1/routes/{route_id}` | JWT | Delete route with ownership validation. Triggers async cascade (images → storage cleanup). |
| `GET` | `/api/v1/routes` | JWT | List routes with filtering and pagination. Query params: `discovery_mode` (false=own routes, true=others' public routes), `visibility`, `access_method`, `route_status` (default: published), `location_name`, `address`, `description`, `start_point`, `end_point`, `navigable_only`, `page`, `page_size`. |

**Responses**: 200 (success), 400 (validation failed), 401 (unauthorized), 403 (not owner / limit exceeded), 404 (not found), 422 (invalid route state), 500 (storage error)

#### Route Service — Waypoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `PUT` | `/api/v1/routes/{route_id}/waypoints/{waypoint_id}` | JWT | Update waypoint properties (description, marker coordinates, marker type). |

**Responses**: 200 (success), 400 (validation failed), 401 (unauthorized), 403 (limit exceeded), 404 (not found), 422 (invalid route state), 500 (storage error)

#### Route Service — Image Management

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `POST` | `/api/v1/routes/{route_id}/waypoints/{waypoint_id}/replace-image/prepare` | JWT | Prepare waypoint image replacement. Validates ownership, creates new image entity, returns `image_id` + `upload_url` + `upload_token` + `expires_at`. Route stays PUBLISHED during replacement. |
| `POST` | `/api/v1/routes/{route_id}/waypoints/{waypoint_id}/retry-upload` | JWT | Retry upload for a failed waypoint image. Clears failed state, returns fresh upload URL. |
| `GET` | `/api/v1/routes/{route_id}/images/{image_id}/status` | JWT | Get processing status for a single image within a route. Caller must own the route. |

**Responses**: 200 (success), 400 (validation failed), 401 (unauthorized), 403 (not owner / limit exceeded), 404 (not found), 422 (invalid route state), 500 (storage error)

#### Route Service — Real-Time Streaming

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/api/v1/routes/{route_id}/status/stream` | JWT | SSE stream of real-time image processing status. Polls Valkey every 500ms, emits `processing`, `ready`, `failed`, and `complete` events. Max duration: 5 minutes. Heartbeat every 30s. |

**Event format** (Server-Sent Events):
```
event: ready
data: {"event_type":"ready","image_id":"<uuid>","status":"ready","timestamp":"2026-03-18T21:26:43Z"}
```

**Responses**: 101 (SSE stream), 401 (unauthorized), 403 (not owner), 404 (not found), 422 (invalid state), 500 (error)

#### Health Service

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/health` | None | General health check. Returns server status. |
| `GET` | `/health/db` | None | Database connectivity check. Pings PostgreSQL, verifies schemas. |
| `GET` | `/health/storage` | None | Storage system check. Verifies MinIO bucket accessibility. |

**Responses**: 200 (healthy), 503 (service unavailable)

---

### 3.2 follow-image-gateway (port 8090)

#### Upload Service

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `PUT` | `/api/v1/upload` | Ed25519 JWT | Upload an image for processing. Accepts raw binary image data. JWT claims specify `image_id`, `storage_key`, `max_file_size`, `content_type`. Image is validated, analyzed (ML detection), transformed (resize + WebP), and uploaded to MinIO. Returns immediately with 202 — processing is asynchronous. |

**Request**:
```
PUT /api/v1/upload
Authorization: Bearer <Ed25519 JWT signed by follow-api>
Content-Type: application/octet-stream
Body: raw image bytes
```

**Responses**:
| Status | Error Name | Description |
|--------|-----------|-------------|
| 202 | — | Accepted. Image received, processing started asynchronously. |
| 400 | `bad_request` | Malformed request or missing required parameters. |
| 401 | `unauthorized` | Upload token is invalid or expired. |
| 409 | `conflict` | Upload already in progress for this image (duplicate guard). |
| 413 | `payload_too_large` | File exceeds maximum allowed size (min of 10MB global, token claim). |
| 503 | `service_unavailable` | Pipeline unavailable or submission timeout (30s). |

#### Health Service

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/health` | None | General health check. Verifies Redis, MinIO, and Valkey connectivity. |
| `GET` | `/health/ready` | None | Readiness check. Same checks as general health — indicates service can accept uploads. |

**Responses**: 200 (healthy), 503 (service unavailable)

---

## 4. follow-api Architecture

### 4.1 Module System

The API server uses a modular-monolithic design with three domain modules:

```
cmd/server/
  app.go              ← Application bootstrap, lifecycle
  modules_factory.go  ← Module registration, dependency validation

internal/
  domains/
    route/             ← Route aggregate, waypoints, route lifecycle
    user/              ← Anonymous users, authentication
    image/             ← Image entities, upload preparation, status tracking
  infrastructure/
    database/          ← PostgreSQL connection pool, migrations, schemas
    storage/           ← MinIO client, presigned URLs
    eventbus/          ← Watermill GoChannel event bus
    consumers/         ← Valkey stream consumer, result processor
    subscribers/       ← In-memory event handlers (cascade cleanup)
    publishers/        ← Domain event publishing
    streaming/         ← SSE streamer, Valkey poller
    scheduler/         ← Background job scheduler
    reaper/            ← Stale image reaper
  api/
    services/          ← Goa service implementations
    server/            ← Goa HTTP server, middleware
```

### 4.2 Startup Sequence

The server boots in a strict dependency order. Each step must succeed before the
next begins:

```
1. Logger initialization
2. Configuration loading (Viper + CLI flags)
3. Database initialization
   a. Create PostgreSQL connection pool (retry: 5 attempts, 500ms→30s backoff)
   b. Create schemas (route, user, images)
   c. Run migrations (idempotent, skip already-applied)
4. MinIO storage client initialization
   a. Connect to MinIO endpoint
   b. Verify bucket exists (create if missing)
5. Valkey client initialization
   a. Ping Valkey server
6. Stale image reaper start (background goroutine)
7. JWT service initialization (Ed25519 key pair for upload tokens)
8. Upload URL generator initialization
9. Event bus initialization (Watermill GoChannel)
   a. Configure middleware: retry (5 max, 1s→16s backoff), poison queue, timeout (30s)
   b. Buffer size: 64 messages
10. Modules factory initialization
    a. Validate all module dependencies (DB, storage, event bus health checks)
    b. Register Image module
    c. Register User module
    d. Register Route module
11. Image result consumer initialization
    a. Create consumer group `api-workers` on `image:result` stream
    b. Start consumer `api-1` (XREADGROUP loop)
    c. Start reclaimer `api-1-reclaimer` (XAUTOCLAIM loop)
12. Event subscriber registration
    a. Subscribe to `domain.route.deleted` → RouteDeletedHandler
    b. Subscribe to `domain.user.anonymous.deleted` → UserDeletedHandler
    c. Subscribe to `domain.image.deleted` → ImageDeletedHandler
    d. Subscribe to `domain.image.upload.prepared` → ImageUploadPreparedHandler
13. Background job scheduler start
    a. image-cleanup: every 5m, max 30m duration
    b. orphan-image-scan: every 15m, max 10m duration
    c. route-cleanup: every 10m, max 10m duration
    d. orphan-storage-scan: every 1h, max 30m duration
14. Goa HTTP server start (blocking)
```

### 3.3 Shutdown Sequence

Graceful shutdown proceeds in reverse dependency order:

```
1. Signal received (SIGTERM/SIGINT)
2. Stop reclaimer (cancel context, wait for scan to complete)
3. Stop background scheduler (cancel all jobs, wait with timeout)
4. Stop stale image reaper (cancel context)
5. Stop consumer supervisor (cancel XREADGROUP loop, wait for in-flight)
6. Shutdown Goa HTTP server (drain active connections)
7. Close event bus
   a. Cancel all subscriptions (4 topics)
   b. Wait for in-flight handlers to complete (10s timeout)
8. Close database connection pool
```

### 3.4 Database Schema

Three PostgreSQL schemas provide domain isolation:

| Schema   | Tables | Purpose |
|----------|--------|---------|
| `route`  | routes, waypoints | Route aggregates with optimistic locking |
| `user`   | anonymous_users | Anonymous user entities with in-memory cache |
| `images` | images | Image entities tracking upload/processing state |

Migrations are versioned and idempotent. The migration runner skips already-applied
migrations on startup. Current migrations (in application order):

- v0: create_schemas (global)
- v1: create_images_table
- v2: fix_file_metadata_schema
- v3: create_route_tables
- v4: create_anonymous_users
- v5-v21: incremental schema evolution

---

## 4. follow-image-gateway Architecture

### 4.1 Pipeline Design (Pipes and Filters)

The gateway processes images through a 4-stage concurrent pipeline:

```
Upload Request
     │
     ▼
┌──────────┐    ┌──────────┐    ┌───────────┐    ┌──────────┐
│ Validate │───►│ Analyze  │───►│ Transform │───►│  Upload  │
│ (2 workers)   │ (2 workers)   │ (2 workers)    │ (3 workers)
└──────────┘    └──────────┘    └───────────┘    └──────────┘
                                                       │
                                                       ▼
                                              Result Publisher
                                              (Valkey stream)
```

Each stage runs as independent goroutine workers connected by Go channels:

| Stage      | Workers | Purpose |
|------------|---------|---------|
| **Validate**   | 2 | Magic byte verification, format validation, dimension checks |
| **Analyze**    | 2 | ML inference — SCRFD face detection, YOLOv11 plate detection |
| **Transform**  | 2 | Image resize, format conversion (JPEG→WebP), quality optimization |
| **Upload**     | 3 | Upload processed WebP to MinIO with retry |

### 4.2 Worker Processing Model

Each worker goroutine follows this pattern:

```
for job := range inputChannel {
    jobCtx = context.WithTimeout(pipelineCtx, 5m)

    observer.OnJobStartStage(jobID, stageName)
    err = stage.Process(jobCtx, &job)

    if err != nil {
        observer.OnJobError(jobID, stageName, err, errorCode)
        errCh <- job          // route to error channel
    } else {
        observer.OnJobCompleteStage(jobID, stageName, elapsed)
        stage.Cleanup(&job)   // release memory before next stage
        outputCh <- job       // route to next stage
    }
}
```

Key design decisions:
- **Per-job timeouts**: Each job gets a 5-minute context timeout independent of other jobs
- **Separate error channel**: Failed jobs do not block the success path
- **Memory cleanup**: Each stage implements an optional `Cleaner` interface to release
  buffers between stages, preventing memory accumulation
- **Channel merging**: The last stage output and error channel merge into a single result
  channel consumed by the result publisher

### 4.3 Upload Service (HTTP Handler)

The upload endpoint processes incoming image data:

```
PUT /upload/{image_id}
Authorization: Bearer <Ed25519 JWT>
Content-Type: application/octet-stream
Body: raw image bytes
```

Processing steps:

1. **JWT validation**: Verify Ed25519 signature, extract claims (image_id, storage_key,
   max_file_size, content_type)
2. **File size enforcement**: `min(global_max=10MB, token_claim_max)` via `io.LimitReader`
3. **Upload guard** (Valkey SET NX): Prevent duplicate uploads for the same image_id.
   Returns 409 Conflict if already claimed. Graceful degradation on Valkey errors.
4. **Body reading**: Read entire body into memory. Return 400 on empty, 413 on too large.
5. **Pipeline submission**: Create `ImageJob` and submit to pipeline with 30s timeout.
   Return 503 if pipeline unavailable or submission times out.
6. **Response**: 202 Accepted (processing is asynchronous)

### 4.4 Result Publishing

After pipeline completion (success or failure), the result publisher:

1. **Update Valkey progress hash** (`image:status:{id}`):
   - Success: `stage=done`, `progress=100`
   - Failure: `stage=failed`, `progress=-1`, `error=<message>`
   - Retries up to 3 times with exponential backoff (100ms, 200ms, 400ms)

2. **Publish to Valkey stream** (`image:result`):
   - Success fields: `image_id`, `status=processed`, `storage_key` (now `.webp`),
     `sha256`, `etag`, `file_size`, `content_type=image/webp`,
     `original_width`, `original_height`, `processed_width`, `processed_height`,
     `processed_at` (RFC3339)
   - Failure fields: `image_id`, `status=failed`, `error_code`, `error_message`,
     `failed_at` (RFC3339)
   - Stream trimmed to ~10,000 entries (approximate MAXLEN)

---

## 5. follow-pkg Shared Contracts

All cross-service Valkey contracts are defined as Go constants in
`follow-pkg/valkey/contracts.go`. Both services import these — no string literals.

### 5.1 Key Patterns

| Pattern | Example | Purpose |
|---------|---------|---------|
| `image:status:{id}` | `image:status:f0862db7-...` | Hash: per-image processing progress |
| `image:upload:{id}` | `image:upload:f0862db7-...` | String (SET NX): upload deduplication guard |
| `image:result` | — | Stream: gateway→API result messages |
| `image:result:dlq` | — | Stream: dead-letter queue for failed processing |

### 5.2 Progress Hash Fields (`image:status:{id}`)

| Field | Values | Written By |
|-------|--------|------------|
| `stage` | `queued`, `validating`, `decoding`, `processing`, `encoding`, `uploading`, `done`, `failed` | API (queued), Gateway (all others) |
| `progress` | `0`-`100`, `-1` (failure) | Gateway |
| `updated_at` | RFC3339 timestamp | Both |
| `error` | Error message string | Gateway (on failure), Reaper (on timeout) |

### 5.3 Result Stream Fields (`image:result`)

**Success message**:
```
image_id:         UUID string
status:           "processed"
storage_key:      "images/{id}.webp"
sha256:           hex digest
etag:             MinIO ETag
file_size:        bytes (string)
content_type:     "image/webp"
original_width:   pixels (string)
original_height:  pixels (string)
processed_width:  pixels (string)
processed_height: pixels (string)
processed_at:     RFC3339 timestamp
```

**Failure message**:
```
image_id:         UUID string
status:           "failed"
error_code:       e.g., "INVALID_MAGIC_BYTES", "processing_timeout"
error_message:    human-readable description
failed_at:        RFC3339 timestamp
```

### 5.4 Consumer Group Configuration

| Parameter | Value | Purpose |
|-----------|-------|---------|
| Group name | `api-workers` | Consumer group on `image:result` |
| Consumer name | `api-1` | Individual consumer identity |
| Reclaimer name | `api-1-reclaimer` | XAUTOCLAIM consumer identity |
| Block timeout | 5s | XREADGROUP BLOCK duration |
| Max deliveries | 10 | DLQ threshold |
| Reclaim idle timeout | 5s (test) / 5m (prod) | XAUTOCLAIM min-idle |
| Reclaim scan interval | 2s (test) / 1m (prod) | How often reclaimer runs |

---

## 6. Inter-Service Communication

### 6.1 Communication Protocols

| From | To | Protocol | Mechanism |
|------|----|----------|-----------|
| App → API | REST HTTP | All CRUD operations, auth, SSE streaming |
| App → Gateway | HTTP PUT | Image upload with `Authorization: Bearer <JWT>` |
| API → Gateway | Ed25519 JWT | Upload token (API signs, gateway verifies) |
| Gateway → API | Valkey Stream | `image:result` — asynchronous result publishing |
| Gateway → Valkey | HSET | `image:status:{id}` — progress tracking |
| API → Valkey | HSET | `image:status:{id}` — initial "queued" status |
| API ← Valkey | XREADGROUP | Consume `image:result` messages |
| API ← Valkey | HGETALL | Poll `image:status:{id}` for SSE |
| Gateway → MinIO | S3 PutObject | Upload processed WebP images |
| API → MinIO | S3 StatObject | Verify object existence |
| API → MinIO | S3 PresignedGet | Generate download URLs (24h expiry) |
| API → PostgreSQL | SQL | All entity persistence |

### 6.2 Authentication Flow

Two independent JWT systems operate simultaneously:

**User Authentication** (symmetric):
```
App ──POST /users/anonymous──► API creates user, returns JWT
App ──Authorization: Bearer <JWT>──► API validates on every request
App ──POST /auth/refresh──► API issues new JWT
```

**Upload Authentication** (asymmetric, Ed25519):
```
API signs upload token ──(JWT claims: image_id, storage_key, max_file_size)──►
App receives upload_url + upload_token in create-waypoints response
App ──PUT {upload_url} Authorization: Bearer {upload_token}──► Gateway
Gateway verifies Ed25519 signature with public key
```

The upload token is scoped per-image: each image gets its own JWT with specific
claims. The gateway never communicates with the API directly — it only verifies
the token signature.

---

## 7. Entity Lifecycle & Aggregates

### 7.1 Route Aggregate

The Route is the primary aggregate root. It owns Waypoints, which reference Images.

**Status Transitions**:

```
                 /prepare              /create-waypoints
 (no entity) ───────────► PREPARING ─────────────────────► PENDING
                           (12h TTL)                          │
                                                              │ all images processed
                                                              ▼
                          /publish                          READY
              PUBLISHED ◄──────────────────────────────────   │
                  │                                           │
                  │ (future)                                  │
                  ▼                                           │
              ARCHIVED                                        │
```

- **PREPARING**: Route ID allocated, no waypoints yet. Expires after 12 hours if
  not completed. Enforced limit: 1 pending route per user.
- **PENDING**: Route created with waypoints. Images are being uploaded and processed.
  Cannot be navigated.
- **READY**: All waypoint images processed successfully. Gateway confirmed every image
  via Valkey stream. Can be published.
- **PUBLISHED**: Route is navigable. Users can download images and follow waypoints.
  Image replacement is allowed while published (no downtime).
- **ARCHIVED**: Route removed from navigation (future feature).

**Optimistic Locking**:

The route aggregate carries a `version` field. Every mutation increments the version:

```
v1 (create) → v2 (add waypoints) → v3 (confirm image 1) → v4 (confirm image 2)
→ v5 (all confirmed, PENDING→READY) → v6 (publish) → v7 (update metadata) → ...
```

Concurrent writes are detected by checking `WHERE version = expected_version` in
the UPDATE query. On mismatch, `ErrConcurrentModification` is returned. The image
result processor retries up to 3 times with backoff (50ms, 100ms, 150ms).

**Aggregate Boundaries**:

```
Route (aggregate root)
  ├── location_name, description, address, start_point, end_point
  ├── status (preparing → pending → ready → published → archived)
  ├── visibility (public / private)
  ├── access_method (open / password_protected)
  ├── owner (user_id | group_id | anonymous_session_id)
  ├── version (optimistic locking counter)
  └── Waypoints[] (owned entities, ordered)
        ├── position (0-based sequential)
        ├── description
        ├── image_id (FK → Image entity)
        ├── marker_x, marker_y (relative coordinates 0.0-1.0)
        └── pending_replacement_image_id (nullable, for atomic swap)
```

### 7.2 Image Entity

Images are independent entities (not part of the Route aggregate) managed by the
Image module. They track the full lifecycle from upload preparation to deletion.

**Status Transitions**:

```
                 create-waypoints           gateway pipeline
 (no entity) ──────────────────► PENDING ─────────────────► PROCESSED
                                    │                           │
                                    │ gateway failure           │ accessed
                                    ▼                           ▼
                                 FAILED                    PROCESSED
                                    │                      (with download URL)
                                    │                           │
                                    │ cleanup                   │ route deleted
                                    ▼                           ▼
                                 ARCHIVED                  (deleted from DB + MinIO)
```

**Image Entity Fields**:

```
Image
  ├── id (UUID, generated by API)
  ├── storage_key ("images/{id}.jpg" → "images/{id}.webp" after processing)
  ├── content_type ("image/jpeg" → "image/webp" after processing)
  ├── file_size_bytes (original size from client metadata)
  ├── file_name (original filename from client)
  ├── status (pending → processed / failed → archived)
  ├── sha256 (set after gateway processing)
  ├── etag (MinIO ETag, set after upload)
  ├── original_width, original_height (set after processing)
  ├── processed_width, processed_height (set after processing)
  ├── error_code, error_message (set on failure)
  ├── download_url (presigned MinIO URL, refreshed on access)
  ├── download_url_expires_at
  ├── last_accessed_at
  ├── created_at, updated_at
  └── (no FK to route — linked via waypoint.image_id)
```

**Storage Key Evolution**:

The storage key changes extension after processing:
```
Prepared:  images/f0862db7-c7f1-499d-899f-0fea2d11e7b4.jpg
Processed: images/f0862db7-c7f1-499d-899f-0fea2d11e7b4.webp
```

The API updates the entity's `storage_key` and `content_type` when it receives the
processed result from the Valkey stream.

### 7.3 Anonymous User Entity

```
AnonymousUser
  ├── id (UUID)
  ├── created_at
  └── (in-memory LRU cache for frequently accessed users)
```

Anonymous users are lightweight entities. They exist to:
- Associate routes with a session (ownership)
- Enable JWT-based authentication
- Support future upgrade to registered accounts

The repository maintains an in-memory cache populated on create and read,
invalidated on delete.

### 7.4 Waypoint Image Replacement

Image replacement is an atomic swap that keeps the route published:

```
1. Prepare replacement:
   - Create new Image entity (PENDING)
   - Set waypoint.pending_replacement_image_id = new_image_id
   - Route version incremented

2. Upload & process new image (same pipeline as creation)

3. Consumer receives result:
   - Swap waypoint.image_id = new_image_id
   - Clear waypoint.pending_replacement_image_id
   - Scale marker coordinates for new image dimensions
   - Archive old image
   - Route stays PUBLISHED throughout
```

---

## 8. Data Flows

### 8.1 Route Creation Flow (End-to-End)

```
App                          API                           Gateway              Valkey            MinIO
 │                            │                              │                    │                 │
 │ POST /users/anonymous      │                              │                    │                 │
 │───────────────────────────►│                              │                    │                 │
 │◄── JWT + user_id ──────────│                              │                    │                 │
 │                            │                              │                    │                 │
 │ POST /routes/prepare       │                              │                    │                 │
 │───────────────────────────►│ Check pending route limit    │                    │                 │
 │◄── route_id ───────────────│ Create route (PREPARING)     │                    │                 │
 │                            │                              │                    │                 │
 │ POST /routes/{id}/create-waypoints                        │                    │                 │
 │  {location_name, waypoints: [{image_metadata, marker}]}   │                    │                 │
 │───────────────────────────►│                              │                    │                 │
 │                            │ For each waypoint:           │                    │                 │
 │                            │  1. Create Image entity      │                    │                 │
 │                            │  2. Publish image.upload.prepared event           │                 │
 │                            │     └──► Handler writes      │ HSET              │                 │
 │                            │         initial status ──────┼──► image:status:{id}                │
 │                            │         {stage:queued}        │   {stage:queued}   │                 │
 │                            │  3. Sign Ed25519 JWT         │                    │                 │
 │                            │  4. Generate upload URL      │                    │                 │
 │                            │                              │                    │                 │
 │                            │ Create waypoints in route    │                    │                 │
 │                            │ Route: PREPARING → PENDING   │                    │                 │
 │◄── waypoint_ids[] + presigned_urls[]                      │                    │                 │
 │    [{upload_url, upload_token}]                           │                    │                 │
 │                            │                              │                    │                 │
 │ GET /routes/{id}/status/stream (SSE)                      │                    │                 │
 │───────────────────────────►│ Open SSE connection          │                    │                 │
 │                            │ Start polling Valkey ────────┼──► HGETALL         │                 │
 │                            │ (every 500ms per image)      │   image:status:{id}│                 │
 │                            │                              │                    │                 │
 │ PUT {upload_url}           │                              │                    │                 │
 │  Authorization: Bearer {upload_token}                     │                    │                 │
 │  Body: raw JPEG bytes      │                              │                    │                 │
 │────────────────────────────┼─────────────────────────────►│                    │                 │
 │                            │                              │ Verify JWT         │                 │
 │                            │                              │ Upload guard ──────┼──► SET NX       │
 │                            │                              │ Submit to pipeline │   image:upload:{id}
 │◄── 202 Accepted ───────────┼──────────────────────────────│                    │                 │
 │                            │                              │                    │                 │
 │                            │                              │ VALIDATE stage     │                 │
 │                            │                              │  └─ magic bytes    │                 │
 │                            │                              │  HSET ─────────────┼──► stage:validating
 │                            │                              │                    │                 │
 │                            │                              │ ANALYZE stage      │                 │
 │                            │                              │  └─ ML detection   │                 │
 │                            │                              │  HSET ─────────────┼──► stage:processing
 │                            │                              │                    │                 │
 │◄── SSE: processing ────────│ Poll sees stage change       │                    │                 │
 │                            │                              │                    │                 │
 │                            │                              │ TRANSFORM stage    │                 │
 │                            │                              │  └─ resize, WebP   │                 │
 │                            │                              │                    │                 │
 │                            │                              │ UPLOAD stage       │                 │
 │                            │                              │  └─ PutObject ─────┼──────────────── │─► MinIO
 │                            │                              │                    │                 │
 │                            │                              │ Result publish:    │                 │
 │                            │                              │  HSET ─────────────┼──► stage:done   │
 │                            │                              │  XADD ─────────────┼──► image:result │
 │                            │                              │  {status:processed,│   stream msg    │
 │                            │                              │   storage_key,     │                 │
 │                            │                              │   sha256, ...}     │                 │
 │                            │                              │                    │                 │
 │                            │ XREADGROUP ◄─────────────────┼────────────────────│                 │
 │                            │ Process result:              │                    │                 │
 │                            │  1. Update Image entity      │                    │                 │
 │                            │     (storage_key, sha256,    │                    │                 │
 │                            │      status→processed)       │                    │                 │
 │                            │  2. Confirm waypoint         │                    │                 │
 │                            │     (route version++)        │                    │                 │
 │                            │  3. If ALL confirmed:        │                    │                 │
 │                            │     Route PENDING → READY    │                    │                 │
 │                            │  4. XACK message             │                    │                 │
 │                            │                              │                    │                 │
 │◄── SSE: ready (per image)──│ Poll sees stage=done         │                    │                 │
 │◄── SSE: complete ──────────│ All images terminal +        │                    │                 │
 │                            │ DB confirms route READY      │                    │                 │
```

### 8.2 Image Result Processing (API Consumer Detail)

When the API consumer reads a message from `image:result`:

```
XREADGROUP GROUP api-workers api-1 BLOCK 5000 COUNT 10 STREAMS image:result >
│
├── Parse message fields (image_id, status, storage_key, sha256, ...)
│
├── Execute MarkImageProcessedCommand (Image module)
│   ├── Find image by ID in PostgreSQL
│   ├── Update: storage_key (.jpg→.webp), content_type, sha256, etag,
│   │          file_size, dimensions, status (pending→processed/failed)
│   ├── Publish image.upload.completed / image.upload.failed event
│   └── Return new status
│
├── Execute ConfirmWaypointImageCommand (Route module) ← retries on version conflict
│   ├── Find route containing this image
│   ├── For CREATION flow:
│   │   ├── Mark waypoint as confirmed
│   │   ├── Check if ALL waypoints confirmed
│   │   ├── If yes: transition route PENDING → READY, publish route.activated
│   │   └── Save route (version++)
│   ├── For REPLACEMENT flow:
│   │   ├── Swap waypoint.image_id to new image
│   │   ├── Scale marker coordinates for new dimensions
│   │   ├── Clear pending_replacement_image_id
│   │   ├── Archive old image
│   │   └── Save route (version++), route stays PUBLISHED
│   └── Return {RouteTransitioned: bool, Reason: string}
│
└── XACK image:result message_id
```

### 8.3 Route Navigation Flow

```
App                          API                                          MinIO
 │                            │                                             │
 │ GET /routes/{id}?include_images=true                                     │
 │───────────────────────────►│                                             │
 │                            │ Load route aggregate                        │
 │                            │ For each waypoint image:                    │
 │                            │   1. Find Image entity                      │
 │                            │   2. StatObject ─────────────────────────── │─► verify exists
 │                            │   3. PresignedGetObject ────────────────── │─► generate URL
 │                            │   4. Update image.download_url             │   (24h expiry)
 │                            │   5. Publish image.accessed event           │
 │                            │                                             │
 │◄── route + waypoints[] + download_urls[]                                │
 │                            │                                             │
 │ Download images via presigned URLs (direct to MinIO)                     │
 │──────────────────────────────────────────────────────────────────────────►│
 │◄── image data ───────────────────────────────────────────────────────────│
 │                            │                                             │
 │ Cache images locally       │                                             │
 │ Navigate: image → marker → walk → next image                            │
 │ (no further server contact)│                                             │
```

### 8.4 Cascade Deletion Flow

User deletion triggers a full cascade through the event bus:

```
DELETE /users/anonymous/{id}
│
├── Delete user from PostgreSQL
├── Remove from in-memory cache
├── Publish domain.user.anonymous.deleted
│
└──► UserDeletedHandler (event subscriber)
     ├── List all routes owned by user (paginated, 100/page)
     └── For each route:
         ├── Delete route aggregate from PostgreSQL
         ├── Publish domain.route.deleted (with image_ids[])
         │
         └──► RouteDeletedHandler (event subscriber)
              ├── If image_ids is empty: skip (no images to clean)
              └── For each image_id:
                  ├── Find Image entity
                  ├── Delete from MinIO (S3 DeleteObject)
                  ├── Delete from PostgreSQL
                  ├── Publish domain.image.deleted
                  │
                  └──► ImageDeletedHandler (event subscriber)
                       └── Delete Valkey key: image:status:{id}
                           (prevents stale reaper from processing)
```

**Important**: All event handlers are best-effort. They log errors but return nil,
because the Watermill GoChannel event bus does not support retry on handler failure.
If a handler fails mid-cascade, some resources may become orphans — these are caught
by background cleanup jobs (orphan-image-scan, orphan-storage-scan).

---

## 9. Event Bus & Cascade Patterns

### 9.1 Event Bus Configuration

The event bus uses Watermill's GoChannel implementation (in-memory, non-persistent):

| Parameter | Value |
|-----------|-------|
| Type | GoChannel (non-persistent) |
| Buffer size | 64 messages |
| Retry middleware | Max 5 retries, 1s→16s exponential backoff |
| Poison queue buffer | 32 messages |
| Handler timeout | 30s |

### 9.2 Subscribed Topics and Handlers

| Topic | Handler | Action |
|-------|---------|--------|
| `domain.route.deleted` | RouteDeletedHandler | Delete all images (MinIO + PostgreSQL) for the route |
| `domain.user.anonymous.deleted` | UserDeletedHandler | Delete all routes owned by the user |
| `domain.image.deleted` | ImageDeletedHandler | Delete Valkey progress key `image:status:{id}` |
| `domain.image.upload.prepared` | ImageUploadPreparedHandler | Write initial Valkey status `{stage:queued}` |

### 9.3 Published but Unsubscribed Events

These events are published for observability and future extensibility but currently
have no subscribers:

| Topic | Published By | Notes |
|-------|-------------|-------|
| `domain.user.anonymous.created` | User module | No downstream action needed |
| `domain.route.created` | Route module | No downstream action needed |
| `domain.route.accessed` | Route service | Could drive analytics |
| `domain.route.activated` | Image result processor | Route PENDING→READY transition |
| `domain.route.published` | Route service | Could trigger notifications |
| `domain.route.updated` | Route service | Could trigger cache invalidation |
| `domain.image.upload.completed` | Image result processor | Could drive analytics |
| `domain.image.upload.failed` | Image result processor | Could drive alerting |
| `domain.image.accessed` | Image download service | Could drive access tracking |

These appear as `[watermill] No subscribers to send message` in logs — this is
normal behavior, not an error.

---

## 10. Background Systems

### 10.1 Stale Image Reaper

**Purpose**: Detect images stuck in non-terminal processing stages and mark them as
failed. This handles cases where the gateway crashes, the pipeline hangs, or a Valkey
message is lost.

**Mechanism**:

```
Every ScanInterval (1s test / 10s prod):
│
├── SCAN 0 MATCH "image:status:*" COUNT 100
│   └── (cursor pagination until cursor = "0")
│
├── For each key:
│   ├── HGETALL → read {stage, progress, updated_at, error}
│   │
│   ├── If stage is terminal (done, failed):
│   │   └── Remove from tracking map, skip
│   │
│   ├── If key is new (first seen this scan cycle):
│   │   └── Record first-seen timestamp in memory, skip
│   │
│   └── If now - first_seen >= StaleThreshold (2s test / 30s prod):
│       ├── HSET key: stage=failed, error="image processing timed out"
│       ├── XADD image:result: {image_id, status=failed,
│       │   error_code=processing_timeout, error_message=..., failed_at=...}
│       └── DEL key (remove from Valkey)
│
└── Continue loop
```

**Production Timing**: ScanInterval=10s, StaleThreshold=30s means an image must be
stuck for at least 30 seconds before it's reaped. This gives the pipeline adequate
time for ML inference (~300-500ms) and image processing (~200-400ms).

### 10.2 Valkey Stream Consumer

**Purpose**: Continuously read image processing results from the gateway and update
API state.

**Consumer Loop**:
```
XREADGROUP GROUP api-workers api-1 BLOCK 5000 COUNT 10 STREAMS image:result >
│
├── On message: call ImageResultProcessor.Process()
│   ├── HandlerResultACK → XACK
│   ├── HandlerResultPermanent → log WARN, XACK (message is invalid)
│   └── HandlerResultTransient → keep in PEL for redelivery
│
├── On empty (block timeout): continue loop
├── On context cancelled: return nil (graceful shutdown)
└── On Valkey error: retry with backoff (100ms, 200ms, 400ms), then terminate
```

### 10.3 Valkey Stream Reclaimer

**Purpose**: Reclaim messages that were delivered to a consumer but never ACK'd
(e.g., consumer crashed mid-processing).

**Reclaimer Loop**:
```
Every ScanInterval (2s test / 1m prod):
│
├── XAUTOCLAIM image:result api-workers api-1-reclaimer IdleTimeout 0-0 COUNT 10
│   └── (cursor pagination until cursor = "0-0")
│
├── For each reclaimed message:
│   ├── Process same as Consumer (call handler)
│   ├── If delivery count >= MaxDeliveries (10):
│   │   ├── XADD image:result:dlq {original fields + dlq metadata}
│   │   └── XACK (remove from PEL)
│   └── Otherwise: normal processing
│
└── Continue loop
```

### 10.4 Background Scheduler Jobs

| Job | Interval | Max Duration | Purpose |
|-----|----------|-------------|---------|
| `image-cleanup` | 5m | 30m | Clean up failed/orphaned images from PostgreSQL and MinIO |
| `orphan-image-scan` | 15m | 10m | Find image entities with no waypoint reference |
| `route-cleanup` | 10m | 10m | Clean up expired PREPARING routes (>12h old) |
| `orphan-storage-scan` | 1h | 30m | Find MinIO objects with no corresponding image entity |

Each job runs in its own goroutine with a per-run context timeout. Jobs are
independent — one job's failure does not affect others.

---

## 11. SSE Real-Time Streaming

### 11.1 Endpoint

```
GET /api/v1/routes/{route_id}/status/stream
Authorization: Bearer <user JWT>
Accept: text/event-stream
```

### 11.2 Polling Architecture

The SSE endpoint does not use pub/sub. It polls Valkey directly:

```
SSE Connection opened
│
├── Load route to get image_ids[]
├── Start ticker (500ms)
│
├── Every 500ms:
│   ├── For each non-terminal image:
│   │   ├── HGETALL image:status:{id}
│   │   ├── If stage changed since last poll:
│   │   │   ├── Non-terminal (queued→uploading): send "processing" event
│   │   │   ├── done: send "ready" event, mark terminal
│   │   │   └── failed: send "failed" event with error_reason, mark terminal
│   │   └── If stage unchanged: skip (no event)
│   │
│   └── If ALL images terminal:
│       ├── Call CompletionVerifier (check PostgreSQL: route is READY?)
│       ├── If verified: send "complete" {all_done: true}
│       └── If not: continue polling (wait for route transition)
│
├── Every 30s: send "heartbeat" event (keep connection alive)
│
└── After 5m: send "complete" {all_done: false}, close stream
```

### 11.3 Event Types

| Event Type | Fields | When |
|------------|--------|------|
| `heartbeat` | `event_type`, `timestamp` | Every 30s |
| `processing` | `event_type`, `image_id`, `status`, `timestamp` | Image in non-terminal stage |
| `ready` | `event_type`, `image_id`, `status`, `timestamp` | Image stage=done |
| `failed` | `event_type`, `image_id`, `status`, `error_reason`, `timestamp` | Image stage=failed |
| `complete` | `event_type`, `all_done`, `timestamp` | All images terminal + DB verified |

### 11.4 Typical SSE Event Sequence (3 images)

```
event: processing
data: {"event_type":"processing","image_id":"img-1","status":"processing","timestamp":"..."}

event: processing
data: {"event_type":"processing","image_id":"img-2","status":"processing","timestamp":"..."}

event: processing
data: {"event_type":"processing","image_id":"img-3","status":"processing","timestamp":"..."}

event: ready
data: {"event_type":"ready","image_id":"img-1","status":"ready","timestamp":"..."}

event: ready
data: {"event_type":"ready","image_id":"img-2","status":"ready","timestamp":"..."}

event: ready
data: {"event_type":"ready","image_id":"img-3","status":"ready","timestamp":"..."}

event: complete
data: {"event_type":"complete","all_done":true,"timestamp":"..."}
```

---

## 12. Error Handling Patterns

### 12.1 Error Classification by Layer

| Layer | Error Type | Handling |
|-------|-----------|----------|
| **Valkey Consumer** | No messages (block timeout) | Continue loop (expected) |
| **Valkey Consumer** | Transient Valkey errors | Retry with exponential backoff (3 attempts) |
| **Valkey Consumer** | Unrecoverable errors | Return error, terminate consumer |
| **Message Processor** | Handler returns ACK | XACK message |
| **Message Processor** | Handler returns Permanent | Log WARN, XACK (message is invalid/stale) |
| **Message Processor** | Handler returns Transient | Keep in PEL for redelivery |
| **Message Processor** | Delivery count >= 10 | Move to DLQ (`image:result:dlq`), XACK |
| **Reclaimer** | Idle messages found | Re-process via same handler |
| **Stale Reaper** | SCAN/HGETALL errors | Log WARN, skip key, retry next cycle |
| **Image Processor** | Image not found in DB | Return Permanent (consumer ACKs) |
| **Image Processor** | Route version conflict | Retry up to 3 times (50ms, 100ms, 150ms backoff) |
| **Event Handlers** | Any error | Log error, return nil (best-effort, no retry) |
| **Gateway Pipeline** | Validation failure | Route to error channel, publish failure result |
| **Gateway Pipeline** | Job timeout (5m) | Wrap as `ErrJobTimeout`, route to error channel |
| **Result Publisher** | Valkey timeout | Retry 3 times (100ms, 200ms, 400ms backoff) |
| **Upload Service** | Duplicate upload | Return 409 Conflict |
| **Upload Service** | Pipeline unavailable | Return 503 Service Unavailable |
| **Upload Service** | Body too large | Return 413 Payload Too Large |

### 12.2 Error Domain Types

Both Go services use rich domain errors with `errors.Is()` and `fmt.Errorf("%w")`:

```go
// Route domain
ErrRouteNotFound
ErrConcurrentModification
ErrInvalidRouteStatus
ErrMaxPendingRoutesExceeded

// Image domain
ErrImageNotFound
ErrImageAlreadyProcessed

// User domain
ErrUserNotFound

// Gateway pipeline
ErrJobTimeout
ErrInvalidMagicBytes
ErrFileTooLarge
```

Infrastructure errors are mapped to domain errors at the repository boundary.
API service layer maps domain errors to HTTP status codes.

---

## 13. Timing Reference

### 13.1 Production Timing Values

| Component | Parameter | Value |
|-----------|-----------|-------|
| **DB Connection** | Max pool size | 25 connections |
| **DB Connection** | Max idle time | 15 minutes |
| **DB Connection** | Max lifetime | 1 hour |
| **DB Retry** | Max attempts | 5 |
| **DB Retry** | Initial delay | 500ms |
| **DB Retry** | Max delay | 30s |
| **DB Retry** | Overall timeout | 2 minutes |
| **Consumer** | Block timeout | 5s |
| **Consumer** | Max deliveries (DLQ) | 10 |
| **Reclaimer** | Idle timeout | 5 minutes |
| **Reclaimer** | Scan interval | 1 minute |
| **Stale Reaper** | Scan interval | 10s |
| **Stale Reaper** | Stale threshold | 30s |
| **SSE Polling** | Poll interval | 500ms |
| **SSE** | Heartbeat interval | 30s |
| **SSE** | Max duration | 5 minutes |
| **Pipeline** | Job timeout | 5 minutes |
| **Upload Service** | Submit timeout | 30s |
| **Upload Service** | Max file size | 10MB |
| **Event Bus** | Buffer size | 64 messages |
| **Event Bus** | Handler timeout | 30s |
| **Event Bus** | Retry: max attempts | 5 |
| **Event Bus** | Retry: initial interval | 1s |
| **Event Bus** | Retry: max interval | 16s |
| **Route** | Preparing TTL | 12 hours |
| **Route** | Temporary TTL | 4 hours |
| **MinIO** | Presigned URL expiry | 24 hours |
| **Scheduler: image-cleanup** | Interval / Max duration | 5m / 30m |
| **Scheduler: orphan-image-scan** | Interval / Max duration | 15m / 10m |
| **Scheduler: route-cleanup** | Interval / Max duration | 10m / 10m |
| **Scheduler: orphan-storage-scan** | Interval / Max duration | 1h / 30m |

### 13.2 Integration Test Timing Overrides

| Component | Test Value | Production Value |
|-----------|-----------|-----------------|
| Stale reaper scan interval | 1s | 10s |
| Stale reaper stale threshold | 2s | 30s |
| Reclaimer idle timeout | 5s | 5m |
| Reclaimer scan interval | 2s | 1m |
| Server runtime timeout | 10-15s | (no limit) |

### 13.3 Observed Processing Times (from integration test)

| Operation | Duration |
|-----------|----------|
| Full behavioral flow (15 steps) | 2.6s |
| Single image pipeline (validate→upload) | 500-800ms |
| Validate stage | <1ms |
| Analyze stage (ML inference) | 230-480ms |
| Transform stage (resize + WebP) | 220-430ms |
| Upload stage (MinIO PutObject) | ~13ms |
| Route creation with 3 waypoints | ~10ms |
| Image download URL generation (3 images) | 5-9ms |
| SSE stream total duration | ~1.5s |
| DB connection establishment | ~2ms |
| Presigned URL generation | <1ms |

---

## Appendix: Process-Level Architecture Summary

```
┌─────────────────────────────────────────────────────────────────────┐
│                         follow-api process                          │
│                                                                     │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐              │
│  │ Route Module  │  │ User Module  │  │ Image Module │              │
│  │  (DDD domain) │  │  (DDD domain)│  │  (DDD domain)│              │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘              │
│         │                  │                  │                      │
│  ┌──────┴──────────────────┴──────────────────┴───────┐             │
│  │              Watermill Event Bus (GoChannel)        │             │
│  │  Subscribers:                                       │             │
│  │    route.deleted → cleanup images                   │             │
│  │    user.deleted  → cleanup routes                   │             │
│  │    image.deleted → cleanup Valkey keys              │             │
│  │    image.upload.prepared → write initial status     │             │
│  └────────────────────────────────────────────────────┘             │
│                                                                     │
│  ┌──────────────────────────────────────┐                          │
│  │        Valkey Stream Consumer         │                          │
│  │  XREADGROUP api-workers api-1         │                          │
│  │  → ImageResultProcessor               │                          │
│  │    → MarkImageProcessed               │                          │
│  │    → ConfirmWaypointImage (w/ retry)  │                          │
│  └──────────────────────────────────────┘                          │
│                                                                     │
│  ┌──────────────────────────────────────┐                          │
│  │        Valkey Stream Reclaimer        │                          │
│  │  XAUTOCLAIM api-workers              │                          │
│  │  → same handler as Consumer           │                          │
│  │  → DLQ on max deliveries             │                          │
│  └──────────────────────────────────────┘                          │
│                                                                     │
│  ┌──────────────────────────────────────┐                          │
│  │        Stale Image Reaper             │                          │
│  │  SCAN image:status:* → mark failed   │                          │
│  │  → publish to image:result stream     │                          │
│  └──────────────────────────────────────┘                          │
│                                                                     │
│  ┌──────────────────────────────────────┐                          │
│  │        Background Scheduler           │                          │
│  │  image-cleanup (5m)                   │                          │
│  │  orphan-image-scan (15m)              │                          │
│  │  route-cleanup (10m)                  │                          │
│  │  orphan-storage-scan (1h)             │                          │
│  └──────────────────────────────────────┘                          │
│                                                                     │
│  ┌──────────────────────────────────────┐                          │
│  │        SSE Streamer                   │                          │
│  │  Polls Valkey (500ms) per image       │                          │
│  │  Sends events to HTTP response        │                          │
│  │  Heartbeat every 30s, max 5m          │                          │
│  └──────────────────────────────────────┘                          │
│                                                                     │
│  ┌──────────────────────────────────────┐                          │
│  │        Goa HTTP Server                │                          │
│  │  REST API + SSE endpoint              │                          │
│  └──────────────────────────────────────┘                          │
└─────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────┐
│                   follow-image-gateway process                      │
│                                                                     │
│  ┌──────────────────────────────────────────────────────────┐      │
│  │                 4-Stage Pipeline                          │      │
│  │  ┌──────────┐  ┌──────────┐  ┌───────────┐  ┌────────┐ │      │
│  │  │ Validate │─►│ Analyze  │─►│ Transform │─►│ Upload │ │      │
│  │  │ (2w)     │  │ (2w)     │  │ (2w)      │  │ (3w)   │ │      │
│  │  └──────────┘  └──────────┘  └───────────┘  └────────┘ │      │
│  │                                        │         │       │      │
│  │                                    errCh ◄───────┘       │      │
│  │                                        │                 │      │
│  │                                   merge channels         │      │
│  │                                        │                 │      │
│  │                                   Result Publisher        │      │
│  │                                   → HSET progress        │      │
│  │                                   → XADD image:result    │      │
│  └──────────────────────────────────────────────────────────┘      │
│                                                                     │
│  ┌──────────────────────────────────────┐                          │
│  │        Upload Service (HTTP)          │                          │
│  │  1. JWT validation (Ed25519)          │                          │
│  │  2. Upload guard (SET NX)             │                          │
│  │  3. Body read + validation            │                          │
│  │  4. Pipeline submission (30s timeout) │                          │
│  └──────────────────────────────────────┘                          │
│                                                                     │
│  ┌──────────────────────────────────────┐                          │
│  │        Goa HTTP Server                │                          │
│  │  Upload endpoint + health checks      │                          │
│  └──────────────────────────────────────┘                          │
└─────────────────────────────────────────────────────────────────────┘
```
