# Follow Platform — Cross-Repo Documentation

This directory contains documentation that spans **two or more repositories** in the Follow platform. It is the single source of truth for system-level architecture, cross-repo ADRs, and multi-repo planning documents.

## Organizational Rule

> **If a document mentions 2+ repos, it belongs here. If it's about one repo's internals, it stays in that repo.**

This prevents documentation drift caused by maintaining duplicate copies across repos.

## Directory Structure

```
ai-docs/
├── architecture/       # System-level architecture spanning 2+ repos
├── planning/
│   ├── active/         # Cross-repo plans currently in progress
│   ├── backlog/        # Cross-repo plans not yet started
│   └── completed/      # Finished cross-repo plans
├── adr/                # ADRs that affect 2+ repos
└── research/           # Cross-repo research
```

## Co-Location Pattern

The Follow platform uses a **`.gitignore` co-location pattern**: all 5 repos (`follow-api`, `follow-image-gateway`, `follow-app`, `follow-pkg`, `follow-business`) live as sibling directories under `follow/`, but each is an independent git repository. The `follow/` coordination repo's `.gitignore` excludes the sub-repos.

This means:
- Each repo is cloned independently and has its own git history
- The `follow/` repo tracks only coordination files (this `ai-docs/`, `CLAUDE.md`, `docker-compose.yml`)
- Relative paths from sub-repos to this directory use `../../ai-docs/` (e.g., from `follow-image-gateway/ai-docs/`)

## What Lives Here

### Architecture (`architecture/`)
- **image-gateway-architecture.md** — Complete system integration spec for the image upload/processing flow across follow-api, follow-image-gateway, MinIO, and Valkey

### ADRs (`adr/`)
Cross-repo Architecture Decision Records:
- **012-valkey-over-redis.md** — Decision to use Valkey instead of Redis (affects all Go services)
- **follow-api-015-introduce-image-gateway.md** — Introduction of separate image processing microservice
- **follow-api-016-redis-streams-inter-service-communication.md** — Redis Streams for api ↔ gateway messaging
- **follow-api-017-ed25519-asymmetric-signing.md** — Ed25519 JWT for api → gateway auth
- **follow-api-022-domain-agnostic-processing.md** — Gateway processing model agreed with api

### Planning (`planning/`)
- **active/cross-repo-valkey-integration-master-plan.md** — Shared module bootstrap + Valkey messaging (spans follow-pkg, gateway, api)
- **completed/image-gateway-architecture-plan.md** — Original gateway architecture plan (phases 1-4 done)

## Cross-References

Each sub-repo that previously hosted these documents now contains a pointer back to this directory. See:
- `follow-image-gateway/ai-docs/README.md` → "Cross-Repo Documentation" section
- `follow-api/ai-docs/README.md` → "Cross-Repo Documentation" section
- `follow-pkg/ai-docs/README.md` → "Cross-Repo Documentation" section
