# ADR-022: Domain-Agnostic Image Processing Pipeline

**Date:** 2026-01-29
**Status:** Accepted

## Context

The image-gateway and Redis communication layer could carry route and waypoint IDs as pass-through metadata, coupling the image pipeline to route domain concepts. Alternatively, the pipeline can be kept completely domain-agnostic, processing images without knowledge of their business context.

## Decision

Keep image-gateway and Redis Streams completely domain-agnostic. Redis messages contain only `image_id` and image-related metadata (`storage_key`, `content_type`, `file_size`, dimensions, `sha256`). No `route_id`, `waypoint_id`, or marker coordinates flow through Redis or image-gateway. follow-api owns the mapping between images and domain entities (routes, waypoints) and performs all domain-specific logic (marker scaling, completion detection) internally using PostgreSQL.

### Redis Message Schema (Image-Scoped Only)

**image:process stream (Job Queue):**
```json
{
  "image_id": "550e8400-e29b-41d4-a716-446655440000",
  "storage_key": "images/550e8400/photo.webp",
  "expected_content_type": "image/jpeg",
  "max_file_size": 10485760,
  "upload_token": "<Ed25519-signed JWT>",
  "requested_at": "2026-01-29T10:00:00Z"
}
```

**image:result stream (Results - Success):**
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

## Alternatives Considered

1. **Pass route_id/waypoint_id through Redis as metadata:** Simpler for follow-api's result consumer (no DB lookup needed to find the owning waypoint/route). Rejected because it couples the pipeline to route domain concepts, preventing reuse for other image needs (profile photos, report images, etc.).

2. **Redis SET/DECR counters for completion detection:** Use `route:{route_id}:pending_waypoints` counter in Redis, decrementing on each processed result. Rejected because it adds Redis state management complexity for something PostgreSQL handles efficiently within an already-open transaction. The COUNT query (~0.1-0.5ms on max 50 rows with an indexed foreign key) is cheaper than the UPDATE writes in the same transaction.

## Consequences

### Positive
- image-gateway is reusable for ANY image processing need (profile photos, report images, etc.) without changes
- Redis schema is simple and image-scoped -- only three key patterns: `image:process`, `image:result`, `image:status:{image_id}`
- Completion detection uses a DB transaction (cheap COUNT query piggybacking on existing writes)
- follow-api is the single place that understands domain relationships
- No domain knowledge leaks into infrastructure (Redis, image-gateway)

### Negative
- follow-api's result consumer must perform a DB lookup (`SELECT waypoint_id, route_id FROM waypoints WHERE image_id = $1`) to resolve domain context for each result -- adds one indexed SELECT per image result
- If image-gateway is later used by a different service, that service must also maintain its own image-to-domain mapping

## Result Consumer Transaction Flow

follow-api processes each successful result in a single DB transaction:

```sql
BEGIN TRANSACTION

  -- Step 1: Look up which waypoint/route owns this image
  SELECT waypoint_id, route_id FROM waypoints WHERE image_id = $image_id

  -- Step 2: Update Image entity
  UPDATE images SET
    status           = 'PROCESSED',
    sha256           = <from result>,
    etag             = <from result>,
    file_size        = <from result>,
    content_type     = <from result>,
    original_width   = <from result>,
    original_height  = <from result>,
    processed_width  = <from result>,
    processed_height = <from result>
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
```

The COUNT query is cheap (~0.1-0.5ms) because:
- route_id is indexed (foreign key)
- Max 50 waypoints per route
- Piggybacking on an already-open DB connection/transaction
- Cheaper than the UPDATE writes in the same transaction

## References

- State Ownership and Domain Separation: `#4.2-state-ownership-and-domain-separation` in architecture document
- Redis Data Structures: `#6-redis-data-structures` in architecture document
- Result Consumption: `#5.4-phase-4-result-consumption--auto-activation` in architecture document
