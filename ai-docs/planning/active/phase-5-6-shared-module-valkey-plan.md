# Phase 5-6: Shared Module + Valkey Messaging Implementation Plan

**Date:** 2026-02-10
**Status:** Backlog
**Prerequisites:** Phase 4 complete (pipeline wired, backpressure tests passing)

> **STALE CONTENT WARNING (2026-02-14):**
> This plan was written before the architecture was finalized. Two significant changes affect this plan:
>
> 1. **`image:process` stream removed**: The gateway receives images via HTTP PUT (not from a Valkey stream). The `image:process` stream is replaced by `image:upload:{id}` (String NX + TTL) as a one-time upload guard written by the gateway. Sections referencing `image:process` consumer/producer need revision.
>
> 2. **Client library changed**: `redis/go-redis/v9` is replaced by `valkey-io/valkey-go` per ADR-012. Code examples using `go-redis` imports need updating.
>
> **Valkey key patterns (current):**
> - `image:upload:{id}` (String NX + TTL) -- one-time upload guard, written by gateway
> - `image:status:{id}` (Hash + TTL) -- live progress, written by api (queued) then gateway (stages)
> - `image:result` (Stream) -- final result from gateway to api (consumer group)
>
> This plan will be fully revised when Phase 5 implementation begins.

---

## Executive Summary

This plan introduces the `follow-pkg` Go module and implements Valkey Streams messaging for both follow-api and follow-image-gateway. The approach is **incremental** — start with only what's needed (logger and Valkey), defer everything else.

**Critical principle:** Do not extract MinIO, config, values, auth, or health checks into the shared module yet. Those migrations happen later when justified by actual duplication pain.

**Scope:**
- Part 1: Bootstrap `follow-pkg` module (logger + Valkey packages)
- Part 2: Design and implement Valkey client, consumer, producer, progress, health
- Part 3: Wire Valkey messaging into follow-image-gateway
- Part 4: E2E testing with Valkey in Docker Compose
- Part 5: Documentation notes for future migrations (NOT implemented)

**Estimated effort:** 34 story points across 12 tasks

---

## Part 1: Shared Module Bootstrap (4 story points)

### Task 1.1: Create follow-pkg Module Scaffold

**Objective:** Initialize the `follow-pkg` Go module at `/home/yoseforb/pkg/follow/follow-pkg/`.

**TDD Order:**
1. Create directory structure
2. Initialize go.mod
3. Create README.md with module purpose
4. Add .gitignore for Go projects
5. Add LICENSE (same as parent services)
6. Add CLAUDE.md file with same workflow and quality gates
7. Copy custom linters file and linter from follow-image-gateway
8. Create ai-docs/{adr,research,planning} directories

**Files Created:**
- `/home/yoseforb/pkg/follow/follow-pkg/go.mod`
- `/home/yoseforb/pkg/follow/follow-pkg/README.md`
- `/home/yoseforb/pkg/follow/follow-pkg/CLAUDE.md`
- `/home/yoseforb/pkg/follow/follow-pkg/.gitignore`
- `/home/yoseforb/pkg/follow/follow-pkg/LICENSE`

**go.mod:**
```go
module github.com/follow/follow-pkg

go 1.23
```

**README.md:**
```markdown
# follow-pkg

Shared Go utilities for the Follow platform services (follow-api and follow-image-gateway).

## Packages

- `logger/` — Structured logging with zerolog
- `valkey/` — Valkey Streams messaging client, consumer, producer, progress tracking

## Usage

Services use this module via `replace` directive during development:

```go
replace github.com/follow/follow-pkg => ../follow-pkg
```

## Philosophy

This module contains only code that is:
1. Used by BOTH services
2. Identical or nearly identical between them
3. Justified by actual duplication pain

Do not proactively migrate code "because it could be shared." Wait until duplication becomes painful.
```

**Acceptance Criteria:**
- Module compiles: `cd /home/yoseforb/pkg/follow/follow-pkg && go mod tidy`
- README.md documents scope and usage
- Directory structure matches Go conventions

**Story Points:** 1

---

### Task 1.2: Copy Logger Package from Existing Codebase

**Objective:** Copy the logger package from follow-image-gateway to follow-pkg with minimal changes.

**TDD Order:**
1. Copy `internal/shared/logger/logger.go` to `follow-pkg/logger/logger.go`
2. Copy `internal/shared/logger/errors.go` to `follow-pkg/logger/errors.go`
3. Update imports to remove gateway-specific paths
4. Generalize service name (make it a parameter instead of hardcoded)
5. Add go.mod dependencies (zerolog)
6. Write example usage in logger_test.go

**Files Created:**
- `/home/yoseforb/pkg/follow/follow-pkg/logger/logger.go`
- `/home/yoseforb/pkg/follow/follow-pkg/logger/errors.go`
- `/home/yoseforb/pkg/follow/follow-pkg/logger/logger_test.go`

**Interface Changes:**

```go
// Before (gateway-specific):
func InitGlobalLogger(cfg *config.LoggingConfig) error {
    log.Logger = log.With().Str("service", "follow-image-gateway").Logger()
}

// After (service-agnostic):
func InitGlobalLogger(serviceName string, cfg *LoggingConfig) error {
    log.Logger = log.With().Str("service", serviceName).Logger()
}

// LoggingConfig moves to follow-pkg/logger/config.go
type LoggingConfig struct {
    Level  string
    Format string
    Colors bool
}
```

**Dependencies to Add:**
- `github.com/rs/zerolog`

**Acceptance Criteria:**
- Logger package compiles independently
- No import cycles
- Example test demonstrates logger initialization and component loggers
- Service name is parameterized, not hardcoded

**Story Points:** 1

---

### Task 1.3: Add follow-pkg Dependency to Both Services

**Objective:** Wire `follow-pkg` into follow-image-gateway and follow-api using `replace` directives.

**TDD Order:**
1. Add `replace` directive to gateway's go.mod
2. Add `replace` directive to api's go.mod
3. Update gateway imports to use `github.com/follow/follow-pkg/logger`
4. Update gateway's InitGlobalLogger call to pass service name
5. Run quality gates (go mod tidy, go vet, tests)

**Files Modified:**
- `/home/yoseforb/pkg/follow/follow-image-gateway/go.mod`
- `/home/yoseforb/pkg/follow/follow-image-gateway/cmd/server/app.go` (Init step)

**Gateway go.mod changes:**
```go
require github.com/follow/follow-pkg v0.0.0

replace github.com/follow/follow-pkg => ../follow-pkg
```

**Gateway app.go changes:**
```go
// Before:
import "follow-image-gateway/internal/shared/logger"
err := logger.InitGlobalLogger(cfg.Logging)

// After:
import sharedlogger "github.com/follow/follow-pkg/logger"
err := sharedlogger.InitGlobalLogger("follow-image-gateway", &sharedlogger.LoggingConfig{
    Level:  cfg.Logging.Level,
    Format: cfg.Logging.Format,
    Colors: cfg.Logging.Colors,
})
```

**Acceptance Criteria:**
- `go mod tidy` succeeds in both services
- `go test ./...` passes in gateway
- Gateway starts successfully with shared logger
- No import cycles or unresolved dependencies

**Story Points:** 2

---

## Part 2: Valkey Package Design (14 story points)

### Task 2.1: Define Valkey Client Interface and Config

**Objective:** Define the core Valkey client interface and configuration types that will be used by both services.

**TDD Order:**
1. Define `ValkeyConfig` struct
2. Define `Client` interface with all required operations
3. Write interface documentation
4. Create `errors.go` with sentinel errors

