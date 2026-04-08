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

**Total tasks: 38.** Realistic effort: **3-4 focused days**, +1 day buffer for cert/CORS/SSE surprises.

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

A runaway upload or a query gone wrong can OOM the host. Cap memory per service so the kernel kills the offender, not postgres. Recommended starting points (tune after observing real load):

| Service | Memory limit | CPU limit |
|---------|--------------|-----------|
| postgres | 1g | 1.0 |
| valkey | 384m | 0.5 |
| minio | 768m | 1.0 |
| follow-api | 512m | 1.0 |
| follow-image-gateway | 1g (libvips + ONNX models) | 2.0 |
| caddy | 128m | 0.5 |

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

### Task 8: Move aggressive REAPER values out of the root compose

**Story Points**: 1

**Description**

Root compose currently sets `REAPER_SCAN_INTERVAL=1s` and `REAPER_STALE_THRESHOLD=2s` on `follow-api`. These are integration-test tunings (aggressive cleanup to keep tests fast) that bled into the root file. Running 1-second reaper scans under real load would melt the box — the reaper holds table locks during each scan and there's no business reason to scan more than once a minute in production.

**Action**:

1. Remove both env var overrides from `docker-compose.yml` — let the service use its own defaults from `follow-api/configs/config.yaml`.
2. Verify `follow-api/configs/config.yaml` has sane production defaults (target: `scan_interval: 60s`, `stale_threshold: 5m`). If not, update them.
3. Move the aggressive test values to `tests/integration/.env` (or wherever the integration harness keeps its env overrides) so tests still get their fast cleanup.

**Files Affected**

- `docker-compose.yml` — remove the two `REAPER_*` lines (around 130-131)
- `follow-api/configs/config.yaml` — verify/set production defaults
- `tests/integration/.env` (or equivalent) — move the 1s/2s values here

**Acceptance Criteria**

- Root `docker-compose.yml` no longer sets `REAPER_*` env vars.
- Production defaults in `config.yaml` are on the order of minutes, not seconds.
- Integration tests still pass with the moved values.

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
    follow-api:
      condition: service_healthy
    follow-image-gateway:
      condition: service_healthy
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
    reverse_proxy follow-api:8080
}

upload.follow.example {
    reverse_proxy follow-image-gateway:8090
    request_body {
        max_size 15MB
    }
}

download.follow.example {
    reverse_proxy minio:9000
}
```

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

### Task 11: Scope host port publishing to the dev profile

**Story Points**: 1

**Description**

In production, `follow-api` and `follow-image-gateway` must not be reachable from the public internet directly — only via Caddy. Caddy reaches them by service name on the internal compose network, so the `ports:` blocks that publish 8080 and 8090 to the host are only needed for laptop convenience.

**Approach**: extract the two app services' host port publishing into a separate override-style service definition under `profiles: [dev]`. The default (no-profile) `docker compose up -d` still gets ports for the laptop workflow; `docker compose --profile prod up -d` does not. Do NOT mix this with the `127.0.0.1` binding trick — pick one mechanism and stick with it.

The cleanest pattern is a small compose merge: keep the base service definition without `ports:`, and add a `ports:`-only fragment under `profiles: [dev]` for each service. Alternatively, leave `ports:` on the base service but use a `profiles: [dev]` top-level override if your compose version supports it cleanly.

**Files Affected**

- `docker-compose.yml`

**Acceptance Criteria**

- After deploy on Hetzner with `--profile prod`, `curl http://<hetzner-ip>:18080/health` from outside the box fails (connection refused or timeout).
- `curl https://api.follow.example/health` succeeds (via Caddy).
- Laptop dev workflow unchanged: `docker compose up -d` still exposes 8080/8090 (or the configured host ports) on localhost.
- Exactly one mechanism controls port publishing — no `127.0.0.1:` + `profiles:` dual config.

---

### Task 12: Add backup sidecar service under `profiles: [prod]`

**Story Points**: 2

**Description**

Add a backup container that runs on a cron, dumps postgres, mirrors MinIO to Cloudflare R2, and prunes old backups. Alpine base + `postgresql17-client` + `mc` (MinIO client). Mounted script handles the work.

