# CLAUDE.md - Follow Platform Coordination Hub

This file provides guidance to Claude Code when working across the Follow platform repositories. This is a coordination repo used for cross-repo research, architecture decisions, planning, and multi-repo implementation work.

## System Overview

**Follow** is a visual navigation solution that works without GPS. Users navigate by following a sequence of images with visual markers -- like a trail of breadcrumbs made of photos. Each image shows what the user should see at their current location, with a marker pointing to where they should walk next.

**Core Value Proposition:** Reliable, simple image-based navigation that works everywhere -- especially in GPS-dead zones like underground parking, building interiors, complex campuses, and dense urban areas.

The platform consists of 5 repositories that together form the complete system:

| Repository | Purpose | Language | Port |
|------------|---------|----------|------|
| **follow-api** | Backend API server -- route/user domains, PostgreSQL, event bus | Go | 8080 |
| **follow-image-gateway** | Stateless image processing microservice -- validation, ML detection, MinIO upload | Go | 8090 |
| **follow-app** | Cross-platform mobile application -- route creation, navigation, management | Flutter/Dart | - |
| **follow-pkg** | Shared Go utilities used by both Go services | Go | - |
| **follow-business** | Business research, market analysis, partnership strategy | Markdown | - |

## Repository Locations

```
/home/yoseforb/pkg/follow/                  # This coordination repo
/home/yoseforb/pkg/follow/follow-api/       # Backend API server
/home/yoseforb/pkg/follow/follow-image-gateway/  # Image processing gateway
/home/yoseforb/pkg/follow/follow-app/       # Flutter mobile app
/home/yoseforb/pkg/follow/follow-pkg/       # Shared Go utilities
/home/yoseforb/pkg/follow/follow-business/  # Business research context
```

**Co-location pattern**: Each sub-repo is an independent git repository. The `.gitignore` in this coordination repo excludes all sub-repos so they don't appear as untracked files. Cross-repo documentation lives in `ai-docs/` here; repo-internal documentation stays within each repo.

**Full-stack development**: `docker-compose.yml` in this repo orchestrates the active services (PostgreSQL, MinIO, follow-api) for integrated local development. Valkey and follow-image-gateway will be added once gateway integration is complete. Per-repo compose files still work for isolated single-service development.

## Cross-Repo Architecture

### Current Architecture (Pre-Gateway)

Currently, follow-api is the **only backend service**. Image uploads go directly to MinIO via presigned URLs -- there is no image gateway, no Valkey messaging, and no image processing pipeline. Everything is synchronous.

#### Data Flow: Route Creation (Current)

```
follow-app (Flutter)
  |
  | POST /api/v1/users/anonymous → JWT token
  |
  | POST /api/v1/routes/prepare → route_id
  |
  | POST /api/v1/routes/{route_id}/create-waypoints
  |   (route metadata + waypoints with image_metadata, markers, descriptions)
  v
follow-api (Go, port 8080)
  |
  | Creates route, waypoints, image records (all PENDING)
  | Generates presigned upload URLs (direct to MinIO)
  | Returns: waypoint_ids[], presigned_urls[]
  v
follow-app
  |
  | PUT {presigned_url} (raw binary image data → direct to MinIO)
  |   (repeated for each waypoint image)
  v
MinIO/S3 (object storage)
  |
follow-app
  |
  | POST /api/v1/routes/{route_id}/confirm-waypoints
  v
follow-api
  |
  | Validates uploads, transitions route PENDING → ACTIVE
  v
Route is live and navigable
```

#### Data Flow: Route Navigation (Current)

```
follow-app
  |
  | GET /api/v1/routes/{route_id}?include_images=true
  v
follow-api
  |
  | Returns route with waypoints and presigned download URLs (from MinIO)
  v
follow-app
  |
  | Downloads images via presigned URLs, caches locally
  | User follows waypoints sequentially: image → marker → walk → next image
  v
Offline navigation (no further server contact needed)
```

#### Data Flow: Image Replacement (Current)

```
follow-app
  |
  | POST /api/v1/routes/{route_id}/waypoints/{waypoint_id}/replace-image/prepare
  |   (file_name, file_size_bytes, content_type → validated: 1KB-10MB, image/*)
  v
follow-api → returns image_id + presigned upload_url
  |
follow-app
  |
  | PUT {presigned_url} (raw binary → direct to MinIO)
  |
  | POST /api/v1/routes/{route_id}/waypoints/{waypoint_id}/replace-image/confirm
  |   (image_id, file_hash, marker coordinates)
  v
follow-api → swaps old image for new, updates marker coordinates
```