**Files Created:**
- `/home/yoseforb/pkg/follow/follow-pkg/valkey/client.go`
- `/home/yoseforb/pkg/follow/follow-pkg/valkey/config.go`
- `/home/yoseforb/pkg/follow/follow-pkg/valkey/errors.go`

**ValkeyConfig:**
```go
package valkey

import "time"

// ValkeyConfig holds connection configuration for Valkey.
type ValkeyConfig struct {
    Address  string        // Host:port (e.g., "localhost:6379")
    Password string        // Optional password
    DB       int           // Database number (default 0)
    PoolSize int           // Max connections (default 10)

    // Timeouts
    DialTimeout  time.Duration // Connection timeout (default 5s)
    ReadTimeout  time.Duration // Read timeout (default 3s)
    WriteTimeout time.Duration // Write timeout (default 3s)
}

// Validate checks if the configuration is valid.
func (c *ValkeyConfig) Validate() error {
    if c.Address == "" {
        return ErrInvalidAddress
    }
    if c.PoolSize <= 0 {
        c.PoolSize = 10 // Default
    }
    return nil
}
```

**Client Interface:**
```go
package valkey

import "context"

// Client wraps valkey-go operations used by Follow services.
// This interface enables testing with fakes and provides a
// stable API boundary between services and Valkey.
type Client interface {
    // Connection
    Ping(ctx context.Context) (string, error)
    Close() error

    // Streams
    XAdd(ctx context.Context, streamKey string, fields map[string]interface{}) (string, error)
    XAddWithMaxLen(ctx context.Context, streamKey string, maxLen int64, fields map[string]interface{}) (string, error)
    XReadGroup(ctx context.Context, group, consumer, streamKey, id string, count int64, block time.Duration) ([]StreamMessage, error)
    XAck(ctx context.Context, streamKey, group string, messageIDs ...string) error
    XCLAIM(ctx context.Context, streamKey, group, consumer string, minIdleTime time.Duration, messageIDs ...string) ([]StreamMessage, error)
    XPending(ctx context.Context, streamKey, group string) (*PendingInfo, error)
    XTrim(ctx context.Context, streamKey string, maxLen int64) error

    // Consumer Groups
    XGroupCreate(ctx context.Context, streamKey, group, start string) error
    XGroupCreateMkStream(ctx context.Context, streamKey, group, start string) error
    StreamGroupExists(ctx context.Context, streamKey, group string) (bool, error)

    // Hashes
    HSet(ctx context.Context, key string, values map[string]interface{}) error
    HGetAll(ctx context.Context, key string) (map[string]string, error)
    Expire(ctx context.Context, key string, ttl time.Duration) error
    Del(ctx context.Context, keys ...string) error
}

// StreamMessage represents a message from XREADGROUP.
type StreamMessage struct {
    ID     string
    Fields map[string]interface{}
}

// PendingInfo contains XPENDING summary data.
type PendingInfo struct {
    Count     int64
    Consumers map[string]int64 // consumer name -> pending count
}
```

**Errors:**
```go
package valkey

import "errors"

var (
    // Config errors
    ErrInvalidAddress = errors.New("valkey: invalid address")
    ErrInvalidPoolSize = errors.New("valkey: invalid pool size")

    // Connection errors
    ErrConnectionFailed = errors.New("valkey: connection failed")
    ErrPingFailed = errors.New("valkey: ping failed")

    // Stream errors
    ErrStreamNotFound = errors.New("valkey: stream not found")
    ErrGroupNotFound = errors.New("valkey: consumer group not found")
    ErrGroupAlreadyExists = errors.New("valkey: consumer group already exists")
    ErrNoMessages = errors.New("valkey: no messages available")

    // Consumer errors
    ErrConsumerClosed = errors.New("valkey: consumer is closed")
    ErrConsumerNotStarted = errors.New("valkey: consumer not started")
    ErrMessageHandlerNil = errors.New("valkey: message handler is nil")
)
```

**Acceptance Criteria:**
- Interfaces compile
- All operations needed by both services are covered
- Documentation explains each method's purpose
- Error types cover expected failure modes

**Story Points:** 2

---

### Task 2.2: Implement ValkeyClient (valkey-go Wrapper)

**Objective:** Implement the `Client` interface using `valkey-io/valkey-go`.

**TDD Order:**
1. Create `RedisClient` struct wrapping `*redis.Client`
2. Write unit tests using go-redis mock (testify/mock allowed for external service)
3. Implement each interface method (RED)
4. Make tests pass (GREEN)
5. Refactor for clarity

**Files Created:**
- `/home/yoseforb/pkg/follow/follow-pkg/valkey/redis_client.go`
- `/home/yoseforb/pkg/follow/follow-pkg/valkey/redis_client_test.go`

**RedisClient Implementation:**
```go
package valkey

import (
    "context"
    "time"

    "github.com/valkey-io/valkey-go"
)

// Compile-time interface check
var _ Client = (*ValkeyClient)(nil)

// ValkeyClient implements Client using valkey-io/valkey-go.
// This is the production implementation that connects to actual Valkey.
type ValkeyClient struct {
    client valkey.Client
}

// NewRedisClient creates a new Valkey client from the given config.
func NewRedisClient(cfg *ValkeyConfig) (*RedisClient, error) {
    if err := cfg.Validate(); err != nil {
        return nil, err
    }

    client := redis.NewClient(&redis.Options{
        Addr:         cfg.Address,
        Password:     cfg.Password,
        DB:           cfg.DB,
        PoolSize:     cfg.PoolSize,
        DialTimeout:  cfg.DialTimeout,
        ReadTimeout:  cfg.ReadTimeout,
        WriteTimeout: cfg.WriteTimeout,
    })

    return &RedisClient{client: client}, nil
}

func (r *RedisClient) Ping(ctx context.Context) (string, error) {
    result, err := r.client.Ping(ctx).Result()
    if err != nil {
        return "", fmt.Errorf("%w: %w", ErrPingFailed, err)
    }
    return result, nil
}

func (r *RedisClient) Close() error {
    return r.client.Close()
}

func (r *RedisClient) XAdd(ctx context.Context, streamKey string, fields map[string]interface{}) (string, error) {
    args := &redis.XAddArgs{
        Stream: streamKey,
        Values: fields,
    }
    return r.client.XAdd(ctx, args).Result()
}

func (r *RedisClient) XAddWithMaxLen(ctx context.Context, streamKey string, maxLen int64, fields map[string]interface{}) (string, error) {
    args := &redis.XAddArgs{
        Stream: streamKey,
        MaxLen: maxLen,
        Values: fields,
    }
    return r.client.XAdd(ctx, args).Result()
}

// ... implement remaining interface methods
```

**Test Strategy:**
- Use testify/mock for valkey-go client (external service exception)
- Table-driven tests for each method
- Error path coverage (connection failure, invalid args, etc.)

**Dependencies to Add:**
- `github.com/valkey-io/valkey-go`
- `github.com/stretchr/testify` (for mocks)

**Acceptance Criteria:**
- All interface methods implemented
- Unit tests pass with mock client
- Error wrapping uses sentinel errors
- go vet, golangci-lint pass

**Story Points:** 3

---

### Task 2.3: Implement Consumer (XREADGROUP Loop)

**Objective:** Implement a generic Valkey Streams consumer that reads messages, calls a handler, and acks on success.

**TDD Order:**
1. Define `Consumer` struct and `MessageHandler` callback
2. Define `ConsumerConfig` for block timeout, count, etc.
3. Write fake client for unit tests (hand-written fake, not mock)
4. Write tests for consumer lifecycle (RED)
5. Implement consumer (GREEN)
6. Add graceful shutdown tests
7. Refactor

