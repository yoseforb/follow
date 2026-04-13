# Hetzner MVP Deployment Plan

**Status**: Active
**Scope**: Cross-repo — affects root `docker-compose.yml`, `follow-api` (CORS), `follow-app` (API endpoints), new infra files (`Caddyfile`, `scripts/backup.sh`), Hetzner host setup
**Goal**: Deploy the full Follow platform (follow-api, follow-image-gateway, postgres, valkey, minio) to a single managed Hetzner server, behind Caddy with TLS, with R2-backed nightly backups, ready for the first pilot customer. Drop fly.io. Keep Flutter web on Cloudflare Pages.

---

## Context

The platform is MVP+ and a pilot customer is imminent. Today:

- `follow-api` runs on fly.io (to be dropped).
- Flutter web runs on Cloudflare Pages (to be kept).
- A pre-MVP MinIO bucket lives on Cloudflare R2 (to be repurposed: a separate R2 bucket becomes the backup target for the new self-hosted MinIO and PostgreSQL on Hetzner).
- The root `docker-compose.yml` is solid for local development (healthchecks, parameterized via `.env`, crash-resilient integration test harness) but not yet hardened for a real server (no resource limits, no log rotation, no TLS, no backups, no reverse proxy).

The deployment target is **one Hetzner VPS**. Not Kubernetes, not multi-region, not HA. One box, one customer, one source of truth.

### Architecture after migration

```
                                       Cloudflare DNS + Registrar
                                                  |
           +-------------------+-------------------+-----------------------+------------------+
           |                   |                   |                       |                  |
  app.follow.example   api.follow.example   upload.follow.example   download.follow.example
  (CNAME, proxied)     (A, DNS-only)        (A, DNS-only)           (A, DNS-only)
           |                   |                   |                       |
           v                   v                   v                       v
  Cloudflare Pages     +-------+-------------------+-----------------------+--------+
  (Flutter web build)  |   Hetzner VPS (single box)                                 |
                       |                                                            |
                       |   Caddy (TLS termination, ports 80/443)                    |
                       |     |                                                      |
                       |     +--> follow-api:8080           (api.follow.example)    |
                       |     +--> follow-image-gateway:8090 (upload.follow.example) |
                       |     +--> minio:9000                (download.follow.example)|
                       |                                                            |
                       |   postgres, valkey (internal only)                         |
                       |   minio (internal + exposed via Caddy for downloads)       |
                       |   backup sidecar (cron) -> Cloudflare R2                   |
                       +------------------------------------------------------------+
```

**Hostname → service mapping**:

| Hostname | Purpose | Traffic shape |
|----------|---------|---------------|
| `app.follow.example` | Flutter web app | Served by Cloudflare Pages, not the Hetzner box |
| `api.follow.example` | REST API, SSE status streaming | Caddy → `follow-api:8080` |
| `upload.follow.example` | Image uploads from the app | Caddy → `follow-image-gateway:8090` |
| `download.follow.example` | Presigned image downloads | Caddy → `minio:9000` |

The download hostname is required because the Flutter app downloads images from MinIO directly using presigned URLs issued by follow-api. Those URLs must embed a publicly reachable host — `minio:9000` is only reachable inside the Docker network. The `MINIO_EXTERNAL_ENDPOINT` env var controls the host that gets embedded in presigned URLs; on the production box it must point at `download.follow.example` (with `MINIO_USE_SSL=true` so the scheme comes out as `https`).

### Deployment philosophy

- **Single `docker-compose.yml`** with `profiles: [prod]` for prod-only services (Caddy, backup sidecar). No separate `docker-compose.prod.yml`.
- **Resource limits, log rotation, healthcheck `start_period` fixes** apply unconditionally (laptop and Hetzner alike).
- **Secrets via host `.env` file**, `chmod 600`, owned by a dedicated `follow` user. Not Vault, not Docker secrets — overkill for one box.
- **Test the prod profile locally first** before touching Hetzner.
- **Test backup AND restore on the laptop** before customer data ever touches the box.

---

## Planning Order

Tasks are grouped into 6 phases. Within a phase, tasks are mostly sequential. Phases must complete in order — Phase 5 (deploy) cannot start before Phase 4 (provisioning), and Phase 6 (cutover) cannot start before Phase 5 verification passes.

1. **Phase 1**: Domain & DNS prep
2. **Phase 2**: Local docker-compose hardening (test on laptop)
3. **Phase 3**: Application code changes
4. **Phase 4**: Hetzner provisioning
5. **Phase 5**: First deploy & verify
6. **Phase 6**: Cutover & cleanup

**Total tasks: 40.** Realistic effort: **3-4 focused days**, +1 day buffer for cert/CORS/SSE surprises.

---

## Phase 1 — Domain & DNS Prep

### Task 1: Pick a domain name

**Story Points**: 1

**Description**