#### Current Service Communication Map

| From | To | Protocol | Purpose |
|------|----|----------|---------|
| follow-app | follow-api | REST HTTP (port 8080) | All route/user API operations |
| follow-app | MinIO | HTTP PUT (presigned URL) | Direct image upload (no gateway) |
| follow-app | MinIO | HTTP GET (presigned URL) | Direct image download for navigation |
| follow-api | MinIO | S3 API (PresignedPut, PresignedGet) | Generate signed URLs for client |
| follow-api | PostgreSQL | SQL | Route, User, Image entity persistence |
| follow-api, follow-pkg | Go module import | - | Shared logger utility |

### Future Architecture (With Image Gateway + Valkey)

The follow-image-gateway and Valkey integration is **planned but not yet wired**. The gateway repo has its pipeline implemented (Phases 1-4), but inter-service communication (Phase 5) is not yet connected. When complete, the architecture will change to:

- **Upload path**: Client → follow-image-gateway (via Ed25519 JWT) → processes image → MinIO + Valkey result
- **Status path**: Client ← SSE from follow-api ← Valkey status updates from gateway
- **Key additions**: Ed25519 asymmetric JWT signing, Valkey Redis Streams messaging, ML face/plate detection, image processing pipeline, real-time SSE status streaming

See `ai-docs/architecture/image-gateway-architecture.md` for the full future architecture design.

## Tech Stack Summary

### Backend (Go Services)

**Currently active (follow-api + follow-pkg):**

| Technology | Used In | Purpose |
|------------|---------|---------|
| Go (Golang) | follow-api, follow-pkg | Server language |
| Goa-Design | follow-api | HTTP API framework / DSL |
| PostgreSQL | follow-api | Primary database (domain-separated schemas) |
| MinIO/S3 | follow-api (presigned URLs) | Object storage for images |
| Watermill | follow-api | In-memory event bus (GoChannel) |
| zerolog | follow-api, follow-pkg | Structured logging |
| Viper | follow-api | Configuration management |
| golang-jwt/jwt/v5 | follow-api | JWT authentication (symmetric) |
| gofumpt, golines | All Go repos | Code formatting |
| golangci-lint + NilAway | All Go repos | Linting with nil panic detection |

**Planned (follow-image-gateway integration -- not yet wired):**

| Technology | Used In | Purpose |
|------------|---------|---------|
| Goa-Design | follow-image-gateway | HTTP API framework / DSL |
| Valkey (Redis-compatible) | follow-api, follow-image-gateway | Messaging via Redis Streams |
| vipsgen | follow-image-gateway | Image processing (4-8x faster than stdlib) |
| onnxruntime_go | follow-image-gateway | ML inference (face/plate detection) |
| Ed25519 JWT | follow-api (sign), follow-image-gateway (verify) | Upload token authentication |

### Frontend (Flutter)

| Technology | Purpose |
|------------|---------|
| Flutter/Dart | Cross-platform mobile framework |
| Provider + ChangeNotifier | State management (MVVM) |
| go_router | Declarative navigation |
| Hive | Local structured storage |
| FlutterSecureStorage | Secure credential storage |
| SharedPreferences | Simple key-value storage |
| CachedNetworkImage | Image caching |

### Infrastructure

| Technology | Purpose | Status |
|------------|---------|--------|
| Docker / Docker Compose | Local development orchestration | Active |
| PostgreSQL | Relational database | Active |
| MinIO | S3-compatible object storage (local dev) | Active |
| Valkey | Redis-compatible messaging (BSD-3-Clause) | Planned (gateway integration) |

## Architecture Patterns by Repo

| Repo | Architecture | Testing Philosophy |
|------|-------------|-------------------|
| follow-api | Modular-monolithic, DDD, Clean/Hexagonal Architecture | TDD, three-tier (domain/module/API), classical testing |
| follow-image-gateway | Pipes and Filters (4-stage pipeline) | TDD, classical/Detroit (fakes not mocks), dual-mode integration |
| follow-app | MVVM with Provider, Repository pattern | Unit (ViewModels) -> Widget -> Integration |
| follow-pkg | Utility library | Classical/Detroit |

### Shared Go Patterns (follow-api, follow-image-gateway, follow-pkg)

- **Error handling**: Rich domain errors with `errors.Is()` / `fmt.Errorf("%w: detail", Err)`. Errors defined in each package's `errors.go`. Infrastructure errors mapped to domain errors at repository boundary.
- **Testing**: Classical/Detroit style -- hand-written fakes (NOT mock frameworks), verify outcomes not interactions, table-driven tests, `t.Parallel()`.
- **Code formatting**: `gofumpt -w .` and `golines -w --max-len=80 .`
- **Linting**: `./custom-gcl run -c .golangci-custom.yml ./...` (includes NilAway plugin)
- **Module communication**: Generic Command/Query interface pattern
- **Commit format**: `type(scope): description` with imperative mood