**Files Created:**
- `/home/yoseforb/pkg/follow/follow-pkg/valkey/consumer.go`
- `/home/yoseforb/pkg/follow/follow-pkg/valkey/consumer_test.go`
- `/home/yoseforb/pkg/follow/follow-pkg/valkey/testdoubles_test.go` (FakeValkeyClient)

**Consumer Interface:**
```go
package valkey

import (
    "context"
    "time"
)

// MessageHandler is called for each message consumed from the stream.
// If it returns an error, the message is NOT acked (will be redelivered).
// If it returns nil, the message is acked.
type MessageHandler func(ctx context.Context, msg StreamMessage) error

// ConsumerConfig configures a stream consumer.
type ConsumerConfig struct {
    StreamKey    string          // Stream to consume from
    GroupName    string          // Consumer group name
    ConsumerName string          // This consumer's name (e.g., "gateway-{hostname}")
    Handler      MessageHandler  // Callback for each message

    // XREADGROUP options
    Count        int64           // Max messages per XREADGROUP call (default 10)
    BlockTimeout time.Duration   // BLOCK duration (default 5s)

    // Lifecycle
    ReadyTimeout time.Duration   // How long to wait for Ready() signal (default 10s)
}

// Consumer reads messages from a Valkey stream using XREADGROUP.
type Consumer struct {
    client   Client
    config   ConsumerConfig
    readyCh  chan struct{}
    isRunning atomic.Bool
    cancel   context.CancelFunc
}

// NewConsumer creates a new stream consumer.
func NewConsumer(client Client, config ConsumerConfig) (*Consumer, error) {
    if client == nil {
        return nil, fmt.Errorf("client is nil")
    }
    if config.Handler == nil {
        return nil, ErrMessageHandlerNil
    }
    if config.StreamKey == "" || config.GroupName == "" || config.ConsumerName == "" {
        return nil, fmt.Errorf("streamKey, groupName, and consumerName are required")
    }

    // Defaults
    if config.Count == 0 {
        config.Count = 10
    }
    if config.BlockTimeout == 0 {
        config.BlockTimeout = 5 * time.Second
    }
    if config.ReadyTimeout == 0 {
        config.ReadyTimeout = 10 * time.Second
    }

    return &Consumer{
        client:  client,
        config:  config,
        readyCh: make(chan struct{}),
    }, nil
}

// Start begins consuming messages in a background goroutine.
// Blocks until the consumer loop is running or ctx is cancelled.
func (c *Consumer) Start(ctx context.Context) error {
    if c.isRunning.Swap(true) {
        return fmt.Errorf("consumer already started")
    }

    ctx, cancel := context.WithCancel(ctx)
    c.cancel = cancel

    go c.consumeLoop(ctx)

    // Wait for consumer to be ready
    select {
    case <-c.readyCh:
        return nil
    case <-ctx.Done():
        return ctx.Err()
    case <-time.After(c.config.ReadyTimeout):
        return fmt.Errorf("consumer ready timeout")
    }
}

// Ready returns a channel that is closed when the consumer is running.
func (c *Consumer) Ready() <-chan struct{} {
    return c.readyCh
}

// Stop gracefully stops the consumer.
func (c *Consumer) Stop() error {
    if !c.isRunning.Load() {
        return ErrConsumerNotStarted
    }

    if c.cancel != nil {
        c.cancel()
    }

    return nil
}

func (c *Consumer) consumeLoop(ctx context.Context) {
    close(c.readyCh) // Signal ready

    for {
        select {
        case <-ctx.Done():
            return
        default:
        }

        messages, err := c.client.XReadGroup(
            ctx,
            c.config.GroupName,
            c.config.ConsumerName,
            c.config.StreamKey,
            ">", // Only new messages
            c.config.Count,
            c.config.BlockTimeout,
        )

        if err != nil {
            if errors.Is(err, ErrNoMessages) {
                continue // Normal - no messages available
            }
            if errors.Is(err, context.Canceled) {
                return // Graceful shutdown
            }
            // Log error and continue
            continue
        }

        for _, msg := range messages {
            if err := c.processMessage(ctx, msg); err != nil {
                // Log error but don't crash consumer
                continue
            }
        }
    }
}

func (c *Consumer) processMessage(ctx context.Context, msg StreamMessage) error {
    // Call handler
    if err := c.config.Handler(ctx, msg); err != nil {
        return err // Don't ack - message will be redelivered
    }

    // Ack on success
    return c.client.XAck(ctx, c.config.StreamKey, c.config.GroupName, msg.ID)
}
```

**FakeValkeyClient (testdoubles_test.go):**
```go
package valkey_test

import (
    "context"
    "sync"
    "time"

    "github.com/follow/follow-pkg/valkey"
)

var _ valkey.Client = (*FakeValkeyClient)(nil)

type FakeValkeyClient struct {
    mu       sync.RWMutex
    streams  map[string][]valkey.StreamMessage // streamKey -> messages
    acked    map[string][]string               // streamKey -> acked IDs
    closed   bool
}

func NewFakeValkeyClient() *FakeValkeyClient {
    return &FakeValkeyClient{
        streams: make(map[string][]valkey.StreamMessage),
        acked:   make(map[string][]string),
    }
}

func (f *FakeValkeyClient) AddMessage(streamKey string, msg valkey.StreamMessage) {
    f.mu.Lock()
    defer f.mu.Unlock()
    f.streams[streamKey] = append(f.streams[streamKey], msg)
}

func (f *FakeValkeyClient) XReadGroup(ctx context.Context, group, consumer, streamKey, id string, count int64, block time.Duration) ([]valkey.StreamMessage, error) {
    f.mu.Lock()
    defer f.mu.Unlock()

    messages := f.streams[streamKey]
    if len(messages) == 0 {
        return nil, valkey.ErrNoMessages
    }

    // Return up to 'count' messages
    n := int(count)
    if n > len(messages) {
        n = len(messages)
    }

    result := make([]valkey.StreamMessage, n)
    copy(result, messages[:n])
    f.streams[streamKey] = messages[n:] // Remove consumed messages

    return result, nil
}

func (f *FakeValkeyClient) XAck(ctx context.Context, streamKey, group string, messageIDs ...string) error {
    f.mu.Lock()
    defer f.mu.Unlock()
    f.acked[streamKey] = append(f.acked[streamKey], messageIDs...)
    return nil
}

// ... implement other interface methods (no-ops for consumer tests)
```

**Test Cases:**
- Consumer processes messages and acks
- Handler error prevents ack
- Graceful shutdown on context cancel
- Consumer blocks until Ready()
- Multiple Start() calls return error

**Acceptance Criteria:**
- Consumer tests pass with FakeValkeyClient
- Graceful shutdown doesn't leak goroutines
- Handler errors don't crash consumer
- XACK only called on successful processing

**Story Points:** 3

---

### Task 2.4: Implement Producer (XADD Wrapper)

**Objective:** Implement a simple producer that wraps XADD with optional MAXLEN trimming.

**TDD Order:**
1. Define `Producer` struct
2. Write tests with FakeValkeyClient (RED)
3. Implement (GREEN)
4. Refactor

**Files Created:**
- `/home/yoseforb/pkg/follow/follow-pkg/valkey/producer.go`
- `/home/yoseforb/pkg/follow/follow-pkg/valkey/producer_test.go`

