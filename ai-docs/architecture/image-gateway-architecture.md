# follow-image-gateway Architecture Document

**Date:** 2026-01-29
**Status:** Proposed
**Author:** Architecture Team
**Scope:** New microservice for secure image upload, validation, and processing
**Supersedes:** Direct-to-MinIO presigned URL upload flow

---

## Table of Contents

1. [Executive Summary](#1-executive-summary)
2. [Problem Statement](#2-problem-statement)
3. [Solution Overview](#3-solution-overview)
4. [Architecture Overview](#4-architecture-overview) *(includes domain separation)*
5. [Detailed Flow](#5-detailed-flow) *(DB transaction-based completion detection)*
6. [Valkey Data Structures](#6-valkey-data-structures) *(domain-agnostic, image-scoped only)*
7. [Authentication and Security](#7-authentication-and-security)
8. [Image Resize and Marker Scaling](#8-image-resize-and-marker-scaling)
9. [Client Communication](#9-client-communication)
10. [Error Handling and Recovery](#10-error-handling-and-recovery)
11. [Infrastructure and Deployment](#11-infrastructure-and-deployment)
12. [Route Lifecycle (Updated)](#12-route-lifecycle-updated)
13. [Architecture Decision Records](#13-architecture-decision-records)
14. [Migration Path](#14-migration-path)
15. [Future Enhancements](#15-future-enhancements)

---

## 1. Executive Summary

The Follow API currently allows clients to upload images directly to MinIO/S3 via presigned PUT URLs. A security review (2026-01-10) and subsequent deep-dive research (`ai-docs/security/image-upload-integrity-and-validation.md`, 2026-01-28) identified three critical vulnerabilities in this flow:

1. **TOCTOU race condition** between storage metadata verification and database update during upload confirmation
2. **No content integrity verification** -- the client sends `content_type` and `file_size` but no hash; any file matching those properties passes confirmation
3. **No server-side image validation** -- uploaded files are never verified to be actual images; scripts, malware, and polyglot files pass through undetected

These vulnerabilities exist because the server never sees the raw file bytes. The MinIO `PresignedPutObject()` method supports no conditions (no size enforcement, no content-type enforcement), and the client is an untrusted actor in the upload chain.

This document specifies `follow-image-gateway`, a new Go microservice that receives image uploads from clients, validates and processes them server-side, and uploads clean images to object storage. This eliminates the entire class of presigned URL upload vulnerabilities while enabling image processing capabilities (EXIF stripping, resize, future face/plate blurring).

---

## 2. Problem Statement

### CRIT-001: TOCTOU Race Condition

**Location:** `internal/domains/image/usecases/confirm_image_upload.go`

The current confirmation flow executes these steps sequentially:

1. `verifyObjectAndMetadata()` -- calls `storageService.VerifyObjectExists()` and `storageService.GetObjectMetadata()` to check a file exists at the expected storage key with matching size and content type
2. `updateImageAsUploaded()` -- calls `image.MarkAsUploaded()` and `imageRepo.Update()` to transition the Image entity to uploaded status

Between step 1 (check) and step 2 (use), there is a time window. An attacker who has the presigned URL can:

1. Upload a legitimate image
2. Wait for the client to trigger confirmation
3. After the metadata check passes but before the database update commits, overwrite the object in MinIO with malicious content via the same presigned URL (which may still be valid)

The database now records the image as "uploaded and verified" while the actual stored object is different from what was checked. This is a classic Time-of-Check-Time-of-Use (TOCTOU) vulnerability.

### CRIT-002: No Content Integrity Verification

The `ImageMetadataInput` type (defined in `design/route_types.go`) accepts only:

```go
Attribute("content_type", String)  // "image/jpeg", "image/png", etc.
Attribute("file_size", Int64)      // File size in bytes
```

No hash or checksum is required. During confirmation, `validateObjectMetadata()` in `confirm_image_upload.go` checks only:

- `metadata.ContentType` matches expected content type
- `metadata.ContentLength` matches expected file size
- `metadata.LastModified` is within the last hour

A client can upload ANY file that happens to match the declared size and MIME type. For example, a 2MB JavaScript file with `Content-Type: image/jpeg` would pass all validation checks.

**Inconsistency in existing codebase:** The `ReplaceWaypointImageConfirmPayload` (in `design/route_types.go`) already requires a SHA256 hash:

```go
Attribute("file_hash", String, func() {
    Description("SHA256 hash of uploaded file (64 hex characters)")
    Pattern("^[a-f0-9]{64}$")
})
```

But the primary `create_route_with_waypoints` + `confirm_route_waypoints` flow does not require any hash. This inconsistency means the main upload path has weaker security than the image replacement path.

### CRIT-003: No Server-Side Image Validation

There is no validation anywhere in the codebase that uploaded files are actually images. The storage service (`internal/infrastructure/storage/minio/image_storage_service.go`) validates content type strings against an allow list:

```go
supportedTypes := map[string]bool{
    "image/jpeg": true,
    "image/png":  true,
    "image/webp": true,
}
```

But this only validates the declared MIME type string, not the actual file content. A client can:

- Upload a PHP/Python script with `.jpg` extension and `image/jpeg` content type
- Upload a polyglot file that is both a valid JPEG and an executable
- Upload a file with embedded malicious EXIF metadata
- Upload a ZIP archive disguised as a PNG

SHA256 does not solve this problem because a malicious client can compute a valid SHA256 hash of malicious content and submit it alongside the upload.

### Root Cause: Client Never Touches Server

The fundamental architectural issue is that `PresignedPutObject()` in the MinIO Go SDK generates a URL that the client uses to upload directly to MinIO. The follow-api server never sees the raw bytes. This means:

- No magic bytes validation possible
- No image header parsing possible
- No decode/re-encode to strip embedded payloads
- No EXIF/GPS metadata stripping
- No server-computed checksums

The presigned PUT URL enforces nothing -- MinIO's `PresignedPutObject()` does not support conditions on file size, content type, or content hash. The only enforcement is the URL expiration time.

---

## 3. Solution Overview

Introduce `follow-image-gateway` as a new Go microservice that sits between the client and object storage:

```
BEFORE (vulnerable):
  Client --[presigned PUT URL]--> MinIO (direct, unvalidated)
  Client --[confirm request]----> follow-api --[StatObject]--> MinIO (TOCTOU gap)

AFTER (secure):
  Client --[PUT /upload?token=JWT]--> follow-image-gateway
  follow-image-gateway: validate --> decode --> process --> encode --> upload
  follow-image-gateway --[Redis Stream]--> follow-api --[update DB]--> PostgreSQL
```

The gateway:

- **Receives image uploads from clients** instead of direct-to-MinIO uploads
- **Validates images server-side** using a 4-layer approach: magic bytes, header parsing, full decode, and re-encode
- **Processes images** by stripping EXIF/GPS metadata, resizing to max width, re-encoding to target format, and (future) blurring faces and license plates
- **Uploads processed images to MinIO/S3** with server-computed SHA256
- **Communicates results to follow-api** via Redis Streams
- **Eliminates the entire class** of presigned URL vulnerabilities because the client never touches object storage

---

## 4. Architecture Overview

### 4.1 System Architecture Diagram

```mermaid
graph TB
    subgraph "Client Layer"
        FC[Flutter Client]
    end

    subgraph "API Layer"
        FA[follow-api<br/>Go, port 8080<br/>Owns PostgreSQL]
    end

    subgraph "Processing Layer"
        IG[follow-image-gateway<br/>Go, port 8090<br/>Stateless]
    end

    subgraph "Data Layer"
        PG[(PostgreSQL<br/>port 5432)]
        RD[(Valkey<br/>port 6379<br/>Streams + Hash)]
        S3[(MinIO / S3 / R2<br/>port 9000<br/>Object Storage)]
    end

    FC -->|"1. HTTPS REST + SSE"| FA
    FC -->|"2. PUT /upload?token=JWT"| IG

    FA -->|"3. Ed25519-signed JWT<br/>(via upload URL, relayed by client)"| IG

    FA <-->|"4. Write image:status (queued)<br/>Consume from image:result"| RD
    IG <-->|"5. SET NX image:upload:{id}<br/>Publish to image:result<br/>Update image:status:{id}"| RD

    IG -->|"6. S3 PutObject<br/>(processed images)"| S3
    FA -->|"7. S3 StatObject<br/>(verification at publish)"| S3
    FA -->|"8. S3 PresignedGetObject<br/>(download URLs)"| S3

    FA <-->|"9. Read/Write entities"| PG

    FA -->|"10. SSE event stream<br/>(real-time status + preview)"| FC
```

**Communication channels:**

| Channel | From | To | Protocol | Purpose |
|---------|------|----|----------|---------|
| 1 | Client | follow-api | HTTPS REST + SSE | Route CRUD, status streaming |
| 2 | Client | image-gateway | HTTPS PUT | Raw image upload |
| 3 | follow-api | image-gateway | Ed25519-signed JWT (via client relay) | Upload authorization |
| 4 | follow-api | Valkey | HSET / XREADGROUP | Write initial status, consume results |
| 5 | image-gateway | Valkey | SET NX / XADD / HSET | Upload guard, publish results, update progress |
| 6 | image-gateway | MinIO | S3 PutObject | Upload processed images |
| 7 | follow-api | MinIO | S3 StatObject | Verification during route publish |
| 8 | follow-api | MinIO | S3 PresignedGetObject | Generate download URLs for clients |
| 9 | follow-api | PostgreSQL | SQL | Entity persistence |
| 10 | follow-api | Client | SSE | Real-time processing status |

### 4.2 State Ownership and Domain Separation

| Component | Owns | Role |
|-----------|------|------|
| **follow-api** | PostgreSQL (images, routes, waypoints, users) | Source of truth for all entity state. Owns ALL domain knowledge: routes, waypoints, users, and the mapping between them (route -> waypoints -> image_ids). Performs completion detection via DB |
| **follow-image-gateway** | Nothing persistent | **Domain-agnostic** image processor. Knows NOTHING about routes, waypoints, markers, or who uploaded. Receives images, processes them, returns results. Could serve any service that needs image processing |
| **Valkey** | Strings (upload guard), Streams (results), Hashes (progress) | **Image-scoped** communication only. No route or waypoint keys exist in Valkey. Streams carry only image_id and image metadata. NOT source of truth -- PostgreSQL is |
| **MinIO/S3** | Object blobs | Processed images only. Raw uploads never persist to storage |

### 4.3 Trust Model

| Component | Trust Level | Justification |
|-----------|-------------|---------------|
| follow-api | TRUSTED | Our server, runs our code |
| follow-image-gateway | TRUSTED | Our server, runs our code |
| Redis | TRUSTED | Our infrastructure, internal network only |
| MinIO/S3 | TRUSTED | Our storage, access-controlled |
| **Client** | **UNTRUSTED** | Never part of the integrity chain |

The integrity chain is entirely server-to-server:

```
image-gateway (validates + processes + computes SHA256)
    --> Redis Stream (delivers result message)
        --> follow-api (updates PostgreSQL with trusted data)
```

The client's only roles are:
1. Uploading raw bytes to the gateway
2. Reviewing processed results via preview URLs
3. Explicitly publishing the route when satisfied

The client is never asked to confirm, verify, or attest to anything in the security chain.

---

## 5. Detailed Flow

### 5.1 Phase 1: Create Route (Client to follow-api)

```
Client: POST /routes/{id}/create-waypoints
  Body: {
    waypoints: [
      {
        image_metadata: {content_type, file_size},
        marker_x_coordinate, marker_y_coordinate,
        marker_type, description
      },
      ...
    ]
  }
```

**follow-api processing:**

```
1.  Create Route entity (status: PREPARING)
2.  Create Waypoint entities with ORIGINAL marker coordinates
3.  Create Image entities (status: PENDING)
4.  Generate storage keys for each image
5.  Sign Ed25519 JWT tokens containing:
      - image_id
      - storage_key
      - content_type
      - max_size
      - expiry (15 minutes)
6.  Build upload URLs:
      https://img-gw.example.com/upload?token=<Ed25519-JWT>
7.  Set initial status in Valkey Hash "image:status:{image_id}":
      {stage: "queued", progress: 0}
8.  Transition Route to PENDING status
9.  Return upload URLs to client (replaces presigned MinIO URLs)
```

**Response shape** (replaces current `CreateRouteWithWaypointsResult`):

```json
{
  "route_id": "uuid",
  "route_status": "pending",
  "waypoint_ids": ["uuid1", "uuid2", "uuid3"],
  "upload_urls": [
    {
      "image_id": "uuid",
      "upload_url": "https://img-gw.example.com/upload?token=eyJ...",
      "position": 0,
      "expires_at": "2026-01-29T10:15:00Z"
    }
  ],
  "status_stream_url": "/images/status/stream?image_ids=uuid1,uuid2,uuid3",
  "created_at": "2026-01-29T10:00:00Z"
}
```

### 5.2 Phase 2: Upload (Client to image-gateway)

```
Client: Opens SSE connection to follow-api:
  GET /images/status/stream?image_ids=id1,id2,id3

Client: For each image, sends:
  PUT https://img-gw.example.com/upload?token=<JWT>
  Content-Type: application/octet-stream
  Body: <raw image bytes>
```

**image-gateway processing:**

```
1. Extract JWT from query parameter
2. Validate JWT signature using follow-api's Ed25519 public key
3. Extract claims: image_id, storage_key, content_type, max_size, expiry
4. Validate token not expired
5. Read request body, enforcing Content-Length <= max_size from token
6. Buffer file bytes in memory
7. SET NX image:upload:{image_id} in Valkey (409 Conflict if exists)
8. Update Valkey Hash: {stage: "waiting_upload", progress: 5}
9. Return 202 Accepted:
     {"image_id": "uuid", "status": "processing"}
10. Feed buffered bytes into async processing pipeline
```

### 5.3 Phase 3: Process (image-gateway async pipeline)

The pipeline uses Go channels with a fan-in/fan-out pattern. Each stage has configurable worker counts to balance throughput and resource usage.

```mermaid
graph LR
    IN[Upload Handler] --> V[Stage 1: VALIDATE<br/>N=2 workers]
    V --> D[Stage 2: DECODE<br/>N=2 workers]
    D --> P[Stage 3: PROCESS<br/>N=1 worker]
    P --> E[Stage 4: ENCODE<br/>N=2 workers]
    E --> U[Stage 5: UPLOAD<br/>N=3 workers]
    U --> OUT[Result Publisher]
```

#### Stage 1: VALIDATE (N=2 workers)

- Check magic bytes against declared content type:
  - JPEG: `FF D8 FF`
  - PNG: `89 50 4E 47 0D 0A 1A 0A`
  - WebP: `52 49 46 46 xx xx xx xx 57 45 42 50` (RIFF....WEBP)
  - HEIC: `xx xx xx xx 66 74 79 70 68 65 69 63` (...ftypheic)
- Reject if magic bytes do not match the content type declared in the JWT token
- Verify file size is within bounds from token
- Update Redis Hash: `{stage: "validating", progress: 10}`

#### Stage 2: DECODE (N=2 workers)

- `image.DecodeConfig()` -- parse image header only, extract dimensions (safe against decompression bombs because it does not allocate pixel buffer)
- Detect decompression bombs: reject if pixel count exceeds limit (e.g., 100 megapixels = 10000x10000)
- `image.Decode()` -- full pixel decode into `image.Image`
- Read and apply EXIF orientation (auto-orient) before stripping
- Strip ALL EXIF/metadata including GPS coordinates (privacy protection)
- Record original dimensions: `original_width`, `original_height`
- Update Redis Hash: `{stage: "decoding", progress: 30}`

#### Stage 3: PROCESS (N=1 worker initially, ML-heavy)

- **MVP:** Pass-through stage (no ML models yet)
- **Future:** Face detection + Gaussian blur
- **Future:** License plate detection + blur
- **Future:** Image quality scoring (reject blurry navigation photos)
- Update Redis Hash: `{stage: "processing", progress: 55}`

#### Stage 4: ENCODE (N=2 workers)

- Resize to max width (default 1920px), maintaining aspect ratio -- NEVER crop
- If original width is less than or equal to max width, keep original dimensions
- Re-encode to target format (WebP preferred, JPEG fallback for older device compatibility)
- Compression quality: 85 (configurable via `IMG_GW_COMPRESSION_QUALITY`)
- Compute SHA256 of final encoded bytes
- Record processed dimensions: `processed_width`, `processed_height`
- Update Redis Hash: `{stage: "encoding", progress: 75}`

#### Stage 5: UPLOAD (N=3 workers)

- `PutObject` to MinIO/S3 at the storage key from the JWT token
- Receive ETag from MinIO response
- Optional paranoia check: `StatObject` to verify SHA256 matches
- Update Redis Hash: `{stage: "uploading_to_storage", progress: 90}`

#### Final: Publish Result

- `XADD` to Valkey Stream `image:result` with complete processing results
- Update Valkey Hash: `{stage: "done", progress: 100}`

#### Pipeline Job Struct

Each stage communicates via typed Go channels. The `ImageJob` struct flows through the pipeline:

```go
type ImageJob struct {
    ID              string
    Token           *UploadToken    // Parsed JWT claims
    RawBytes        []byte          // Stages 1-2: raw upload bytes
    DecodedImg      image.Image     // Stages 2-4: decoded pixel data
    OriginalWidth   int             // Stage 2 records
    OriginalHeight  int             // Stage 2 records
    ProcessedWidth  int             // Stage 4 records
    ProcessedHeight int             // Stage 4 records
    EncodedBytes    []byte          // Stages 4-5: final encoded output
    SHA256          string          // Stage 4 computes
    ContentType     string          // Stage 4 sets (may differ from input)
    ETag            string          // Stage 5 receives from MinIO
    StorageKey      string          // From JWT token
    Error           error           // Any stage can set; halts pipeline
}
```

When any stage sets `Error`, the job skips remaining stages and goes directly to the result publisher, which emits a failure result to Redis.

### 5.4 Phase 4: Result Consumption + Auto-Activation (follow-api)

```
follow-api runs a Redis consumer goroutine:

1. XREADGROUP GROUP "api-workers" CONSUMER "api-1"
     BLOCK 5000 STREAMS image:result >

2. For each result message:
   a. Parse result: {image_id, status, sha256, dimensions, ...}
      Note: The message contains ONLY image_id and image metadata.
      follow-api looks up domain context (waypoint, route) from PostgreSQL.

   b. SUCCESS path -- single DB transaction:
      BEGIN TRANSACTION
        -- Step 1: Look up which waypoint/route owns this image
        SELECT waypoint_id, route_id FROM waypoints WHERE image_id = $image_id

        -- Step 2: Update Image entity
        UPDATE images SET
          status           = 'PROCESSED',
          sha256            = <from result>,
          etag              = <from result>,
          file_size         = <from result>,
          content_type      = <from result>,
          original_width    = <from result>,
          original_height   = <from result>,
          processed_width   = <from result>,
          processed_height  = <from result>
        WHERE id = $image_id

        -- Step 3: Auto-scale marker coordinates and update Waypoint
        scale_x = processed_width / original_width
        scale_y = processed_height / original_height
        new_marker_x = round(original_marker_x * scale_x)
        new_marker_y = round(original_marker_y * scale_y)
        UPDATE waypoints SET
          status = 'CONFIRMED',
          marker_x = new_marker_x,
          marker_y = new_marker_y
        WHERE id = $waypoint_id

        -- Step 4: Check remaining pending waypoints for this route
        SELECT COUNT(*) FROM waypoints
        WHERE route_id = $route_id AND status = 'pending'

        -- Step 5: If count == 0, all done -- activate the route
        IF count == 0 THEN
          UPDATE routes SET status = 'ready' WHERE id = $route_id
        END IF
      COMMIT

      The COUNT query is cheap (~0.1-0.5ms) because:
        - route_id is indexed (foreign key)
        - Max 50 waypoints per route
        - Piggybacking on an already-open DB connection/transaction
        - Cheaper than the UPDATE writes in the same transaction

      No Redis counters or sets needed for completion detection.

      -- Outside transaction: generate preview and push SSE events
      - Generate signed download URL:
          PresignedGetObject(storage_key, 1 hour expiry)
      - Push SSE event to connected client:
          event: image_processed
          data: {image_id, status, preview_url, processed_dimensions, updated_marker}
      - If route transitioned to READY:
          Push SSE event: route_ready {route_id}

   c. FAILURE path:
      - Look up waypoint/route from PostgreSQL: SELECT ... WHERE image_id = $image_id
      - Update Image entity: status = FAILED, error_code, error_message
      - Push SSE event:
          event: image_failed
          data: {image_id, error_code, error_message}
      - Route stays PENDING; client can retry failed images

   d. XACK the result message
```

### 5.5 Phase 5: Publish (Client to follow-api)

```
Client: Reviews all processed images with auto-scaled markers via preview URLs
Client: POST /routes/{id}/publish

follow-api:
  1. Verify route status is READY
  2. Verify ALL images are in PROCESSED status
  3. Optional: StatObject verification on each image in MinIO (belt-and-suspenders)
  4. Route status: READY --> PUBLISHED (visible to other users)
  5. Return publication confirmation
```

This is NOT upload confirmation (that concept is eliminated). This is **publishing intent** -- the creator reviews the processed results and approves the route for public consumption.

### 5.6 Sequence Diagram

```mermaid
sequenceDiagram
    participant C as Flutter Client
    participant API as follow-api
    participant R as Valkey
    participant GW as image-gateway
    participant S3 as MinIO/S3

    C->>API: POST /routes/{id}/create-waypoints
    API->>API: Create Route, Waypoints, Images (PENDING)
    API->>API: Sign Ed25519 JWT tokens
    API->>R: HSET image:status:{id} {stage: queued}
    API-->>C: 201 {upload_urls, status_stream_url}

    C->>API: GET /images/status/stream?image_ids=...
    Note over C,API: SSE connection held open

    loop For each image
        C->>GW: PUT /upload?token=JWT (raw bytes)
        GW->>GW: Validate JWT (Ed25519 public key)
        GW-->>C: 202 Accepted {image_id, status: processing}

        GW->>R: HSET image:status:{id} {stage: validating}
        GW->>GW: Validate magic bytes
        GW->>R: HSET image:status:{id} {stage: decoding}
        GW->>GW: Decode + strip EXIF
        GW->>R: HSET image:status:{id} {stage: processing}
        GW->>GW: Process (MVP: pass-through)
        GW->>R: HSET image:status:{id} {stage: encoding}
        GW->>GW: Resize + re-encode + SHA256
        GW->>S3: PutObject (processed image)
        GW->>R: HSET image:status:{id} {stage: done}
        GW->>R: XADD image:result {sha256, dimensions, ...}
    end

    R-->>API: XREADGROUP image:result
    API->>API: Update Image entity (PROCESSED)
    API->>API: Auto-scale marker coordinates
    API->>S3: PresignedGetObject (preview URL)
    API-->>C: SSE: image_processed {preview_url, updated_marker}

    Note over API: When ALL images processed:
    API->>API: Route status: PENDING --> READY
    API-->>C: SSE: route_ready {route_id}

    C->>C: Review processed images + markers
    C->>API: POST /routes/{id}/publish
    API->>API: Route status: READY --> PUBLISHED
    API-->>C: 200 {route_id, published_at}
```

---

## 6. Valkey Data Structures

### 6.1 String: `image:upload:{image_id}` (Upload Guard)

| Property | Value |
|----------|-------|
| **Written by** | follow-image-gateway (on upload receipt) |
| **Read by** | follow-image-gateway (duplicate check) |
| **TTL** | 1 hour (auto-cleanup) |
| **Set mode** | NX (only if not exists) |

**Purpose:** One-time upload guard. Prevents duplicate uploads of the same image.

**Flow:**
1. Gateway receives `PUT /upload?token=JWT`
2. Gateway executes `SET image:upload:{image_id} NX EX 3600`
3. If SET succeeds → proceed with processing
4. If SET fails (key exists) → return `409 Conflict` to client

This replaces the old `image:process` stream-based job distribution. The gateway now receives images directly via HTTP PUT with JWT tokens, not from a Valkey stream.

### 6.2 Stream: `image:result` (Results)

| Property | Value |
|----------|-------|
| **Producer** | image-gateway |
| **Consumer Group** | `api-workers` |
| **Consumers** | follow-api instances |
| **Retention** | 24 hours (auto-trimmed) |

**Success message schema:**

```json
{
  "image_id": "550e8400-e29b-41d4-a716-446655440000",
  "status": "processed",
  "sha256": "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2",
  "etag": "\"def456abc789\"",
  "file_size": 204800,
  "content_type": "image/webp",
  "original_width": 4032,
  "original_height": 3024,
  "processed_width": 1920,
  "processed_height": 1440,
  "processed_at": "2026-01-29T10:00:05Z"
}
```

**Failure message schema:**

```json
{
  "image_id": "550e8400-e29b-41d4-a716-446655440000",
  "status": "failed",
  "error_code": "INVALID_IMAGE_FORMAT",
  "error_message": "File magic bytes (4D 5A) do not match declared content type image/jpeg",
  "failed_at": "2026-01-29T10:00:02Z"
}
```

**Defined error codes:**

| Error Code | Stage | Meaning |
|------------|-------|---------|
| `INVALID_MAGIC_BYTES` | Validate | Magic bytes do not match declared content type |
| `UNSUPPORTED_FORMAT` | Validate | Content type not in supported list |
| `FILE_TOO_LARGE` | Validate | File exceeds max_size from JWT |
| `FILE_TOO_SMALL` | Validate | File is 0 bytes or below minimum |
| `DECODE_HEADER_FAILED` | Decode | image.DecodeConfig() failed; corrupt header |
| `DECOMPRESSION_BOMB` | Decode | Pixel count exceeds 100 megapixel limit |
| `DECODE_FAILED` | Decode | image.Decode() failed; corrupt image data |
| `PROCESS_FAILED` | Process | Processing stage error (future: ML failure) |
| `ENCODE_FAILED` | Encode | Re-encoding to target format failed |
| `STORAGE_UPLOAD_FAILED` | Upload | MinIO PutObject failed after retries |
| `UPLOAD_TOKEN_EXPIRED` | Pre-pipeline | JWT token expired before processing started |
| `UPLOAD_TOKEN_INVALID` | Pre-pipeline | JWT signature verification failed |

### 6.3 Hash: `image:status:{image_id}` (Progress)

| Property | Value |
|----------|-------|
| **Written by** | follow-api (initial `queued` state) then follow-image-gateway (pipeline stages) |
| **Read by** | follow-api (for SSE progress push and polling fallback) |
| **TTL** | 1 hour (Valkey EXPIRE, auto-cleanup) |

**Note:** follow-api writes `{stage: "queued", progress: 0}` at route creation time, before the client uploads. follow-image-gateway then updates the hash at each pipeline stage transition.

**Schema:**

```json
{
  "stage": "processing",
  "progress": 55,
  "updated_at": "2026-01-29T10:00:03Z"
}
```

**Stage progression:**

| Stage | Progress | Description |
|-------|----------|-------------|
| `queued` | 0 | Job published to stream, waiting for gateway pickup |
| `waiting_upload` | 5 | Gateway claimed job, waiting for client upload |
| `validating` | 10 | Checking magic bytes and file properties |
| `decoding` | 30 | Parsing header, decoding pixels, stripping EXIF |
| `processing` | 55 | ML processing (MVP: pass-through) |
| `encoding` | 75 | Resizing, re-encoding, computing SHA256 |
| `uploading_to_storage` | 90 | PutObject to MinIO/S3 |
| `done` | 100 | Successfully processed and uploaded |
| `failed` | -1 | Error occurred; check image:result for details |

### 6.4 Complete Valkey Key Inventory

Valkey contains ONLY these three key patterns. No route, waypoint, user, or other domain keys exist:

| Key Pattern | Type | Purpose |
|-------------|------|---------|
| `image:upload:{image_id}` | String (NX + TTL) | One-time upload guard (duplicate prevention) |
| `image:result` | Stream | Processing results (image fields only) |
| `image:status:{image_id}` | Hash (TTL) | Per-image progress tracking (image fields only) |

---

## 7. Authentication and Security

### 7.1 Ed25519 Asymmetric Signing

Each service maintains its own Ed25519 key pair. Keys are generated once and distributed via environment variables or Docker secrets.

**follow-api key pair:**

- `FOLLOW_API_ED25519_PRIVATE_KEY` -- signs upload JWT tokens
- `IMG_GW_ED25519_PUBLIC_KEY` -- verifies callback signatures (future use)

**image-gateway key pair:**

- `FOLLOW_API_ED25519_PUBLIC_KEY` -- verifies upload JWT tokens
- `IMG_GW_ED25519_PRIVATE_KEY` -- signs result messages (future use)

**JWT token structure** (signed by follow-api with Ed25519 private key):

```json
{
  "sub": "image-upload",
  "iss": "follow-api",
  "image_id": "550e8400-e29b-41d4-a716-446655440000",
  "storage_key": "images/550e8400/photo.webp",
  "content_type": "image/jpeg",
  "max_file_size": 10485760,
  "iat": 1738141200,
  "exp": 1738142100
}
```

**Note:** The JWT intentionally omits `route_id`, `waypoint_id`, and marker coordinates. The image-gateway is domain-agnostic and needs only image-related claims to authorize and process the upload. follow-api maintains the image-to-waypoint-to-route mapping in PostgreSQL.

**Key benefit:** image-gateway validates the token WITHOUT calling follow-api. The Ed25519 public key is configured at startup. Token verification is a pure cryptographic operation -- no network round-trip, no shared database, no service discovery.

**Why Ed25519 over HMAC-SHA256:** With HMAC, both services share the same secret. If the gateway is compromised, the attacker can forge tokens. With Ed25519, compromise of the gateway's key pair only reveals the gateway's signing key -- the attacker cannot forge follow-api's upload tokens because the private key is different.

### 7.2 Image Validation Layers

The 4-layer validation strategy ensures that only genuine images are accepted, regardless of what the client claims:

| Layer | Technique | What It Catches | Performance | Stage |
|-------|-----------|----------------|-------------|-------|
| 1 | Magic bytes check | Scripts, executables, wrong format, random data | Microseconds | Validate |
| 2 | `image.DecodeConfig()` | Corrupted headers, decompression bombs (dimension check) | Milliseconds | Decode |
| 3 | Full `image.Decode()` + re-encode | Polyglot files, EXIF payloads, steganography, embedded scripts | 100ms-2s | Decode + Encode |
| 4 | `Content-Disposition: attachment` + `X-Content-Type-Options: nosniff` on serve | XSS via MIME sniffing (defense-in-depth on download) | Zero runtime cost | Serving |

**Layer 3 is the critical defense.** Re-encoding destroys any non-pixel data embedded in the file. A polyglot file (e.g., valid JPEG that is also valid JavaScript) cannot survive full decode to `image.Image` (pixel buffer) and re-encode to a new file. The re-encoded file contains only pixel data in the target format's container.

### 7.3 What This Architecture Eliminates

| Vulnerability | How It Is Eliminated |
|---------------|---------------------|
| **TOCTOU race condition (CRIT-001)** | Client never touches MinIO. Cannot swap files because only the gateway writes to storage, and results are communicated via trusted Redis channel |
| **No content integrity (CRIT-002)** | SHA256 is computed by trusted server (image-gateway) after processing. Never client-provided |
| **File type spoofing (CRIT-003)** | Magic bytes + full decode + re-encode. A non-image file cannot survive `image.Decode()` |
| **EXIF/GPS data leaks** | Stripped during decode stage. Re-encode produces clean file |
| **Presigned URL abuse** | No presigned PUT URLs exist anymore. Presigned GET URLs remain for downloads only |
| **Oversized uploads** | image-gateway enforces `max_file_size` from JWT token at HTTP layer (`http.MaxBytesReader`) |
| **Content-type mismatch** | Magic bytes must match JWT-declared content type. Then re-encoded to target format |
| **Decompression bombs** | `image.DecodeConfig()` extracts dimensions without allocating pixel buffer. Reject before full decode |

---

## 8. Image Resize and Marker Scaling

### 8.1 Resize Rules

| Parameter | Value | Configurable |
|-----------|-------|-------------|
| Max width | 1920px | Yes (`IMG_GW_MAX_IMAGE_WIDTH`) |
| Aspect ratio | Always maintained | No (hardcoded invariant) |
| Crop | NEVER | No (hardcoded invariant) |
| Upscale | NEVER (if original <= max, keep original) | No (hardcoded invariant) |

**Resize formula:**

```
IF original_width > max_width:
    scale = max_width / original_width
    processed_width = max_width
    processed_height = round(original_height * scale)
ELSE:
    processed_width = original_width
    processed_height = original_height
    scale = 1.0
```

### 8.2 Marker Auto-Scaling

When image dimensions change due to resize, waypoint marker coordinates must scale proportionally to remain accurately positioned on the processed image.

```
scale_x = processed_width / original_width
scale_y = processed_height / original_height

new_marker_x = round(original_marker_x * scale_x)
new_marker_y = round(original_marker_y * scale_y)
```

**Critical invariant:** Because we always maintain aspect ratio and never crop, `scale_x == scale_y` (always). This guarantees that markers remain accurately positioned -- there is no distortion or offset introduced by the resize.

**Example:**

```
Original:   4032 x 3024 pixels
Max width:  1920 pixels
Scale:      1920 / 4032 = 0.4762

Processed:  1920 x 1440 pixels (3024 * 0.4762 = 1440)

Original marker: (2016, 1512)
Scaled marker:   (round(2016 * 0.4762), round(1512 * 0.4762))
                = (960, 720)
```

### 8.3 Dimension Reporting and Coordinate Update Flow

image-gateway reports both original and processed dimensions in the result message. follow-api then:

1. Stores both dimension sets on the Image entity (`original_width`, `original_height`, `processed_width`, `processed_height`)
2. Computes the scale factor from original to processed
3. Updates the Waypoint entity with the new scaled marker coordinates
4. Sends updated coordinates to the client via SSE so the client displays markers on the processed image correctly

**Database schema additions for Image entity:**

```sql
ALTER TABLE image.images ADD COLUMN original_width  INTEGER;
ALTER TABLE image.images ADD COLUMN original_height INTEGER;
ALTER TABLE image.images ADD COLUMN processed_width  INTEGER;
ALTER TABLE image.images ADD COLUMN processed_height INTEGER;
ALTER TABLE image.images ADD COLUMN sha256           VARCHAR(64);
```

---

## 9. Client Communication

### 9.1 SSE (Server-Sent Events) -- Real-time

**Endpoint:** `GET /images/status/stream?image_ids=id1,id2,id3`

**Authentication:** Standard JWT Bearer token (same as all follow-api endpoints)

**Connection behavior:**

- follow-api holds the SSE connection open
- Internal Go channel bridges the Redis consumer goroutine to the SSE HTTP handler
- follow-api reads `image:status:{id}` hashes from Redis for progress updates
- follow-api receives result messages from `image:result` stream for completion events
- Connection auto-closes after all requested images reach terminal state (done or failed)
- Heartbeat every 30 seconds to detect stale connections

**Event types:**

```
event: image_progress
data: {"image_id": "uuid", "stage": "decoding", "progress": 30}

event: image_processed
data: {
  "image_id": "uuid",
  "status": "processed",
  "preview_url": "https://storage.example.com/signed/...",
  "original_dimensions": {"width": 4032, "height": 3024},
  "processed_dimensions": {"width": 1920, "height": 1440},
  "updated_marker": {"x": 960, "y": 720}
}

event: image_failed
data: {
  "image_id": "uuid",
  "error_code": "INVALID_MAGIC_BYTES",
  "error_message": "File magic bytes do not match declared content type"
}

event: route_ready
data: {"route_id": "uuid", "all_images_processed": true}

event: heartbeat
data: {"timestamp": "2026-01-29T10:00:30Z"}
```

### 9.2 HTTP Polling -- Fallback

**Endpoint:** `GET /images/{id}/status`

**Authentication:** Standard JWT Bearer token

For clients that cannot maintain SSE connections (network constraints, background processing):

```json
{
  "image_id": "uuid",
  "status": "processing",
  "stage": "encoding",
  "progress": 75,
  "preview_url": null,
  "updated_at": "2026-01-29T10:00:04Z"
}
```

After processing completes:

```json
{
  "image_id": "uuid",
  "status": "processed",
  "stage": "done",
  "progress": 100,
  "preview_url": "https://storage.example.com/signed/...",
  "processed_dimensions": {"width": 1920, "height": 1440},
  "updated_marker": {"x": 960, "y": 720},
  "updated_at": "2026-01-29T10:00:05Z"
}
```

This endpoint reads from PostgreSQL (already updated by the Redis consumer goroutine), so it reflects the most recent committed state.

---

## 10. Error Handling and Recovery

### 10.1 image-gateway Crashes Mid-Processing

**Scenario:** Gateway instance dies while processing an image (e.g., OOM during decode of a very large image).

**Recovery mechanism:**

- The job was claimed via `XREADGROUP` but never `XACK`-ed
- Redis tracks the pending entry in the consumer group's Pending Entries List (PEL)
- After a configurable idle timeout (default: 5 minutes), another gateway instance claims the orphaned job via `XCLAIM`
- The new instance re-processes the image from scratch (re-reads raw bytes from the upload, or the client re-uploads)

**Idempotency guarantee:** Re-processing the same image is safe. The `PutObject` to MinIO overwrites the same storage key. The result message to Redis uses the same `image_id`.

**Configuration:**

```
IMG_GW_JOB_CLAIM_IDLE_MS=300000  # 5 minutes before orphan reclaim
IMG_GW_MAX_DELIVERY_COUNT=3       # Max retries before marking as failed
```

### 10.2 follow-api Misses a Result

**Scenario:** follow-api restarts or its Redis consumer goroutine crashes after a result is published but before it is acknowledged.

**Recovery mechanism:**

- The result message was delivered via `XREADGROUP` but not `XACK`-ed
- On restart, follow-api re-reads all pending (unacknowledged) messages using `XREADGROUP ... 0` (read pending entries)
- Updates are idempotent: setting an Image entity to PROCESSED with the same SHA256 and dimensions is safe

### 10.3 Client Upload Never Arrives

**Scenario:** follow-api created the route and published jobs to Redis, but the client never uploads (e.g., user closes app).

**Recovery mechanism:**

- The job sits in Redis Stream with `stage: "queued"` or `stage: "waiting_upload"` in the status hash
- The JWT upload token expires after 15 minutes -- the gateway rejects late uploads
- Redis Hash TTL: 1 hour auto-cleanup via `EXPIRE`
- follow-api background job (runs every 10 minutes): images stuck in PENDING status for more than 30 minutes are transitioned to EXPIRED status
- The route remains in PENDING status until all images are resolved (processed, failed, or expired)

### 10.4 MinIO Upload Fails

**Scenario:** `PutObject` to MinIO fails (network issue, disk full, bucket policy error).

**Recovery mechanism:**

- image-gateway retries `PutObject` with exponential backoff: 1s, 2s, 4s (3 attempts max)
- On final failure: publish failure result to `image:result` stream with `{image_id, status: "failed", error_code: "STORAGE_UPLOAD_FAILED", error_message}` (no route/waypoint fields)
- follow-api consumes the failure, looks up the waypoint/route from DB, marks the Image entity as FAILED, and pushes SSE error to client
- Client can retry: call `POST /routes/{id}/retry-image/{image_id}` (future endpoint) to get a new upload URL

### 10.5 Redis Unavailable

**Scenario:** Redis is down or unreachable.

**Impact:**

- follow-api cannot publish jobs -- route creation fails with 503 Service Unavailable
- image-gateway cannot consume jobs or publish results -- uploads are rejected with 503
- SSE progress updates stop (follow-api cannot read status hashes)

**Mitigation:**

- Redis health check at startup (both services)
- Redis connection pool with automatic reconnection
- Circuit breaker pattern: after N consecutive Redis failures, stop accepting new uploads and return 503
- All Redis operations have timeouts (default: 5 seconds)

### 10.6 Partial Route Failure

**Scenario:** 3 out of 5 images process successfully, but 2 fail validation.

**Behavior:**

- Route remains in PENDING status (not all images resolved)
- Client receives SSE events for each image individually
- Client can see which images succeeded (with preview URLs) and which failed (with error messages)
- Client can retry failed images by requesting new upload URLs for specific waypoints
- Route transitions to READY only when ALL images are in PROCESSED status

---

## 11. Infrastructure and Deployment

### 11.1 Docker Compose (Local Development)

```yaml
services:
  follow-api:
    build: ./follow-api
    ports:
      - "8080:8080"
    environment:
      - DATABASE_URL=postgres://follow:password@postgres:5432/follow?sslmode=disable
      - REDIS_URL=redis://redis:6379
      - MINIO_ENDPOINT=minio:9000
      - FOLLOW_API_ED25519_PRIVATE_KEY=${FOLLOW_API_ED25519_PRIVATE_KEY}
      - IMG_GW_ED25519_PUBLIC_KEY=${IMG_GW_ED25519_PUBLIC_KEY}
    depends_on:
      - postgres
      - redis
      - minio

  follow-image-gateway:
    build: ./follow-image-gateway
    ports:
      - "8090:8090"
    environment:
      - IMG_GW_PORT=8090
      - IMG_GW_REDIS_URL=redis://redis:6379
      - IMG_GW_MINIO_ENDPOINT=minio:9000
      - IMG_GW_MINIO_BUCKET=follow-images
      - IMG_GW_MINIO_ACCESS_KEY=${MINIO_ACCESS_KEY}
      - IMG_GW_MINIO_SECRET_KEY=${MINIO_SECRET_KEY}
      - FOLLOW_API_ED25519_PUBLIC_KEY=${FOLLOW_API_ED25519_PUBLIC_KEY}
      - IMG_GW_ED25519_PRIVATE_KEY=${IMG_GW_ED25519_PRIVATE_KEY}
    depends_on:
      - redis
      - minio

  postgres:
    image: postgres:17-alpine
    ports:
      - "5432:5432"
    environment:
      - POSTGRES_DB=follow
      - POSTGRES_USER=follow
      - POSTGRES_PASSWORD=password
    volumes:
      - postgres_data:/var/lib/postgresql/data

  redis:
    image: redis:7-alpine
    ports:
      - "6379:6379"
    command: redis-server --appendonly yes --maxmemory 256mb --maxmemory-policy noeviction
    volumes:
      - redis_data:/data

  minio:
    image: minio/minio:latest
    ports:
      - "9000:9000"
      - "9001:9001"
    environment:
      - MINIO_ROOT_USER=${MINIO_ACCESS_KEY}
      - MINIO_ROOT_PASSWORD=${MINIO_SECRET_KEY}
    command: server /data --console-address ":9001"
    volumes:
      - minio_data:/data

volumes:
  postgres_data:
  redis_data:
  minio_data:
```

**Service connectivity:**

| Service | Connects To | Protocol | Purpose |
|---------|-------------|----------|---------|
| follow-api | PostgreSQL | TCP 5432 | Entity persistence |
| follow-api | Redis | TCP 6379 | Job publishing, result consuming, status reading |
| follow-api | MinIO | TCP 9000 | StatObject (verification), PresignedGetObject (downloads) |
| follow-image-gateway | Redis | TCP 6379 | Job consuming, result publishing, status writing |
| follow-image-gateway | MinIO | TCP 9000 | PutObject (processed images) |
| follow-image-gateway | PostgreSQL | NONE | Gateway has no database access |

### 11.2 Configuration

**follow-image-gateway environment variables:**

| Variable | Default | Description |
|----------|---------|-------------|
| `IMG_GW_PORT` | 8090 | HTTP server port |
| `IMG_GW_HOST` | 0.0.0.0 | HTTP server host |
| `IMG_GW_LOG_LEVEL` | info | Log level (trace, debug, info, warn, error) |
| `IMG_GW_REDIS_URL` | redis://localhost:6379 | Redis connection URL |
| `IMG_GW_MINIO_ENDPOINT` | localhost:9000 | MinIO/S3 endpoint |
| `IMG_GW_MINIO_BUCKET` | follow-images | Target bucket name |
| `IMG_GW_MINIO_ACCESS_KEY` | (required) | MinIO access key |
| `IMG_GW_MINIO_SECRET_KEY` | (required) | MinIO secret key |
| `IMG_GW_MINIO_USE_SSL` | false | Use SSL for MinIO connection |
| `FOLLOW_API_ED25519_PUBLIC_KEY` | (required) | follow-api's Ed25519 public key (base64) |
| `IMG_GW_ED25519_PRIVATE_KEY` | (required) | Gateway's own Ed25519 private key (base64) |
| `IMG_GW_MAX_FILE_SIZE` | 10485760 | Maximum upload size in bytes (10MB) |
| `IMG_GW_MAX_IMAGE_WIDTH` | 1920 | Maximum output image width in pixels |
| `IMG_GW_MAX_PIXEL_COUNT` | 100000000 | Decompression bomb threshold (100 megapixels) |
| `IMG_GW_COMPRESSION_QUALITY` | 85 | Output compression quality (1-100) |
| `IMG_GW_OUTPUT_FORMAT` | webp | Preferred output format (webp, jpeg) |
| `IMG_GW_PIPELINE_VALIDATE_WORKERS` | 2 | Concurrent validate stage workers |
| `IMG_GW_PIPELINE_DECODE_WORKERS` | 2 | Concurrent decode stage workers |
| `IMG_GW_PIPELINE_PROCESS_WORKERS` | 1 | Concurrent process stage workers |
| `IMG_GW_PIPELINE_ENCODE_WORKERS` | 2 | Concurrent encode stage workers |
| `IMG_GW_PIPELINE_UPLOAD_WORKERS` | 3 | Concurrent upload stage workers |
| `IMG_GW_JOB_CLAIM_IDLE_MS` | 300000 | Orphan job reclaim timeout (ms) |
| `IMG_GW_MAX_DELIVERY_COUNT` | 3 | Max retries before permanent failure |
| `IMG_GW_UPLOAD_RETRY_COUNT` | 3 | MinIO PutObject retry attempts |
| `IMG_GW_UPLOAD_RETRY_BASE_MS` | 1000 | Base delay for exponential backoff (ms) |
| `IMG_GW_SHUTDOWN_TIMEOUT` | 30s | Graceful shutdown timeout |

**follow-api new environment variables (additions):**

| Variable | Default | Description |
|----------|---------|-------------|
| `REDIS_URL` | redis://localhost:6379 | Redis connection URL |
| `FOLLOW_API_ED25519_PRIVATE_KEY` | (required) | follow-api's Ed25519 private key (base64) |
| `IMG_GW_ED25519_PUBLIC_KEY` | (required) | Gateway's Ed25519 public key (base64) |
| `IMG_GW_BASE_URL` | http://localhost:8090 | Gateway base URL for upload URLs |
| `IMAGE_EXPIRY_MINUTES` | 30 | Minutes before pending images are marked expired |
| `SSE_HEARTBEAT_SECONDS` | 30 | SSE heartbeat interval |

---

## 12. Route Lifecycle (Updated)

### Current Lifecycle

```
PREPARING --> PENDING --> ACTIVE --> ARCHIVED
```

### Updated Lifecycle (with image-gateway)

```
PREPARING --> PENDING --> READY --> PUBLISHED --> ARCHIVED
     |            |         |          |            |
     |            |         |          |            +-- Route taken down by owner
     |            |         |          +-- Visible to other users (navigable)
     |            |         +-- All images processed, creator can review
     |            +-- Images queued/uploading/processing
     +-- Route entity being created, JWT tokens being signed
```

**Transition rules:**

| From | To | Trigger | Automatic? |
|------|----|---------|-----------|
| PREPARING | PENDING | Route entities created, jobs queued to Redis | Yes (within create-waypoints handler) |
| PENDING | READY | ALL images reach PROCESSED status | Yes (Redis consumer checks after each image result) |
| READY | PUBLISHED | Client calls `POST /routes/{id}/publish` | No (explicit client action) |
| PUBLISHED | ARCHIVED | Client calls `DELETE /routes/{id}` or admin action | No (explicit action) |

**Note on existing codebase:** The current `RouteStatus` value object (`internal/domains/route/domain/valueobjects/route_status.go`) defines `preparing`, `pending`, `active`, `archived`. The new lifecycle replaces `active` with two distinct states: `ready` (creator reviewing) and `published` (publicly available). This requires:

- Adding `ready` and `published` to `validRouteStatuses`
- Updating `CanTransitionTo()` to reflect new transitions
- Updating `IsNavigable()` to return true for `published` (and optionally `ready` for the creator)
- Renaming or deprecating `active` status

### Route Status and Image Status Relationship

```
Route: PENDING
  |-- Image 1: PENDING --> PROCESSED
  |-- Image 2: PENDING --> PROCESSED
  |-- Image 3: PENDING --> FAILED (client retries --> PROCESSED)
  |
  All PROCESSED? --> Route: READY
  |
  Client reviews and publishes --> Route: PUBLISHED
```

### Waypoint Lifecycle

Waypoint status values: `pending` --> `confirmed`

- **pending**: Created during Phase 1 (create-waypoints). Waypoint has original marker coordinates and a PENDING image.
- **confirmed**: Set AUTOMATICALLY by follow-api when the image processing result is consumed from Redis. The DB transaction in Phase 4 updates the waypoint status to `confirmed` and auto-scales marker coordinates in the same transaction.

**Trigger change:** Previously, waypoint confirmation was client-initiated (via `confirm_route_waypoints` endpoint). Now it is server-initiated -- follow-api's Redis consumer automatically confirms each waypoint when its image result arrives. The `WaypointStatus` value object does NOT need changes; only the trigger mechanism changes.

---

## 13. Architecture Decision Records

This section references formal Architecture Decision Records (ADRs). Each ADR documents a major architectural choice with context, decision, alternatives, and consequences.

### Decision Records

The following ADRs describe critical architectural choices for follow-image-gateway:

- **ADR-001:** [Use Pipes and Filters Architecture for Image Gateway](../adr/001-pipes-and-filters-architecture.md) -- Why Pipes and Filters instead of Clean Architecture for a stateless pipeline service.

- **ADR-015:** [Introduce follow-image-gateway for Secure Image Processing](../adr/015-introduce-image-gateway.md) -- Why a separate microservice is needed to address TOCTOU race conditions, lack of content integrity verification, and missing server-side image validation.

- **ADR-016:** [Redis Streams for Inter-Service Communication](../adr/016-redis-streams-inter-service-communication.md) -- Why Redis Streams with consumer groups instead of HTTP callbacks, RabbitMQ, or gRPC.

- **ADR-017:** [Ed25519 Asymmetric Signing for Service-to-Service Authentication](../adr/017-ed25519-asymmetric-signing.md) -- Why Ed25519 asymmetric signing instead of HMAC-SHA256 or mTLS for JWT token verification.

- **ADR-018:** [Go Channel Pipeline for Async Image Processing](../adr/018-go-channel-pipeline.md) -- Why Go channels with fan-out workers instead of single-threaded processing or external task queues.

- **ADR-019:** [Server-Side Image Validation with Re-encoding](../adr/019-server-side-image-validation.md) -- Why 4-layer validation (magic bytes, header parsing, full decode, re-encode) is necessary to eliminate polyglot files and embedded payloads.

- **ADR-020:** [SSE + HTTP Polling for Client Status Updates](../adr/020-sse-http-polling-status.md) -- Why Server-Sent Events with HTTP polling fallback instead of WebSockets, long polling, or push notifications.

- **ADR-021:** [Eliminate Client Upload Confirmation](../adr/021-eliminate-client-confirmation.md) -- Why the server-to-server trust chain removes the need for client confirmation, and how routes auto-transition from PENDING to READY.

- **ADR-022:** [Domain-Agnostic Image Processing Pipeline](../adr/022-domain-agnostic-processing.md) -- Why Redis messages contain only image metadata (no route_id/waypoint_id), enabling reuse of the gateway for other image processing needs.

---

## 14. Migration Path

The migration from the current presigned URL flow to the image-gateway architecture should be executed in phases to minimize risk.

### Phase 1: Infrastructure Setup

1. Add Redis to Docker Compose and production infrastructure
2. Generate Ed25519 key pairs for both services
3. Distribute keys via environment variables / Docker secrets
4. Create `follow-image-gateway` repository (or module within monorepo)

### Phase 2: Build image-gateway

1. Implement HTTP server with `PUT /upload` endpoint
2. Implement Ed25519 JWT token verification
3. Implement 4-stage processing pipeline (validate, analyze, transform, upload)
4. Implement Valkey SET NX upload guard (`image:upload:{id}`) and Streams producer (`image:result`)
5. Implement Valkey Hash progress updates (`image:status:{id}`)
6. Integration tests with real Valkey and MinIO

### Phase 3: Update follow-api

1. Add Valkey client and connection pool to follow-api
2. Implement Valkey Streams consumer (`image:result`) and Hash writer (`image:status:{id}` queued)
3. Add SSE endpoint: `GET /images/status/stream`
4. Add polling endpoint: `GET /images/{id}/status`
5. Update `CreateRouteWithWaypointsUseCase` to:
   - Sign Ed25519 JWT tokens (replacing presigned MinIO URLs)
   - Write `image:status:{id}` hash with `{stage: queued}` to Valkey
   - Return gateway upload URLs instead of MinIO presigned URLs
6. Implement Valkey consumer goroutine for processing results
7. Add marker auto-scaling logic to result consumption
8. Add `ready` and `published` route statuses to domain model
9. Implement route auto-transition (PENDING -> READY when all images processed)
10. Update `ConfirmRouteWaypointsUseCase` to become `PublishRouteUseCase`
11. Add Image entity fields: `original_width`, `original_height`, `processed_width`, `processed_height`, `sha256`
12. Add background job for expiring stale pending images

### Phase 4: Database Migration

1. Add new columns to `image.images` table (dimensions, SHA256)
2. Add `ready` and `published` values to route status enum (if using PostgreSQL enum)
3. Migrate existing `active` routes to `published` status

### Phase 5: Client Update

1. Update Flutter client to upload to gateway URL instead of MinIO presigned URL
2. Implement SSE client for real-time progress tracking
3. Replace confirmation step with publish step
4. Update UI to show processing progress, preview images, and scaled markers

### Phase 6: Cleanup

1. Remove `ConfirmImageUploadUseCase` and `ConfirmRouteWaypointsUseCase`
2. Remove presigned PUT URL generation (`GeneratePresignedUploadURL`)
3. Remove `BatchConfirmImageUploadsUseCase`
4. Keep presigned GET URL generation for downloads (`GeneratePresignedDownloadURL`)
5. Update API design files (`design/route_types.go`, `design/route_service.go`)
6. Remove `ConfirmRouteWaypointsResult` and related types
7. Update tests to reflect new flow

---

## 15. Future Enhancements

Listed in approximate priority order:

| Enhancement | Description | Prerequisite |
|-------------|-------------|-------------|
| **Face detection + blur** | ML model integration in pipeline Stage 3. Gaussian blur on detected face regions. Privacy protection for bystanders | ML model selection, GPU evaluation |
| **License plate detection + blur** | ML model integration in pipeline Stage 3. Blur detected plates. Legal compliance | ML model selection |
| **Image quality scoring** | Reject blurry, dark, or unusable navigation photos. Return quality score with error message | Quality metric research |
| **Multiple output formats** | Generate both WebP and JPEG for older device compatibility. Store both, serve appropriate format | Content negotiation in download URLs |
| **Thumbnail generation** | Generate small thumbnails (200px wide) for route listing previews. Store at separate storage key | Thumbnail storage key convention |
| **Retry endpoint** | `POST /routes/{id}/retry-image/{image_id}` -- generate new upload URL for a failed image without recreating the route | Partial route failure UX |
| **Batch upload progress** | Aggregate progress across all images in a route (e.g., "3/5 images processed, 60% overall") | Route-level progress aggregation |
| **CDN integration** | Serve processed images via CDN edge nodes. Cache with long TTL since processed images are immutable | CDN provider selection, cache invalidation strategy |
| **Webhook notifications** | `POST` to configurable URL when route processing completes. For third-party integrations | Webhook registration API |

---

## References

### Internal Documents

- Security review: `ai-docs/security/security-review-2026-01-10.md`
- Image upload integrity research: `ai-docs/security/image-upload-integrity-and-validation.md`
- Current image domain types: `internal/domains/image/interfaces/types.go`
- Current confirmation flow: `internal/domains/image/usecases/confirm_image_upload.go`
- Current API design: `design/route_service.go`, `design/route_types.go`
- Route status value object: `internal/domains/route/domain/valueobjects/route_status.go`
- Existing ADRs: `docs/adr/0001-0014`
- Event system architecture: `ai-docs/architecture/event-system-architecture.md`

### External References

- Redis Streams documentation: https://redis.io/docs/data-types/streams/
- Go `image` package: https://pkg.go.dev/image
- Ed25519 signing: https://pkg.go.dev/crypto/ed25519
- MinIO Go SDK: https://min.io/docs/minio/linux/developers/go/minio-go.html
- Server-Sent Events specification: https://html.spec.whatwg.org/multipage/server-sent-events.html
- JPEG magic bytes: https://en.wikipedia.org/wiki/JPEG#Syntax_and_structure
- WebP container specification: https://developers.google.com/speed/webp/docs/riff_container
