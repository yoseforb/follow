# follow-image-gateway Architecture Planning Document

**Date:** 2026-01-30
**Status:** Proposed
**Author:** Architecture Planning Team
**Purpose:** Answer the critical question: **Is DDD/Clean Architecture right for follow-image-gateway?**

---

## Table of Contents

1. [Executive Summary](#1-executive-summary)
2. [The Critical Question](#2-the-critical-question)
3. [Why NOT DDD/Clean Architecture](#3-why-not-dddclean-architecture)
4. [Proposed Architecture: Pipes and Filters](#4-proposed-architecture-pipes-and-filters)
5. [Project Structure](#5-project-structure)
6. [Key Interfaces (Building Blocks)](#6-key-interfaces-building-blocks)
7. [Pipeline Architecture Detail](#7-pipeline-architecture-detail)
8. [What to Reuse from follow-api](#8-what-to-reuse-from-follow-api)
9. [Architecture Comparison](#9-architecture-comparison)
10. [TDD Workflow Adaptation](#10-tdd-workflow-adaptation)
11. [Implementation Phases](#11-implementation-phases)
12. [Definition of Done](#12-definition-of-done)
13. [Main Thread Report Template](#13-main-thread-report-template)

---

## 1. Executive Summary

### The Answer: NO, DDD/Clean Architecture is NOT Right for follow-image-gateway

The follow-image-gateway is a **stateless byte processor**, not a domain-driven system. It has:

- ❌ **NO complex business domain** - it validates, decodes, processes, encodes, and uploads bytes
- ❌ **NO rich entities** - no aggregates, value objects, or domain events
- ❌ **NO bounded contexts** - a single pipeline with clear stages
- ❌ **NO CRUD operations** - no database, no persistence layer
- ❌ **NO complex orchestration** - linear processing pipeline

### What It Actually Needs

The gateway is a **processing pipeline** that needs:

- ✅ **Small, decoupled blocks** - each stage does ONE thing
- ✅ **Interface-based design** - for TDD and testability
- ✅ **Efficient for Claude Code** - small context per task
- ✅ **Proven infrastructure patterns** - reuse from follow-api
- ✅ **Clear data flow** - input → stages → output

### The Right Architecture: Pipes and Filters + Interface-Based Design

This document proposes a **Pipes and Filters** architecture with:
- Go channels as "pipes" connecting independent "filter" stages
- Each external dependency behind an interface
- Flat package structure with meaningful grouping
- Reuse of proven patterns from follow-api (logging, config, MinIO, testing)

---

## 2. The Critical Question

### User's Context

The user is building `follow-image-gateway` with these priorities:

1. **Small, decoupled blocks** - each responsible for ONE thing
2. **Interface-based design** - for TDD and testability
3. **Efficient for Claude Code** - small context per task
4. **Reuse infrastructure patterns** - from follow-api (logging, config, MinIO, testing, linter)
5. **Follow TDD workflow** - from `ai-docs/workflows/tdd-red-green-refactor-workflow.md`

### The Gateway's Reality

The gateway is a stateless microservice that:
- Receives image uploads from clients via HTTP PUT
- Validates images (magic bytes, header parsing, full decode, re-encode)
- Processes images (EXIF strip, resize, re-encode)
- Uploads processed images to MinIO/S3
- Communicates results to follow-api via Redis Streams
- Has **NO database**, **NO complex domain model**, **NO CRUD operations**

Full spec: `/home/yoseforb/pkg/follow/follow-api/ai-docs/architecture/image-gateway-architecture.md`

### The Question

**Should we apply DDD/Clean Architecture's 4-layer structure to this stateless pipeline service?**

**Answer: NO. Here's why.**

---

## 3. Why NOT DDD/Clean Architecture

### 3.1 DDD is for Complex Business Domains

**Domain-Driven Design** (DDD) is designed for systems with:

- **Rich Domain Models**: Entities with business invariants (e.g., "A Route must have 2-50 waypoints")
- **Aggregates**: Clusters of entities treated as a single unit (e.g., Route + Waypoints)
- **Value Objects**: Immutable domain concepts (e.g., RouteStatus, GeoCoordinate)
- **Domain Events**: Business-meaningful state changes (e.g., RoutePublished, WaypointConfirmed)
- **Bounded Contexts**: Domain boundaries with their own ubiquitous language

**The image-gateway has NONE of these:**

| DDD Concept | follow-api (Has It) | image-gateway (Has It?) |
|-------------|---------------------|------------------------|
| Rich Entities | Route, Waypoint, User | ❌ NO - just byte buffers |
| Aggregates | Route owns Waypoints | ❌ NO - stateless processing |
| Value Objects | RouteStatus, UserID | ❌ NO - just image dimensions |
| Domain Events | RouteCreated, ImageProcessed | ❌ NO - just Redis messages |
| Bounded Contexts | Route, User, Image domains | ❌ NO - single pipeline |
| Business Rules | "User can create max 10 waypoints" | ❌ NO - "Pixel count sub-100MP" is technical |

**Conclusion**: The gateway processes bytes, not business concepts. DDD patterns add no value here.

### 3.2 Clean Architecture's 4 Layers Don't Map to a Pipeline

**Clean Architecture** structures code into 4 concentric layers:

```
┌─────────────────────────────────────────┐
│  Frameworks & Drivers (HTTP, DB, MinIO) │
│  ┌──────────────────────────────────┐   │
│  │ Interface Adapters (Controllers, │   │
│  │   Presenters, Gateways)          │   │
│  │  ┌───────────────────────────┐   │   │
│  │  │ Use Cases (orchestration)│   │   │
│  │  │ ┌──────────────────────┐ │   │   │
│  │  │ │ Entities (business  │ │   │   │
│  │  │ │   logic, invariants)│ │   │   │
│  │  │ └──────────────────────┘ │   │   │
│  │  └───────────────────────────┘   │   │
│  └──────────────────────────────────┘   │
└─────────────────────────────────────────┘
```

**The gateway's actual logic is:**

```
Receive bytes → Validate → Decode → Process → Encode → Upload → Report
```

**Attempting to map this to Clean Architecture:**

| Layer | What Would Go Here? | Problem |
|-------|---------------------|---------|
| **Entities** | Image entity with business rules? | ❌ An `image.Image` pixel buffer is not a business entity |
| **Use Cases** | ValidateImageUseCase, ProcessImageUseCase? | ❌ These aren't "use cases" - they're pipeline stages with technical validation |
| **Interface Adapters** | Controllers, presenters, gateways? | ❌ The pipeline itself is the adapter between HTTP and storage |
| **Frameworks** | HTTP server, MinIO client, Redis client | ✅ Only this layer makes sense |

**The mismatch**: Clean Architecture assumes **orchestration of domain logic** (use cases) around **domain entities**. The gateway has **sequential byte processing** with **no domain entities**.

### 3.3 The Overhead of Layers Would Hurt

Each layer in Clean Architecture adds indirection. For a CRUD application with business rules, this is beneficial:

```go
// follow-api: Creating a route (makes sense with layers)
HTTP Controller → CreateRouteUseCase → Route Entity (validates business rules)
                                     → RouteRepository (persistence)
                                     → EventPublisher (domain events)
```

Each layer has a clear role. The entity enforces "must have 2-50 waypoints." The use case orchestrates user validation, route creation, and event publishing.

**For the gateway's pipeline:**

```go
// Attempting to apply layers to image validation (doesn't make sense)
HTTP Handler → ValidateImageUseCase → ??? Entity (a byte buffer with magic bytes?)
                                    → ??? Repository (there's no persistence)
                                    → Validator Gateway (finally checks magic bytes)
```

**The overhead:**
- **More files to navigate per feature**: Instead of one `validate.go` stage, you'd have `validate_usecase.go`, `image_validator_gateway.go`, `image_entity.go`, `validate_controller.go`
- **More boilerplate for no benefit**: Constructor DI, interface definitions, adapter methods, all for a 50-line magic byte check
- **Harder to understand the flow**: The pipeline flow (validate → decode → encode) is scattered across 4 layers
- **Slower development with Claude Code**: More context needed per task (must understand all 4 layers instead of one stage)

### 3.4 What the Gateway DOES Need from Clean Architecture

Clean Architecture embodies important **principles**, not just the 4-layer structure:

| Principle | How the Gateway Uses It |
|-----------|------------------------|
| **Interfaces for testability** | ✅ YES - `TokenValidator`, `ObjectUploader`, `JobConsumer` interfaces |
| **Dependency injection** | ✅ YES - Inject Redis, MinIO, JWT validator into pipeline |
| **Separation of concerns** | ✅ YES - Each pipeline stage has a single responsibility |
| **Independent of frameworks** | ✅ YES - Core pipeline logic doesn't depend on HTTP or Redis details |
| **Testable** | ✅ YES - Fake implementations for all external dependencies |

**Key insight**: We want the **principles** (interfaces, DI, separation of concerns) but NOT the **4-layer structure** (entities, use cases, adapters, frameworks).

### 3.5 Summary: Why NOT Clean Architecture

| Aspect | Clean Architecture Assumes | image-gateway Reality |
|--------|----------------------------|---------------------|
| **Domain complexity** | Rich business rules requiring entities | Simple technical validation (magic bytes, dimensions) |
| **Orchestration needs** | Use cases coordinate multiple entities | Linear pipeline with no coordination |
| **Persistence** | Repository pattern for data access | No database, no persistence |
| **Business events** | Domain events for workflow coordination | Infrastructure messages (Redis Streams) |
| **Code organization** | 4 layers with dependency inversion | Pipeline stages with clear data flow |

**Verdict**: Clean Architecture adds **overhead without benefit** for a stateless pipeline service.

---

## 4. Proposed Architecture: Pipes and Filters

### 4.1 The Pipes and Filters Pattern

**Pipes and Filters** is a proven architectural pattern for data processing systems:

- **Filters**: Independent processing components that transform data (stages: validate, decode, encode, upload)
- **Pipes**: Data channels connecting filters (Go channels carrying `ImageJob` structs)
- **Data flow**: Each filter reads from an input pipe, processes data, writes to an output pipe
- **Decoupling**: Filters know nothing about each other, only their input/output contract

**Example from real-world systems:**
- Unix shell pipelines: `cat file.txt | grep error | sort | uniq`
- Video encoding: decode → color correct → resize → encode
- ETL pipelines: extract → transform → load

### 4.2 Why Pipes and Filters for image-gateway

| Benefit | How It Applies |
|---------|----------------|
| **Small blocks** | Each stage is ~100-200 lines, does ONE thing (validate magic bytes, decode pixels, resize, encode, upload) |
| **Decoupled** | Stages only know about their input/output channel, not other stages |
| **Interface-based** | External dependencies (Redis, MinIO, JWT) behind interfaces for TDD |
| **Minimal context** | To work on "decode stage", you only need to understand: read `ImageJob` from channel → decode pixels → write to next channel |
| **No unnecessary layers** | No artificial entities/use cases/adapters - just filters and pipes |
| **Parallel processing** | Multiple workers per stage (2 decode workers, 3 upload workers) for throughput |
| **Easy failure handling** | If any stage fails, set `job.Error` and send directly to result channel |

### 4.3 Architecture Diagram

```
Upload Handler (HTTP PUT)
    │
    ▼
┌─────────────────────────────────────────────────────┐
│                  Pipeline Orchestrator               │
│  (creates channels, launches workers, wires stages) │
└─────────────────────────────────────────────────────┘
    │
    ▼
validateCh ──→ [Validate Stage (N workers)] ──→ decodeCh
               - Check magic bytes
               - Verify file size
               - Update progress
                                                  │
                                                  ▼
                                            [Decode Stage (N workers)] ──→ processCh
                                            - Parse image header
                                            - Full pixel decode
                                            - Strip EXIF
                                            - Update progress
                                                                            │
                                                                            ▼
                                                                       [Process Stage (N workers)] ──→ encodeCh
                                                                       - MVP: pass-through
                                                                       - Future: face/plate blur
                                                                       - Update progress
                                                                                                       │
                                                                                                       ▼
                                                                                                  [Encode Stage (N workers)] ──→ uploadCh
                                                                                                  - Resize to max width
                                                                                                  - Re-encode to WebP/JPEG
                                                                                                  - Compute SHA256
                                                                                                  - Update progress
                                                                                                                                 │
                                                                                                                                 ▼
                                                                                                                            [Upload Stage (N workers)] ──→ resultCh
                                                                                                                            - PutObject to MinIO
                                                                                                                            - Retry on failure
                                                                                                                            - Update progress
                                                                                                                                                           │
                                                                                                                                                           ▼
                                                                                                                                                      Result Publisher
                                                                                                                                                      - XADD to Redis
                                                                                                                                                      - XACK job
                                                                                                                                                      - Final status
```

### 4.4 Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| **Go channels as pipes** | Native Go concurrency primitive, built-in backpressure, clean goroutine coordination |
| **Configurable workers per stage** | Decode is CPU-heavy (2 workers), Upload is I/O-heavy (3 workers), Process is ML-heavy (1 worker) |
| **`ImageJob` struct flows through** | Single data structure carries all context (bytes, decoded image, metadata, errors) |
| **Error = skip remaining stages** | If validate fails, job goes directly to result channel with error details |
| **Progress via Redis Hash** | Each stage updates `image:status:{id}` for real-time client feedback |

---

## 5. Project Structure

```
follow-image-gateway/
├── cmd/
│   └── server/
│       └── main.go                     # Entry point, wiring, graceful shutdown
│
├── design/                             # Goa API design DSL
│   ├── design.go                       # API definition + security schemes
│   ├── upload_service.go               # PUT /upload endpoint
│   ├── health_service.go               # GET /health endpoint
│   └── types.go                        # Result types (UploadResult, HealthResult)
│
├── internal/
│   ├── config/                         # Configuration (reuse follow-api patterns)
│   │   ├── config.go                   # Gateway config struct + Load()
│   │   ├── validation.go               # Config validation
│   │   └── config_test.go
│   │
│   ├── server/                         # Goa server setup + service implementations
│   │   ├── server.go                   # Goa server initialization + middleware
│   │   ├── upload_service.go           # Upload service implementation
│   │   ├── health_service.go           # Health service implementation
│   │   ├── errors.go                   # Error mapping (domain → Goa errors)
│   │   ├── gen/                        # Goa generated code (gitignored)
│   │   └── upload_service_test.go
│   │
│   ├── auth/                           # JWT token validation (single responsibility)
│   │   ├── validator.go                # Ed25519 JWT validation interface + impl
│   │   ├── claims.go                   # Upload token claims struct
│   │   ├── errors.go                   # Auth-specific errors
│   │   └── validator_test.go
│   │
│   ├── pipeline/                       # Pipeline orchestrator
│   │   ├── pipeline.go                 # Creates channels, launches workers, wires stages
│   │   ├── job.go                      # ImageJob struct (flows through pipeline)
│   │   ├── errors.go                   # Pipeline-level errors
│   │   ├── pipeline_test.go            # Integration test of full pipeline
│   │   │
│   │   └── stages/                     # Individual pipeline stages
│   │       ├── stage.go                # Stage interface definition
│   │       ├── validate.go             # Stage 1: Magic bytes + size checks
│   │       ├── validate_test.go
│   │       ├── decode.go               # Stage 2: Decode + EXIF strip + dimensions
│   │       ├── decode_test.go
│   │       ├── process.go              # Stage 3: Processing (MVP: pass-through)
│   │       ├── process_test.go
│   │       ├── encode.go               # Stage 4: Resize + re-encode + SHA256
│   │       ├── encode_test.go
│   │       ├── upload.go               # Stage 5: PutObject to MinIO
│   │       └── upload_test.go
│   │
│   ├── messaging/                      # Redis Streams communication
│   │   ├── consumer.go                 # XREADGROUP from image:process stream
│   │   ├── producer.go                 # XADD to image:result stream
│   │   ├── progress.go                 # HSET to image:status:{id} hash
│   │   ├── types.go                    # Message schemas (ProcessJob, ProcessResult)
│   │   ├── errors.go                   # Messaging-specific errors
│   │   ├── consumer_test.go
│   │   ├── producer_test.go
│   │   └── progress_test.go
│   │
│   ├── storage/                        # MinIO upload client
│   │   ├── uploader.go                 # PutObject wrapper + retry logic
│   │   ├── errors.go                   # Storage-specific errors
│   │   └── uploader_test.go
│   │
│   └── health/                         # Health checks
│       ├── checker.go                  # Combined Redis + MinIO health
│       ├── errors.go
│       └── checker_test.go
│
├── testutil/                           # Test helpers (reuse patterns from follow-api)
│   ├── config.go                       # Test config helpers
│   ├── fixtures.go                     # Test image fixtures (valid JPEG, PNG, WebP + invalid files)
│   ├── fakes.go                        # Fake implementations of interfaces
│   └── helpers.go                      # Common test helpers
│
├── configs/
│   └── config.yaml                     # Default configuration
│
├── go.mod
├── go.sum
├── Dockerfile
├── docker-compose.yml                  # Standalone dev + integration with follow-api
├── .golangci-custom.yml                # Reuse from follow-api
├── CLAUDE.md                           # Project-specific instructions
└── README.md
```

### Structure Rationale

| Package | Purpose | Lines (Estimated) | Files |
|---------|---------|------------------|-------|
| `design/` | Goa API design DSL | ~80 | 4 |
| `config/` | Load and validate configuration | ~200 | 3 |
| `server/` | Goa server setup, service implementations | ~300 | 6 |
| `auth/` | JWT token validation | ~150 | 4 |
| `pipeline/` | Orchestrator + job struct | ~200 | 4 |
| `pipeline/stages/` | 5 pipeline stages | ~500 (100/stage) | 11 |
| `messaging/` | Redis consumer, producer, progress | ~400 | 7 |
| `storage/` | MinIO upload with retry | ~100 | 3 |
| `health/` | Health checks | ~100 | 3 |
| `testutil/` | Test helpers | ~200 | 4 |
| **TOTAL** | **Full implementation** | **~2,150 lines** | **45 files** |

**Key observation**: The entire gateway is **~2,000 lines of Go**. Applying Clean Architecture's 4-layer structure would add significant boilerplate with no benefit.

---

## 6. Key Interfaces (Building Blocks)

### 6.1 Core Interfaces

```go
// auth/validator.go
// TokenValidator validates Ed25519-signed JWT upload tokens.
type TokenValidator interface {
    // Validate parses and validates the JWT token.
    // Returns parsed claims on success, error on invalid signature or expired token.
    Validate(tokenString string) (*UploadClaims, error)
}

// UploadClaims represents the JWT claims for an upload token.
type UploadClaims struct {
    Subject       string    `json:"sub"`         // "image-upload"
    Issuer        string    `json:"iss"`         // "follow-api"
    ImageID       string    `json:"image_id"`    // UUID string
    StorageKey    string    `json:"storage_key"` // "images/{id}/photo.webp"
    ContentType   string    `json:"content_type"`// "image/jpeg"
    MaxFileSize   int64     `json:"max_file_size"`
    IssuedAt      int64     `json:"iat"`
    ExpiresAt     int64     `json:"exp"`
}
```

```go
// pipeline/stages/stage.go
// Stage represents a single processing stage in the pipeline.
// Each stage reads from an input channel, processes jobs, and writes to an output channel.
type Stage interface {
    // Name returns the human-readable stage name (for logging and progress).
    Name() string

    // Process performs the stage's work on a single job.
    // If error occurs, it sets job.Error and the job skips remaining stages.
    Process(ctx context.Context, job *Job) error
}
```

```go
// pipeline/job.go
// ImageJob represents a single image flowing through the pipeline.
type ImageJob struct {
    // Identity
    ID         string
    Token      *auth.UploadClaims // Parsed JWT claims

    // Pipeline data (populated by stages)
    RawBytes        []byte      // Stages 1-2: raw upload bytes
    DecodedImg      image.Image // Stages 2-4: decoded pixel data
    OriginalWidth   int         // Stage 2 records
    OriginalHeight  int         // Stage 2 records
    ProcessedWidth  int         // Stage 4 records
    ProcessedHeight int         // Stage 4 records
    EncodedBytes    []byte      // Stages 4-5: final encoded output
    SHA256          string      // Stage 4 computes
    ContentType     string      // Stage 4 sets (may differ from input)
    ETag            string      // Stage 5 receives from MinIO
    StorageKey      string      // From JWT token

    // Error handling
    Error           error       // Any stage can set; halts pipeline
    ErrorCode       string      // Error code for client (e.g., "INVALID_MAGIC_BYTES")
}
```

```go
// storage/uploader.go
// ObjectUploader uploads processed images to object storage (MinIO/S3).
type ObjectUploader interface {
    // Upload puts an object in storage and returns the ETag.
    Upload(ctx context.Context, key string, data []byte, contentType string) (etag string, err error)

    // Stat retrieves object metadata (for paranoid verification after upload).
    Stat(ctx context.Context, key string) (*ObjectInfo, error)
}

// ObjectInfo represents metadata about an object in storage.
type ObjectInfo struct {
    Key         string
    Size        int64
    ETag        string
    ContentType string
}
```

```go
// messaging/consumer.go
// JobConsumer consumes image processing jobs from Redis Streams.
type JobConsumer interface {
    // Consume returns a channel that receives jobs from the Redis Stream.
    // Blocks until a job is available or context is cancelled.
    Consume(ctx context.Context) (<-chan *ProcessJob, error)

    // Ack acknowledges a message after successful processing.
    Ack(ctx context.Context, messageID string) error
}

// ProcessJob represents a job from the image:process stream.
type ProcessJob struct {
    MessageID           string    // Redis Stream message ID
    ImageID             string
    StorageKey          string
    ExpectedContentType string
    MaxFileSize         int64
    UploadToken         string    // JWT token (relayed from follow-api)
    RequestedAt         time.Time
}
```

```go
// messaging/producer.go
// ResultProducer publishes processing results to Redis Streams.
type ResultProducer interface {
    // PublishSuccess publishes a successful processing result.
    PublishSuccess(ctx context.Context, result *SuccessResult) error

    // PublishFailure publishes a failed processing result.
    PublishFailure(ctx context.Context, result *FailureResult) error
}

// SuccessResult represents a successful image processing result.
type SuccessResult struct {
    ImageID         string
    SHA256          string
    ETag            string
    FileSize        int64
    ContentType     string
    OriginalWidth   int
    OriginalHeight  int
    ProcessedWidth  int
    ProcessedHeight int
    ProcessedAt     time.Time
}

// FailureResult represents a failed image processing result.
type FailureResult struct {
    ImageID      string
    ErrorCode    string    // "INVALID_MAGIC_BYTES", "DECODE_FAILED", etc.
    ErrorMessage string
    FailedAt     time.Time
}
```

```go
// messaging/progress.go
// ProgressReporter updates processing progress in Redis for SSE streaming.
type ProgressReporter interface {
    // Report updates the progress hash for an image.
    Report(ctx context.Context, imageID string, stage string, progress int) error
}
```

```go
// health/checker.go
// HealthChecker checks the health of external dependencies.
type HealthChecker interface {
    // Check returns the current health status of all dependencies.
    Check(ctx context.Context) *HealthStatus
}

// HealthStatus represents the health of the gateway and its dependencies.
type HealthStatus struct {
    Status      string              // "healthy", "degraded", "unhealthy"
    Checks      map[string]CheckResult
    Timestamp   time.Time
}

// CheckResult represents the result of a single health check.
type CheckResult struct {
    Status  string // "up", "down"
    Message string
    Latency time.Duration
}
```

### 6.2 Interface Benefits

| Interface | Enables |
|-----------|---------|
| `TokenValidator` | Test with fake validator (no JWT dependency), swap Ed25519 for RSA if needed |
| `Stage` | Add new stages without modifying pipeline orchestrator |
| `ObjectUploader` | Test without MinIO (in-memory fake), swap MinIO for S3/R2 |
| `JobConsumer` | Test without Redis (in-memory queue) |
| `ResultProducer` | Test without Redis (collect results in memory) |
| `ProgressReporter` | Test without Redis (no-op reporter) |
| `HealthChecker` | Test health endpoint without external dependencies |

---

## 7. Pipeline Architecture Detail

### 7.1 Pipeline Orchestrator

```go
// pipeline/pipeline.go
package pipeline

import (
    "context"
    "sync"

    "follow-image-gateway/internal/messaging"
    "follow-image-gateway/internal/pipeline/stages"
)

// Pipeline orchestrates the image processing stages.
type Pipeline struct {
    // Stages (injected dependencies)
    validate stages.Stage
    decode   stages.Stage
    process  stages.Stage
    encode   stages.Stage
    upload   stages.Stage

    // External services (injected dependencies)
    progressReporter messaging.ProgressReporter
    resultProducer   messaging.ResultProducer

    // Configuration
    bufferSize      int
    validateWorkers int
    decodeWorkers   int
    processWorkers  int
    encodeWorkers   int
    uploadWorkers   int
}

// NewPipeline creates a new pipeline with the given stages and configuration.
func NewPipeline(
    validate, decode, process, encode, upload stages.Stage,
    progressReporter messaging.ProgressReporter,
    resultProducer messaging.ResultProducer,
    cfg *Config,
) *Pipeline {
    return &Pipeline{
        validate:         validate,
        decode:           decode,
        process:          process,
        encode:           encode,
        upload:           upload,
        progressReporter: progressReporter,
        resultProducer:   resultProducer,
        bufferSize:       cfg.BufferSize,
        validateWorkers:  cfg.ValidateWorkers,
        decodeWorkers:    cfg.DecodeWorkers,
        processWorkers:   cfg.ProcessWorkers,
        encodeWorkers:    cfg.EncodeWorkers,
        uploadWorkers:    cfg.UploadWorkers,
    }
}

// Run starts the pipeline and blocks until ctx is cancelled.
func (p *Pipeline) Run(ctx context.Context) error {
    // Create channels (pipes between stages)
    validateCh := make(chan *ImageJob, p.bufferSize)
    decodeCh   := make(chan *ImageJob, p.bufferSize)
    processCh  := make(chan *ImageJob, p.bufferSize)
    encodeCh   := make(chan *ImageJob, p.bufferSize)
    uploadCh   := make(chan *ImageJob, p.bufferSize)
    resultCh   := make(chan *ImageJob, p.bufferSize)

    var wg sync.WaitGroup

    // Launch workers for each stage
    p.launchWorkers(ctx, &wg, p.validateWorkers, p.validate, validateCh, decodeCh, resultCh)
    p.launchWorkers(ctx, &wg, p.decodeWorkers, p.decode, decodeCh, processCh, resultCh)
    p.launchWorkers(ctx, &wg, p.processWorkers, p.process, processCh, encodeCh, resultCh)
    p.launchWorkers(ctx, &wg, p.encodeWorkers, p.encode, encodeCh, uploadCh, resultCh)
    p.launchWorkers(ctx, &wg, p.uploadWorkers, p.upload, uploadCh, resultCh, resultCh)

    // Launch result publisher
    wg.Add(1)
    go p.publishResults(ctx, &wg, resultCh)

    // Wait for graceful shutdown
    <-ctx.Done()
    close(validateCh) // Close entry point, cascade through pipeline
    wg.Wait()         // Wait for all workers to finish
    return nil
}

// Submit submits a new job to the pipeline.
func (p *Pipeline) Submit(job *ImageJob) {
    // Implementation: send to validateCh
}

// launchWorkers starts N workers for a stage.
func (p *Pipeline) launchWorkers(
    ctx context.Context,
    wg *sync.WaitGroup,
    count int,
    stage stages.Stage,
    inputCh <-chan *ImageJob,
    outputCh chan<- *ImageJob,
    errorCh chan<- *ImageJob,
) {
    for i := 0; i < count; i++ {
        wg.Add(1)
        go p.worker(ctx, wg, stage, inputCh, outputCh, errorCh)
    }
}

// worker is a generic worker that processes jobs from inputCh.
func (p *Pipeline) worker(
    ctx context.Context,
    wg *sync.WaitGroup,
    stage stages.Stage,
    inputCh <-chan *ImageJob,
    outputCh chan<- *ImageJob,
    errorCh chan<- *ImageJob,
) {
    defer wg.Done()

    for job := range inputCh {
        // Update progress
        _ = p.progressReporter.Report(ctx, job.ID, stage.Name(), progressValue(stage))

        // Process job
        err := stage.Process(ctx, job)
        if err != nil {
            job.Error = err
            job.ErrorCode = errorCodeFor(err)
            errorCh <- job // Send to result publisher
            continue
        }

        // Send to next stage
        outputCh <- job
    }
}

// publishResults consumes from resultCh and publishes to Redis.
func (p *Pipeline) publishResults(ctx context.Context, wg *sync.WaitGroup, resultCh <-chan *ImageJob) {
    defer wg.Done()

    for job := range resultCh {
        if job.Error != nil {
            // Publish failure
            _ = p.resultProducer.PublishFailure(ctx, &messaging.FailureResult{
                ImageID:      job.ID,
                ErrorCode:    job.ErrorCode,
                ErrorMessage: job.Error.Error(),
                FailedAt:     time.Now(),
            })
        } else {
            // Publish success
            _ = p.resultProducer.PublishSuccess(ctx, &messaging.SuccessResult{
                ImageID:         job.ID,
                SHA256:          job.SHA256,
                ETag:            job.ETag,
                FileSize:        int64(len(job.EncodedBytes)),
                ContentType:     job.ContentType,
                OriginalWidth:   job.OriginalWidth,
                OriginalHeight:  job.OriginalHeight,
                ProcessedWidth:  job.ProcessedWidth,
                ProcessedHeight: job.ProcessedHeight,
                ProcessedAt:     time.Now(),
            })
        }
    }
}
```

### 7.2 Example Stage: Validate

```go
// pipeline/stages/validate.go
package stages

import (
    "bytes"
    "context"
    "fmt"
)

var (
    // Magic bytes for supported formats
    jpegMagic = []byte{0xFF, 0xD8, 0xFF}
    pngMagic  = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
    webpMagic = []byte{0x52, 0x49, 0x46, 0x46} // "RIFF"
)

// ValidateStage checks magic bytes and file size.
type ValidateStage struct {
    maxPixelCount int64 // Decompression bomb limit
}

// NewValidateStage creates a new validation stage.
func NewValidateStage(maxPixelCount int64) *ValidateStage {
    return &ValidateStage{maxPixelCount: maxPixelCount}
}

// Name returns the stage name.
func (s *ValidateStage) Name() string {
    return "validating"
}

// Process validates the image file.
func (s *ValidateStage) Process(ctx context.Context, job *ImageJob) error {
    // Check magic bytes
    if !s.matchesMagicBytes(job.RawBytes, job.Token.ContentType) {
        return fmt.Errorf("%w: magic bytes do not match declared content type %s",
            ErrInvalidMagicBytes, job.Token.ContentType)
    }

    // Check file size
    fileSize := int64(len(job.RawBytes))
    if fileSize > job.Token.MaxFileSize {
        return fmt.Errorf("%w: file size %d exceeds max %d",
            ErrFileTooLarge, fileSize, job.Token.MaxFileSize)
    }

    if fileSize == 0 {
        return ErrFileTooSmall
    }

    return nil
}

func (s *ValidateStage) matchesMagicBytes(data []byte, contentType string) bool {
    switch contentType {
    case "image/jpeg":
        return bytes.HasPrefix(data, jpegMagic)
    case "image/png":
        return bytes.HasPrefix(data, pngMagic)
    case "image/webp":
        return bytes.HasPrefix(data, webpMagic)
    default:
        return false
    }
}
```

### 7.3 Data Flow Example

```
1. Client uploads 4032x3024 JPEG (4.2MB) to PUT /upload?token=JWT

2. Upload Handler:
   - Validates JWT token
   - Reads request body into memory
   - Creates ImageJob{ID: "abc123", RawBytes: [...], Token: {...}}
   - Submits to pipeline.Submit(job)
   - Returns 202 Accepted

3. Validate Stage (Worker 1):
   - Reads job from validateCh
   - Checks magic bytes: FF D8 FF ✓
   - Checks file size: 4.2MB < 10MB ✓
   - Updates Redis: HSET image:status:abc123 {stage: "validating", progress: 10}
   - Sends job to decodeCh

4. Decode Stage (Worker 2):
   - Reads job from decodeCh
   - image.DecodeConfig(): width=4032, height=3024 ✓
   - Pixel count check: 4032*3024 = 12MP < 100MP ✓
   - image.Decode(): full decode to pixel buffer ✓
   - Strip EXIF metadata
   - Records job.OriginalWidth = 4032, job.OriginalHeight = 3024
   - Updates Redis: HSET image:status:abc123 {stage: "decoding", progress: 30}
   - Sends job to processCh

5. Process Stage (Worker 1 - single worker for MVP):
   - Reads job from processCh
   - MVP: pass-through (no ML processing yet)
   - Updates Redis: HSET image:status:abc123 {stage: "processing", progress: 55}
   - Sends job to encodeCh

6. Encode Stage (Worker 1):
   - Reads job from encodeCh
   - Resize: 4032 > 1920, scale to 1920x1440
   - Re-encode to WebP (quality=85)
   - Compute SHA256: "a1b2c3d4..."
   - Records job.ProcessedWidth = 1920, job.ProcessedHeight = 1440
   - job.EncodedBytes = [...] (1.8MB WebP)
   - job.SHA256 = "a1b2c3d4..."
   - Updates Redis: HSET image:status:abc123 {stage: "encoding", progress: 75}
   - Sends job to uploadCh

7. Upload Stage (Worker 3):
   - Reads job from uploadCh
   - PutObject to MinIO: images/abc123/photo.webp
   - Receives ETag: "def456"
   - job.ETag = "def456"
   - Updates Redis: HSET image:status:abc123 {stage: "uploading_to_storage", progress: 90}
   - Sends job to resultCh

8. Result Publisher:
   - Reads job from resultCh
   - XADD image:result {image_id: "abc123", sha256: "a1b2c3d4...", ...}
   - Updates Redis: HSET image:status:abc123 {stage: "done", progress: 100}
   - XACK the original job from image:process

9. follow-api receives result via Redis Stream consumer and updates PostgreSQL
```

---

## 8. What to Reuse from follow-api

### 8.1 Direct Reuse (Copy & Adapt)

| Component | Source in follow-api | Adaptation for Gateway |
|-----------|---------------------|----------------------|
| **Goa Framework** | `design/` + `cmd/server/` | Use Goa with `SkipRequestBodyEncodeDecode()` for raw binary upload. Consistent HTTP patterns across follow-api and image-gateway. |
| **Logging** | `internal/shared/logger/` | Copy pattern. Use zerolog. Adapt service name to "image-gateway". Keep `GetLoggerForComponent()` pattern. |
| **Config** | `internal/shared/config/` | Copy pattern. Use Viper. Same precedence (env > file > defaults). Gateway-specific config struct. |
| **MinIO Client** | `internal/infrastructure/storage/storage.go` | Copy MinIO client setup. Simplify to upload-only (no presigned URLs). Remove download logic. |
| **Linter Setup** | `.golangci-custom.yml` + `custom-gcl` | Copy entire linter config. Same quality standards. Same nilaway setup. Add mnd (magic numbers detector) |
| **Testing Patterns** | `testutil/` | Copy test config helpers, assertion patterns. Add image-specific fixtures (valid/invalid images). |
| **Error Handling** | `ai-docs/architecture/error-architecture/` | Reuse `fmt.Errorf("%w: context", ErrorType)` pattern. Simpler hierarchy (no domain/usecase/infrastructure split - just component-level errors). |
| **Dockerfile** | `Dockerfile` | Same multi-stage Alpine pattern. No database dependency. Smaller binary. |
| **Quality Gates** | Quality gate workflow from CLAUDE.md | Same gates: gofumpt, golines, go vet, golangci-lint, go test -race. |

### 8.2 Patterns to Adapt (Not Copy Directly)

| Pattern | How It Applies to Gateway |
|---------|--------------------------|
| **TDD Workflow** | Follow same stub-first RED-GREEN-REFACTOR from `ai-docs/workflows/tdd-red-green-refactor-workflow.md`. But test **stages**, not use cases. |
| **Interface-First Design** | Define all interfaces (`TokenValidator`, `ObjectUploader`, `Stage`) before implementation. |
| **Constructor Validation** | Validate all dependencies in constructors (nil checks, return errors). |
| **Graceful Shutdown** | Same pattern: listen for SIGTERM, close channels, wait for goroutines with timeout. |

### 8.3 What NOT to Reuse

| Component | Why NOT |
|-----------|---------|
| **Module Registry / Command-Query Pattern** | No modules in gateway. No CQRS. |
| **Event Bus (Watermill)** | Use Redis Streams directly (simpler, no abstraction needed). |
| **Database Layer** | No database in gateway. |
| **Domain/Entity/Value Object Patterns** | No domain model. Just byte processing. |
| **Repository Pattern** | No persistence, no need for repositories. |

### 8.4 Shared Dependencies (Same Libraries)

```go
// go.mod dependencies to reuse from follow-api
require (
    github.com/go-redis/redis/v8     // Redis client
    github.com/minio/minio-go/v7     // MinIO client
    github.com/spf13/viper           // Configuration
    github.com/rs/zerolog            // Logging
    github.com/golang-jwt/jwt/v5     // JWT validation
    github.com/stretchr/testify      // Testing (assert, require, mock)
    github.com/google/uuid           // UUID handling
)

// New dependencies for image processing
require (
    golang.org/x/image/webp          // WebP encode/decode
    github.com/disintegration/imaging // Image resize
    github.com/rwcarlsen/goexif      // EXIF stripping
)
```

---

## 9. Architecture Comparison

### 9.1 Side-by-Side Comparison

| Aspect | Clean Architecture (follow-api) | Pipes & Filters (image-gateway) |
|--------|-------------------------------|-------------------------------|
| **Layers** | 4 (entity → usecase → adapter → framework) | 2 (interface → implementation) |
| **Domain Model** | Rich entities, value objects, aggregates | None - just byte processing |
| **Use Cases** | Complex orchestration of business rules | None - pipeline stages do one thing each |
| **Repository Pattern** | Yes (abstract DB access) | No (direct MinIO upload via interface) |
| **Event Pattern** | Domain events via event bus | Redis Streams (infrastructure, not domain) |
| **Files per Feature** | ~8-12 files across 4 layers | ~2-3 files (interface + impl + test) |
| **Context Needed per Task** | Medium-high (understand layers) | Low (understand one stage) |
| **TDD Approach** | Stub-first with ErrNotImplemented | Same TDD, but per-stage instead of per-usecase |
| **Suitable For** | Complex business domains with entities | Stateless processing pipelines |
| **Total Lines of Code** | ~15,000+ (follow-api current state) | ~2,000 (gateway estimate) |

### 9.2 Code Organization Comparison

**follow-api (Route Domain, Clean Architecture):**

```
internal/domains/route/
├── domain/
│   ├── entities/
│   │   ├── route.go              # Rich entity with business rules
│   │   ├── waypoint.go           # Aggregate member
│   │   └── route_test.go
│   ├── valueobjects/
│   │   ├── route_status.go       # Value object with transitions
│   │   └── waypoint_status.go
│   ├── events/
│   │   ├── route_created.go      # Domain event
│   │   └── waypoint_confirmed.go
│   └── errors.go                 # Domain-level errors
├── usecases/
│   ├── create_route.go           # Orchestrates domain entities
│   ├── create_route_test.go
│   └── errors.go                 # Use case errors
├── interfaces/
│   ├── repository.go             # Port for persistence
│   ├── event_publisher.go        # Port for events
│   └── types.go                  # DTOs
└── repository/
    └── postgres/
        ├── route_repository.go   # Adapter for DB
        └── errors.go             # Infrastructure errors
```

**image-gateway (Pipes and Filters):**

```
internal/
├── pipeline/
│   ├── pipeline.go               # Orchestrator (wires stages)
│   ├── job.go                    # Data structure (flows through)
│   └── stages/
│       ├── stage.go              # Interface
│       ├── validate.go           # Filter (one responsibility)
│       ├── decode.go             # Filter
│       ├── encode.go             # Filter
│       └── upload.go             # Filter
├── auth/
│   ├── validator.go              # Interface + impl (single file)
│   └── claims.go                 # Data structure
├── messaging/
│   ├── consumer.go               # Interface + impl
│   └── producer.go               # Interface + impl
└── storage/
    └── uploader.go               # Interface + impl
```

**Difference**: Clean Architecture has **3-4 files per concept** (interface + entity + usecase + adapter). Pipes and Filters has **1-2 files per component** (interface + impl, often in same file).

### 9.3 When to Use Each

| Use Clean Architecture When | Use Pipes and Filters When |
|----------------------------|---------------------------|
| Rich business domain with complex rules | Stateless data transformation |
| Multiple entities with relationships | No persistence, no entities |
| Complex workflows spanning domains | Linear or branching processing flow |
| Business logic needs protection from infrastructure | Infrastructure concerns dominate |
| Long-term domain evolution expected | Technical processing with clear stages |
| **Example: follow-api** | **Example: image-gateway** |

---

## 10. TDD Workflow Adaptation

### 10.1 TDD for Pipeline Stages

The TDD workflow from `ai-docs/workflows/tdd-red-green-refactor-workflow.md` adapts naturally to pipeline stages:

**Instead of Use Cases, test Stages:**

```go
// follow-api: TDD for Use Case
1. Define interface: CreateRouteUseCaseInterface
2. Define Input/Output types
3. Create stub: returns ErrNotImplemented
4. Write tests (constructor, validation, success, errors)
5. Implement use case logic
6. Refactor

// image-gateway: TDD for Stage
1. Define interface: Stage (already exists)
2. Define what the stage does: "Validate magic bytes"
3. Create stub: returns ErrNotImplemented
4. Write tests (magic bytes match, size checks, errors)
5. Implement stage logic
6. Refactor
```

### 10.2 Example: TDD for Validate Stage

**Step 1: Define Interface (Already Done)**

```go
// pipeline/stages/stage.go
type Stage interface {
    Name() string
    Process(ctx context.Context, job *ImageJob) error
}
```

**Step 2: Create Stub**

```go
// pipeline/stages/validate.go
package stages

import (
    "context"
    "errors"
)

var ErrNotImplemented = errors.New("stage not implemented")

type ValidateStage struct {
    maxPixelCount int64
}

func NewValidateStage(maxPixelCount int64) *ValidateStage {
    return &ValidateStage{maxPixelCount: maxPixelCount}
}

func (s *ValidateStage) Name() string {
    return "validating"
}

func (s *ValidateStage) Process(ctx context.Context, job *ImageJob) error {
    if ctx == nil {
        return ErrContextNil
    }
    if job == nil {
        return ErrJobNil
    }

    // TDD STUB: Returns ErrNotImplemented until real implementation
    return ErrNotImplemented
}
```

**Step 3: Write Tests (RED State)**

```go
// pipeline/stages/validate_test.go
package stages_test

import (
    "context"
    "testing"

    "follow-image-gateway/internal/pipeline"
    "follow-image-gateway/internal/pipeline/stages"
    "follow-image-gateway/testutil"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestValidateStage_Name(t *testing.T) {
    stage := stages.NewValidateStage(100000000)
    assert.Equal(t, "validating", stage.Name())
}

func TestValidateStage_Process_NilContext(t *testing.T) {
    stage := stages.NewValidateStage(100000000)
    job := &pipeline.ImageJob{}

    err := stage.Process(nil, job)

    require.Error(t, err)
    assert.ErrorIs(t, err, stages.ErrContextNil)
}

func TestValidateStage_Process_NilJob(t *testing.T) {
    stage := stages.NewValidateStage(100000000)
    ctx := context.Background()

    err := stage.Process(ctx, nil)

    require.Error(t, err)
    assert.ErrorIs(t, err, stages.ErrJobNil)
}

// BEHAVIOR TESTS (FAIL with stub - RED state)

func TestValidateStage_Process_ValidJPEG(t *testing.T) {
    stage := stages.NewValidateStage(100000000)
    ctx := context.Background()

    job := &pipeline.ImageJob{
        RawBytes: testutil.ValidJPEGBytes(), // FF D8 FF ...
        Token: &auth.UploadClaims{
            ContentType: "image/jpeg",
            MaxFileSize: 10485760,
        },
    }

    err := stage.Process(ctx, job)

    require.NoError(t, err) // FAILS: got ErrNotImplemented
}

func TestValidateStage_Process_InvalidMagicBytes(t *testing.T) {
    stage := stages.NewValidateStage(100000000)
    ctx := context.Background()

    job := &pipeline.ImageJob{
        RawBytes: []byte{0x4D, 0x5A}, // MZ (EXE header)
        Token: &auth.UploadClaims{
            ContentType: "image/jpeg",
            MaxFileSize: 10485760,
        },
    }

    err := stage.Process(ctx, job)

    require.Error(t, err)
    assert.ErrorIs(t, err, stages.ErrInvalidMagicBytes) // FAILS: got ErrNotImplemented
}

func TestValidateStage_Process_FileTooLarge(t *testing.T) {
    stage := stages.NewValidateStage(100000000)
    ctx := context.Background()

    job := &pipeline.ImageJob{
        RawBytes: testutil.ValidJPEGBytes(),
        Token: &auth.UploadClaims{
            ContentType: "image/jpeg",
            MaxFileSize: 100, // Very small limit
        },
    }

    err := stage.Process(ctx, job)

    require.Error(t, err)
    assert.ErrorIs(t, err, stages.ErrFileTooLarge) // FAILS: got ErrNotImplemented
}
```

**Step 4: Human Approval**

Review tests:
- [ ] Constructor tests? N/A (simple struct)
- [ ] Input validation tests? ✓ (nil context, nil job)
- [ ] Success scenarios? ✓ (valid JPEG, PNG, WebP)
- [ ] Error scenarios? ✓ (invalid magic bytes, file too large, file too small)
- [ ] Edge cases? ✓ (empty file, max size boundary)

**Step 5: Implement (GREEN State)**

Replace `return ErrNotImplemented` with real validation logic (see Section 7.2 for implementation).

**Step 6: Refactor**

Extract `matchesMagicBytes()` helper, improve error messages, etc.

### 10.3 TDD for Infrastructure Components

For Redis, MinIO, Auth components:

```go
// 1. Define interface
type TokenValidator interface {
    Validate(tokenString string) (*UploadClaims, error)
}

// 2. Create fake implementation for testing
type FakeTokenValidator struct {
    ValidateFunc func(string) (*UploadClaims, error)
}

func (f *FakeTokenValidator) Validate(token string) (*UploadClaims, error) {
    if f.ValidateFunc != nil {
        return f.ValidateFunc(token)
    }
    return nil, errors.New("not configured")
}

// 3. Write tests against the interface using the fake
func TestUploadHandler_ValidToken(t *testing.T) {
    fakeValidator := &FakeTokenValidator{
        ValidateFunc: func(token string) (*UploadClaims, error) {
            return &UploadClaims{ImageID: "abc123"}, nil
        },
    }

    handler := NewUploadHandler(fakeValidator, ...)
    // Test handler logic
}

// 4. Implement real version (Ed25519JWTValidator)
type Ed25519JWTValidator struct {
    publicKey ed25519.PublicKey
}

func (v *Ed25519JWTValidator) Validate(tokenString string) (*UploadClaims, error) {
    // Parse JWT, verify signature, extract claims
}

// 5. Integration test with real JWT tokens
func TestEd25519JWTValidator_Integration(t *testing.T) {
    // Generate real Ed25519 keypair
    // Sign a token
    // Validate it
}
```

---

## 11. Implementation Phases

### Phase 1: Foundation (Infrastructure + Goa Setup)

**Goal**: Project skeleton with Goa API design and health checks

**Tasks**:
- [ ] Initialize Go module: `go mod init follow-image-gateway`
- [ ] Set up Goa v3 dependency and code generation (`goa gen`)
- [ ] Copy `.golangci-custom.yml` from follow-api
- [ ] Copy `custom-gcl` binary and setup from follow-api
- [ ] Create `internal/config/` package (reuse follow-api patterns)
- [ ] Create `internal/health/` package with Redis + MinIO checks
- [ ] Create Goa API design:
  - [ ] `design/design.go` — API definition, security scheme
  - [ ] `design/health_service.go` — `GET /health` endpoint DSL
  - [ ] `design/types.go` — `HealthResult` type
- [ ] Run `goa gen` to generate server code
- [ ] Implement health Goa service (`internal/server/health_service.go`)
- [ ] Wire Goa server in `cmd/server/main.go` with graceful shutdown
- [ ] Dockerfile (multi-stage Alpine, includes `goa gen` in build)
- [ ] Docker Compose (standalone: gateway + Redis + MinIO)

**Definition of Done**:
- [ ] `goa gen follow-image-gateway/design` succeeds
- [ ] `go build ./...` succeeds
- [ ] `go test ./...` succeeds (minimal tests)
- [ ] `./custom-gcl run -c .golangci-custom.yml ./...` passes
- [ ] `docker-compose up` starts all services
- [ ] `curl http://localhost:8090/health` returns 200 OK

**Estimated Effort**: 2-3 days

---

### Phase 2: Auth & HTTP (Goa Upload Service)

**Goal**: Accept uploads with JWT token validation via Goa

**Tasks**:
- [ ] Define `auth.TokenValidator` interface
- [ ] Implement `auth.Ed25519JWTValidator` (TDD)
- [ ] Define `auth.UploadClaims` struct
- [ ] Create test Ed25519 keypair for development
- [ ] Create Goa upload design:
  - [ ] `design/security.go` — Ed25519 JWT security scheme
  - [ ] `design/upload_service.go` — `PUT /upload` with `SkipRequestBodyEncodeDecode()`
  - [ ] `design/types.go` — add `UploadResult`, error types
- [ ] Run `goa gen` to regenerate server code
- [ ] Implement upload Goa service (`internal/server/upload_service.go`) (TDD)
  - [ ] Receive `io.ReadCloser` body from Goa generated interface
  - [ ] Validate JWT via `auth.TokenValidator`
  - [ ] Enforce size limit via `io.LimitReader` from token claims
  - [ ] Read request body into memory buffer
  - [ ] Return 202 Accepted with `UploadResult`
- [ ] Map errors to Goa error responses (401, 413, 400, 503)
- [ ] Add Goa middleware: request logging, panic recovery

**Definition of Done**:
- [ ] `goa gen` succeeds with upload + health services
- [ ] Unit tests for `Ed25519JWTValidator` pass
- [ ] Unit tests for upload Goa service pass
- [ ] Integration test: upload with valid token returns 202
- [ ] Integration test: upload with invalid token returns 401
- [ ] Integration test: upload exceeding size limit returns 413
- [ ] All quality gates pass

**Estimated Effort**: 2-3 days

---

### Phase 3: Pipeline Core

**Goal**: Pipeline orchestrator with channel wiring

**Tasks**:
- [ ] Define `pipeline.ImageJob` struct
- [ ] Define `pipeline/stages.Stage` interface
- [ ] Implement `pipeline.Pipeline` orchestrator (TDD)
  - [ ] Create channels (validateCh, decodeCh, processCh, encodeCh, uploadCh, resultCh)
  - [ ] Launch workers for each stage
  - [ ] Implement generic `worker()` function
  - [ ] Graceful shutdown (close channels, wait for WaitGroup)
- [ ] Create stub stages (all return `ErrNotImplemented`)
- [ ] Integration test: Submit job → receives it in validateCh

**Definition of Done**:
- [ ] Unit tests for `Pipeline` orchestrator pass
- [ ] Integration test: pipeline starts and shuts down cleanly
- [ ] Channels wire correctly (job flows from validateCh to resultCh)
- [ ] All quality gates pass

**Estimated Effort**: 2-3 days

---

### Phase 4: Pipeline Stages (One at a Time, TDD)

**Goal**: Implement each stage with comprehensive tests

#### Phase 4a: Stage 1 - Validate

**Tasks**:
- [ ] Define `stages.ValidateStage` (TDD)
  - [ ] Write tests for magic bytes (JPEG, PNG, WebP)
  - [ ] Write tests for file size limits
  - [ ] Implement validation logic
  - [ ] Extract `matchesMagicBytes()` helper

**Definition of Done**:
- [ ] All validation tests pass (magic bytes, size limits)
- [ ] Edge cases covered (empty file, wrong format)
- [ ] Quality gates pass

**Estimated Effort**: 1 day

#### Phase 4b: Stage 2 - Decode

**Tasks**:
- [ ] Define `stages.DecodeStage` (TDD)
  - [ ] Write tests for `image.DecodeConfig()` (header parsing)
  - [ ] Write tests for decompression bomb detection
  - [ ] Write tests for full `image.Decode()` (pixel buffer)
  - [ ] Write tests for EXIF stripping
  - [ ] Implement decode logic

**Definition of Done**:
- [ ] All decode tests pass (header, full decode, EXIF strip)
- [ ] Decompression bomb protection works (reject 50000x50000 image)
- [ ] EXIF/GPS metadata removed from decoded image
- [ ] Quality gates pass

**Estimated Effort**: 2 days

#### Phase 4c: Stage 3 - Process

**Tasks**:
- [ ] Define `stages.ProcessStage` (TDD)
  - [ ] Write tests for pass-through behavior (MVP)
  - [ ] Implement pass-through (no-op for now)
  - [ ] Document future ML processing hooks

**Definition of Done**:
- [ ] Pass-through tests pass
- [ ] Stage is ready for future ML integration
- [ ] Quality gates pass

**Estimated Effort**: 0.5 day

#### Phase 4d: Stage 4 - Encode

**Tasks**:
- [ ] Define `stages.EncodeStage` (TDD)
  - [ ] Write tests for resize logic (maintain aspect ratio)
  - [ ] Write tests for no-upscale rule
  - [ ] Write tests for WebP encoding
  - [ ] Write tests for SHA256 computation
  - [ ] Implement encode logic

**Definition of Done**:
- [ ] All encode tests pass (resize, WebP, SHA256)
- [ ] Aspect ratio maintained (scale_x == scale_y)
- [ ] Images not upscaled if smaller than max width
- [ ] Quality gates pass

**Estimated Effort**: 2 days

#### Phase 4e: Stage 5 - Upload

**Tasks**:
- [ ] Define `storage.ObjectUploader` interface
- [ ] Implement `storage.MinIOUploader` (TDD)
  - [ ] Write tests with fake MinIO client
  - [ ] Implement `PutObject` with retry logic (exponential backoff)
  - [ ] Implement `Stat` for paranoid verification
- [ ] Define `stages.UploadStage` (TDD)
  - [ ] Write tests for successful upload
  - [ ] Write tests for retry logic
  - [ ] Write tests for final failure after retries
  - [ ] Implement upload logic

**Definition of Done**:
- [ ] All upload tests pass (success, retry, failure)
- [ ] Retry logic uses exponential backoff (1s, 2s, 4s)
- [ ] ETag captured from MinIO response
- [ ] Quality gates pass

**Estimated Effort**: 2 days

**Total Phase 4 Effort**: 7.5 days

---

### Phase 5: Messaging (Redis Streams)

**Goal**: Connect pipeline to Redis for job consumption and result publishing

**Tasks**:
- [ ] Define `messaging.JobConsumer` interface
- [ ] Implement `messaging.RedisJobConsumer` (TDD)
  - [ ] Write tests with fake Redis client
  - [ ] Implement `XREADGROUP` consumption
  - [ ] Implement `XACK` acknowledgment
  - [ ] Implement orphan job reclaim via `XCLAIM`
- [ ] Define `messaging.ResultProducer` interface
- [ ] Implement `messaging.RedisResultProducer` (TDD)
  - [ ] Write tests for success result publishing
  - [ ] Write tests for failure result publishing
  - [ ] Implement `XADD` to image:result stream
- [ ] Define `messaging.ProgressReporter` interface
- [ ] Implement `messaging.RedisProgressReporter` (TDD)
  - [ ] Write tests for progress updates
  - [ ] Implement `HSET` to image:status:{id} with TTL

**Definition of Done**:
- [ ] All messaging tests pass (consumer, producer, progress)
- [ ] Integration test: consume job from Redis, process, publish result
- [ ] Orphan job reclaim works (XCLAIM after timeout)
- [ ] Quality gates pass

**Estimated Effort**: 3 days

---

### Phase 6: Integration & E2E Testing

**Goal**: Full end-to-end workflow with real Redis + MinIO

**Tasks**:
- [ ] Wire Goa server with all service implementations and middleware in `cmd/server/main.go`
- [ ] E2E test: full pipeline with real services
  - [ ] Start Redis + MinIO via Docker Compose
  - [ ] Start gateway
  - [ ] Generate Ed25519 keypair
  - [ ] Sign upload JWT token
  - [ ] Upload test image via HTTP PUT
  - [ ] Verify processing via Redis progress hash
  - [ ] Verify result in Redis Stream
  - [ ] Verify processed image in MinIO
  - [ ] Compare SHA256
- [ ] Performance test: concurrent uploads (10 workers, 100 images)
- [ ] Failure scenarios:
  - [ ] Redis unavailable (circuit breaker)
  - [ ] MinIO unavailable (retry exhaustion)
  - [ ] Invalid image (magic bytes mismatch)
  - [ ] Decompression bomb (reject)

**Definition of Done**:
- [ ] E2E test passes with real dependencies
- [ ] Performance test processes 100 images in sub-60s
- [ ] All failure scenarios handled gracefully
- [ ] Quality gates pass

**Estimated Effort**: 3 days

---

### Phase 7: follow-api Integration

**Goal**: Integrate gateway with follow-api for full workflow

**Tasks**:
- [ ] Add Redis client to follow-api
- [ ] Implement Redis Streams producer in follow-api (publish jobs to image:process)
- [ ] Implement Redis Streams consumer in follow-api (consume results from image:result)
- [ ] Update `CreateRouteWithWaypointsUseCase`:
  - [ ] Sign Ed25519 JWT tokens (replace presigned MinIO URLs)
  - [ ] Publish jobs to Redis Stream
  - [ ] Return gateway upload URLs
- [ ] Implement marker auto-scaling in result consumer
- [ ] Add SSE endpoint: `GET /images/status/stream`
- [ ] Integration test: follow-api + gateway + Redis + MinIO
  - [ ] Create route with waypoints
  - [ ] Upload images to gateway
  - [ ] Verify results consumed by follow-api
  - [ ] Verify markers auto-scaled
  - [ ] Verify route transitions to READY

**Definition of Done**:
- [ ] Integration test passes (full workflow)
- [ ] SSE streams progress to client
- [ ] Markers auto-scaled correctly
- [ ] Route auto-transitions to READY
- [ ] Quality gates pass

**Estimated Effort**: 4-5 days

---

### Total Implementation Effort

| Phase | Days |
|-------|------|
| Phase 1: Foundation + Goa Setup | 2-3 |
| Phase 2: Auth & HTTP (Goa Upload Service) | 2-3 |
| Phase 3: Pipeline Core | 2-3 |
| Phase 4: Pipeline Stages | 7.5 |
| Phase 5: Messaging | 3 |
| Phase 6: Integration & E2E | 3 |
| Phase 7: follow-api Integration | 4-5 |
| **TOTAL** | **24-28 days** (~1 month) |

---

## 12. Definition of Done

### Per-Component Definition of Done

Each component (stage, interface, package) is "done" when:

- [ ] **Interface defined** - Clear contract with documentation
- [ ] **Tests written** - All passing with good coverage
  - [ ] Constructor tests (if applicable)
  - [ ] Input validation tests (nil checks)
  - [ ] Success scenarios
  - [ ] Error scenarios
  - [ ] Edge cases
- [ ] **Implementation complete** - All tests GREEN
- [ ] **Quality gates pass**:
  - [ ] `gofumpt -w .` (formatted)
  - [ ] `golines -w --max-len=80 .` (line length)
  - [ ] `go vet ./...` (no vet warnings)
  - [ ] `./custom-gcl run -c .golangci-custom.yml ./...` (no linter issues, includes nilaway)
  - [ ] `go test -race -cover ./...` (all tests pass, no races)
  - [ ] `go mod tidy` (clean dependencies)
- [ ] **No TODOs/FIXMEs** without linked issue
- [ ] **Errors use predefined types** (not inline strings)
- [ ] **Documentation complete** (godoc comments, README if needed)

### Per-Phase Definition of Done

Each implementation phase is "done" when:

- [ ] All phase tasks completed
- [ ] All components in phase meet per-component DoD
- [ ] Integration tests for the phase pass
- [ ] Server starts without errors: `go run ./cmd/server -runtime-timeout 10s`
- [ ] Health check passes: `curl -f http://localhost:8090/health`
- [ ] Docker Compose starts all services
- [ ] No regressions in previous phases

### Project-Level Definition of Done

The entire follow-image-gateway project is "done" when:

- [ ] All 7 implementation phases completed
- [ ] Full E2E test passes (follow-api + gateway + Redis + MinIO)
- [ ] Performance test passes (100 images in sub-60s)
- [ ] All failure scenarios handled gracefully
- [ ] Documentation complete:
  - [ ] README.md (architecture overview, quick start)
  - [ ] CLAUDE.md (project-specific instructions for Claude Code)
  - [ ] Configuration docs (environment variables)
  - [ ] Deployment guide (Docker Compose, production)
- [ ] follow-api integration complete:
  - [ ] Presigned URL flow removed
  - [ ] Gateway upload URLs working
  - [ ] SSE streaming working
  - [ ] Marker auto-scaling working
  - [ ] Route auto-transition to READY working
- [ ] Zero tolerance quality:
  - [ ] All quality gates pass
  - [ ] No known bugs
  - [ ] No security vulnerabilities

---

## 13. Main Thread Report Template

Since the main thread doesn't have visibility into the planning work, use this template when reporting back:

```markdown
## Feature Planning Complete

### Planning Document
- **Created**: `ai-docs/planning/backlog/image-gateway-architecture-plan.md`
- **Total Pages**: ~50 pages (comprehensive architecture guide)
- **Architecture Decision**: Pipes and Filters (NOT DDD/Clean Architecture)

### Critical Question Answered

**Question**: Is DDD/Clean Architecture right for follow-image-gateway?

**Answer**: **NO**

**Reason**: The gateway is a stateless byte processor with no business domain, no rich entities, no bounded contexts, and no CRUD operations. Applying Clean Architecture's 4-layer structure would add overhead without benefit.

**Recommended Architecture**: Pipes and Filters with interface-based design.

### Project Scope Summary

**What It Is**:
- Stateless Go microservice for secure image upload, validation, and processing
- Replaces vulnerable presigned URL flow
- 5-stage pipeline: Validate → Decode → Process → Encode → Upload
- Communicates with follow-api via Redis Streams

**What It Is NOT**:
- NOT a domain-driven system (no business rules, no entities)
- NOT a CRUD application (no database, no persistence)
- NOT using Clean Architecture's 4-layer structure

### Architecture Proposed

**Pattern**: Pipes and Filters + Interface-Based Design

**Key Components**:
1. **Pipeline Stages** (5 filters):
   - Validate: Magic bytes + size checks
   - Decode: Image decode + EXIF strip
   - Process: ML processing (MVP: pass-through)
   - Encode: Resize + re-encode + SHA256
   - Upload: MinIO PutObject with retry

2. **External Dependencies** (behind interfaces):
   - `TokenValidator`: Ed25519 JWT validation
   - `ObjectUploader`: MinIO/S3 upload
   - `JobConsumer`: Redis Streams consumer
   - `ResultProducer`: Redis Streams producer
   - `ProgressReporter`: Redis Hash updates

3. **Data Flow**:
   - Go channels as "pipes" between stages
   - `ImageJob` struct flows through pipeline
   - Configurable workers per stage (parallelism)

### Implementation Plan

**Total Effort**: 23-27 days (~1 month)

**7 Implementation Phases**:
1. **Foundation** (1-2 days): Project setup, config, health checks
2. **Auth & HTTP** (2-3 days): JWT validation, upload handler
3. **Pipeline Core** (2-3 days): Orchestrator with channel wiring
4. **Pipeline Stages** (7.5 days): Implement 5 stages with TDD
5. **Messaging** (3 days): Redis Streams consumer/producer
6. **Integration & E2E** (3 days): Full pipeline testing
7. **follow-api Integration** (4-5 days): Connect to existing system

**Task Breakdown**: ~45 files, ~2,150 lines of Go

### What to Reuse from follow-api

**Direct Reuse** (copy & adapt):
- Goa framework (with `SkipRequestBodyEncodeDecode()` for raw binary upload)
- Logging pattern (`internal/shared/logger/`)
- Config pattern (`internal/shared/config/`)
- MinIO client setup (simplified to upload-only)
- Linter setup (`.golangci-custom.yml`, `custom-gcl`)
- Testing patterns (`testutil/`)
- Error handling pattern (Rich Domain Errors, simplified)
- Dockerfile (multi-stage Alpine)
- Quality gates workflow

**What NOT to Reuse**:
- ❌ Module registry / Command-Query pattern
- ❌ Event bus (Watermill) - use Redis Streams directly
- ❌ Database layer - no database
- ❌ Domain/entity/value object patterns

### Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| **Pipes and Filters** | Natural fit for linear processing pipeline |
| **Go channels** | Native concurrency, built-in backpressure |
| **Interface-based** | TDD-friendly, testable, swappable dependencies |
| **No Clean Architecture layers** | No domain model to protect, no use case orchestration |
| **Reuse infrastructure patterns** | Proven logging, config, MinIO, testing patterns from follow-api |
| **Small, decoupled stages** | Each stage ~100-200 lines, single responsibility |

### Architecture Comparison

| Aspect | Clean Architecture | Pipes & Filters |
|--------|-------------------|----------------|
| **Layers** | 4 (entity → usecase → adapter → framework) | 2 (interface → implementation) |
| **Files per feature** | ~8-12 files | ~2-3 files |
| **Context per task** | Medium-high | Low |
| **Suitable for** | Complex business domains | Stateless pipelines |
| **LOC estimate** | ~15,000+ (follow-api) | ~2,000 (gateway) |

### Documentation Delivered

The planning document includes:

1. **Executive Summary** - The answer: NO, not DDD/Clean Architecture
2. **Detailed Analysis** - Why NOT Clean Architecture (3 key reasons)
3. **Proposed Architecture** - Pipes and Filters with diagrams
4. **Project Structure** - Complete file layout with rationale
5. **Key Interfaces** - All interface definitions with examples
6. **Pipeline Architecture** - Detailed stage flow with code examples
7. **Reuse Strategy** - What to copy/adapt from follow-api
8. **Architecture Comparison** - Side-by-side with Clean Architecture
9. **TDD Workflow** - Adapted for pipeline stages
10. **Implementation Phases** - 7 phases with task breakdowns and DoD
11. **Definition of Done** - Per-component, per-phase, project-level

### Next Steps

**For User**:
1. Review the planning document: `ai-docs/planning/backlog/image-gateway-architecture-plan.md`
2. Approve the Pipes and Filters architecture decision
3. Approve the implementation phases
4. Decide: Start with Phase 1 (Foundation) or request changes?

**For Implementation**:
- Start with Phase 1: Foundation (project setup, config, health checks)
- Follow TDD workflow: stub → tests → implement → refactor
- Apply same quality gates as follow-api
- Target: ~1 month for full implementation
```

---

## ADR-023: Use Goa Framework for HTTP Layer

**Date:** 2026-01-30
**Status:** Accepted

### Context

The image-gateway needs an HTTP layer for two endpoints: `PUT /upload` (raw binary image bytes) and `GET /health`. The initial assumption was that Goa would be overkill for two endpoints and couldn't handle raw binary uploads. Both assumptions were wrong.

Goa v3 provides `SkipRequestBodyEncodeDecode()` — a DSL function that gives the service implementation direct access to the raw `io.ReadCloser` body without JSON marshaling. This is exactly what the binary upload endpoint needs.

### Decision

Use Goa v3 as the HTTP framework for follow-image-gateway, matching the follow-api codebase.

**Goa DSL for the upload endpoint:**

```go
var _ = Service("upload", func() {
    Description("Image upload and processing service")
    Security(JWTAuth)

    Method("upload", func() {
        Description("Upload raw image bytes for processing")
        Payload(func() {
            Token("token", String, func() {
                Description("Ed25519-signed JWT upload token")
            })
            Required("token")
        })
        Result(UploadResult)
        Error("unauthorized", ErrorResult)
        Error("payload_too_large", ErrorResult)
        Error("bad_request", ErrorResult)
        Error("service_unavailable", ErrorResult)
        HTTP(func() {
            PUT("/upload")
            Param("token:token")
            SkipRequestBodyEncodeDecode()
            Response(StatusAccepted)
            Response("unauthorized", StatusUnauthorized)
            Response("payload_too_large", StatusRequestEntityTooLarge)
            Response("bad_request", StatusBadRequest)
            Response("service_unavailable", StatusServiceUnavailable)
        })
    })
})
```

The generated service interface receives `io.ReadCloser` for the raw body:

```go
type Service interface {
    Upload(ctx context.Context, p *UploadPayload, body io.ReadCloser) (*UploadResult, error)
}
```

### Alternatives Considered

1. **Standard `net/http`:** Simpler for 2 endpoints but loses consistency with follow-api, requires hand-rolling error responses, middleware wiring, and OpenAPI spec. No boilerplate reduction.

2. **Chi/Echo/Gin router:** Lightweight HTTP frameworks but still require manual error format standardization and don't match follow-api patterns.

### Consequences

**Positive:**
- Consistent HTTP patterns across follow-api and image-gateway
- Auto-generated OpenAPI spec documents the gateway API
- Standardized error responses (same format as follow-api)
- `SkipRequestBodyEncodeDecode()` provides raw body access for binary uploads
- Middleware composition follows same pattern as follow-api
- Future endpoints (retry, metrics, debug) just add to DSL

**Negative:**
- Goa code generation step required in build process
- `gen/` directory with generated code (gitignored, rebuilt on generate)
- Slight learning curve for `SkipRequestBodyEncodeDecode()` pattern (non-standard Goa usage)

---

## References

### Internal Documents

- **Architecture Spec**: `ai-docs/architecture/image-gateway-architecture.md` - Full gateway specification
- **TDD Workflow**: `ai-docs/workflows/tdd-red-green-refactor-workflow.md` - Stub-first TDD guide
- **Error Architecture**: `ai-docs/architecture/error-architecture/` - Rich Domain Errors patterns
- **CLAUDE.md**: Quality gates, development workflow, testing strategy

### External References

- **Pipes and Filters Pattern**: [Microsoft Architecture Guide](https://docs.microsoft.com/en-us/azure/architecture/patterns/pipes-and-filters)
- **Go Concurrency**: [Go Blog - Pipelines and cancellation](https://go.dev/blog/pipelines)
- **Redis Streams**: [Redis Streams Introduction](https://redis.io/docs/data-types/streams/)
- **Image Processing in Go**: [Go Blog - Image package](https://go.dev/blog/image)
- **Ed25519**: [Go crypto/ed25519](https://pkg.go.dev/crypto/ed25519)

---

## Appendix: Key Takeaways

### For the User

1. **DDD/Clean Architecture is the wrong pattern** for image-gateway because:
   - No business domain to model
   - No rich entities with business rules
   - No complex orchestration needs
   - Would add overhead without benefit

2. **Pipes and Filters is the right pattern** because:
   - Natural fit for linear processing pipeline
   - Small, decoupled stages (each ~100-200 lines)
   - Interface-based for TDD and testability
   - Efficient for Claude Code (small context per task)
   - Reuses proven infrastructure patterns from follow-api

3. **What to reuse from follow-api**:
   - Goa framework (with `SkipRequestBodyEncodeDecode()` for raw binary upload)
   - Infrastructure patterns (logging, config, MinIO, testing, linter)
   - TDD workflow (adapted for stages instead of use cases)
   - Quality gates (same standards)

4. **What NOT to reuse**:
   - Clean Architecture's 4-layer structure
   - Domain-driven patterns (entities, value objects, domain events)
   - CQRS / Command-Query pattern
   - Repository pattern

5. **Implementation estimate**: ~1 month (23-27 days) for full gateway

### For Claude Code Agents

When working on image-gateway tasks:

1. **Understand the architecture**: Pipes and Filters, not Clean Architecture
2. **Focus on one stage at a time**: Each stage is an independent unit
3. **Follow TDD workflow**: Stub → Tests → Implement → Refactor
4. **Use interfaces**: All external dependencies behind interfaces
5. **Keep stages small**: ~100-200 lines per stage, single responsibility
6. **Test with fakes**: Use in-memory fakes for Redis, MinIO, JWT
7. **Apply quality gates**: Same standards as follow-api

---

**End of Planning Document**