```yaml
backup:
  image: alpine:3.21
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
    - POSTGRES_DB=${POSTGRES_DB}
    - MINIO_ACCESS_KEY_ID=${MINIO_ACCESS_KEY_ID}
    - MINIO_SECRET_ACCESS_KEY=${MINIO_SECRET_ACCESS_KEY}
  volumes:
    - ./scripts/backup.sh:/usr/local/bin/backup.sh:ro
  entrypoint: /bin/sh
  command: -c "apk add --no-cache postgresql17-client minio-client tzdata && /usr/local/bin/backup.sh install-cron && crond -f -d 8 -L /dev/stdout"
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

**Acceptance Criteria**

- `docker compose --profile prod up -d` starts the backup container.
- Container stays healthy (cron loop running).
- Manual `docker exec backup /usr/local/bin/backup.sh run-now` succeeds end-to-end.

---

### Task 13: Write `scripts/backup.sh`

**Story Points**: 2

**Description**

Two-mode script:

1. `install-cron` — writes a `/etc/crontabs/root` entry that runs the backup nightly (e.g., 03:00 UTC).
2. `run-now` — performs the actual backup:
   - `pg_dump -Fc -h postgres -U $POSTGRES_USER $POSTGRES_DB | gzip` → upload to `r2://$R2_BACKUP_BUCKET/postgres/YYYY-MM-DD-HHMMSS.dump.gz`
   - `mc alias set r2 $R2_ENDPOINT $R2_ACCESS_KEY $R2_SECRET_KEY`
   - `mc alias set local http://minio:9000 $MINIO_ACCESS_KEY_ID $MINIO_SECRET_ACCESS_KEY`
   - `mc mirror --overwrite --remove local/follow-images r2/$R2_BACKUP_BUCKET/minio/follow-images/`
   - After all steps succeed, write a last-success timestamp to `r2://$R2_BACKUP_BUCKET/_last_success.txt` for Task 34 monitoring.
   - Log success/failure to stdout (caught by Docker logging driver).

Use `set -euo pipefail`. Exit non-zero on any failure so the container restarts and the failure is visible in logs/monitoring.

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
- `_last_success.txt` is written only on full success (not on partial failure).
- Runbook (Task 38) contains the "when to switch from full mirror to incremental" decision criteria above.

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
```

**Files Affected**

- `.env.example`

**Acceptance Criteria**

- Variables added with placeholder values.
- Comment explains where to find R2 credentials in the Cloudflare dashboard.

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

For local cert testing, override the Caddyfile temporarily to use Caddy's `tls internal` directive (self-signed). Don't commit that change — it's a one-time local sanity check.

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

The CORS middleware in `follow-api` currently allows fly.io and laptop origins. Add the new Pages domain (`https://app.follow.example`). Find the config: search `internal/api/middleware/` and `configs/config.yaml` for the existing CORS allow-list.

Make sure SSE preflight headers are also covered — `/api/v1/routes/{id}/status/stream` is the most CORS-sensitive endpoint.

**Files Affected**

- `follow-api/configs/config.yaml` (or wherever the allow-list lives)
- Possibly `follow-api/internal/api/middleware/cors.go`

**Acceptance Criteria**

- New origin in the allow-list.
- A browser fetch from `https://app.follow.example` to `https://api.follow.example/api/v1/users/anonymous` succeeds with correct CORS headers (test in Phase 5).
- `go test -race -cover ./...` still passes.

---

### Task 21: Update Flutter app API endpoints

**Story Points**: 2

**Description**

Search `follow-app/` for any references to fly.io URLs and replace them with the new Hetzner-backed hostnames. Likely locations:

- `lib/data/network/` or similar
- `lib/core/config/`
- `.env` files for the Flutter build
- Any hardcoded base URLs in service classes

Use `grep -r 'fly.dev' follow-app/` (via the Grep tool) to catch them all.

**Files Affected**

- TBD — depends on grep results

**Acceptance Criteria**

- Zero references to `fly.dev` remain in `follow-app/`.
- Build configuration uses environment-specific base URLs (so dev still works against localhost).
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

