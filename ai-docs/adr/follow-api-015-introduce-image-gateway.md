# ADR-015: Introduce follow-image-gateway for Secure Image Processing

**Date:** 2026-01-29
**Status:** Proposed

## Context

The Follow API security review (2026-01-10) and subsequent image upload security research (2026-01-28) identified three critical vulnerabilities in the direct-to-MinIO upload flow via presigned URLs:

- **CRIT-001:** TOCTOU race condition in `confirm_image_upload.go` between storage metadata check and database update
- **CRIT-002:** No content integrity verification -- client provides only `content_type` and `file_size`, no hash
- **CRIT-003:** No server-side image validation -- uploaded files never verified as actual images

The root cause is that the server never sees the raw file bytes. MinIO's `PresignedPutObject()` generates a URL for direct client-to-storage upload with no enforceable conditions.

## Decision

Introduce `follow-image-gateway` as a separate Go microservice that:

1. Receives image uploads from clients via HTTP PUT (replacing presigned MinIO URLs)
2. Validates uploaded files are genuine images (magic bytes, header parsing, full decode)
3. Processes images (EXIF stripping, resize, re-encode; future: face/plate blurring)
4. Uploads processed images to MinIO/S3
5. Communicates results to follow-api via Redis Streams

## Alternatives Considered

1. **Server-side validation within follow-api (monolithic approach):** Add upload endpoint directly to follow-api. Rejected because image processing is CPU-intensive and would block API request handling. Separate service allows independent scaling and resource isolation.

2. **Keep presigned URLs with post-upload server validation:** After client confirms, download the file from MinIO and validate server-side. Rejected because this does not fix CRIT-001 (file can still be swapped), doubles storage bandwidth, and adds latency to the confirmation flow.

3. **S3 Lambda/trigger-based validation:** Use MinIO notifications or S3 Lambda to validate after upload. Rejected because MinIO notification support is limited, Lambda adds AWS coupling, and we need processing capabilities not just validation.

## Consequences

### Positive
- Eliminates entire class of presigned URL upload vulnerabilities (CRIT-001, CRIT-002, CRIT-003)
- Enables image processing pipeline (EXIF stripping, resize, future ML processing)
- SHA256 computed by trusted server -- never client-provided
- Gateway scales independently from API server
- Stateless design enables horizontal scaling

### Negative
- Adds infrastructure complexity (new service, Redis dependency)
- All image traffic flows through gateway (bandwidth cost -- acceptable for navigation photo sizes, typically 2-10MB)
- Additional latency in upload flow (processing time) -- mitigated by async pipeline and SSE progress
- Requires Ed25519 key generation and distribution

## References

- Security review: `ai-docs/security/security-review-2026-01-10.md`
- Image upload integrity research: `ai-docs/security/image-upload-integrity-and-validation.md`
- Current confirmation flow: `internal/domains/image/usecases/confirm_image_upload.go`