**Producer Interface:**
```go
package valkey

import "context"

// Producer publishes messages to a Valkey stream.
type Producer struct {
    client Client
}

// NewProducer creates a new stream producer.
func NewProducer(client Client) *Producer {
    return &Producer{client: client}
}

// Publish adds a message to the stream.
// Returns the message ID (e.g., "1234567890-0").
func (p *Producer) Publish(ctx context.Context, streamKey string, fields map[string]interface{}) (string, error) {
    return p.client.XAdd(ctx, streamKey, fields)
}

// PublishWithTrim adds a message and trims the stream to maxLen.
// Use this to prevent unbounded stream growth.
func (p *Producer) PublishWithTrim(ctx context.Context, streamKey string, maxLen int64, fields map[string]interface{}) (string, error) {
    return p.client.XAddWithMaxLen(ctx, streamKey, maxLen, fields)
}
```

**Test Cases:**
- Publish adds message to stream
- PublishWithTrim enforces maxLen
- Context cancellation returns error

**Acceptance Criteria:**
- Producer tests pass with FakeValkeyClient
- Simple, minimal wrapper (no complex logic)

**Story Points:** 1

---

### Task 2.5: Implement Progress Tracker (Hash Operations)

**Objective:** Implement progress tracking using Valkey hashes with TTL for `image:status:{id}`.

**TDD Order:**
1. Define `ProgressTracker` struct
2. Write tests with FakeValkeyClient (RED)
3. Implement (GREEN)
4. Refactor

**Files Created:**
- `/home/yoseforb/pkg/follow/follow-pkg/valkey/progress.go`
- `/home/yoseforb/pkg/follow/follow-pkg/valkey/progress_test.go`

**Progress Interface:**
```go
package valkey

import (
    "context"
    "fmt"
    "time"
)

const DefaultProgressTTL = 1 * time.Hour

// ProgressTracker manages image processing progress hashes.
// Used by gateway to publish progress, and by follow-api to read for SSE.
type ProgressTracker struct {
    client Client
    ttl    time.Duration
}

// NewProgressTracker creates a progress tracker with the given TTL.
func NewProgressTracker(client Client, ttl time.Duration) *ProgressTracker {
    if ttl == 0 {
        ttl = DefaultProgressTTL
    }
    return &ProgressTracker{
        client: client,
        ttl:    ttl,
    }
}

// SetProgress updates the progress hash for an image.
// Automatically sets TTL.
func (p *ProgressTracker) SetProgress(ctx context.Context, imageID string, fields map[string]interface{}) error {
    key := fmt.Sprintf("image:status:%s", imageID)

    if err := p.client.HSet(ctx, key, fields); err != nil {
        return err
    }

    return p.client.Expire(ctx, key, p.ttl)
}

// GetProgress retrieves the progress hash for an image.
func (p *ProgressTracker) GetProgress(ctx context.Context, imageID string) (map[string]string, error) {
    key := fmt.Sprintf("image:status:%s", imageID)
    return p.client.HGetAll(ctx, key)
}

// DeleteProgress removes the progress hash for an image.
func (p *ProgressTracker) DeleteProgress(ctx context.Context, imageID string) error {
    key := fmt.Sprintf("image:status:%s", imageID)
    return p.client.Del(ctx, key)
}
```

**FakeValkeyClient Extensions (for hash tests):**
```go
type FakeValkeyClient struct {
    // ... existing fields
    hashes map[string]map[string]string // key -> field -> value
}

func (f *FakeValkeyClient) HSet(ctx context.Context, key string, values map[string]interface{}) error {
    f.mu.Lock()
    defer f.mu.Unlock()

    if f.hashes[key] == nil {
        f.hashes[key] = make(map[string]string)
    }

    for field, value := range values {
        f.hashes[key][field] = fmt.Sprintf("%v", value)
    }

    return nil
}

func (f *FakeValkeyClient) HGetAll(ctx context.Context, key string) (map[string]string, error) {
    f.mu.Lock()
    defer f.mu.Unlock()

    result := make(map[string]string)
    for field, value := range f.hashes[key] {
        result[field] = value
    }

    return result, nil
}

// ... implement Expire, Del
```

**Test Cases:**
- SetProgress creates hash with TTL
- GetProgress returns all fields
- DeleteProgress removes hash
- Multiple SetProgress calls update hash

**Acceptance Criteria:**
- Progress tests pass with FakeValkeyClient
- TTL is set on every SetProgress call
- Key format matches ADR-016: `image:status:{id}`

**Story Points:** 2

---

### Task 2.6: Implement Orphan Reclaimer (XCLAIM Loop)

**Objective:** Implement a background loop that claims orphaned messages using XPENDING + XCLAIM.

**TDD Order:**
1. Define `Reclaimer` struct and config
2. Write tests with FakeValkeyClient (RED)
3. Implement (GREEN)
4. Refactor

**Files Created:**
- `/home/yoseforb/pkg/follow/follow-pkg/valkey/reclaimer.go`
- `/home/yoseforb/pkg/follow/follow-pkg/valkey/reclaimer_test.go`

**Reclaimer Interface:**
```go
package valkey

import (
    "context"
    "time"
)

// ReclaimerConfig configures the orphan reclaimer.
type ReclaimerConfig struct {
    StreamKey    string        // Stream to monitor
    GroupName    string        // Consumer group
    ConsumerName string        // This consumer's name
    IdleTimeout  time.Duration // Messages idle for this long are reclaimed (default 5m)
    ScanInterval time.Duration // How often to scan for orphans (default 1m)
}

// Reclaimer reclaims orphaned messages from other consumers.
// Runs as a background goroutine alongside the consumer.
type Reclaimer struct {
    client Client
    config ReclaimerConfig
    cancel context.CancelFunc
}

// NewReclaimer creates an orphan reclaimer.
func NewReclaimer(client Client, config ReclaimerConfig) (*Reclaimer, error) {
    if client == nil {
        return nil, fmt.Errorf("client is nil")
    }

    // Defaults
    if config.IdleTimeout == 0 {
        config.IdleTimeout = 5 * time.Minute
    }
    if config.ScanInterval == 0 {
        config.ScanInterval = 1 * time.Minute
    }

    return &Reclaimer{
        client: client,
        config: config,
    }, nil
}

// Start begins the reclaim loop.
func (r *Reclaimer) Start(ctx context.Context) error {
    ctx, cancel := context.WithCancel(ctx)
    r.cancel = cancel

    go r.reclaimLoop(ctx)
    return nil
}

// Stop stops the reclaim loop.
func (r *Reclaimer) Stop() error {
    if r.cancel != nil {
        r.cancel()
    }
    return nil
}

func (r *Reclaimer) reclaimLoop(ctx context.Context) {
    ticker := time.NewTicker(r.config.ScanInterval)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            r.reclaimOnce(ctx)
        }
    }
}

func (r *Reclaimer) reclaimOnce(ctx context.Context) {
    pending, err := r.client.XPending(ctx, r.config.StreamKey, r.config.GroupName)
    if err != nil {
        // Log and continue
        return
    }

    if pending.Count == 0 {
        return // No pending messages
    }

    // For simplicity, claim all pending messages from other consumers
    // In production, you'd fetch specific message IDs via XPENDING with range
    // This is a simplified implementation suitable for the initial version

    // TODO: Implement XPENDING with range to get specific message IDs
    // then XCLAIM those IDs
}
```

**Note:** Full XCLAIM implementation requires extending the Client interface with `XPendingExt` (range query). For the initial version, this task creates the structure and lifecycle. Full reclaim logic can be added in a follow-up task if needed.

**Test Cases:**
- Reclaimer starts and stops gracefully
- reclaimOnce is called on interval
- Context cancellation stops loop

**Acceptance Criteria:**
- Reclaimer lifecycle works
- Tests pass with FakeValkeyClient
- No goroutine leaks on Stop()

**Story Points:** 2 (simplified version)

---

### Task 2.7: Implement Health Check

**Objective:** Implement Valkey health check compatible with follow-image-gateway's existing health checker.