# Ed25519 keypair (base64-encoded seed + public key)
python3 -c "
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from cryptography.hazmat.primitives import serialization
import base64
priv = Ed25519PrivateKey.generate()
seed = priv.private_bytes(
    encoding=serialization.Encoding.Raw,
    format=serialization.PrivateFormat.Raw,
    encryption_algorithm=serialization.NoEncryption(),
)
pub = priv.public_key().public_bytes(
    encoding=serialization.Encoding.Raw,
    format=serialization.PublicFormat.Raw,
)
print('FOLLOW_API_ED25519_PRIVATE_KEY=' + base64.b64encode(seed).decode())
print('FOLLOW_API_ED25519_PUBLIC_KEY=' + base64.b64encode(pub).decode())
"
```

Save the output in the password manager. Do NOT paste it into any committed file.

**Acceptance Criteria**

- Fresh JWT secret generated.
- Fresh Ed25519 keypair generated.
- All three values stored in the password manager, ready for Task 28.

---

## Phase 4 — Hetzner provisioning

### Task 24: Provision the Hetzner box

**Story Points**: 1

**Description**

Pick a server size at Hetzner Cloud. For MVP+ with one pilot customer:

Realistic RAM budget for the full stack (postgres ~1-2GB, image-gateway ~200-500MB with ML models loaded, follow-api ~50-100MB, valkey ~50-100MB, MinIO ~200-300MB, OS+Docker ~500MB) totals ~3-4GB at MVP traffic. The CPU-bursty workload is the gateway's image pipeline (libvips + ONNX inference) which runs in short bursts during route creation, then idles for hours.

Prefer the **CPX line (AMD EPYC)** over the CX line (Intel) — at the same price point, CPX gives more vCPUs and is better for the gateway's CPU-bursty workload.

- **CPX21** (3 vCPU AMD shared, 4GB RAM, 80GB SSD) — ~€8/month. Right-sized for MVP. Live-resizable to CPX31 with one click if image processing becomes a bottleneck.
- **CPX31** (4 vCPU AMD shared, 8GB RAM, 160GB SSD) — ~€11/month. Comfortable headroom; pick this if you want to size up once and not think about it.
- **CCX13** (2 vCPU dedicated, 8GB RAM) — only if pipeline benchmarks show actual sustained vCPU contention on shared CPUs. Roughly 2-3x the price for guaranteed cores. Skip unless evidence demands it.

Shared vCPU is fine for this workload: bursts are seconds long, the rest of the day is idle. Hetzner allows live resizing of CPX instances, so start small and scale up only if observed load demands it.

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
4. Install and configure `ufw`: allow 22 (SSH), 80, 443. Deny everything else inbound. Default-deny.
5. Install `unattended-upgrades` for automatic security patches.
6. Set timezone to UTC (`timedatectl set-timezone UTC`).
7. Optional but recommended: install `fail2ban` for SSH brute-force protection.

**Acceptance Criteria**

- `ssh follow@<hetzner-ip>` works.
- `ssh root@<hetzner-ip>` rejected.
- `ufw status` shows only 22, 80, 443 allowed.
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
#
# Hostname-related vars that must point at the public hostnames, NOT localhost:
# - MINIO_EXTERNAL_ENDPOINT=download.follow.example      (presigned download URLs)
# - MINIO_USE_SSL=true                                    (HTTPS via Caddy)
# - GATEWAY_BASE_URL=https://upload.follow.example        (presigned upload URLs)
# - HOST_IP=<hetzner-public-ipv4>                         (still useful for docker-compose
#                                                          variable fallbacks)
#
# POSTGRES_SSLMODE=disable is correct — postgres stays on the internal
# docker network, all traffic stays on the box, no TLS needed.

chmod 600 /home/follow/follow/.env
chown follow:follow /home/follow/follow/.env
```

**Acceptance Criteria**

- File exists with all required vars from `.env.example`.
- Permissions are `600`, owner `follow:follow`.
- `cat .env` as any other user fails.
- File is in `.gitignore` (verify it does not appear in `git status`).
- `MINIO_EXTERNAL_ENDPOINT`, `MINIO_USE_SSL=true`, and `GATEWAY_BASE_URL` all reference the production hostnames with `https://` scheme.

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

**Note on build time**: The first `docker compose build` for `follow-image-gateway` takes 10-15 minutes on a CPX21 because the Dockerfile builds libvips 8.18 from source and downloads ONNX Runtime. This is not a hang — tail `docker compose logs follow-image-gateway` or watch the build output. If you want to skip this on-box build entirely, build the image locally on your laptop and ship it:

```bash
# On laptop
docker compose build follow-image-gateway follow-api
docker save follow-image-gateway:latest follow-api:latest | \
  ssh follow@<hetzner-ip> 'docker load'
# Then on the box, use `docker compose up -d` (not `up -d --build`)
```

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