Decide the production domain. Check `.com` availability first (universally trusted, customer expectation). If taken, fall back to `.app` (Google's TLD, HTTPS-enforced, ~$15/year). Avoid trendy/expensive TLDs (`.io`, `.ai`). Do a quick trademark check. "Follow" is generic — needs a prefix/suffix (`getfollow.com`, `followapp.com`, `usefollow.app`).

**Acceptance Criteria**

- A specific domain string chosen.
- Both the chosen TLD AND `.com` reserved if different (cheap impersonation insurance).
- No active trademark conflict found via a basic search.

---

### Task 2: Register the domain at Cloudflare Registrar

**Story Points**: 1

**Description**

Register through Cloudflare Registrar (sells at wholesale, no markup, no upsells). The user already has a Cloudflare account from Pages. Free WHOIS privacy.

**Acceptance Criteria**

- Domain registered, visible in Cloudflare dashboard.
- Auto-renew enabled.
- WHOIS privacy active.

---

### Task 3: Add DNS records in Cloudflare

**Story Points**: 1

**Description**

Create the five DNS records that map the customer-facing hostnames to the right places.

| Subdomain | Type | Target | Cloudflare Proxy |
|-----------|------|--------|------------------|
| `app.follow.example` | CNAME | Cloudflare Pages target | **Proxied (orange)** — Pages is on CF edge already |
| `api.follow.example` | A | Hetzner public IPv4 | **DNS-only (grey)** — Caddy needs real client IP for HTTP-01 cert challenge |
| `upload.follow.example` | A | Hetzner public IPv4 | **DNS-only (grey)** — same reason; also avoids CF free-plan 100MB request size cap |
| `download.follow.example` | A | Hetzner public IPv4 | **DNS-only (grey)** — same reason; image downloads bypass CF to avoid free-plan bandwidth caps and ensure presigned URL signatures stay valid (signature includes host) |
| apex `follow.example` | redirect / CNAME | `app.follow.example` | Proxied |

The Hetzner IP may not exist yet at this point — that is fine, A records can be created with a placeholder and updated in Phase 4.

**Acceptance Criteria**

- All five records exist.
- Proxy state per the table.
- DNS propagation verified with `dig api.follow.example`, `dig upload.follow.example`, and `dig download.follow.example` from outside the local network.

---

### Task 4: Add the custom domain to Cloudflare Pages

**Story Points**: 1

**Description**

Cloudflare Pages requires a two-sided handshake: the DNS record points at Pages, AND the Pages project must list the custom domain in its dashboard. Without the second step, Pages will reject requests with the wrong host header.

**Acceptance Criteria**

- `app.follow.example` listed in the Pages project's "Custom domains" tab.
- Visiting `https://app.follow.example` serves the existing Flutter web build with a valid TLS cert (Pages issues automatically).

---

## Phase 2 — Local docker-compose hardening

These tasks all edit the root `docker-compose.yml` (and add new files alongside it). Verify everything on the laptop before touching Hetzner. Most are mechanical config edits.

### Task 5: Add log rotation to every service

**Story Points**: 1

**Description**

Default `json-file` logging has no rotation — disk fills up in weeks. Add a `logging:` block to every service with `max-size` and `max-file` so logs cap themselves.

```yaml
logging:
  driver: json-file
  options:
    max-size: "10m"
    max-file: "5"
```

**Files Affected**

- `docker-compose.yml` — add `logging:` block to `postgres`, `valkey`, `minio`, `createbuckets`, `follow-api`, `follow-image-gateway`.

**Acceptance Criteria**

- All services have the `logging:` block.
- `docker compose up -d` works unchanged on laptop.
- `docker inspect <container>` shows the log driver options applied.

---

### Task 6: Add resource limits to every service

**Story Points**: 2

**Description**

A runaway upload or a query gone wrong can OOM the host. Cap memory per service so the kernel kills the offender, not postgres. Limits below are derived from measured benchmarks (`ai-docs/research/gateway-memory-benchmark.md` — 60 routes, ~140 images):

| Service | Measured Peak | Memory limit | CPU limit | Rationale |
|---------|--------------|--------------|-----------|-----------|
| postgres | 40 MiB (idle) | 768m | 1.0 | Room for `shared_buffers` tuning in production |
| valkey | 11 MiB | 128m | 0.5 | Rock solid; `--maxmemory 256mb` already caps data size |
| minio | 220 MiB | 384m | 1.0 | Grows linearly with stored images; ~75% headroom |
| follow-api | 35 MiB | 128m | 1.0 | Trivial even under sustained load; 3.5x headroom |
| follow-image-gateway | 1.3 GiB (with malloc fix) | 1792m | 2.0 | ~35% headroom for sawtooth pattern; **requires malloc_trim fix** |

**Prerequisite**: The gateway's 1.3 GiB peak requires the `cmem/malloc_trim.go` fix (commit `2273fba`). Without it, peak is 3.4 GiB due to glibc malloc fragmentation.

Use `deploy.resources.limits` (compose v3+ syntax — works with `docker compose up -d`).

**Files Affected**

- `docker-compose.yml` — add `deploy.resources.limits` per service.

**Acceptance Criteria**

- Limits visible in `docker stats` after `docker compose up -d`.
- Stack still boots cleanly with all healthchecks passing.
- An intentional pipeline stress test (large image batch) does not OOM the host.

---

### Task 7: Bump `start_period` on follow-api healthcheck

**Story Points**: 1

**Description**

Current `start_period: 5s` is too aggressive — `follow-api` runs migrations on boot, which can take 10-30s on a cold host. Aggressive start_period causes restart loops on slow Hetzner first-boot. Bump to 60s. Also review `follow-image-gateway` start_period (libvips/ONNX init takes a few seconds — 30s is safer).

**Files Affected**

- `docker-compose.yml` — `follow-api.healthcheck.start_period: 60s`, `follow-image-gateway.healthcheck.start_period: 30s`.

**Acceptance Criteria**

- Cold boot `docker compose up -d` reliably reaches healthy state without restart loops.

---

### Task 8: Verify Valkey persistence and reformat command block

**Story Points**: 1

**Description**

Valkey holds two pieces of state that matter to the upload pipeline:

- `image:status:{id}` hashes — current stage of each in-flight image (queued / validating / analyzing / transforming / uploading).
- `image:result` Redis Stream — the messages the gateway publishes and `follow-api` consumes to mark images as processed.

If these are lost on a Valkey restart, any in-flight image is orphaned: the client's SSE stream never gets a terminal event, `follow-api` never sees the result, and the route stays stuck in `PENDING` forever.

**Current state (verified)**: `docker-compose.yml` already runs Valkey with `command: valkey-server --appendonly yes --maxmemory 256mb --maxmemory-policy noeviction` and maps the `valkey_data:/data` volume. AOF persistence is already on, `appendfsync` defaults to `everysec` when AOF is enabled, and memory is capped. This task is therefore a **verify-and-polish** pass, not a new feature enablement — and the reason it stays in the plan is that the kill-restart drill is the only way to *prove* the persistence actually survives a restart with real pipeline traffic.

**Action**:

1. Reformat the `command:` line from a single string into a YAML array (easier to diff and add flags to later):
   ```yaml
   valkey:
     image: valkey/valkey:8
     command:
       - valkey-server
       - --appendonly
       - "yes"
       - --appendfsync
       - everysec        # explicit, even though this is the default
       - --maxmemory
       - 256mb
       - --maxmemory-policy
       - noeviction
   ```
2. Run the kill-restart drill (acceptance criterion #3) to confirm the existing config actually does what we think it does. Do NOT skip this step even though the config looks correct — the whole point is to catch the "oh, turns out it wasn't" scenario before the pilot customer does.

`maxmemory-policy: noeviction` is deliberate — if Valkey runs out of memory, new writes fail loudly (visible in logs and healthchecks). Silent eviction of pipeline state would be worse than an outage.

**Files Affected**

- `docker-compose.yml` — reformat `valkey.command` to array form, optionally add explicit `--appendfsync everysec`.

**Acceptance Criteria**

- `docker compose exec valkey valkey-cli CONFIG GET appendonly` returns `yes`.
- `docker compose exec valkey valkey-cli CONFIG GET appendfsync` returns `everysec`.
- `docker compose exec valkey ls /data` shows `appendonlydir/` (AOF file layout).
- **Kill-restart drill**: trigger an image upload, kill valkey mid-pipeline (`docker compose kill valkey && docker compose up -d valkey`), verify the stream replays the `image:result` message and the image completes processing without client intervention.
- Memory cap enforced (`CONFIG GET maxmemory` returns `268435456`).

---

### Task 9: Add Caddy service under `profiles: [prod]`

**Story Points**: 2

**Description**

Add a Caddy reverse proxy service that terminates TLS, serves Let's Encrypt automatically, and routes to the two app services on the internal compose network. Use `profiles: [prod]` so it does not start on the laptop by default.

```yaml
caddy:
  image: caddy:2-alpine
  profiles: [prod]
  restart: unless-stopped
  ports:
    - "80:80"
    - "443:443"
    - "443:443/udp"  # HTTP/3
  volumes:
    - ./Caddyfile:/etc/caddy/Caddyfile:ro
    - caddy_data:/data
    - caddy_config:/config
  networks:
    - internal
  depends_on:
    - follow-api
    - follow-image-gateway
  logging:
    driver: json-file
    options:
      max-size: "10m"
      max-file: "5"
  deploy:
    resources:
      limits:
        memory: 128m
```

Add `caddy_data` and `caddy_config` to the volumes block.

**Why start-order only, NOT `condition: service_healthy`**: a health-gated `depends_on` creates a chicken-and-egg failure mode. If either app service fails its healthcheck (bad migration, config typo, OOM), Caddy never starts, port 80 is unreachable, and Let's Encrypt cannot renew certs via HTTP-01. Caddy must be allowed to bind `:80` and serve ACME challenges regardless of upstream health — upstream 502s are a correct and observable failure mode, an unrenewable cert is a silent time bomb.

**Acceptance Criteria**

- `docker compose --profile prod up -d` starts Caddy.
- `docker compose up -d` (no profile) does NOT start Caddy.
- Caddy boots healthy, listens on 80 and 443.

---

### Task 10: Write the Caddyfile

**Story Points**: 1

**Description**

Minimal Caddyfile that maps the two production hostnames to the internal services and enforces a request body cap on the upload endpoint.

```caddyfile
api.follow.example {
    request_body {
        max_size 1MB
    }
    reverse_proxy follow-api:8080 {
        flush_interval -1
        transport http {
            read_timeout 10m
            write_timeout 10m
        }
    }
}

upload.follow.example {
    request_body {
        max_size 15MB
    }
    reverse_proxy follow-image-gateway:8090
}

download.follow.example {
    reverse_proxy minio:9000
}
```

**Why `flush_interval -1` and long read/write timeouts on `api.follow.example`**: the SSE endpoint `/api/v1/routes/{id}/status/stream` is a long-lived streaming response. Caddy's default `reverse_proxy` buffers responses (events arrive in batches, not as they happen) and has an idle timeout that can kill streams after a minute or two. `flush_interval -1` disables buffering (flush after every write), and the 10-minute read/write timeouts let the stream stay open through quiet periods. Task 22 verifies this end-to-end, but the config must land here so the verification actually tests the committed file.

**Why `max_size 1MB` on `api.follow.example`**: JSON endpoints never accept more than a few KB. Capping at 1MB prevents the API from being used as a general-purpose upload vector and shields it from payload-based abuse. Image uploads have their own dedicated hostname with a 15MB cap.

The actual hostnames come from Phase 1.

**Body size limits — verify against gateway code before committing the Caddyfile**:

The gateway's current `MaxFileSize` default (as of writing) is `10 * 1024 * 1024` (10MB), defined in `follow-image-gateway/internal/shared/config/defaults.go`. The Caddy `max_size 15MB` gives ~5MB headroom for multipart overhead and is the correct value IF the gateway is still at 10MB. Before merging this task:

```bash
# Verify the current gateway limit
grep -r "DefaultServerMaxFileSize\|MaxFileSize" follow-image-gateway/internal/shared/config/
```

If the gateway limit has grown (e.g., to 20MB for higher-res images), bump the Caddy limit to `gateway_limit + 5MB` to match. The two limits must stay aligned: Caddy too low → legitimate uploads rejected at the proxy; Caddy too high → wasted bandwidth on malicious oversized payloads that the gateway would reject anyway.

**No body limit on `download.follow.example`** — downloads are GET requests with no request body. MinIO responses can be large (multi-MB images) and Caddy does not cap response size by default. Presigned URLs embed the signing host, so MinIO verifies the `host` header against `download.follow.example`; `MINIO_EXTERNAL_ENDPOINT` must be set to this hostname on the box (Task 28).

**Files Affected**

- `Caddyfile` (new, repo root)

**Acceptance Criteria**

- File exists and is mounted into the Caddy container.
- `docker compose --profile prod up -d caddy` boots healthy.
- Caddy validates the file at startup (no syntax errors).

---

### Task 11: Bind all host-published ports to `127.0.0.1`

**Story Points**: 1

**Description**

In production, `follow-api`, `follow-image-gateway`, `postgres`, `valkey`, and `minio` must not be reachable from the public internet directly — only via Caddy (for the two app services) or the internal docker network (for postgres/valkey/minio). Caddy reaches upstreams by service name on the internal compose network, so published ports are only useful for laptop `curl` / `psql` convenience.

**Approach**: bind every `ports:` entry in `docker-compose.yml` to `127.0.0.1` explicitly. This is the simplest, single-mechanism solution and works identically on laptop and Hetzner with no profile gymnastics:

```yaml
follow-api:
  ports:
    - "127.0.0.1:${FOLLOW_API_HOST_PORT:-8080}:8080"

follow-image-gateway:
  ports:
    - "127.0.0.1:${GATEWAY_HOST_PORT:-8090}:8090"

postgres:
  ports:
    - "127.0.0.1:${POSTGRES_HOST_PORT:-5432}:5432"

valkey:
  ports:
    - "127.0.0.1:${VALKEY_HOST_PORT:-6379}:6379"

minio:
  ports:
    - "127.0.0.1:${MINIO_HOST_PORT:-9000}:9000"
    - "127.0.0.1:${MINIO_CONSOLE_HOST_PORT:-9001}:9001"
```

`127.0.0.1:` binding tells Docker to only listen on the loopback interface. On the Hetzner box, UFW also default-denies external access — this is belt-and-suspenders, but the loopback binding is the authoritative mechanism because it survives a misconfigured UFW. Caddy still reaches upstreams via the internal compose network by service name (`follow-api:8080`), which has nothing to do with the host-published port.

The earlier plan considered `profiles: [dev]` to gate port publishing. That approach was dropped because it contradicted itself — a service under `profiles: [dev]` only starts when the dev profile is active, which would break the default no-profile laptop workflow. Loopback binding is simpler, works unconditionally, and requires zero profile logic.

**Files Affected**

- `docker-compose.yml`

**Acceptance Criteria**

- Every `ports:` entry is prefixed with `127.0.0.1:`.
- After deploy on Hetzner with `--profile prod`, `curl http://<hetzner-ip>:8080/health` from outside the box fails (connection refused).
- `ssh follow@<hetzner-ip> curl http://127.0.0.1:8080/health` succeeds from the box itself.
- `curl https://api.follow.example/health` succeeds (via Caddy).
- Laptop dev workflow unchanged: `curl http://localhost:8080/health` still works.
- Single mechanism — no `profiles: [dev]` gate on ports anywhere in the file.

---

### Task 11b: Rewire `MINIO_EXTERNAL_ENDPOINT` for direct pass-through

**Story Points**: 1

**Description**

Today the root `docker-compose.yml` composes `MINIO_EXTERNAL_ENDPOINT` from two other env vars:

```yaml
- MINIO_EXTERNAL_ENDPOINT=${HOST_IP:-localhost}:${MINIO_HOST_PORT:-9000}
```

This works on the laptop (where `HOST_IP=localhost` and `MINIO_HOST_PORT=9000` produce `localhost:9000`), but it is wrong for production. On the Hetzner box, presigned download URLs must embed `download.follow.example` with no port (Caddy routes that hostname to the internal MinIO on 9000, and the public entry point is port 443 via TLS). With the current interpolation, setting `HOST_IP=download.follow.example` and leaving `MINIO_HOST_PORT=9000` produces `download.follow.example:9000` — unreachable from the public internet — and there is no knob to emit a bare host.

Verified against the Go MinIO SDK (`follow-api/internal/infrastructure/storage/storage.go:59-90`): `minio.New(endpoint, ...)` accepts a bare hostname and infers the port from the `Secure` option (443 when `UseSSL=true`). So the correct production value is literally `download.follow.example`, no colon, no port.

**Action**: change the interpolation to pass `MINIO_EXTERNAL_ENDPOINT` through directly, with a laptop-safe default.

```yaml
- MINIO_EXTERNAL_ENDPOINT=${MINIO_EXTERNAL_ENDPOINT:-localhost:9000}
```

Update `.env.example` to document both values:

```bash
# Host embedded in presigned MinIO URLs. On the laptop this must
# include the port (Docker publishes MinIO on :9000). On the
# Hetzner box, set this to the public download hostname with NO
# port and set MINIO_USE_SSL=true — the MinIO SDK will infer 443.
#   Laptop:   MINIO_EXTERNAL_ENDPOINT=localhost:9000
#   Hetzner:  MINIO_EXTERNAL_ENDPOINT=download.follow.example
MINIO_EXTERNAL_ENDPOINT=localhost:9000
```

Do NOT delete `HOST_IP` — it is still used elsewhere (e.g., `GATEWAY_BASE_URL` interpolation at `docker-compose.yml:134`). Only `MINIO_EXTERNAL_ENDPOINT` switches to direct pass-through.

**Files Affected**

- `docker-compose.yml` — line ~125, replace composed value with direct pass-through.
- `.env.example` — add `MINIO_EXTERNAL_ENDPOINT` with laptop default and comment explaining the prod value.

**Acceptance Criteria**

- Laptop `docker compose up -d` with no `.env` override still produces presigned URLs rooted at `http://localhost:9000/` (unchanged behavior).
- `docker compose config` shows `MINIO_EXTERNAL_ENDPOINT` resolving from `.env` without the `HOST_IP:PORT` concatenation.
- With `MINIO_EXTERNAL_ENDPOINT=download.follow.example` and `MINIO_USE_SSL=true` set in `.env`, follow-api emits presigned URLs that start with `https://download.follow.example/` (verified in Task 35 against the live box).

---

### Task 12: Add backup sidecar service under `profiles: [prod]`

**Story Points**: 2

**Description**

Add a backup container that runs on a cron, dumps postgres, mirrors MinIO to Cloudflare R2, and prunes old backups. Alpine base + `postgresql17-client` + `mc` (MinIO client). Mounted script handles the work.

Build the backup container from a tiny dedicated Dockerfile with `postgresql17-client` and `mc` baked in. Do NOT `apk add` at container startup — that reinstalls packages on every restart, depends on Alpine mirrors being reachable from the running service, and is fragile by design. Fix at the root: ship a proper image.

**`mc` version pinning** matters: Alpine's `minio-client` package tracks upstream and shifts with every rebuild, which means `mc mirror` flag semantics can drift between redeploys without a code change. Download a specific MinIO Client release directly from `https://dl.min.io/client/mc/release/linux-amd64/` instead, pinned to a known-good build. `postgresql17-client` is pinned via the Alpine base version itself (`alpine:3.21` locks the package repo snapshot).

```dockerfile
# scripts/backup.Dockerfile
FROM alpine:3.21

# Pin MC_RELEASE to a known-good MinIO Client build. Bump explicitly
# when a newer version is validated — never implicitly via apk.
ARG MC_RELEASE=RELEASE.2025-01-17T23-25-50Z

RUN apk add --no-cache postgresql17-client tzdata ca-certificates curl \
 && curl -fsSL "https://dl.min.io/client/mc/release/linux-amd64/archive/mc.${MC_RELEASE}" \
      -o /usr/local/bin/mc \
 && chmod +x /usr/local/bin/mc \
 && apk del curl

COPY scripts/backup.sh /usr/local/bin/backup.sh
RUN chmod +x /usr/local/bin/backup.sh
ENTRYPOINT ["/bin/sh", "-c", "/usr/local/bin/backup.sh install-cron && exec crond -f -d 8 -L /dev/stdout"]
```

The `ARG MC_RELEASE` default can be overridden at build time (`docker compose --profile prod build --build-arg MC_RELEASE=RELEASE.YYYY-MM-DDTHH-MM-SSZ backup`) when a new version is validated. Treat version bumps as code changes.

```yaml
backup:
  build:
    context: .
    dockerfile: scripts/backup.Dockerfile
  profiles: [prod]
  restart: unless-stopped
  depends_on:
    postgres:
      condition: service_healthy
    minio:
      condition: service_healthy
  environment:
    - R2_ENDPOINT=${R2_ENDPOINT}
    - R2_ACCESS_KEY=${R2_ACCESS_KEY}
    - R2_SECRET_KEY=${R2_SECRET_KEY}
    - R2_BACKUP_BUCKET=${R2_BACKUP_BUCKET}
    - POSTGRES_USER=${POSTGRES_USER}
    - POSTGRES_PASSWORD=${POSTGRES_PASSWORD}
    - PGPASSWORD=${POSTGRES_PASSWORD}
    - POSTGRES_DB=${POSTGRES_DB}
    - MINIO_ACCESS_KEY_ID=${MINIO_ACCESS_KEY_ID}
    - MINIO_SECRET_ACCESS_KEY=${MINIO_SECRET_ACCESS_KEY}
  networks:
    - internal
  logging:
    driver: json-file
    options:
      max-size: "10m"
      max-file: "5"
  deploy:
    resources:
      limits:
        memory: 256m
```

`PGPASSWORD` is set from `POSTGRES_PASSWORD` so `pg_dump` authenticates non-interactively without a `.pgpass` file. The script itself (Task 13) does not need to manage credentials.

**Files Affected**

- `docker-compose.yml` — add `backup` service under `profiles: [prod]`.
- `scripts/backup.Dockerfile` (new).

**Acceptance Criteria**

- `scripts/backup.Dockerfile` exists and builds cleanly (`docker compose --profile prod build backup`).
- `docker compose --profile prod up -d` starts the backup container — no `apk add` at runtime.
- Container stays healthy (cron loop running).
- Manual `docker compose --profile prod exec backup /usr/local/bin/backup.sh run-now` succeeds end-to-end.
- `docker compose --profile prod exec backup which pg_dump mc` returns both binaries (proves they're baked in, not installed on start).
- `docker compose --profile prod exec backup mc --version` returns the pinned `MC_RELEASE` tag from the Dockerfile (proves the binary is the pinned download, not the drifting Alpine package).

---

### Task 13: Write `scripts/backup.sh`

**Story Points**: 2

**Description**

Two-mode script:

1. `install-cron` — writes a `/etc/crontabs/root` entry that runs the backup nightly (e.g., 03:00 UTC).
2. `run-now` — performs the actual backup:
   - `pg_dump -Fc -h postgres -U $POSTGRES_USER $POSTGRES_DB | gzip` → upload to `r2://$R2_BACKUP_BUCKET/postgres/YYYY-MM-DD-HHMMSS.dump.gz`. `PGPASSWORD` is already set in the container environment (Task 12), so no credential handling in the script.
   - `mc alias set r2 $R2_ENDPOINT $R2_ACCESS_KEY $R2_SECRET_KEY`
   - `mc alias set local http://minio:9000 $MINIO_ACCESS_KEY_ID $MINIO_SECRET_ACCESS_KEY`
   - `mc mirror --overwrite local/follow-images r2/$R2_BACKUP_BUCKET/minio/follow-images/`
   - **Back up the encrypted `.env` file** (see "Encrypted `.env` backup" below) — upload to `r2://$R2_BACKUP_BUCKET/env/YYYY-MM-DD-HHMMSS.env.age`.
   - After all steps succeed, write a last-success timestamp to `r2://$R2_BACKUP_BUCKET/_last_success.txt` for Task 34 monitoring.
   - Log success/failure to stdout (caught by Docker logging driver).

**Encrypted `.env` backup**:

The `.env` file on the Hetzner box holds every production secret: `JWT_SECRET`, the Ed25519 keypair, `POSTGRES_PASSWORD`, `MINIO_ROOT_PASSWORD`, `R2_*` credentials. The postgres + MinIO mirror backups are useless if the host is destroyed and the secrets are lost — JWTs issued before the incident can't be verified, the restored MinIO bucket can't be unlocked, and the pilot customer is effectively locked out. The password manager is the primary source of truth (Task 23 / Task 28 both require it), but a machine-readable backup that's always in sync with what's actually running on the box is cheap insurance.

Use `age` (small, simple, no key management ceremony) for symmetric encryption with a passphrase known only to the operator and stored in the password manager. Add `age` to `scripts/backup.Dockerfile`:

```dockerfile
# In backup.Dockerfile, alongside the existing apk add line
RUN apk add --no-cache age
```

In `backup.sh`, before uploading:

```bash
# AGE_PASSPHRASE is a container env var sourced from the host .env
# (circular, but intentional — the operator types it in once and it
# never leaves the box unencrypted).
age --passphrase \
    -o /tmp/env-backup.age \
    /backup-src/.env \
  < <(printf '%s\n%s\n' "$AGE_PASSPHRASE" "$AGE_PASSPHRASE")
mc cp /tmp/env-backup.age \
    "r2/${R2_BACKUP_BUCKET}/env/$(date -u +%Y-%m-%d-%H%M%S).env.age"
shred -u /tmp/env-backup.age
```

The `.env` file must be mounted read-only into the backup container at `/backup-src/.env` (update the `backup` service's `volumes:` block in Task 12 to add `- ./.env:/backup-src/.env:ro`). `AGE_PASSPHRASE` is itself an env var declared in `.env` — the operator sets it when provisioning the box (Task 28) and records it in the password manager alongside the other secrets.

**Recovery procedure** (document in the runbook, Task 38): download the latest `env/*.age` from R2, `age --decrypt -o .env.recovered env-backup.age` (prompts for the passphrase from the password manager), compare to any stale version the operator still has, place on the new box, `chmod 600`.

Use `set -euo pipefail`. Exit non-zero on any failure so the container restarts and the failure is visible in logs/monitoring.

**Critical: do NOT pass `--remove` to `mc mirror`.** `--remove` deletes objects from the destination (R2) that no longer exist at the source (local MinIO). If a bug, misconfiguration, or human error deletes objects from local MinIO, the next backup propagates the deletion to R2 — your backup is gone, exactly at the moment you need it most. The mirror must be *additive*: local truth flows to R2, but R2 never shrinks based on local state. The tradeoff is that orphaned objects accumulate in R2 over time. That cost is handled by the 30-day lifecycle rule in Task 16, which ages out anything that hasn't been re-mirrored recently — orphans cost pennies per month on R2 and self-clean within the retention window. A missing `--remove` flag is a rounding error on storage cost; a present `--remove` flag is a data-loss incident waiting to happen.

**MinIO backup strategy — scaling note**:

At MVP scale (expected: low hundreds of routes, low GB of images), a nightly full `mc mirror` is fine — R2 egress is free, the mirror is idempotent, and restores are trivial (`mc mirror` in the other direction). As the platform grows this strategy gets expensive in two ways:

- **Transfer time**: a full mirror of 10GB+ starts eating meaningful wall-clock time during the nightly window.
- **Cost**: R2 storage is cheap but not free; if you hold multiple backup generations (via the lifecycle rule in Task 16), you're paying for N copies of everything that never changes.

**Switch criteria**: revisit this when MinIO bucket size exceeds **~10GB** or nightly backup wall-clock time exceeds **~5 minutes**. At that point, evaluate:

1. **Incremental sync via `mc mirror --newer-than`** — only copies files modified in the last 24h+1h. Smallest code change, still a single-generation backup. Good next step.
2. **R2 object versioning** on the source bucket — let R2 keep historical versions natively, stop mirroring entirely. Requires rethinking the "MinIO on box, R2 as backup" model but is the cleanest long-term answer if you're willing to make R2 the source of truth.
3. **Dedicated incremental backup tool** (restic, borg) — overkill for MVP, mentioned for completeness.

Document the switch criteria and decision tree in the runbook (Task 38) so future-you knows when to look at this without having to re-derive it.

**Files Affected**

- `scripts/backup.sh` (new)

**Acceptance Criteria**

- `bash -n scripts/backup.sh` passes.
- `shellcheck scripts/backup.sh` passes.
- `run-now` mode works end-to-end against laptop postgres + MinIO + a real R2 bucket.
- `r2://$R2_BACKUP_BUCKET/env/<timestamp>.env.age` exists after a successful run and is decryptable with the stored passphrase.
- Decryption test: `age --decrypt env-backup.age` against the latest uploaded file recovers the exact bytes of the original `.env` (verified with `diff`).
- `_last_success.txt` is written only on full success (not on partial failure), meaning a failed `.env` backup must also prevent the last-success ping.
- Runbook (Task 38) contains the "when to switch from full mirror to incremental" decision criteria above AND the `.env` recovery procedure.

---

### Task 14: Add R2 credentials to `.env.example`

**Story Points**: 1

**Description**

Document the new env vars the backup sidecar needs. Use placeholder values in `.env.example` (committed). Real values go in the host `.env` (not committed) in Phase 4.

```bash
# ── Cloudflare R2 (backup target) ────────────────────────────────
R2_ENDPOINT=https://<account-id>.r2.cloudflarestorage.com
R2_ACCESS_KEY=<r2-access-key>
R2_SECRET_KEY=<r2-secret-key>
R2_BACKUP_BUCKET=follow-backups

# ── .env backup encryption ───────────────────────────────────────
# Passphrase used by `age` to encrypt the .env file before pushing
# it to R2 (Task 13). Store this value in the password manager
# alongside the other production secrets. Never commit a real value.
AGE_PASSPHRASE=<long-random-passphrase>
```

**Files Affected**

- `.env.example`

**Acceptance Criteria**

- `R2_*` and `AGE_PASSPHRASE` variables added with placeholder values.
- Comment explains where to find R2 credentials in the Cloudflare dashboard.
- Comment explains that `AGE_PASSPHRASE` must be preserved in the password manager and is the only way to decrypt the `.env` backups in R2.

---

### Task 15: Create a separate R2 bucket for backups

**Story Points**: 1

**Description**

Create a NEW R2 bucket dedicated to backups. Do NOT reuse the existing pre-MVP R2 bucket that holds the legacy MinIO data. Generate a new R2 API token scoped to ONLY this bucket, with read+write permissions. Keep the credentials in a password manager until Phase 4.

**Acceptance Criteria**

- New R2 bucket exists, named e.g., `follow-backups`.
- A scoped API token exists with access to only that bucket.
- Credentials saved in the user's password manager.

---

### Task 16: Set R2 lifecycle rule for backup retention

**Story Points**: 1

**Description**

Configure a lifecycle rule on the backups bucket to delete objects older than 30 days (or whatever retention the user wants — 30 days is a sensible MVP default). Without this, backups accumulate forever and R2 storage cost grows linearly.

**Acceptance Criteria**

- Lifecycle rule exists on the bucket, deletes objects >30 days old.
- Verified in the Cloudflare dashboard.

---

### Task 17: Test the full prod profile locally

**Story Points**: 2

**Description**

End-to-end smoke test of the prod profile on the laptop, BEFORE touching Hetzner. This catches Caddy config errors, healthcheck mistakes, missing env vars, and resource limit problems while you can still debug them with familiar tools.

```bash
docker compose --profile prod up -d
docker compose ps          # all services healthy
docker compose logs caddy  # caddy boots, tries to issue certs (will fail locally without real DNS — expected)
docker compose logs backup # cron loop running
```

For local cert testing, override the Caddyfile temporarily to use Caddy's `tls internal` directive (self-signed). Don't commit that change — it's a one-time local sanity check. Note: `tls internal` issues a cert chained to Caddy's private CA that browsers won't trust, so `curl -k` is required and the Flutter web client may refuse to connect. If you need a browser-trusted local cert (for the SSE verification in Task 22), install [`mkcert`](https://github.com/FiloSottile/mkcert) and mount the generated cert into the Caddy container instead of using `tls internal`.

**Acceptance Criteria**

- All prod-profile services boot to healthy state.
- Caddy serves traffic on local 443 (with self-signed cert for the laptop test).
- `curl -k https://localhost/health` reaches `follow-api` through Caddy.

---

### Task 18: Test backup end-to-end on laptop

**Story Points**: 2

**Description**

Trigger `backup.sh run-now` against the local stack. Verify the postgres dump and MinIO mirror both land in R2.

```bash
docker compose --profile prod exec backup /usr/local/bin/backup.sh run-now
# In the Cloudflare dashboard, verify:
# - r2://follow-backups/postgres/<timestamp>.dump.gz exists, non-zero size
# - r2://follow-backups/minio/follow-images/ contains all objects from local MinIO
```

**Acceptance Criteria**

- Postgres dump file exists in R2, restorable size (not zero, not absurd).
- MinIO mirror contains every object from the source bucket.
- Script exits 0; logs show no errors.

---

### Task 19: Test restore end-to-end on laptop

**Story Points**: 3

**Description**

**This is the single most important task in the entire plan.** An untested backup is not a backup.

Process:

1. Spin up a throwaway postgres container (`docker run --rm postgres:17-alpine`).
2. Pull the latest dump from R2: `mc cp r2/follow-backups/postgres/latest.dump.gz ./`.
3. Restore: `gunzip -c latest.dump.gz | pg_restore -d <throwaway-db>`.
4. Spot-check the data: row counts on `routes`, `users`, `images` match the source.
5. Pull MinIO mirror: `mc mirror r2/follow-backups/minio/follow-images/ ./minio-restore/`.
6. Spot-check object counts and a few files for byte-equality with source.

If any step fails, fix `backup.sh` and re-test until restore is clean.

**Acceptance Criteria**

- Postgres restore succeeds without errors.
- Restored row counts match source.
- MinIO restore succeeds; spot-checked files are byte-identical.
- Process documented in `scripts/RESTORE.md` (Task 38).

---

## Phase 3 — Application code changes

Small set of code changes to make the apps aware of the new domain layout.

### Task 20: Update follow-api CORS allowed origins

**Story Points**: 1

**Description**

The CORS allow-list in `follow-api/configs/config.yaml:68-70` is currently a single comma-separated string with only the laptop dev origin:

```yaml
cors:
  allowed_origins: "http://localhost:3000"
  max_age: 3600
```

There is no fly.io origin to remove — the fly.io deployment currently relies on this narrow allow-list and the browser never enforces CORS there because the Flutter app and the API are on the same origin today. Adding Cloudflare Pages as the client origin is a new concern that CORS will now enforce for real.

**Action**: extend the allow-list to include the production Pages hostname:

```yaml
cors:
  allowed_origins: "http://localhost:3000, https://app.follow.example"
  max_age: 3600
```

The parser at `follow-api/internal/api/server/goa_server.go:585-587` (`parseAllowedOrigins`) splits on commas and trims whitespace, so either comma-separated form works. Keep `http://localhost:3000` — the Flutter web dev build still needs it.

**SSE preflight** is the most CORS-sensitive endpoint (`/api/v1/routes/{id}/status/stream`). Browsers send an `OPTIONS` preflight before opening an EventSource connection, and the response must include `Access-Control-Allow-Origin` matching the Pages hostname and `Access-Control-Allow-Credentials: true` if the stream carries an auth cookie/header. Verify end-to-end in Task 22.

**Files Affected**

- `follow-api/configs/config.yaml` (line ~70 — the single `allowed_origins` string)
- `follow-api/internal/api/server/cors_middleware_test.go` — add a test case that includes the Pages origin so regressions are caught

**Acceptance Criteria**

- `cors.allowed_origins` in `config.yaml` contains `https://app.follow.example`.
- A browser fetch from `https://app.follow.example` to `https://api.follow.example/api/v1/users/anonymous` succeeds with correct CORS headers (test in Phase 5).
- SSE handshake from the Pages origin to `https://api.follow.example/api/v1/routes/{id}/status/stream` succeeds (verified in Task 22 locally, Task 35 in prod).
- `go test -race -cover ./...` still passes.

---

### Task 21: Update Flutter app API endpoints

**Story Points**: 2

**Description**

The Flutter app reads `api_base_url` at startup from a JSON config file selected by the build flavor — see `follow-app/lib/config/config_service.dart:193,214,289`. The current hardcoded fly.io URLs live in the config JSON files, not in Dart source, so this is a config edit, not a code edit.

**Files to update** (verified via grep):

| File | Current value | New value |
|------|---------------|-----------|
| `follow-app/assets/config/config.json` | `https://follow-api.fly.dev` | `https://api.follow.example` |
| `follow-app/assets/config/config.production.json` | `https://follow-api.fly.dev` | `https://api.follow.example` |
| `follow-app/assets/config/config.development.json` | `http://192.168.68.55:18080` | leave as-is (local dev only) |
| `follow-app/assets/config/config.staging.json` | `https://staging-api.follow.app` | leave as-is — staging flavor is unused per project decision |

The upload and download hostnames are NOT read from this config today — the Flutter app reads upload URLs out of the API response and download URLs out of the presigned MinIO URLs embedded in the route payload. As long as follow-api's `MINIO_EXTERNAL_ENDPOINT` and `GATEWAY_BASE_URL` are correct on the box (Task 28), the app will follow them automatically. No hardcoded upload or download host needs to change in the Flutter codebase.

After the JSON swap, grep the whole repo one more time to catch any accidental stragglers:

```bash
# via the Grep tool
fly\.dev    # expect zero matches in follow-app/
fly\.io     # expect zero matches in follow-app/
```

**Files Affected**

- `follow-app/assets/config/config.json`
- `follow-app/assets/config/config.production.json`

**Acceptance Criteria**

- Zero references to `fly.dev` or `fly.io` remain anywhere in `follow-app/` (except historical references in `ai-docs/planning/completed/`, which are intentional snapshots of past work).
- `config.development.json` still points at the local dev API (unchanged).
- `config.staging.json` unchanged (unused flavor — project decision, do not touch).
- `flutter analyze` returns no errors.
- `flutter test` passes.

---

### Task 22: Verify SSE works through the new domain locally

**Story Points**: 3

**Description**

SSE + CORS + cross-origin + long-lived connection through a reverse proxy is the single most likely thing to eat half a day in this plan. Caddy buffers responses by default (which breaks SSE), browser EventSource has strict CORS preflight rules, and Flutter's web SSE client has its own quirks. Test it BEFORE Hetzner so you debug locally with browser devtools and familiar logs instead of over SSH.

Process:

1. Edit `/etc/hosts` to point `api.follow.example` and `app.follow.example` at `127.0.0.1`.
2. Run the prod profile locally with `tls internal` in the Caddyfile (self-signed certs).
3. Run the Flutter web app (configured to point at `https://api.follow.example`).
4. Trigger a route creation flow that opens an SSE stream.
5. Verify the SSE connection establishes, events flow in real time, no CORS errors in browser console, no premature disconnects.

**Likely gotchas to verify explicitly**:

- **Caddy buffering**: confirm Caddy does NOT buffer the SSE response. If events arrive in batches instead of as they happen, add `flush_interval -1` to the `reverse_proxy` directive for the SSE endpoint (or route it with a dedicated matcher).
- **CORS preflight on EventSource**: browsers send `OPTIONS` before opening the stream. Verify follow-api's CORS middleware answers `OPTIONS` with the right `Access-Control-Allow-*` headers for `text/event-stream`.
- **Idle timeout**: Caddy's default reverse_proxy timeout can close long-lived SSE streams. Confirm the route stream stays alive for at least 5 minutes without disconnect.
- **HTTP/2 vs HTTP/1.1**: SSE works on both, but some clients prefer one. Verify whichever the Flutter web client uses actually streams.

**Acceptance Criteria**

- SSE handshake succeeds from the browser (visible in devtools Network tab as `EventStream`).
- Progress events arrive in order, in real time (not batched).
- Terminal `ready` event arrives.
- No CORS errors anywhere in the browser console or server logs.
- Connection survives at least 5 minutes of idle time without being killed by the proxy.

---

### Task 23: Rotate dummy Ed25519 keypair and JWT secret

**Story Points**: 1

**Description**

`.env.example` ships dummy values for `JWT_SECRET`, `FOLLOW_API_ED25519_PRIVATE_KEY`, `FOLLOW_API_ED25519_PUBLIC_KEY`. For the production box, generate a fresh keypair and a fresh 32+ char JWT secret. NEVER commit them. They go directly into the host `.env` in Task 28.

```bash
# JWT secret (32+ chars)
openssl rand -base64 48

# Ed25519 keypair — pure openssl, no Python dep
openssl genpkey -algorithm Ed25519 -out /tmp/ed25519_private.pem

echo -n "FOLLOW_API_ED25519_PRIVATE_KEY="
openssl pkey -in /tmp/ed25519_private.pem -outform DER | tail -c 32 | base64 -w0
echo

echo -n "FOLLOW_API_ED25519_PUBLIC_KEY="
openssl pkey -in /tmp/ed25519_private.pem -pubout -outform DER | tail -c 32 | base64 -w0
echo

# Clean up the PEM file once the base64 values are captured
shred -u /tmp/ed25519_private.pem 2>/dev/null || rm -f /tmp/ed25519_private.pem
```

Ed25519 raw keys are exactly 32 bytes. `openssl pkey -outform DER` emits a DER-encoded structure with a fixed prefix; `tail -c 32` slices off the raw 32-byte key at the end — identical to what the cryptography library's Raw encoding produces, without the Python dependency.

Save the output in the password manager. Do NOT paste it into any committed file. Delete the temporary PEM file after capturing the base64 values (the `shred` / `rm` line above).

**Acceptance Criteria**

- Fresh JWT secret generated.
- Fresh Ed25519 keypair generated.
- All three values stored in the password manager, ready for Task 28.

---

## Phase 4 — Hetzner provisioning

### Task 24: Provision the Hetzner box

**Story Points**: 1

**Description**

Pick a server size at Hetzner Cloud. For MVP+ with one pilot customer.

#### Measured Memory Budget

Memory usage was benchmarked under sustained load (60 routes, ~140 images through the full ML pipeline). See [`ai-docs/research/gateway-memory-benchmark.md`](../../../ai-docs/research/gateway-memory-benchmark.md) for the full investigation.

**Measured per-service memory (with `malloc_trim` fix applied):**

| Service | Idle | Peak (sustained load) | Notes |
|---------|------|-----------------------|-------|
| follow-image-gateway | 100 MiB | **1.3 GiB** | ONNX + libvips CGO allocations; sawtooth 362 MiB - 1.1 GiB between jobs |
| follow-api | 11 MiB | 35 MiB | Trivial even under heavy Valkey consuming + SSE |
| PostgreSQL 17 | 40 MiB | 40 MiB | Will grow with `shared_buffers` tuning (~512 MiB) |
| MinIO | 92 MiB | 220 MiB | Grows linearly with stored images |
| Valkey 8 | 9 MiB | 11 MiB | Rock solid |
| OS + Docker | - | ~512 MiB | Kernel, Docker daemon, page cache |
| **Total** | **~252 MiB** | **~2.6 GiB** | |

The gateway's peak RSS was originally 3.4 GiB due to glibc malloc retaining freed CGO memory. This was fixed in-code via `mallopt(M_MMAP_THRESHOLD, 65536)` + `malloc_trim(0)` per job (commit `2273fba` in follow-image-gateway), reducing peak to 1.3 GiB. The Go heap stays flat at 20-31 MiB — the CGO memory is the only significant consumer.

#### Server Sizing

Use the **Shared Regular Performance** plan (not Cost-Optimized, not Dedicated):
- **Not Cost-Optimized**: older hardware; the gateway's ONNX inference benefits from newer CPUs to complete bursts faster
- **Not Dedicated**: the workload is bursty (seconds of processing per route), not sustained; paying for guaranteed cores that idle 99% of the time is wasteful at MVP

Prefer the **CPX line (AMD EPYC)** over the CX line (Intel) — at the same price point, CPX gives more vCPUs and better burst performance.

- **CPX21** (3 vCPU AMD shared, 4GB RAM, 80GB SSD) — ~8 EUR/month. **Default recommendation**. Measured peak is 2.6 GiB under extreme sustained load (60 routes back-to-back). Realistic MVP usage (1-5 routes per session) peaks at ~1.8 GiB total, leaving 2.2 GiB headroom. 3 vCPUs is sufficient for the bursty pipeline. 80 GB SSD is plenty (PostgreSQL is tiny, images go to MinIO). Live-resizable to CPX31 in minutes if needed.
- **CPX31** (4 vCPU AMD shared, 8GB RAM, 160GB SSD) — ~11 EUR/month. Upgrade path if CPX21 proves tight — e.g. PostgreSQL `shared_buffers` tuned to 1 GB, or concurrent route creation causes memory pressure. Zero-downtime resize from CPX21.
- **CCX13** (2 vCPU dedicated, 8GB RAM) — skip. ~2-3x the price for guaranteed cores the workload doesn't need.

Shared vCPU is fine: bursts are seconds long, the rest of the day is idle. Hetzner allows live resizing of CPX instances.

**CPX21 benchmark reference** (Geekbench 6.3.0, AMD EPYC Zen 2): single-core 1450, multi-core 4767. Object Detection score 805 (single) / 2863 (multi). Route processing that takes ~1.3s on a dev machine (i7-11700, 16 cores) will likely take 4-6s on CPX21. Acceptable for MVP.

#### Location and OS

Pick a location close to the pilot customer (Hetzner has Falkenstein/Nuremberg in Germany and Helsinki in Finland for EU; Ashburn for US).

Use **Debian 13 Trixie (Base 64-Bit)** from Hetzner's standard images. Reasons:
- Current Debian stable (released 2025), gets security backports without version churn
- Smaller and quieter than Ubuntu (no snap, no Canonical telemetry, no MOTD ads)
- Massive community for troubleshooting
- Docker and Caddy have first-class packages
- Boring and predictable — exactly what you want for a server you SSH into once a month

Avoid Arch (rolling release is a footgun for unattended servers), Ubuntu (snap + Canonical baggage), CentOS Stream (less stable than Rocky/Alma), and older Debian/Ubuntu releases.

**Acceptance Criteria**

- Box exists in Hetzner Cloud dashboard.
- Public IPv4 noted.
- SSH access verified with the user's existing SSH key.

---

### Task 25: Harden the host

**Story Points**: 2

**Description**

Basic security hygiene before anything sensitive lands on the box.

1. Create a non-root `follow` user, add to `sudo` and `docker` groups.
2. Disable root SSH login (`PermitRootLogin no`).
3. Disable password SSH (`PasswordAuthentication no`) — keys only.
4. Install and configure `ufw`:
   - **Enable IPv6 first**: `sudo sed -i 's/^IPV6=.*/IPV6=yes/' /etc/default/ufw`. Hetzner boxes come with a public IPv6 address, and UFW's default configuration on some Debian releases only applies rules to IPv4. Without this step, your "default-deny" covers v4 while v6 traffic flows in unchecked — a silent, critical hole.
   - `sudo ufw default deny incoming`
   - `sudo ufw default allow outgoing`
   - `sudo ufw allow 22/tcp` (SSH)
   - `sudo ufw allow 80/tcp` (HTTP, needed for Let's Encrypt HTTP-01 challenge)
   - `sudo ufw allow 443/tcp` (HTTPS)
   - `sudo ufw allow 443/udp` (HTTP/3 — matches the Caddy port publish from Task 9)
   - `sudo ufw enable`
5. Install `unattended-upgrades` for automatic security patches.
6. Set timezone to UTC (`timedatectl set-timezone UTC`).
7. Optional but recommended: install `fail2ban` for SSH brute-force protection. Minimal viable config: `sudo apt install fail2ban`, then drop a one-file jail at `/etc/fail2ban/jail.d/sshd.local`:

   ```ini
   [sshd]
   enabled = true
   backend = systemd
   bantime = 1h
   findtime = 10m
   maxretry = 5
   ```

   Restart with `sudo systemctl restart fail2ban` and verify with `sudo fail2ban-client status sshd`. Don't touch the default `jail.conf` — the `.local` overlay is the only thing you should edit. This alone covers 99% of SSH brute-force noise; skip custom filters, custom actions, and anything in `fail2ban.conf`.

**Test the hardened SSH config from a SECOND terminal BEFORE closing the original session** — if key auth is misconfigured you will be locked out and must recover via the Hetzner web console.

**Acceptance Criteria**

- `ssh follow@<hetzner-ip>` works.
- `ssh root@<hetzner-ip>` rejected.
- `/etc/default/ufw` has `IPV6=yes`.
- `ufw status verbose` shows only 22/tcp, 80/tcp, 443/tcp, 443/udp allowed, and lists rules for both v4 and v6.
- From an outside box: `nc -zv <hetzner-ipv6> 8080` fails (connection refused / filtered) — proves v6 default-deny works.
- `unattended-upgrades --dry-run` runs cleanly.

---

### Task 26: Install Docker + Compose plugin on the box

**Story Points**: 1

**Description**

Install Docker Engine and the Compose plugin from Docker's official APT repository (NOT the distro's docker.io package — usually old).

```bash
# Follow https://docs.docker.com/engine/install/debian/
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker follow
# Log out, log back in for group to take effect
docker compose version  # confirms plugin
```

**Acceptance Criteria**

- `docker --version` shows recent version (28.x or newer).
- `docker compose version` shows v2.x.
- `docker run --rm hello-world` works as the `follow` user (no sudo).

---

### Task 27: Clone the coordination repo to the box

**Story Points**: 1

**Description**

As the `follow` user, clone the coordination repo AND each of the required sub-repos. The sub-repos are independent git repositories co-located inside the coordination repo by convention (`.gitignore` excludes them from the parent). Cloning the parent alone leaves you with empty sub-repo directories — you must clone each one explicitly.

For this deployment you need: coordination repo, `follow-api`, `follow-image-gateway`, `follow-pkg`. The Flutter `follow-app` is NOT needed on the server (served from Cloudflare Pages). `follow-business` is documentation only, skip.

```bash
cd ~
git clone <coordination-repo-url> follow
cd follow

# Clone each Go sub-repo that Docker needs to build
git clone <follow-api-url> follow-api
git clone <follow-image-gateway-url> follow-image-gateway
git clone <follow-pkg-url> follow-pkg

# Verify each has its own .git dir and a clean tree
for d in follow-api follow-image-gateway follow-pkg; do
  (cd "$d" && git status && git log -1 --oneline)
done
```

**Acceptance Criteria**

- Coordination repo cloned to `/home/follow/follow`.
- `follow-api`, `follow-image-gateway`, `follow-pkg` cloned as sub-directories with their own `.git` dirs.
- `git status` clean in each of the four repos.
- `docker compose config` from the coordination repo resolves all build contexts without errors.

---

### Task 28: Create real `.env` on the box

**Story Points**: 1

**Description**

Create `/home/follow/follow/.env` with REAL production values. NEVER committed. Source values from the password manager (Task 23 keys, Task 15 R2 credentials, freshly generated DB/MinIO passwords).

```bash
# Key production differences from .env.example:
# - Strong unique passwords for POSTGRES_PASSWORD, MINIO_ROOT_PASSWORD, etc.
# - Real JWT_SECRET and Ed25519 keypair from Task 23
# - Real R2_* credentials from Task 15
# - Real AGE_PASSPHRASE for .env backup encryption (Task 13/14)
#
# Hostname-related vars (thanks to Task 11b rewiring, MINIO_EXTERNAL_ENDPOINT
# is now a direct pass-through — set it to the bare host with NO port; the
# MinIO SDK infers 443 from MINIO_USE_SSL=true):
# - MINIO_EXTERNAL_ENDPOINT=download.follow.example      (presigned download URLs)
# - MINIO_USE_SSL=true                                    (HTTPS via Caddy)
# - GATEWAY_BASE_URL=https://upload.follow.example        (presigned upload URLs)
# - HOST_IP=<hetzner-public-ipv4>                         (still used for
#                                                          GATEWAY_BASE_URL
#                                                          fallback interpolation
#                                                          — leave populated)
#
# POSTGRES_SSLMODE=disable is correct — postgres stays on the internal
# docker network, all traffic stays on the box, no TLS needed.

chmod 600 /home/follow/follow/.env
chown follow:follow /home/follow/follow/.env
```

**Acceptance Criteria**

- File exists with all required vars from `.env.example`, including `AGE_PASSPHRASE`.
- Permissions are `600`, owner `follow:follow`.
- `cat .env` as any other user fails.
- File is in `.gitignore` (verify it does not appear in `git status`).
- `MINIO_EXTERNAL_ENDPOINT=download.follow.example` (bare host, no port, no scheme), `MINIO_USE_SSL=true`, and `GATEWAY_BASE_URL=https://upload.follow.example` all set correctly.
- `docker compose config` renders the MinIO service with `MINIO_EXTERNAL_ENDPOINT=download.follow.example` (proves Task 11b pass-through is doing its job).
- Every secret in `.env` also exists in the password manager verbatim. The file itself is encrypted and backed up to R2 (Task 13), but the password manager is the authoritative human-usable copy.

---

### Task 29: Point DNS A records at the Hetzner IP

**Story Points**: 1

**Description**

If Phase 1 used a placeholder IP, update `api.follow.example`, `upload.follow.example`, and `download.follow.example` A records to the real Hetzner IPv4. Wait for propagation.

```bash
dig +short api.follow.example
dig +short upload.follow.example
dig +short download.follow.example
# All three should return the Hetzner IP
```

**Acceptance Criteria**

- All three A records resolve to the Hetzner IP from outside the local network.
- TTL set to a reasonable value (300s during cutover, can raise to 3600s after).

---

### Task 30: Enable Hetzner automatic snapshots / backups

**Story Points**: 1

**Description**

Hetzner Cloud offers **Automated Backups** as a paid add-on at roughly **20% of the server's monthly cost** (~€1.60/month on a CPX21 at ~€8/month). It creates a full disk-level snapshot nightly and keeps the last 7. Enable it in the Hetzner Cloud dashboard on the server instance.

**Why this is complementary to the R2 backups (Task 12/13), not redundant**:

| Scenario | R2 logical backup | Hetzner snapshot |
|----------|-------------------|------------------|
| Postgres table accidentally dropped | ✅ restore dump into running DB | ⚠️ restores entire host to yesterday — everything else rolls back too |
| MinIO objects deleted by bug | ✅ mc mirror back from R2 | ⚠️ same whole-host rollback |
| Whole host corrupted (filesystem, failed upgrade, rm -rf wrong dir) | ❌ have to provision new box, reinstall, reconfigure, restore dumps (hours) | ✅ restore snapshot, box is back in minutes |
| Ransomware / compromised host | ❌ need clean OS, same recovery pain | ✅ rollback to pre-compromise snapshot |
| Hetzner datacenter loses the disk | ❌ R2 save us | ❌ snapshot gone too (both live in Hetzner's infra) — R2 is the off-site insurance |

The two backup mechanisms cover disjoint failure modes. R2 is your **off-site data backup** — the thing that saves you if Hetzner has a catastrophic failure. Hetzner snapshots are your **fast host recovery** — the thing that saves you if you break the host with a bad command. Having both is cheap (~€1.60/mo on top of the server cost) and the recovery time difference is enormous: snapshot restore is minutes, full rebuild from scratch + R2 restore is hours-to-days.

**Action**: In the Hetzner Cloud dashboard, navigate to the server → Backups tab → enable "Automated Backups". No config needed beyond that. Hetzner handles the rest.

**Acceptance Criteria**

- Automated Backups enabled on the server in the Hetzner Cloud dashboard.
- First snapshot appears within 24 hours (verify in the Backups tab).
- Cost confirmed on the monthly invoice (should be ~20% of server cost).
- Runbook (Task 38) documents the snapshot restore procedure alongside the R2 restore procedure, with decision criteria for which to use when.

---

## Phase 5 — First Deploy & Verify

### Task 31: First deploy on the box

**Story Points**: 2

**Description**

Bring up the full prod profile on Hetzner.

```bash
cd /home/follow/follow
docker compose --profile prod up -d
docker compose ps
docker compose logs -f --tail=100
```

Watch the logs for the first 60-90 seconds. Postgres needs to initialize, `follow-api` runs its migrations on startup, valkey/MinIO come up, app services join the network, Caddy starts requesting certs. Anything failing here is almost always an env var typo or a port collision.

**Build the images on the laptop, not on the box** (recommended path): the first `docker compose build` for `follow-image-gateway` takes 10-15 minutes on a CPX21/CPX31 because the Dockerfile builds libvips 8.18 from source and downloads ONNX Runtime. Every subsequent redeploy pays the same cost if anything in the base layers changes. Build on the laptop (fast CI-grade hardware, cached layers) and ship the image directly to the box — this turns each redeploy from ~15 minutes of on-box build + deploy into ~1 minute of `docker load` + deploy.

```bash
# On laptop — do this BEFORE the first deploy and on every redeploy
docker compose build follow-image-gateway follow-api
docker save follow-image-gateway:latest follow-api:latest | \
  ssh follow@<hetzner-ip> 'docker load'
```

Then on the box, run `docker compose --profile prod up -d` **without** `--build`. The images are already loaded from the laptop transfer, and compose will use them as-is. The runbook (Task 38) should promote this as the default redeploy procedure.

If you do decide to build on the box anyway (first-time experimentation, laptop offline), tail the build output or `docker compose logs follow-image-gateway` — the 10-15 minute wait is not a hang.

**Migration safety**: On the FIRST deploy the database is empty and migrations apply cleanly. On every SUBSEQUENT redeploy that touches migrations (see runbook in Task 38), take a postgres backup BEFORE running `docker compose up -d --build` so a failed migration can be rolled back by restoring the dump. Add a line to the runbook: "Never redeploy a migration-touching change without a fresh backup in hand."

**Acceptance Criteria**

- All services reach healthy state.
- No restart loops.
- `docker compose ps` shows everything `Up (healthy)`.
- `follow-api` logs show migrations applied successfully (or "no migrations to apply" on re-deploys).

---

### Task 32: Verify Caddy got Let's Encrypt certs

**Story Points**: 2

**Description**

The first cert issuance is the most likely thing to fail because it depends on DNS, firewall, and Caddy all being correct simultaneously. Caddy will request three certs — one per hostname — so all three DNS records must be correct before this task can pass.

```bash
# From outside the box:
curl -v https://api.follow.example/health
curl -v https://upload.follow.example/health
curl -v https://download.follow.example/minio/health/live   # MinIO health endpoint

# Cert chain should be Let's Encrypt, NOT self-signed.
for host in api.follow.example upload.follow.example download.follow.example; do
  echo | openssl s_client -showcerts -servername "$host" -connect "$host":443 2>/dev/null \
    | openssl x509 -noout -issuer -subject
done
# Expected for each: issuer=C = US, O = Let's Encrypt, CN = R3 (or similar)
```

If issuance fails, common causes:
- DNS not propagated yet (Task 29) — wait 5-10 minutes.
- ufw blocking port 80 — Caddy needs HTTP-01 challenge access.
- Caddy proxied (orange) instead of DNS-only (grey) on the A records — flip them.
- Rate limit hit (Let's Encrypt has prod rate limits — use staging endpoint while debugging).

**Acceptance Criteria**

- All three hostnames serve valid Let's Encrypt certs.
- `curl https://api.follow.example/health` returns 200 OK.
- `curl https://upload.follow.example/health` returns 200 OK.
- `curl https://download.follow.example/minio/health/live` returns 200 OK (or whatever the MinIO live-health endpoint returns through Caddy).
- Caddy data volume contains cert files for all three hostnames (`docker compose exec caddy ls /data/caddy/certificates`).

---

### Task 33: Run cross-repo integration test suite against the live box

**Story Points**: 2

**Description**

The cross-repo integration tests in `tests/integration/` should be runnable against a remote target via `API_URL=https://api.follow.example GATEWAY_URL=https://upload.follow.example DOWNLOAD_URL=https://download.follow.example go test ./...`.

**Pre-flight check (do this first, NOT on the Hetzner box)**: verify the test harness actually honors these env vars and doesn't hardcode `localhost`, docker-compose service names (`postgres`, `valkey`, `minio`), or direct infrastructure access. If it does, either:

1. Skip the infra-level tests and run only the HTTP/API-level ones that don't need direct DB/Valkey/MinIO access, OR
2. Fix the harness to separate "API flow tests" (remote-safe) from "infrastructure tests" (local-only), then run only the remote-safe subset.

This pre-flight is the reason Task 33 can balloon — don't discover the hardcoded localhost while pointing at production. Start by reading `tests/integration/main_test.go` and the test harness bootstrap files alongside it (config loaders, shared test fixtures) to map which tests touch only HTTP endpoints versus which tests reach into postgres/valkey/MinIO directly. Grep for `localhost`, `postgres:`, `valkey:`, `minio:` inside `tests/integration/` — every hit is a potential remote-unsafe test.

Even just the minimum end-to-end flow — anonymous user creation → route creation → image upload → SSE status streaming → published route → image download — catches most deployment issues without needing full infra access.

**Acceptance Criteria**

- Pre-flight: harness either honors `API_URL`/`GATEWAY_URL` env vars for HTTP-level tests, or a documented subset of tests is identified as remote-safe.
- HTTP-level integration tests pass against the Hetzner box.
- No CORS, cert, or networking errors observed.
- Logs on the box show the test traffic arriving at `follow-api` and `follow-image-gateway`.

---

## Phase 6 — Cutover & cleanup

### Task 34: Verify backup cron actually fires (and alerts on failure)

**Story Points**: 2

**Description**

Either wait for the first nightly run (set the cron to run within the next hour to speed this up), OR trigger manually:

```bash
ssh follow@<hetzner-ip>
docker compose --profile prod exec backup /usr/local/bin/backup.sh run-now
# Then verify in Cloudflare dashboard that today's dump appeared in R2.
```

After the first manual verification, leave it overnight and confirm the next morning that the cron-triggered run also succeeded.

**Silent-failure protection is mandatory**. A backup that fails silently 6 weeks from now is indistinguishable from a working backup until you need to restore — and then it's too late. Pick one of these and implement it in this task, not later:

- **Option A (simplest)**: Have `backup.sh` write a "last success" timestamp to a known R2 object (`r2://follow-backups/_last_success.txt`) only after all steps succeed. Configure UptimeRobot (Task 37) as a keyword monitor against that public object URL — if the timestamp is older than 36 hours, UptimeRobot alerts. Zero extra infrastructure.
- **Option B**: Have `backup.sh` hit a [healthchecks.io](https://healthchecks.io) ping URL only on success. Their free tier sends an email/SMS if the expected ping doesn't arrive on schedule. Dead-simple, purpose-built for this.
- **Option C**: Have `backup.sh` exit non-zero on any failure and rely on the container's restart policy + Docker logging — but this only catches failures if you're actively reading logs, so do NOT pick this alone.

Options A and B are both cheap and correct. Pick one.

**Acceptance Criteria**

- Manual `run-now` succeeds against R2.
- Next-morning cron run succeeds (verified by R2 object timestamp).
- Backup logs visible via `docker compose logs backup`.
- Failure alerting in place: intentionally break the backup (e.g., wrong R2 credentials temporarily) and confirm an alert arrives within the configured window, then fix and re-run.

---

### Task 34b: Hetzner snapshot restore drill

**Story Points**: 1

**Description**

Task 30 enables Hetzner Automated Backups and Task 38 documents the restore procedure, but the restore path itself is never exercised. "The dashboard has a Restore button" is not the same as "we verified the snapshot actually boots, the stack comes up clean, and the data is intact." Run the drill once, on a throwaway box, before declaring the platform production-ready. This is the snapshot equivalent of Task 19 (R2 restore drill).

Prerequisites: Task 30 is done AND at least one automated snapshot has been taken (Hetzner's first nightly snapshot lands within 24 hours of enabling backups). Verify a snapshot exists in the dashboard before starting.

Process:

1. In the Hetzner Cloud dashboard, locate the most recent automated snapshot.
2. Click "Restore" → choose "Restore to a new server" (NOT "Restore to this server" — never overwrite the live box).
3. Pick the smallest size that still fits the stack (CPX21 is fine for a 10-minute test) and a location — doesn't matter which. Name it `follow-snapshot-drill`.
4. Wait for the new box to boot (~2-3 minutes).
5. SSH in as the `follow` user (keys from the snapshot carry over).
6. `docker compose --profile prod ps` — expect everything to come up `Up (healthy)` without any manual intervention. If services need any reconfiguration, the snapshot restore is effectively broken and that's a finding worth capturing in the runbook.
7. Smoke-test: `curl -k https://localhost/health` (or point `/etc/hosts` at the throwaway box and `curl` by hostname). Hit each service's health endpoint.
8. Spot-check a few database rows and MinIO objects exist — `docker compose exec postgres psql -U "$POSTGRES_USER" "$POSTGRES_DB" -c 'SELECT count(*) FROM routes;'` and `docker compose exec minio mc ls local/follow-images | head`.
9. Delete the throwaway box from the Hetzner dashboard.

Cost: roughly €0.02-0.05 for the ~30 minutes the test box exists. Negligible.

**Acceptance Criteria**

- Snapshot restore produces a running box without manual reconfiguration.
- All services reach healthy state.
- Smoke-test health endpoints return 200.
- Database and MinIO row/object counts are plausible (non-zero, match source within a few minutes of drift).
- Throwaway box deleted after verification — no lingering infrastructure.
- Runbook (Task 38) updated with any quirks discovered during the drill (e.g., "after restore, the Caddy cert cache may trigger a fresh ACME request on first boot" — whatever actually happens on your stack).

---

### Task 35: Manually test full Flutter app flow against Hetzner

**Story Points**: 2

**Description**

End-to-end manual smoke test from the deployed Flutter web app against the Hetzner backend. This is the final gate before declaring the box production-ready.

Test scenarios:

1. Open `https://app.follow.example` in a browser.
2. Create an anonymous user (POST /users/anonymous).
3. Prepare a route, create waypoints with 3-5 images.
4. Upload images via the gateway at `upload.follow.example`.
5. Watch SSE stream emit progress events (via `api.follow.example`).
6. Verify route transitions to `ready`.
7. Publish the route.
8. Open the route in navigation mode. Verify presigned download URLs point at `download.follow.example` (not `minio:9000` or `localhost`), return images successfully, and the `host` header matches what MinIO signed.
9. Test image replacement on a waypoint — full round-trip through upload hostname and download hostname.
10. Repeat the same flow on a real Android phone (not just web) — the phone must be able to reach all three hostnames from outside the LAN.

**Acceptance Criteria**

- All 10 scenarios pass.
- No errors in browser console.
- No errors in Hetzner logs.
- Presigned download URLs contain `https://download.follow.example/` (verify by copy-pasting one into a separate browser tab — should load the image directly).
- SSE events arrive in real time over `api.follow.example`.
- Image uploads go to `upload.follow.example` and are rejected above 10MB (per Caddy limit).

---

### Task 36: Drop fly.io

**Story Points**: 1

**Description**

ONLY after Task 35 passes. Stop and delete the fly.io app, cancel the fly.io subscription, remove fly.io secrets from any password manager entries (after archiving).

**Acceptance Criteria**

- fly.io app stopped.
- fly.io billing canceled.
- No remaining DNS records pointing at fly.dev.
- No remaining code references to fly.io (already cleaned in Task 21).

---

### Task 37: Set up basic monitoring

**Story Points**: 2

**Description**

Minimal viable monitoring for one box. Everything below is push-based or pull-from-outside — no MTA on the box, no Postfix, no relay.

1. **UptimeRobot** (free tier, 50 monitors) — HTTP(S) checks every 5 minutes on:
   - `https://api.follow.example/health`
   - `https://upload.follow.example/health`
   - `https://download.follow.example/minio/health/live`
   - `https://app.follow.example` (Pages)
   - **Backup last-success keyword monitor** (from Task 34) — watches the `_last_success.txt` R2 object, alerts if stale.
   Alert via email and SMS to the user.

2. **Disk space alert via healthchecks.io** — free tier, purpose-built for "ping me every N minutes or alert me". Add a tiny cron on the box:

   ```bash
   # /etc/cron.d/follow-disk-check — runs every 15 minutes
   */15 * * * * follow /usr/local/bin/disk-check.sh
   ```

   ```bash
   # /usr/local/bin/disk-check.sh
   #!/bin/sh
   set -eu
   USAGE=$(df --output=pcent / | tail -n1 | tr -dc '0-9')
   if [ "$USAGE" -lt 85 ]; then
     curl -fsS -m 10 --retry 3 "https://hc-ping.com/<healthchecks-uuid>" > /dev/null
   fi
   # If usage >=85% we intentionally do NOT ping, and healthchecks.io alerts
   # when the expected ping fails to arrive within the grace window.
   ```

   Configure the healthchecks.io check with a 15-minute period + 30-minute grace. No MTA setup required; alerts come from healthchecks.io by email (and optionally Slack/SMS/webhook).

3. **Certificate expiry early-warning check** — UptimeRobot only tells you the cert is broken *after* it expires (the HTTPS check starts failing). Caddy auto-renews 30 days before expiry, but silent renewal failures (Cloudflare DNS hiccup during HTTP-01, Let's Encrypt rate limit, corrupted cert store) won't surface until the cert is actually dead. A daily cert-expiry probe catches that window.

   Add a second healthchecks.io check ("cert-expiry") with a 1-day period + 2-day grace, and a daily cron on the box:

   ```bash
   # /etc/cron.d/follow-cert-check — runs daily at 04:00 UTC
   0 4 * * * follow /usr/local/bin/cert-check.sh
   ```

   ```bash
   # /usr/local/bin/cert-check.sh
   #!/bin/sh
   set -eu
   HOSTS="api.follow.example upload.follow.example download.follow.example"
   MIN_DAYS=15
   NOW=$(date -u +%s)
   for host in $HOSTS; do
     END=$(echo | openssl s_client -servername "$host" -connect "$host":443 2>/dev/null \
       | openssl x509 -noout -enddate 2>/dev/null \
       | sed 's/notAfter=//')
     [ -z "$END" ] && exit 1  # probe failed
     END_EPOCH=$(date -u -d "$END" +%s 2>/dev/null) || exit 1
     DAYS_LEFT=$(( (END_EPOCH - NOW) / 86400 ))
     [ "$DAYS_LEFT" -lt "$MIN_DAYS" ] && exit 1  # cert too close to expiry
   done
   curl -fsS -m 10 --retry 3 "https://hc-ping.com/<cert-check-uuid>" > /dev/null
   ```

   The script exits 0 (and pings) only when ALL three production hostnames have ≥15 days of validity remaining. Any probe failure, parse failure, or too-close-to-expiry cert causes the ping to be skipped, and healthchecks.io alerts via its grace window.

Future (post-MVP): Grafana Cloud free tier scrapes Prometheus metrics from `follow-api`'s `/health/metrics` endpoint. Out of scope for this plan.

**Acceptance Criteria**

- UptimeRobot configured with 5 monitors (api, upload, download, app, backup-last-success).
- Test alert verified (intentionally take a service down, confirm alert received).
- Backup alert verified per Task 34 (break backup temporarily, confirm alert fires).
- healthchecks.io disk-check wired up; intentionally fill the disk past 85% in a throwaway test (or stop the cron) and confirm the "missing ping" alert fires within the grace window.
- healthchecks.io cert-expiry check wired up; test by temporarily setting `MIN_DAYS` to 999 in the script (guaranteed to fail) and confirm the alert fires within the grace window.

---

### Task 38: Document the runbook

**Story Points**: 2

**Description**

Write `ai-docs/operations/hetzner-runbook.md` (new file). Contents:

- **⚠️ DANGER ZONE — commands that destroy data**: document explicitly, in red, at the top of the file:
  - `docker compose down -v` — deletes ALL named volumes (postgres_data, minio_data, valkey_data, caddy_data). NEVER run this on the box. Use `docker compose down` (without `-v`) or `docker compose stop` instead.
  - `docker volume rm` — same hazard, explicit.
  - `docker system prune -a --volumes` — same hazard, explicit.
  - `git checkout -- .` / `git reset --hard` on the box — can wipe uncommitted `.env` changes or local patches.
- **Box access**: SSH command, where keys live, how to reset SSH access if locked out.
- **Deploy / redeploy — DEFAULT PATH (laptop-built images)**:
  1. On the laptop: `docker compose build follow-image-gateway follow-api`
  2. `docker save follow-image-gateway:latest follow-api:latest | ssh follow@<hetzner-ip> 'docker load'`
  3. On the box: `cd /home/follow/follow && git pull && docker compose --profile prod up -d` (NO `--build` — images were already loaded). This is the primary procedure because it avoids the 10-15 minute on-box libvips rebuild on every redeploy.
- **Deploy / redeploy — FALLBACK PATH (on-box build)**: only when the laptop is unavailable. `git pull && docker compose --profile prod up -d --build`. Expect ~15 minutes.
- **Deploy / redeploy (migrations present)**: MANDATORY procedure — (1) trigger backup `docker compose --profile prod exec backup /usr/local/bin/backup.sh run-now` and verify the new dump exists in R2, (2) then the default or fallback redeploy path above, (3) verify `follow-api` logs show successful migration, (4) smoke-test the health endpoint. If migration fails, rollback procedure: stop services, restore the latest dump, `git checkout <prev-commit>`, bring up the previous version.
- **Logs**: where they live, how to tail, how to grep for errors, how rotation works. Note: `docker compose down` stops containers but preserves volumes — safe. `docker compose down -v` is the destructive variant — never run.
- **Restart a single service**: `docker compose restart follow-api`.
- **Update the Caddyfile**: edit, reload with `docker compose exec caddy caddy reload --config /etc/caddy/Caddyfile`.
- **Rotate JWT/Ed25519 keys**: procedure (generate new pair, update `.env`, restart, verify clients).
- **Restore — decision tree**: "What broke?" → pick the right restore mechanism:
  - **Logical bug (dropped table, deleted objects, bad data)** → use the R2 logical backup. Fast, surgical, preserves the rest of the host. Reference `scripts/RESTORE.md`.
  - **Whole host broken (filesystem corruption, failed OS upgrade, compromised box, rm -rf wrong dir)** → restore the Hetzner snapshot (Task 30). Fast (minutes), brings everything back atomically. After restore, verify `docker compose --profile prod up -d` boots clean and run a smoke test.
  - **Hetzner datacenter lost the disk entirely** → provision new box, follow Phase 4 from scratch, restore from R2. This is the worst case — expect hours of work.
- **Restore from R2 backup (logical)**: full procedure (postgres + MinIO + encrypted `.env`, with the exact `mc`, `pg_restore`, and `age --decrypt` commands). Reference `scripts/RESTORE.md` from Task 19.
- **Recover `.env` from encrypted backup** (from Task 13/14):
  1. `mc cp r2/<bucket>/env/<latest>.env.age ./env-backup.age`
  2. `age --decrypt -o .env.recovered env-backup.age` (prompts for `AGE_PASSPHRASE` — copy from password manager).
  3. `diff .env.recovered <current-env-on-box>` to confirm nothing unexpected diverged.
  4. `chmod 600 .env.recovered && mv .env.recovered .env` on the target box.
- **Restore from Hetzner snapshot**: dashboard procedure (select server → Backups tab → Restore). Note: rolls back the entire host to the snapshot time, including any config or code changes made after that snapshot. After restore, `git pull` to re-apply any newer committed changes and `docker compose --profile prod up -d`. The drill for this exact procedure ran once during Task 34b — any quirks found there must be written down here.
- **Emergency rollback**: `git checkout <prev-tag> && docker compose --profile prod up -d`. Note: if the rollback crosses a migration boundary, you must restore the database from a pre-migration dump FIRST — downgrading the app binary without downgrading the schema will break.
- **`MINIO_EXTERNAL_ENDPOINT` quick reference** (from Task 11b): laptop dev uses `localhost:9000` (with port), Hetzner uses `download.follow.example` (no port, no scheme, with `MINIO_USE_SSL=true`). Editing this on the box requires a `docker compose up -d follow-api` restart to pick up the new value.
- **When to switch MinIO backup from full mirror to incremental** (from Task 13):
  - Full `mc mirror` is fine up to ~10GB / ~5 min wall-clock.
  - Beyond that, switch to `mc mirror --newer-than` (incremental) or consider moving primary storage to R2 directly.
  - Document the current bucket size check: `docker compose exec minio mc du local/follow-images`.
- **Common failure modes and their fixes**: cert renewal failure (check cert-expiry alert from Task 37, then `docker compose logs caddy` for ACME errors), postgres OOM, valkey memory exhaustion, gateway pipeline stalled, backup cron silently broken (check the `_last_success.txt` timestamp from Task 34), disk full (log rotation + MinIO bucket bloat), `.env` corruption (recover from encrypted R2 backup per the procedure above).

Also write `scripts/RESTORE.md` documenting the restore drill from Task 19 in detail, including the `age --decrypt` step for the `.env` recovery.

**Acceptance Criteria**

- `ai-docs/operations/hetzner-runbook.md` exists, covers all bullets above.
- `scripts/RESTORE.md` exists, contains a tested restore procedure.
- Runbook has a clear "R2 restore vs Hetzner snapshot restore" decision tree.
- A second person (or future-you in 6 months) can recover from any of the documented failures using only the runbook.

---

## Risks & Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Let's Encrypt rate limit hit during debugging | Cert issuance blocked for 1 week | Use Let's Encrypt **staging** endpoint (`acme_ca https://acme-staging-v02.api.letsencrypt.org/directory` in Caddyfile) until issuance is reliable, then switch to prod |
| Cloudflare Pages domain handshake forgotten | App serves the wrong response | Task 4 explicitly calls out the two-sided handshake; verified before Phase 5 |
| Backup script silently broken | Customer data loss on host failure | Task 19 (restore drill) is mandatory and gates Phase 6; Task 34 adds failure alerting (last-success timestamp or healthchecks.io ping) |
| First-deploy env var typo | Stack fails to boot | Task 17 (local prod profile test) catches this before touching Hetzner |
| Postgres OOM under image processing load | DB crash, data loss risk | Task 6 (resource limits) caps everything; Task 24 defaults to CPX31 (8GB) with ~5.4 GB headroom over the measured 2.6 GiB peak (see `ai-docs/research/gateway-memory-benchmark.md`); gateway `malloc_trim` fix must be deployed |
| SSE long-lived connections die through Caddy | Status streaming broken in production | Task 22 verifies SSE locally through self-signed Caddy before Hetzner (includes Caddy buffering + idle timeout checks) |
| ufw blocks port 80, Let's Encrypt HTTP-01 fails | Cert issuance fails | Task 25 explicitly opens 80 and 443 |
| User locks themselves out via SSH hardening | Manual recovery via Hetzner console | Test the hardened SSH config from a SECOND terminal before closing the original session |
| Failed migration on redeploy leaves DB in broken state | Partial outage, possible data loss | Task 31 + runbook (Task 38) mandate a pre-migration backup and document the rollback-via-restore procedure |
| Accidental `docker compose down -v` wipes volumes | Total data loss | Runbook (Task 38) has a prominent "DANGER ZONE" section listing the destructive commands explicitly |
| `follow-image-gateway` first build takes 10-15 min, looks hung | Wasted time or panicked abort mid-build | Task 31 promotes `docker save \| ssh docker load` as the DEFAULT redeploy path; on-box `--build` is the explicit fallback; runbook (Task 38) documents both |
| Integration test harness hardcodes localhost | Task 33 blocked, needs harness rewrite mid-deploy | Task 33 pre-flight check reads `tests/integration/main_test.go` BEFORE running tests to identify remote-safe subset |
| Host broken by bad command or failed OS upgrade, R2 restore is slow | Hours of downtime during recovery | Task 30 enables Hetzner Automated Backups — disk-level snapshots restore the whole box in minutes, complementary to the logical R2 backups |
| Presigned download URL signature rejected (wrong host) | Images fail to download on production | Task 28 sets `MINIO_EXTERNAL_ENDPOINT=download.follow.example` and `MINIO_USE_SSL=true`; Task 35 verifies URLs contain the correct host |
| Caddy body size limit drifts from gateway config | Legitimate uploads rejected at proxy, or oversized payloads reach gateway | Task 10 mandates verifying gateway `MaxFileSize` before setting Caddy `max_size` |
| MinIO backup grows beyond viable full-mirror size | Nightly backup runtime balloons, R2 storage cost grows | Task 13 documents switch criteria (~10GB or ~5 min wall-clock) and alternative strategies; runbook (Task 38) surfaces the check command |
| Caddy gated on upstream health, blocks cert renewal when app is down | Cert expires silently during any extended app outage | Task 9 uses start-order-only `depends_on`, not `condition: service_healthy`, so Caddy always binds :80 for ACME |
| `mc mirror --remove` propagates a bad delete into R2 | Backup destroyed at the exact moment it is needed | Task 13 explicitly drops `--remove` and explains why; R2 lifecycle rule (Task 16) handles orphan cleanup instead |
| Valkey persistence assumed rather than verified | Routes stuck forever in PENDING if AOF is actually off | Task 8 is a verify-and-polish pass with a mandatory kill-restart drill that exercises the config; assumption never left untested |
| UFW configured for v4 only, IPv6 traffic reaches exposed ports | Loopback-bound services + UFW both bypassed on v6; public exposure of 8080/8090 | Task 25 sets `IPV6=yes` in `/etc/default/ufw` before adding rules; v6 deny verified from an outside box |
| Backup container reinstalls packages on every restart | Fragile, slow, depends on Alpine mirror availability mid-service | Task 12 builds `scripts/backup.Dockerfile` with `pg_dump` and `mc` baked in; entrypoint only starts `crond` |
| `mc` version drifts via Alpine package rebuilds | `mc mirror` flag semantics could change silently between redeploys | Task 12 pins `mc` to a specific `MC_RELEASE` downloaded from dl.min.io; bumps are explicit code changes |
| SSE breaks in production because Caddyfile was committed without streaming directives | Half-day of debugging on the box instead of on laptop | Task 10 Caddyfile includes `flush_interval -1` and long read/write timeouts from day one; Task 22 verifies end-to-end against the same committed file |
| `MINIO_EXTERNAL_ENDPOINT` interpolation composes `HOST_IP:PORT`, incompatible with the `download.follow.example` public hostname | Presigned download URLs embed `download.follow.example:9000` and fail on every client | Task 11b rewires the env var to direct pass-through; Task 28 sets the bare hostname; Task 35 verifies the embedded host in a real presigned URL |
| Host destroyed with `.env` secrets unrecoverable | Restored DB and MinIO bucket cannot be unlocked; JWTs issued before the incident cannot be verified | Task 13 adds encrypted `.env` backup to R2 using `age`; passphrase lives in the password manager; Task 28 mandates every secret also be stored in the password manager verbatim |
| Hetzner snapshot restore "just works" — assumed but never tested | First snapshot restore during a real outage reveals an unknown quirk | Task 34b runs the snapshot restore drill against a throwaway box before the platform is declared production-ready |
| Cert renewal fails silently, first signal is outage | Pilot customer sees TLS errors with no prior warning | Task 37 adds a daily cert-expiry probe via healthchecks.io that alerts 15+ days before expiry — well inside Caddy's 30-day renewal window |
| CPX21 OOMs under sustained gateway load | Host OOM killer kills the wrong process during routine bursts | Measured peak is 2.6 GiB extreme / 1.8 GiB realistic with `malloc_trim` fix; CPX21 (4GB) has 1.4-2.2 GiB headroom; live-resize to CPX31 in minutes if needed |
| On-box `docker compose build` takes 10-15 min per redeploy, blocks hot-fixes | Slow iteration, risk of panicked abort mid-build | Task 31 promotes `docker save \| ssh docker load` from laptop as the default redeploy path; runbook documents it as the primary procedure |

---

## Definition of Done

The plan is complete when:

- [ ] All 40 tasks marked done.
- [ ] Customer can access `https://app.follow.example` over HTTPS and complete the full route lifecycle (create, upload, navigate).
- [ ] All four production hostnames serve traffic with valid Let's Encrypt certs: `api`, `upload`, `download`, `app`.
- [ ] Presigned download URLs point at `download.follow.example` and work end-to-end from a phone.
- [ ] R2 logical backups (postgres + MinIO + encrypted `.env`) have run successfully for at least 2 consecutive nights, verified in R2.
- [ ] Hetzner Automated Backups enabled, first snapshot visible in dashboard.
- [ ] A test restore from the most recent R2 backup has been performed within the last 7 days (Task 19).
- [ ] A snapshot restore drill has been performed at least once against a throwaway box (Task 34b).
- [ ] fly.io is fully decommissioned.
- [ ] Monitoring alerts are firing on intentional outages (backup-last-success, disk-check, cert-expiry).
- [ ] Runbook exists and has been read end-to-end at least once.

---

## Out of Scope

Deliberately NOT in this plan (move to a separate plan when needed):

- High availability / multi-node deployment.
- Managed Postgres (RDS / CloudSQL / Hetzner Managed PostgreSQL).
- Managed object storage migration (S3, B2) — R2 stays as the backup target only; primary storage stays on self-hosted MinIO.
- Prometheus + Grafana metrics scraping.
- Distributed tracing.
- Image registry (CI build + push) — `docker compose build` on the host is acceptable for one box.
- Cloudflare orange-cloud proxying for the Hetzner endpoints (DNS-01 cert challenge required, defer until needed).
- Automated deployment pipeline (CI/CD) — manual `git pull && docker compose up -d` is fine for one box, one engineer.
- Staging environment.
- Load testing / capacity planning beyond informal smoke tests.

---

## Effort Summary

| Phase | Tasks | Story Points | Realistic Wall Time |
|-------|-------|--------------|---------------------|
| 1. Domain & DNS | 4 | 4 | 30-60 min |
| 2. Local hardening | 16 | 24 | 4-6 hours |
| 3. App code changes | 4 | 7 | 2-3 hours |
| 4. Hetzner provisioning | 7 | 8 | 1-2 hours |
| 5. Deploy & verify | 3 | 6 | 1-2 hours |
| 6. Cutover & cleanup | 6 | 10 | 3-4 hours |
| **Total** | **40** | **59** | **~3-4 days focused** |

Task-count and scope changes from the previous revision:
- **Removed**: Task 8 (REAPER env var cleanup) — already applied to `docker-compose.yml` out of band; no longer actionable.
- **Rewritten**: Task 8 is now the Valkey persistence verification pass (formerly numbered 8b). Persistence was already enabled in compose; this task reframes as verify-and-polish with a mandatory kill-restart drill rather than a new feature.
- **Added**: Task 11b (`MINIO_EXTERNAL_ENDPOINT` direct pass-through) to fix the bare-hostname problem in Phase 2.
- **Added**: Task 34b (Hetzner snapshot restore drill) in Phase 6 to exercise the snapshot restore path once before production.
- **Expanded**: Task 12 pins `mc` version via direct download, Task 13 adds encrypted `.env` backup to R2 using `age`, Task 20 rewritten with concrete file paths, Task 21 lists the four Flutter config files by path, Task 24 defaults to CPX31 after reconciling against Task 6 limits, Task 25 includes a minimal `fail2ban` config, Task 28 references the new `AGE_PASSPHRASE` and the Task 11b rewiring, Task 31 promotes laptop-side `docker save \| ssh docker load` as the default redeploy path, Task 37 adds a daily cert-expiry probe via healthchecks.io.

Story points: Task 22 (SSE through Caddy) remains at 3. Task 30 (Hetzner automated backups) is 1 SP. Task 8 (Valkey verification) is 1 SP. Task 11b (`MINIO_EXTERNAL_ENDPOINT` rewire) is 1 SP. Task 34b (snapshot restore drill) is 1 SP.

The realistic worst case is 4-5 days, with the extra day absorbed by debugging Let's Encrypt cert issuance (across three hostnames), CORS edge cases, the SSE-through-domain test, and verifying presigned download URLs land on the correct host. Everything else is mechanical.