**TDD Order:**
1. Define `HealthChecker` struct
2. Write tests with FakeValkeyClient (RED)
3. Implement (GREEN)
4. Verify compatibility with gateway's RedisClient interface

**Files Created:**
- `/home/yoseforb/pkg/follow/follow-pkg/valkey/health.go`
- `/home/yoseforb/pkg/follow/follow-pkg/valkey/health_test.go`

**Health Interface:**
```go
package valkey

import "context"

// HealthChecker provides health checks for Valkey.
type HealthChecker struct {
    client Client
}

// NewHealthChecker creates a Valkey health checker.
func NewHealthChecker(client Client) *HealthChecker {
    return &HealthChecker{client: client}
}

// Ping checks if Valkey is reachable.
func (h *HealthChecker) Ping(ctx context.Context) (string, error) {
    return h.client.Ping(ctx)
}

// StreamGroupExists checks if a consumer group exists on a stream.
func (h *HealthChecker) StreamGroupExists(ctx context.Context, streamKey, groupName string) (bool, error) {
    return h.client.StreamGroupExists(ctx, streamKey, groupName)
}
```

**RedisClient.StreamGroupExists Implementation:**
```go
func (r *RedisClient) StreamGroupExists(ctx context.Context, streamKey, groupName string) (bool, error) {
    result, err := r.client.XInfoGroups(ctx, streamKey).Result()
    if err != nil {
        if err == redis.Nil {
            return false, nil
        }
        return false, err
    }

    for _, group := range result {
        if group.Name == groupName {
            return true, nil
        }
    }

    return false, nil
}
```

**Acceptance Criteria:**
- HealthChecker implements gateway's RedisClient interface
- Tests verify Ping and StreamGroupExists
- Compatible with existing gateway health checks

**Story Points:** 1

---

## Part 3: Gateway Messaging Integration (8 story points)

### Task 3.1: Add Valkey Config to Gateway

**Objective:** Add Valkey configuration to gateway's config system.

**Files Modified:**
- `/home/yoseforb/pkg/follow/follow-image-gateway/configs/config.yaml`
- `/home/yoseforb/pkg/follow/follow-image-gateway/internal/shared/config/config.go`

**config.yaml:**
```yaml
valkey:
  address: "localhost:6379"
  password: ""
  db: 0
  pool_size: 20
  dial_timeout: 5s
  read_timeout: 3s
  write_timeout: 3s

streams:
  result_stream: "image:result"
  consumer_group: "gateway-workers"
  consumer_count: 2
  upload_guard_key_prefix: "image:upload:"
  upload_guard_ttl: 3600
  block_timeout: 5s
```

**Config struct:**
```go
type Config struct {
    // ... existing fields

    Valkey ValkeyConfig `mapstructure:"valkey"`
    Streams StreamsConfig `mapstructure:"streams"`
}

type ValkeyConfig struct {
    Address      string        `mapstructure:"address"`
    Password     string        `mapstructure:"password"`
    DB           int           `mapstructure:"db"`
    PoolSize     int           `mapstructure:"pool_size"`
    DialTimeout  time.Duration `mapstructure:"dial_timeout"`
    ReadTimeout  time.Duration `mapstructure:"read_timeout"`
    WriteTimeout time.Duration `mapstructure:"write_timeout"`
}

type StreamsConfig struct {
    ProcessStream string        `mapstructure:"process_stream"`
    ResultStream  string        `mapstructure:"result_stream"`
    ConsumerGroup string        `mapstructure:"consumer_group"`
    ConsumerCount int           `mapstructure:"consumer_count"`
    BlockTimeout  time.Duration `mapstructure:"block_timeout"`
}
```

**Acceptance Criteria:**
- Config loads successfully
- Viper reads Valkey config from YAML and env vars
- Config validation includes Valkey fields

**Story Points:** 1

---

### Task 3.2: Add Valkey to Docker Compose

**Objective:** Add Valkey service to both the parent `follow/docker-compose.yml` (full stack) and the per-service `follow-image-gateway/docker-compose.yml` (standalone gateway dev).

**Parent compose** (`/home/yoseforb/pkg/follow/docker-compose.yml`): Already includes Valkey as shared infrastructure (see Docker Compose Strategy section below). No changes needed here -- Valkey is defined once and shared by all services.

**Per-service compose** (`/home/yoseforb/pkg/follow/follow-image-gateway/docker-compose.yml`): Add Valkey for standalone gateway development (when running the gateway without the full stack).

**Files Modified:**
- `/home/yoseforb/pkg/follow/follow-image-gateway/docker-compose.yml`

**Per-service docker-compose.yml changes:**
```yaml
services:
  valkey:
    image: valkey/valkey:8
    ports:
      - "6379:6379"
    volumes:
      - valkey-data:/data
    command: valkey-server --appendonly yes
    healthcheck:
      test: ["CMD", "valkey-cli", "ping"]
      interval: 5s
      timeout: 3s
      retries: 5

  minio:
    # ... existing MinIO config

  gateway:
    # ... existing gateway config
    depends_on:
      - minio
      - valkey
    environment:
      - VALKEY_ADDRESS=valkey:6379

volumes:
  valkey-data:
  minio-data:
```

**Two ways to run:**
- **Full stack:** `cd follow && docker compose up` — uses parent compose with shared Valkey, MinIO, PostgreSQL
- **Gateway only:** `cd follow-image-gateway && docker compose up` — uses per-service compose with local Valkey + MinIO

**Acceptance Criteria:**
- `docker compose up` starts Valkey (both parent and per-service)
- Gateway can connect to Valkey in Docker
- Health check works
- No port conflicts when using parent compose (single Valkey instance)

**Story Points:** 1

---

### Task 3.3: Wire Valkey Client into App Lifecycle

**Objective:** Add Valkey client initialization to cmd/server App.Init() step table.

**TDD Order:**
1. Add Valkey step to Init() step table
2. Create `buildValkeyClient` factory function
3. Update health checker with real client
4. Test startup with local Valkey

**Files Modified:**
- `/home/yoseforb/pkg/follow/follow-image-gateway/cmd/server/app.go`
- `/home/yoseforb/pkg/follow/follow-image-gateway/cmd/server/factories.go`

**App struct:**
```go
type App struct {
    // ... existing fields
    valkeyClient *valkey.RedisClient
}
```

**Init() step table:**
```go
func (a *App) Init(ctx context.Context) error {
    steps := []struct {
        name string
        fn   func(context.Context) error
    }{
        {"logger", a.initLogger},
        {"vips", a.initVips},
        {"auth", a.initAuth},
        {"storage", a.initStorage},
        {"valkey", a.initValkey},       // NEW
        {"detector", a.initDetector},
        {"pipeline", a.initPipeline},
        {"server", a.initServer},
    }

    // ... existing step execution logic
}

func (a *App) initValkey(ctx context.Context) error {
    client, err := buildValkeyClient(a.cfg)
    if err != nil {
        return fmt.Errorf("valkey init failed: %w", err)
    }

    // Test connection
    if _, err := client.Ping(ctx); err != nil {
        return fmt.Errorf("valkey ping failed: %w", err)
    }

    a.valkeyClient = client
    a.log.Info().Msg("Valkey client initialized")
    return nil
}
```

**Factory function (factories.go):**
```go
func buildValkeyClient(cfg *config.Config) (*valkey.RedisClient, error) {
    valkeyConfig := &valkey.ValkeyConfig{
        Address:      cfg.Valkey.Address,
        Password:     cfg.Valkey.Password,
        DB:           cfg.Valkey.DB,
        PoolSize:     cfg.Valkey.PoolSize,
        DialTimeout:  cfg.Valkey.DialTimeout,
        ReadTimeout:  cfg.Valkey.ReadTimeout,
        WriteTimeout: cfg.Valkey.WriteTimeout,
    }

    return valkey.NewRedisClient(valkeyConfig)
}
```