This pre-flight is the reason Task 33 can balloon — don't discover the hardcoded localhost while pointing at production. Find out in advance by reading `tests/integration/main_test.go` and config setup.

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

Minimal viable monitoring for one box:

1. **UptimeRobot** (free tier, 50 monitors) — HTTP(S) checks every 5 minutes on:
   - `https://api.follow.example/health`
   - `https://upload.follow.example/health`
   - `https://download.follow.example/minio/health/live`
   - `https://app.follow.example` (Pages)
   - **Backup last-success keyword monitor** (from Task 34) — watches the `_last_success.txt` R2 object, alerts if stale.
   Alert via email and SMS to the user.

2. **Disk space alert** — simple cron on the box that checks `df` and emails if any partition >85% full. (Or use UptimeRobot's keyword monitor on a custom `/health/disk` endpoint if one exists.)

3. **Log review habit** — once a week, `docker compose logs --since 7d | grep -i error`. Not automated yet, but on the calendar.

Future (post-MVP): Grafana Cloud free tier scrapes Prometheus metrics from `follow-api`'s `/health/metrics` endpoint. Out of scope for this plan.

**Acceptance Criteria**

- UptimeRobot configured with 5 monitors (api, upload, download, app, backup-last-success).
- Test alert verified (intentionally take a service down, confirm alert received).
- Backup alert verified per Task 34 (break backup temporarily, confirm alert fires).
- Disk space alert mechanism in place.

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
- **Deploy / redeploy (no migrations)**: `git pull && docker compose --profile prod up -d --build`.
- **Deploy / redeploy (migrations present)**: MANDATORY procedure — (1) trigger backup `docker compose --profile prod exec backup /usr/local/bin/backup.sh run-now` and verify the new dump exists in R2, (2) then `git pull && docker compose --profile prod up -d --build`, (3) verify `follow-api` logs show successful migration, (4) smoke-test the health endpoint. If migration fails, rollback procedure: stop services, restore the latest dump, `git checkout <prev-commit>`, bring up the previous version.
- **Logs**: where they live, how to tail, how to grep for errors, how rotation works. Note: `docker compose down` stops containers but preserves volumes — safe. `docker compose down -v` is the destructive variant — never run.
- **Restart a single service**: `docker compose restart follow-api`.
- **Update the Caddyfile**: edit, reload with `docker compose exec caddy caddy reload --config /etc/caddy/Caddyfile`.
- **Rotate JWT/Ed25519 keys**: procedure (generate new pair, update `.env`, restart, verify clients).
- **Restore — decision tree**: "What broke?" → pick the right restore mechanism:
  - **Logical bug (dropped table, deleted objects, bad data)** → use the R2 logical backup. Fast, surgical, preserves the rest of the host. Reference `scripts/RESTORE.md`.
  - **Whole host broken (filesystem corruption, failed OS upgrade, compromised box, rm -rf wrong dir)** → restore the Hetzner snapshot (Task 30). Fast (minutes), brings everything back atomically. After restore, verify `docker compose --profile prod up -d` boots clean and run a smoke test.
  - **Hetzner datacenter lost the disk entirely** → provision new box, follow Phase 4 from scratch, restore from R2. This is the worst case — expect hours of work.
- **Restore from R2 backup (logical)**: full procedure (postgres + MinIO, with the exact mc and pg_restore commands). Reference `scripts/RESTORE.md` from Task 19.
- **Restore from Hetzner snapshot**: dashboard procedure (select server → Backups tab → Restore). Note: rolls back the entire host to the snapshot time, including any config or code changes made after that snapshot. After restore, `git pull` to re-apply any newer committed changes and `docker compose --profile prod up -d`.
- **Emergency rollback**: `git checkout <prev-tag> && docker compose --profile prod up -d`. Note: if the rollback crosses a migration boundary, you must restore the database from a pre-migration dump FIRST — downgrading the app binary without downgrading the schema will break.
- **When to switch MinIO backup from full mirror to incremental** (from Task 13):
  - Full `mc mirror` is fine up to ~10GB / ~5 min wall-clock.
  - Beyond that, switch to `mc mirror --newer-than` (incremental) or consider moving primary storage to R2 directly.
  - Document the current bucket size check: `docker compose exec minio mc du local/follow-images`.
- **Common failure modes and their fixes**: cert renewal failure, postgres OOM, valkey memory exhaustion, gateway pipeline stalled, backup cron silently broken (check the last-success timestamp from Task 34), disk full (log rotation + MinIO bucket bloat).

Also write `scripts/RESTORE.md` documenting the restore drill from Task 19 in detail.

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
| Postgres OOM under image processing load | DB crash, data loss risk | Task 6 (resource limits) caps everything; CPX21/CPX31 has headroom for MVP load, live-resizable if not |
| SSE long-lived connections die through Caddy | Status streaming broken in production | Task 22 verifies SSE locally through self-signed Caddy before Hetzner (includes Caddy buffering + idle timeout checks) |
| ufw blocks port 80, Let's Encrypt HTTP-01 fails | Cert issuance fails | Task 25 explicitly opens 80 and 443 |
| User locks themselves out via SSH hardening | Manual recovery via Hetzner console | Test the hardened SSH config from a SECOND terminal before closing the original session |
| Failed migration on redeploy leaves DB in broken state | Partial outage, possible data loss | Task 31 + runbook (Task 38) mandate a pre-migration backup and document the rollback-via-restore procedure |
| Accidental `docker compose down -v` wipes volumes | Total data loss | Runbook (Task 38) has a prominent "DANGER ZONE" section listing the destructive commands explicitly |
| `follow-image-gateway` first build takes 10-15 min, looks hung | Wasted time or panicked abort mid-build | Task 31 documents the build time and the optional `docker save \| ssh docker load` escape hatch |
| Integration test harness hardcodes localhost | Task 33 blocked, needs harness rewrite mid-deploy | Task 33 pre-flight check reads `tests/integration/main_test.go` BEFORE running tests to identify remote-safe subset |
| Host broken by bad command or failed OS upgrade, R2 restore is slow | Hours of downtime during recovery | Task 30 enables Hetzner Automated Backups — disk-level snapshots restore the whole box in minutes, complementary to the logical R2 backups |
| Presigned download URL signature rejected (wrong host) | Images fail to download on production | Task 28 sets `MINIO_EXTERNAL_ENDPOINT=download.follow.example` and `MINIO_USE_SSL=true`; Task 35 verifies URLs contain the correct host |
| Caddy body size limit drifts from gateway config | Legitimate uploads rejected at proxy, or oversized payloads reach gateway | Task 10 mandates verifying gateway `MaxFileSize` before setting Caddy `max_size` |
| MinIO backup grows beyond viable full-mirror size | Nightly backup runtime balloons, R2 storage cost grows | Task 13 documents switch criteria (~10GB or ~5 min wall-clock) and alternative strategies; runbook (Task 38) surfaces the check command |

---

## Definition of Done

The plan is complete when:

- [ ] All 38 tasks marked done.
- [ ] Customer can access `https://app.follow.example` over HTTPS and complete the full route lifecycle (create, upload, navigate).
- [ ] All four production hostnames serve traffic with valid Let's Encrypt certs: `api`, `upload`, `download`, `app`.
- [ ] Presigned download URLs point at `download.follow.example` and work end-to-end from a phone.
- [ ] R2 logical backups have run successfully for at least 2 consecutive nights, verified in R2.
- [ ] Hetzner Automated Backups enabled, first snapshot visible in dashboard.
- [ ] A test restore from the most recent R2 backup has been performed within the last 7 days.
- [ ] fly.io is fully decommissioned.
- [ ] Monitoring alerts are firing on intentional outages (including backup-last-success keyword monitor).
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
| 2. Local hardening | 15 | 23 | 4-6 hours |
| 3. App code changes | 4 | 7 | 2-3 hours |
| 4. Hetzner provisioning | 7 | 8 | 1-2 hours |
| 5. Deploy & verify | 3 | 6 | 1-2 hours |
| 6. Cutover & cleanup | 5 | 9 | 2-3 hours |
| **Total** | **38** | **57** | **~3-4 days focused** |

Story points bumped: Task 22 (SSE through Caddy) raised from 2 to 3 to reflect the realistic debugging surface (Caddy buffering, CORS preflight on EventSource, idle timeout, HTTP/2 vs HTTP/1.1). Task 30 (Hetzner automated backups) added to Phase 4 at 1 SP.

The realistic worst case is 4-5 days, with the extra day absorbed by debugging Let's Encrypt cert issuance (now across three hostnames), CORS edge cases, the SSE-through-domain test, the first `follow-image-gateway` Docker build on the Hetzner box, and verifying presigned download URLs land on the correct host. Everything else is mechanical.
