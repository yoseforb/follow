# Valkey Message Contract

**Source of Truth**: `github.com/yoseforb/follow-pkg/valkey/contracts.go`

All Valkey key patterns, field names, and status values are defined as Go constants in the shared `follow-pkg/valkey` package. Both `follow-api` and `follow-image-gateway` import and use these constants — no string literals allowed.

---

## Key Pattern 1: `image:status:{image_id}` (Progress Hash)

| Aspect | Value |
|--------|-------|
| Type | Hash |
| TTL | 1 hour |
| Writer | Gateway (pipeline stages) |
| Reader | API (SSE poller) |

### Fields

| Field | Constant | Type | Description |
|-------|----------|------|-------------|
| `stage` | `valkey.FieldStage` | string | Current processing stage |
| `progress` | `valkey.FieldProgress` | string | Percentage `"0"`–`"100"`, or `"-1"` on failure |
| `updated_at` | `valkey.FieldUpdatedAt` | string | RFC3339 timestamp |
| `error` | `valkey.FieldError` | string | Error message (only when stage=failed) |

### Stage Values

| Value | Constant | Written By | Meaning |
|-------|----------|------------|---------|
| `queued` | `valkey.StageQueued` | API | Initial state when image record is created |
| `validating` | `valkey.StageValidating` | Gateway | Validate stage running |
| `decoding` | `valkey.StageDecoding` | Gateway | Analyze stage — decoding image |
| `processing` | `valkey.StageProcessing` | Gateway | Analyze stage — ML detection |
| `encoding` | `valkey.StageEncoding` | Gateway | Transform stage — encoding output |
| `uploading` | `valkey.StageUploading` | Gateway | Upload stage — writing to MinIO |
| `done` | `valkey.StageDone` | Gateway | Processing completed successfully |
| `failed` | `valkey.StageFailed` | Gateway | Processing failed |

### SSE Mapping (API → Flutter)

The API maps gateway stage values to SSE event types for the Flutter client:

| Gateway Stage(s) | SSE Event Type | Terminal? |
|-------------------|---------------|-----------|
| `done` | `"ready"` | Yes |
| `failed` | `"failed"` | Yes |
| All others (queued, validating, decoding, processing, encoding, uploading) | `"processing"` | No |

---

## Key Pattern 2: `image:upload:{image_id}` (Upload Guard)

| Aspect | Value |
|--------|-------|
| Type | String (SET NX) |
| TTL | 1 hour |
| Writer | Gateway |
| Reader | Gateway |
| Value | `"claimed"` (`valkey.UploadGuardValue`) |

One-time claim: SET NX succeeds on first upload attempt, fails on duplicates (409 Conflict).

---

## Key Pattern 3: `image:result` (Result Stream)

| Aspect | Value |
|--------|-------|
| Type | Redis Stream |
| Stream Key | `image:result` (`valkey.StreamImageResult`) |
| Consumer Group | `api-workers` (`valkey.ConsumerGroupAPIWorkers`) |
| Writer | Gateway (XADD after pipeline completes) |
| Reader | API (XREADGROUP consumer loop) |
| Trimming | MAXLEN ~1000 |

### Success Message Fields

| Field | Constant | Example |
|-------|----------|---------|
| `image_id` | `valkey.ResultFieldImageID` | `"abc-123"` |
| `status` | `valkey.ResultFieldStatus` | `"processed"` (`valkey.ResultStatusProcessed`) |
| `storage_key` | `valkey.ResultFieldStorageKey` | `"images/abc-123.webp"` |
| `sha256` | `valkey.ResultFieldSHA256` | `"e3b0c44..."` |
| `etag` | `valkey.ResultFieldETag` | `"abc123"` |
| `file_size` | `valkey.ResultFieldFileSize` | `"245760"` |
| `content_type` | `valkey.ResultFieldContentType` | `"image/webp"` |
| `original_width` | `valkey.ResultFieldOriginalWidth` | `"4032"` |
| `original_height` | `valkey.ResultFieldOriginalHeight` | `"3024"` |
| `processed_width` | `valkey.ResultFieldProcessedWidth` | `"1920"` |
| `processed_height` | `valkey.ResultFieldProcessedHeight` | `"1440"` |
| `processed_at` | `valkey.ResultFieldProcessedAt` | `"2026-02-24T10:00:00Z"` |

### Failure Message Fields

| Field | Constant | Example |
|-------|----------|---------|
| `image_id` | `valkey.ResultFieldImageID` | `"abc-123"` |
| `status` | `valkey.ResultFieldStatus` | `"failed"` (`valkey.ResultStatusFailed`) |
| `error_code` | `valkey.ResultFieldErrorCode` | `"VALIDATION_FAILED"` |
| `error_message` | `valkey.ResultFieldErrorMessage` | `"file too large"` |
| `failed_at` | `valkey.ResultFieldFailedAt` | `"2026-02-24T10:00:00Z"` |

---

## Rules

1. **Never use string literals** for Valkey keys, field names, or status values in Go code. Always use `valkey.*` constants from `follow-pkg`.
2. **Adding a new field or stage?** Add the constant to `follow-pkg/valkey/contracts.go` first, then use it in both services.
3. **SSE event types** (`"ready"`, `"failed"`, `"processing"`) are the API-to-Flutter contract and live in `follow-api`, not in `follow-pkg`.
4. **Config YAML files** use raw strings (they're parsed at runtime, not compiled).