**Health checker update:**
```go
func (a *App) initServer(ctx context.Context) error {
    // ... existing code

    healthChecker := health.NewHealthChecker(
        a.storageClient,
        valkey.NewHealthChecker(a.valkeyClient), // Real client now
    )

    // ... rest of server init
}
```

**Acceptance Criteria:**
- Gateway starts successfully with Valkey connection
- Health endpoint reports Valkey status
- Init fails gracefully if Valkey unreachable
- Quality gates pass

**Story Points:** 2

---

### Task 3.4: Implement JobConsumer

**Objective:** Create a consumer that reads from `image:process` stream and submits jobs to the pipeline.

**TDD Order:**
1. Define `messaging.JobConsumer` struct
2. Write tests with FakeValkeyClient and fake pipeline (RED)
3. Implement message parsing and pipeline submission (GREEN)
4. Wire into App lifecycle
5. Refactor

**Files Created:**
- `/home/yoseforb/pkg/follow/follow-image-gateway/internal/messaging/job_consumer.go`
- `/home/yoseforb/pkg/follow/follow-image-gateway/internal/messaging/job_consumer_test.go`
- `/home/yoseforb/pkg/follow/follow-image-gateway/internal/messaging/errors.go`

**JobConsumer:**
```go
package messaging

import (
    "context"
    "fmt"
    "time"

    "github.com/follow/follow-pkg/valkey"
    "github.com/rs/zerolog"

    "follow-image-gateway/internal/auth"
    "follow-image-gateway/internal/pipeline"
)

// JobSubmitter wraps pipeline.TrySubmit for testing.
type JobSubmitter interface {
    TrySubmit(job *pipeline.ImageJob) error
}

// JobConsumer consumes image processing jobs from Valkey and submits to pipeline.
type JobConsumer struct {
    consumer      *valkey.Consumer
    submitter     JobSubmitter
    storageClient StorageClient // For presigned GET URL
    log           zerolog.Logger
}

// StorageClient interface for fetching image bytes.
type StorageClient interface {
    GetObject(ctx context.Context, key string) ([]byte, error)
}

func NewJobConsumer(
    valkeyClient valkey.Client,
    consumerName string,
    streamKey string,
    groupName string,
    submitter JobSubmitter,
    storageClient StorageClient,
    log zerolog.Logger,
) (*JobConsumer, error) {
    jc := &JobConsumer{
        submitter:     submitter,
        storageClient: storageClient,
        log:           log,
    }

    consumer, err := valkey.NewConsumer(valkeyClient, valkey.ConsumerConfig{
        StreamKey:    streamKey,
        GroupName:    groupName,
        ConsumerName: consumerName,
        Handler:      jc.handleMessage,
        Count:        10,
        BlockTimeout: 5 * time.Second,
    })
    if err != nil {
        return nil, err
    }

    jc.consumer = consumer
    return jc, nil
}

func (j *JobConsumer) Start(ctx context.Context) error {
    return j.consumer.Start(ctx)
}

func (j *JobConsumer) Stop() error {
    return j.consumer.Stop()
}

func (j *JobConsumer) handleMessage(ctx context.Context, msg valkey.StreamMessage) error {
    // Parse message fields (from ADR follow-api-016)
    imageID, ok := msg.Fields["image_id"].(string)
    if !ok {
        return fmt.Errorf("missing image_id")
    }

    storageKey, ok := msg.Fields["storage_key"].(string)
    if !ok {
        return fmt.Errorf("missing storage_key")
    }

    contentType, _ := msg.Fields["content_type"].(string)
    maxSize, _ := msg.Fields["max_size"].(int64)

    // Fetch image bytes from MinIO
    rawBytes, err := j.storageClient.GetObject(ctx, storageKey)
    if err != nil {
        return fmt.Errorf("failed to fetch image: %w", err)
    }

    // Create ImageJob
    job := &pipeline.ImageJob{
        ID:          imageID,
        StorageKey:  storageKey,
        ContentType: contentType,
        RawBytes:    rawBytes,
        Token: &auth.UploadClaims{
            ImageID:     imageID,
            StorageKey:  storageKey,
            ContentType: contentType,
            MaxSize:     maxSize,
        },
    }

    // Submit to pipeline (non-blocking)
    if err := j.submitter.TrySubmit(job); err != nil {
        return fmt.Errorf("pipeline submit failed: %w", err)
    }

    j.log.Info().
        Str("image_id", imageID).
        Str("storage_key", storageKey).
        Msg("Job submitted to pipeline")

    return nil
}
```

**Test Strategy:**
- Use FakeValkeyClient and fake JobSubmitter (hand-written)
- Verify message parsing and pipeline submission
- Test error paths: missing fields, GetObject failure, pipeline full

**Acceptance Criteria:**
- JobConsumer tests pass with fakes
- Messages from Valkey trigger pipeline jobs
- Errors are logged and returned (no ack)

**Story Points:** 2

---

### Task 3.5: Implement ResultProducer

**Objective:** Create a producer that publishes pipeline results to `image:result` stream.

**TDD Order:**
1. Define `messaging.ResultProducer` struct
2. Write tests with FakeValkeyClient (RED)
3. Implement result publishing (GREEN)
4. Wire into App to listen to pipeline result channel
5. Refactor

**Files Created:**
- `/home/yoseforb/pkg/follow/follow-image-gateway/internal/messaging/result_producer.go`
- `/home/yoseforb/pkg/follow/follow-image-gateway/internal/messaging/result_producer_test.go`

**ResultProducer:**
```go
package messaging

import (
    "context"
    "fmt"
    "time"

    "github.com/follow/follow-pkg/valkey"
    "github.com/rs/zerolog"

    "follow-image-gateway/internal/pipeline"
)

// ResultProducer publishes pipeline results to Valkey.
type ResultProducer struct {
    producer   *valkey.Producer
    streamKey  string
    resultCh   <-chan *pipeline.ImageJob
    log        zerolog.Logger
    cancel     context.CancelFunc
}

func NewResultProducer(
    valkeyClient valkey.Client,
    streamKey string,
    resultCh <-chan *pipeline.ImageJob,
    log zerolog.Logger,
) *ResultProducer {
    return &ResultProducer{
        producer:  valkey.NewProducer(valkeyClient),
        streamKey: streamKey,
        resultCh:  resultCh,
        log:       log,
    }
}

func (r *ResultProducer) Start(ctx context.Context) error {
    ctx, cancel := context.WithCancel(ctx)
    r.cancel = cancel

    go r.publishLoop(ctx)
    return nil
}

func (r *ResultProducer) Stop() error {
    if r.cancel != nil {
        r.cancel()
    }
    return nil
}

func (r *ResultProducer) publishLoop(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():
            return
        case job, ok := <-r.resultCh:
            if !ok {
                return // Channel closed
            }

            if err := r.publishResult(ctx, job); err != nil {
                r.log.Error().
                    Err(err).
                    Str("image_id", job.ID).
                    Msg("Failed to publish result")
            }
        }
    }
}

func (r *ResultProducer) publishResult(ctx context.Context, job *pipeline.ImageJob) error {
    // Build result message (per ADR follow-api-016)
    fields := map[string]interface{}{
        "image_id":     job.ID,
        "storage_key":  job.StorageKey,
        "processed_at": time.Now().UTC().Format(time.RFC3339),
    }

    if job.Error != nil {
        // Failure result
        fields["status"] = "failure"
        fields["error_code"] = job.ErrorCode
        fields["error_message"] = job.Error.Error()
    } else {
        // Success result
        fields["status"] = "success"
        fields["sha256_hash"] = job.SHA256
        fields["etag"] = job.ETag
        fields["content_type"] = job.ContentType
        fields["original_width"] = job.OriginalWidth
        fields["original_height"] = job.OriginalHeight
        fields["processed_width"] = job.ProcessedWidth
        fields["processed_height"] = job.ProcessedHeight
        fields["blur_count"] = job.BlurCount
    }

    messageID, err := r.producer.PublishWithTrim(ctx, r.streamKey, 10000, fields)
    if err != nil {
        return fmt.Errorf("XADD failed: %w", err)
    }

    r.log.Info().
        Str("image_id", job.ID).
        Str("status", fields["status"].(string)).
        Str("message_id", messageID).
        Msg("Result published")

    return nil
}
```