### Flutter Patterns (follow-app)

- **Navigation**: ALWAYS use `context.go()` / `context.push()` -- NEVER `Navigator.pop()` (causes crashes with go_router)
- **Internationalization**: ALL user-facing strings must be localized (English + Hebrew). Never hardcode UI strings.
- **RTL support**: ALL layouts must support RTL. Use `EdgeInsetsDirectional`, `PositionedDirectional`, `AlignmentDirectional` instead of LTR-only equivalents.
- **Error handling**: Simple try-catch in ViewModels, not functional programming patterns (no Either<Failure, Success>)

## Common Commands

### follow-api
```bash
# Run server
go run ./cmd/server
go run ./cmd/server -port 3000 -log-level debug -runtime-timeout 10s

# Quality gates (mandatory after every change)
gofumpt -w . && golines -w --max-len=80 .
go vet ./... && golangci-lint run -c .golangci.yml ./... --fix
go test -race -cover ./...
go mod tidy
go run ./cmd/server -runtime-timeout 10s  # Verify startup

# Integration test
./scripts/test-image-workflow.sh -t 10s

# Documentation generation
./docs/export-all-docs.sh
```

### follow-image-gateway
```bash
# Run server
go run ./cmd/server
go run ./cmd/server -port 8090 -log-level debug -runtime-timeout 15s

# Docker (gateway + MinIO)
docker compose up -d

# Quality gates (mandatory after every change)
gofumpt -w . && golines -w --max-len=80 .
go vet ./... && golangci-lint run -c .golangci.yml ./... --fix
go test -race -cover ./...
go mod tidy
go run ./cmd/server -runtime-timeout 10s  # Verify startup

# Integration tests (local mode)
go run ./cmd/server -runtime-timeout 15s -port 8099
GATEWAY_URL=http://localhost:8099 INTEGRATION_TEST_MODE=local go test -tags=integration -v ./tests/integration/
```

### follow-app
```bash
# Run app
flutter run --debug
flutter run --debug --web-port 3000  # Web development

# Quality gates (mandatory after every change)
dart format .
dart analyze                    # MUST return "No errors"
dart fix --apply
flutter test --coverage         # >80% coverage required

# Build
flutter build apk --debug      # Android (primary)
flutter build web --debug       # Web mobile
flutter build ios --debug --no-codesign  # iOS

# Localization
flutter gen-l10n

# Testing
flutter test
flutter test test/widget_test/
flutter test integration_test/
```

### follow-pkg
```bash
# Quality gates (mandatory after every change)
gofumpt -w . && golines -w --max-len=80 .
go vet ./... && golangci-lint run -c .golangci.yml ./... --fix
go test -race -cover ./...
go mod tidy
```

### tests/integration (cross-repo integration tests)
```bash
cd tests/integration/

# Quality gates (mandatory after every change)
gofumpt -w . && golines -w --max-len=80 .
golangci-lint run --build-tags integration -c .golangci.yml ./...
go mod tidy
```

## Cross-Repo Development Workflows

### When Changing an API Endpoint

1. **follow-api**: Modify the Goa DSL design, regenerate, implement handler/usecase changes
2. **follow-api**: Run quality gates, update integration tests
3. **follow-app**: Update the corresponding repository/service layer to match new API contract
4. **follow-app**: Update ViewModel if the data model changed
5. **follow-app**: Run quality gates (`dart analyze`, `flutter test`)

### When Changing Image Handling

Currently, image upload/download uses presigned URLs directly between the client and MinIO. There is no image processing pipeline in the flow yet.

1. **follow-api**: Modify presigned URL generation, image entity lifecycle, or validation logic
2. **follow-api**: Run quality gates and integration tests
3. **follow-app**: Update if presigned URL handling, upload flow, or image format changed

**Future (after gateway integration):** Changes to image processing will involve follow-image-gateway pipeline stages, Valkey message contracts (image:result format), and Ed25519 JWT token changes across both Go services.

### When Changing Authentication / JWT

1. **follow-api**: Modify JWT signing/verification logic (currently symmetric JWT)
2. **follow-app**: Update token handling if auth flow changed

**Future (after gateway integration):** JWT changes will also require updating follow-image-gateway's Ed25519 public key verification to match follow-api's signing.

### When Adding Shared Go Code