**Test Strategy:**
- Use FakeValkeyClient
- Create fake result channel with test jobs
- Verify success and failure results are published correctly
- Check field mapping matches ADR-016

**Acceptance Criteria:**
- ResultProducer tests pass
- Success results include all metadata
- Failure results include error_code and error_message
- Stream is trimmed to prevent unbounded growth

**Story Points:** 2

---

## Part 4: E2E Testing (5 story points)

### Task 4.1: Create E2E Test with Valkey

**Objective:** Write an end-to-end test that publishes a job to `image:process`, verifies gateway processes it, and checks `image:result`.

**TDD Order:**
1. Set up test environment with Valkey in Docker
2. Write test skeleton (RED)
3. Implement test (GREEN)
4. Verify in CI

**Files Created:**
- `/home/yoseforb/pkg/follow/follow-image-gateway/tests/integration/valkey_messaging_test.go`

**Test Structure:**
```go
//go:build integration

package integration

import (
    "context"
    "testing"
    "time"

    "github.com/follow/follow-pkg/valkey"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestValkeyMessaging_EndToEnd(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping integration test")
    }

    ctx := context.Background()

    // Connect to Valkey
    client, err := valkey.NewRedisClient(&valkey.ValkeyConfig{
        Address: "localhost:6379",
    })
    require.NoError(t, err)
    defer client.Close()

    // Create consumer group for image:process
    err = client.XGroupCreateMkStream(ctx, "image:process", "gateway-workers", "0")
    require.NoError(t, err)

    // Upload test image to MinIO
    storageKey := uploadTestImage(t)

    // Publish job to image:process
    producer := valkey.NewProducer(client)
    messageID, err := producer.Publish(ctx, "image:process", map[string]interface{}{
        "image_id":     "test-image-id",
        "storage_key":  storageKey,
        "content_type": "image/jpeg",
        "max_size":     10485760,
        "uploaded_at":  time.Now().Format(time.RFC3339),
    })
    require.NoError(t, err)
    t.Logf("Published job: %s", messageID)

    // Wait for result in image:result stream
    result := waitForResult(t, client, "test-image-id", 30*time.Second)

    // Verify result
    assert.Equal(t, "success", result["status"])
    assert.NotEmpty(t, result["sha256_hash"])
    assert.NotEmpty(t, result["etag"])
    assert.Equal(t, storageKey, result["storage_key"])
}

func waitForResult(t *testing.T, client valkey.Client, imageID string, timeout time.Duration) map[string]interface{} {
    ctx, cancel := context.WithTimeout(context.Background(), timeout)
    defer cancel()

    // Poll image:result stream
    for {
        messages, err := client.XReadGroup(
            ctx,
            "test-consumer-group",
            "test-consumer",
            "image:result",
            ">",
            10,
            1*time.Second,
        )

        if err != nil {
            time.Sleep(100 * time.Millisecond)
            continue
        }

        for _, msg := range messages {
            if msg.Fields["image_id"] == imageID {
                return msg.Fields
            }
        }
    }
}
```

**Acceptance Criteria:**
- Test passes with gateway running locally
- Test passes in Docker mode
- Result message contains all expected fields
- Failure scenarios tested (invalid image, etc.)

**Story Points:** 3

---

### Task 4.2: Add Progress Hash Verification Test

**Objective:** Verify that progress hashes are written during processing and cleaned up after completion.

**Files Created:**
- `/home/yoseforb/pkg/follow/follow-image-gateway/tests/integration/progress_tracking_test.go`

**Test Structure:**
```go
func TestProgressTracking(t *testing.T) {
    // Submit job
    imageID := submitTestJob(t)

    // Connect to Valkey
    client := connectValkey(t)
    defer client.Close()

    tracker := valkey.NewProgressTracker(client, 1*time.Hour)

    // Wait for first progress update
    var progress map[string]string
    require.Eventually(t, func() bool {
        p, err := tracker.GetProgress(context.Background(), imageID)
        if err == nil && len(p) > 0 {
            progress = p
            return true
        }
        return false
    }, 10*time.Second, 100*time.Millisecond)

    // Verify progress fields
    assert.NotEmpty(t, progress["stage"])
    assert.NotEmpty(t, progress["updated_at"])

    // Wait for completion
    waitForResult(t, client, imageID, 30*time.Second)

    // Progress hash should still exist (TTL not expired yet)
    progress, err := tracker.GetProgress(context.Background(), imageID)
    require.NoError(t, err)
    assert.Equal(t, "completed", progress["stage"])
}
```

**Acceptance Criteria:**
- Progress updates appear during processing
- Final progress state is "completed"
- TTL is set correctly

**Story Points:** 2

---

## Part 5: Future Migration Notes (Not Implemented)

### Task 5.1: Document Future Shared Module Migrations

**Objective:** Create a document outlining what COULD move to follow-pkg in the future.

**Files Created:**
- `/home/yoseforb/pkg/follow/follow-pkg/FUTURE_MIGRATIONS.md`

**Content:**
```markdown
# Future Migrations to follow-pkg

This document tracks potential future migrations to the shared module. Do NOT implement these proactively. Wait until duplication becomes painful.

## Candidates for Future Migration

### 1. MinIO Client Wrapper
**When:** When follow-api upgrades to newer MinIO SDK and both services align on version
**Effort:** Medium (3-5 points)
**Files:** `storage/minio_client.go`, `storage/uploader.go`

### 2. Config Base Types
**When:** When both services stabilize config patterns and Viper usage
**Effort:** Small (2-3 points)
**Files:** `config/loader.go`, `config/validator.go`

### 3. Value Objects
**When:** When domain models are shared (image metadata, storage keys, etc.)
**Effort:** Medium (3-5 points)
**Files:** Value objects for image_id, storage_key, etc.

### 4. Auth Interfaces
**When:** When both services need JWT verification (gateway already has it)
**Effort:** Small (2 points)
**Files:** `auth/jwt.go`, `auth/claims.go`

## Migration Checklist

When migrating code:
1. Both services MUST be using identical or near-identical code
2. Copy tests alongside implementation
3. Update both services to use shared version
4. Run full test suites in both services
5. Update CLAUDE.md in both repos
```

**Acceptance Criteria:**
- Document clearly states "do not implement yet"
- Lists candidates with effort estimates
- Includes migration checklist

**Story Points:** 1

---

## Task Summary