1. Verify the code is genuinely needed by BOTH Go services (follow-api AND follow-image-gateway)
2. Verify it is identical or nearly identical between them
3. Verify it is justified by actual duplication pain -- do NOT proactively migrate code "because it could be shared"
4. Add to **follow-pkg**
5. Update `go.mod` in both consuming repos
6. Run quality gates in all three repos

### When Changing Data Models

1. **follow-api**: Update domain entities, repository layer, database migrations
2. **follow-api**: Update API response DTOs if the change is user-facing
3. **follow-app**: Update `data/models/` (JSON serialization), `domain/` (business models), and ViewModels
4. Run quality gates in both repos

## Development Status Summary

| Repo | Status | Current Focus |
|------|--------|---------------|
| follow-api | **MVP functional** -- full route lifecycle, user auth, image upload/download via presigned URLs | Route and User domain MVP (6-phase plan) |
| follow-image-gateway | Pipeline built (Phases 1-4), **not yet wired to follow-api** | Phase 5: Valkey Streams messaging + inter-service integration |
| follow-app | Planning complete, implementation starting | MVVM foundation with Provider (5-phase plan) |
| follow-pkg | Active | Shared logger package |
| follow-business | MVP complete, market research phase | Finding pilot customer/partner |

### MVP Scope

- **Active domains**: Route, User (Analytics, Image Processing, Payment postponed)
- **Navigation model**: Trust-based sequential waypoint following ("look and go")
- **Authentication**: Anonymous tokens progressing to user registration
- **Platforms**: Android (primary), iOS (future-ready), Web Mobile (essential features)
- **Storage**: Local MinIO for MVP, cloud storage (S3/GCS) for production
- **Languages**: English + Hebrew (full RTL support)

## Planning and ADR Guidelines

### Architecture Decision Records

ADRs are maintained at two levels:

- **Repo-internal ADRs** (decisions affecting one repo's internals):
  - **follow-api**: `docs/adr/` -- 14 ADRs (Go, DDD, Clean Architecture, event-driven, sagas, testing)
  - **follow-image-gateway**: `ai-docs/adr/` -- pipeline-internal ADRs (Pipes&Filters, vipsgen, ONNX, ML detection, testing, Goa)

- **Cross-repo ADRs** (decisions affecting 2+ repos) live in this coordination repo:
  - `ai-docs/adr/012-valkey-over-redis.md` -- Valkey server choice (all Go services)
  - `ai-docs/adr/follow-api-015-introduce-image-gateway.md` -- gateway introduction (api + gateway)
  - `ai-docs/adr/follow-api-016-redis-streams-inter-service-communication.md` -- Redis Streams messaging (api + gateway)
  - `ai-docs/adr/follow-api-017-ed25519-asymmetric-signing.md` -- Ed25519 JWT auth (api + gateway)
  - `ai-docs/adr/follow-api-022-domain-agnostic-processing.md` -- domain-agnostic pipeline (api + gateway)

### Creating Cross-Repo ADRs

When a decision affects multiple repos:

1. Create the primary ADR in the most affected repo
2. Add cross-references in other affected repos (e.g., `follow-api-015` referenced from gateway)
3. Document in this coordination repo's `ai-docs/architecture/` if it is a system-level decision
4. Include impact analysis for each affected repo

### Planning Documents

- **follow-api**: `ai-docs/planning/` and `DEVELOPMENT_PLAN.md` (6-phase route/user implementation)
- **follow-image-gateway**: `ai-docs/planning/` (active, backlog, completed phases)
- **follow-app**: `ai-docs/planning/active/flutter-mvv-implementation-plan.md` (5-phase MVVM plan)

### Creating Cross-Repo Plans

When planning work that spans multiple repos:

1. Identify all affected repos and the order of changes
2. Document the dependency chain (which repo changes must land first)
3. Plan integration testing touchpoints
4. Consider backward compatibility during the transition
5. Store cross-repo plans in this coordination repo's `ai-docs/planning/`

## Implementation Guidelines for Cross-Repo Changes

### Order of Implementation

For most cross-repo changes, follow this order:

1. **follow-pkg** (if shared utilities are affected) -- changes here affect both Go services
2. **follow-api** (backend changes) -- API contract is the source of truth
3. **follow-image-gateway** (if image processing or messaging is affected)
4. **follow-app** (frontend adapts to backend changes)

### Coordination Checklist

- [ ] Identify all repos affected by the change
- [ ] Verify API contract compatibility between follow-api and follow-app
- [ ] Run quality gates in every affected repo
- [ ] Test end-to-end flow if the change touches the data path

**Additional checks after gateway integration:**
- [ ] Verify JWT/auth contract compatibility between follow-api and follow-image-gateway
- [ ] Verify Valkey message format compatibility between follow-api and follow-image-gateway

### Quality Gate Summary (All Repos)

Every repo enforces **zero tolerance** -- all quality gates must pass before committing:

| Repo | Format | Lint | Test | Verify |
|------|--------|------|------|--------|
| follow-api | `gofumpt -w . && golines -w --max-len=80 .` | `go vet ./... && ./custom-gcl run -c .golangci-custom.yml ./...` | `go test -race -cover ./...` | `go run ./cmd/server -runtime-timeout 10s` |
| follow-image-gateway | `gofumpt -w . && golines -w --max-len=80 .` | `go vet ./... && ./custom-gcl run -c .golangci-custom.yml ./... --fix` | `go test -race -cover ./...` | `go run ./cmd/server -runtime-timeout 10s` |
| follow-app | `dart format .` | `dart analyze` (must show "No errors") | `flutter test --coverage` | `flutter build apk --debug` |
| follow-pkg | `gofumpt -w . && golines -w --max-len=80 .` | `go vet ./... && ./custom-gcl run -c .golangci-custom.yml ./... --fix` | `go test -race -cover ./...` | - |
| tests/integration | `gofumpt -w . && golines -w --max-len=80 .` | `./custom-gcl run --build-tags integration -c .golangci-custom.yml ./...` | - | - |

### Commit Message Convention (All Repos)

**Format**: `type(scope): description`

**Types**: `feat`, `fix`, `refactor`, `docs`, `test`, `style`, `improve`

Use imperative mood ("add", "fix", "update" -- not "added", "fixed", "updated"). Include scope in parentheses when relevant.

## Key Documentation Locations

| Topic | Location |
|-------|----------|
| API ADRs (system architecture) | `/home/yoseforb/pkg/follow/follow-api/docs/adr/` |
| Gateway ADRs (pipeline-internal) | `/home/yoseforb/pkg/follow/follow-image-gateway/ai-docs/adr/` |
| Cross-repo ADRs (012, 015-017, 022) | `/home/yoseforb/pkg/follow/ai-docs/adr/` |
| Cross-repo architecture | `/home/yoseforb/pkg/follow/ai-docs/architecture/` |
| Cross-repo plans (active/backlog/completed) | `/home/yoseforb/pkg/follow/ai-docs/planning/` |
| API entity models and business rules | `/home/yoseforb/pkg/follow/follow-api/docs/entities/` |
| C4 architecture diagrams | `/home/yoseforb/pkg/follow/follow-api/docs/c4/` |
| API development plan | `/home/yoseforb/pkg/follow/follow-api/DEVELOPMENT_PLAN.md` |
| Flutter MVVM architecture | `/home/yoseforb/pkg/follow/follow-app/ai-docs/architecture/` |
| Flutter implementation plan | `/home/yoseforb/pkg/follow/follow-app/ai-docs/planning/active/` |
| Flutter UX design system | `/home/yoseforb/pkg/follow/follow-app/ai-docs/ux-design/` |
| Flutter API integration guide | `/home/yoseforb/pkg/follow/follow-app/ai-docs/api-integration/` |
| Gateway pipeline API reference | `/home/yoseforb/pkg/follow/follow-image-gateway/ai-docs/architecture/pipeline-api-reference.md` |
| API error architecture guide | `/home/yoseforb/pkg/follow/follow-api/ai-docs/architecture/error-architecture/` |
| API TDD workflow | `/home/yoseforb/pkg/follow/follow-api/ai-docs/workflows/tdd-red-green-refactor-workflow.md` |
| Full-stack Docker Compose | `/home/yoseforb/pkg/follow/docker-compose.yml` |
| Business research context | `/home/yoseforb/pkg/follow/follow-business/CLAUDE.md` |

## Business Context

Follow's MVP is complete and the project is in market validation phase -- seeking pilot customers and partnership opportunities. The technical foundation is solid and designed for rapid adaptation to specific partner needs.

**Target market path**: B2B partnerships that reach B2C end users. The route creator (business) pays, not the navigating end user.

**Key verticals under exploration**: Airbnb/vacation rental hosts, hospitals/medical centers, shopping malls, universities, airports, event venues, coworking spaces.

**Critical constraint**: Follow is NOT building infrastructure. No beacons, sensors, or hardware. Someone takes photos once, everyone can navigate forever.

For detailed business research context, see `/home/yoseforb/pkg/follow/follow-business/CLAUDE.md`.