| Task | Description | Story Points | Phase |
|------|-------------|--------------|-------|
| 1.1 | Create follow-pkg module scaffold | 1 | Part 1 |
| 1.2 | Copy logger package to shared module | 1 | Part 1 |
| 1.3 | Wire follow-pkg into both services | 2 | Part 1 |
| 2.1 | Define Valkey client interface and config | 2 | Part 2 |
| 2.2 | Implement RedisClient wrapper | 3 | Part 2 |
| 2.3 | Implement Consumer (XREADGROUP loop) | 3 | Part 2 |
| 2.4 | Implement Producer (XADD wrapper) | 1 | Part 2 |
| 2.5 | Implement Progress Tracker | 2 | Part 2 |
| 2.6 | Implement Orphan Reclaimer | 2 | Part 2 |
| 2.7 | Implement Health Check | 1 | Part 2 |
| 3.1 | Add Valkey config to gateway | 1 | Part 3 |
| 3.2 | Add Valkey to Docker Compose | 1 | Part 3 |
| 3.3 | Wire Valkey client into App lifecycle | 2 | Part 3 |
| 3.4 | Implement JobConsumer | 2 | Part 3 |
| 3.5 | Implement ResultProducer | 2 | Part 3 |
| 4.1 | Create E2E test with Valkey | 3 | Part 4 |
| 4.2 | Add progress hash verification test | 2 | Part 4 |
| 5.1 | Document future migrations | 1 | Part 5 |

**Total:** 34 story points

---

## Implementation Order

Execute tasks sequentially by ID (1.1 → 1.2 → 1.3 → 2.1 → ...). Each task is a discrete unit of work with clear acceptance criteria.

**Critical Path:**
1. Bootstrap shared module (1.1-1.3) — BLOCKS all other work
2. Valkey package design (2.1-2.7) — BLOCKS gateway integration
3. Gateway integration (3.1-3.5) — BLOCKS E2E tests
4. E2E tests (4.1-4.2) — Verifies the entire flow

**Parallelization Opportunities:**
- After 2.1 completes, tasks 2.2-2.7 can be worked on in parallel by different developers
- After 3.3 completes, tasks 3.4 and 3.5 can be worked on in parallel

---

## Testing Strategy

### Unit Tests (TDD)
- All Valkey package code: Use hand-written FakeValkeyClient
- Gateway messaging code: Use FakeValkeyClient + fake pipeline
- Classical testing (ADR-009): Verify outcomes, not interactions
- Table-driven tests with t.Parallel()

### Integration Tests
- E2E messaging flow: Real Valkey in Docker
- Dual-mode support (ADR-010): Local and Docker modes
- Use `//go:build integration` tag

### Quality Gates (Run After Every Task)
```bash
gofumpt -w . && golines -w --max-len=80 .
go vet ./...
./custom-gcl run -c .golangci-custom.yml ./... --fix
go test -race -cover ./...
go mod tidy
```

---

## Docker Compose Strategy

### Current State (Duplicated Infrastructure)
- `follow-api/docker-compose.yml` — PostgreSQL, MinIO
- `follow-image-gateway/docker-compose.yml` — MinIO, gateway

Both services define their own MinIO. When running the full stack, this causes port conflicts and wasted resources.

### Phase 1 (Now): Parent docker-compose.yml
Create a single `follow/docker-compose.yml` at the parent directory that orchestrates everything:

```yaml
# /home/yoseforb/pkg/follow/docker-compose.yml
services:
  # Infrastructure
  postgres:
    image: postgres:17
    ports: ["5432:5432"]
    environment:
      POSTGRES_DB: follow
      POSTGRES_USER: follow
      POSTGRES_PASSWORD: follow
    volumes: [postgres-data:/var/lib/postgresql/data]
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U follow"]
      interval: 5s
      timeout: 3s
      retries: 5

  valkey:
    image: valkey/valkey:8
    ports: ["6379:6379"]
    volumes: [valkey-data:/data]
    command: valkey-server --appendonly yes
    healthcheck:
      test: ["CMD", "valkey-cli", "ping"]
      interval: 5s
      timeout: 3s
      retries: 5

  minio:
    image: minio/minio:latest
    ports: ["9010:9000", "9011:9001"]
    environment:
      MINIO_ROOT_USER: minioadmin
      MINIO_ROOT_PASSWORD: minioadmin
    volumes: [minio-data:/data]
    command: server /data --console-address ":9001"
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:9000/minio/health/live"]
      interval: 5s
      timeout: 3s
      retries: 5

  createbuckets:
    image: minio/mc:latest
    depends_on:
      minio: { condition: service_healthy }
    entrypoint: >
      /bin/sh -c "
      mc alias set myminio http://minio:9000 minioadmin minioadmin;
      mc mb --ignore-existing myminio/follow-images;
      mc version enable myminio/follow-images;
      "

  # Services
  follow-api:
    build:
      context: ./follow-api
      dockerfile: Dockerfile
    ports: ["8080:8080"]
    depends_on:
      postgres: { condition: service_healthy }
      valkey: { condition: service_healthy }
      minio: { condition: service_healthy }
    environment:
      - DATABASE_URL=postgres://follow:follow@postgres:5432/follow?sslmode=disable
      - VALKEY_ADDRESS=valkey:6379
      - MINIO_ENDPOINT=minio:9000

  follow-image-gateway:
    build:
      context: ./follow-image-gateway
      dockerfile: Dockerfile
    ports: ["8090:8090"]
    depends_on:
      valkey: { condition: service_healthy }
      minio: { condition: service_healthy }
    environment:
      - VALKEY_ADDRESS=valkey:6379
      - MINIO_ENDPOINT=minio:9000
      - MINIO_ACCESS_KEY_ID=minioadmin
      - MINIO_SECRET_ACCESS_KEY=minioadmin

volumes:
  postgres-data:
  valkey-data:
  minio-data:
```

**Key benefits:**
- Single MinIO instance shared between services (no port conflicts)
- Single Valkey instance (required for inter-service messaging)
- One command to start everything: `cd follow && docker compose up`
- Each service keeps its own Dockerfile (knows how to build itself)

**Per-service compose files remain** for isolated development:
- `follow-api/docker-compose.yml` — standalone API dev (PostgreSQL + MinIO only)
- `follow-image-gateway/docker-compose.yml` — standalone gateway dev (MinIO only, Valkey added in Phase 5)

### Phase 2 (Future): Dedicated follow-deploy/ repo
When the platform grows (more services, staging environments, CI/CD pipelines), migrate the parent compose to a dedicated `follow-deploy/` repository with environment-specific configs.

---

## Risk Factors

1. **valkey-go client maturity:** Mitigated by ADR-012 — native Valkey client with full protocol support
2. **Consumer group coordination:** Both services must use different group names to avoid competing
3. **Message format drift:** ADR-016 specifies schema, but both services must stay in sync
4. **XCLAIM complexity:** Simplified reclaimer may need enhancement for production scale
5. **Progress hash TTL:** 1 hour may be too short for slow uploads — configurable

---

## Success Criteria

### Functional
- Gateway sets `image:upload:{id}` NX guard on upload
- Gateway publishes results to `image:result`
- Progress hashes are written during processing
- E2E test passes: job in → result out

### Non-Functional
- No goroutine leaks on shutdown
- Graceful degradation if Valkey unavailable
- Memory usage stable (no leaks from unclaimed messages)
- follow-pkg compiles independently
- Both services pass full test suites

### Quality
- All quality gates pass
- Test coverage >80% for new code
- Classical testing patterns followed (no mock frameworks except external services)
- Documentation updated (CLAUDE.md, ADRs)

---

## References

- **ADR-012:** Valkey over Redis (`ai-docs/adr/012-valkey-over-redis.md`)
- **ADR follow-api-016:** Redis Streams messaging (`ai-docs/adr/follow-api-016-redis-streams-inter-service-communication.md`)
- **ADR-009:** Classical testing (`ai-docs/adr/009-classical-testing-over-mockist.md`)
- **ADR-010:** Dual-mode integration tests (`ai-docs/adr/010-dual-mode-integration-tests.md`)
- **Pipeline API:** `ai-docs/architecture/pipeline-api-reference.md`
