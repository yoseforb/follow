# Home PC Deployment Plan (Cloudflare Tunnel)

**Status**: Active
**Scope**: Cross-repo — root `docker-compose.yml` (cloudflared service addition, Caddy removal), `.env` additions, `follow-api` CORS/trusted-proxy config, `follow-app` API endpoint config
**Goal**: Deploy the full Follow platform from the home PC using Cloudflare Tunnel for public ingress, bypassing the blocked Hetzner account. No firewall changes. No exposed ports. No TLS management.

---

## Context

Hetzner account verification is blocked. The home PC is far stronger than the target Hetzner CX21 (i7-11700, 128 GiB RAM, 4 TB NVMe SSD vs. 2 vCPU, 4 GiB, 40 GB). The ML inference workload (SCRFD + YOLOv11) actually benefits from the i7-11700's single-core performance. 300/30 Mbps fiber is adequate for MVP traffic. A 1200W UPS enables 24/7 operation.

Phase 2 of the Hetzner plan is already complete: `docker-compose.yml` has log rotation, resource limits, healthchecks, 127.0.0.1-bound ports, backup sidecar (age-encrypted `.env` to R2, pg_dump, MinIO mirror), and Caddy under `profiles: [prod]`. The compose file is production-ready. What this plan adds is:

1. Swap Caddy for cloudflared as the public ingress mechanism.
2. DNS hosting migration (livedns → Cloudflare, registrar stays at livedns).
3. CORS and trusted-proxy configuration for Cloudflare edge IPs.
4. App endpoint wiring.
5. Operational runbook for a home-hosted service.

### Why Cloudflare Tunnel

- cloudflared is outbound-only: the daemon dials out to Cloudflare's edge. No inbound ports need opening. The existing iptables ruleset (default-DROP INPUT, fwknop SPA for SSH) requires zero changes. This is the primary architectural advantage.
- Dynamic public IP is irrelevant: the tunnel auto-reconnects regardless of IP change.
- TLS is handled at the Cloudflare edge: no ACME, no cert renewal, no Let's Encrypt rate limits.
- Free tier limits are acceptable for MVP: 100 MB request body limit (phone images are 3–15 MB), no bandwidth cap on egress.

### Architecture after this plan

```
                        Cloudflare Edge (TLS termination)
                                      |
         +----------------------------+----------------------------+
         |                            |                            |
  api.follow-nav.com          gateway.follow-nav.com        (MinIO — internal only,
  HTTPS → cloudflared         HTTPS → cloudflared          no public hostname needed)
         |                            |
         v                            v
   Home PC (Docker internal network)
         |                            |
   follow-api:8080          follow-image-gateway:8090
         |
   postgres, valkey, minio (127.0.0.1-bound, never public)
         |
   backup sidecar → Cloudflare R2 (outbound HTTPS, unchanged)
```

**Hostname → service mapping**:

| Hostname | Purpose | Traffic shape |
|----------|---------|---------------|
| `api.follow-nav.com` | REST API, SSE status streaming | cloudflared → `follow-api:8080` |
| `gateway.follow-nav.com` | Image upload from mobile app | cloudflared → `follow-image-gateway:8090` |
| MinIO downloads | Presigned URLs via follow-api | See MinIO note below |

**MinIO presigned URL decision**: Cloudflare's free plan has a 100 MB request size limit on proxied traffic, but presigned download URLs are GET responses — no request body. The constraint is Cloudflare's free Workers/Cache behaviour on large object downloads. For MVP, the simplest approach is to set `MINIO_EXTERNAL_ENDPOINT` to `gateway.follow-nav.com` and handle MinIO download paths through a separate tunnel hostname, OR (simpler) use a third tunnel hostname `storage.follow-nav.com` → `minio:9000`. This plan uses the third hostname. See Phase 3, Task 9.

### SSE keep-alive: already solved

The SSE streamer (`follow-api/internal/infrastructure/streaming/sse_streamer.go`) sends a `heartbeat` event every **30 seconds** (`defaultHeartbeatSeconds = 30`). Cloudflare's proxy idle timeout is 100 seconds. The 30-second heartbeat fires three times before Cloudflare would time out an idle connection. The maximum stream duration is 2 minutes (`defaultMaxDurationMinutes = 2`). No code change is required. This is documented here because it was a pre-flight concern.

### Reuse from Hetzner plan

The following is fully reused without replanning. See `hetzner-mvp-deployment-plan.md` Phase 2 for implementation details:

- docker-compose.yml service definitions (postgres, valkey, minio, createbuckets, follow-api, follow-image-gateway)
- Log rotation, resource limits, healthchecks, start_period tuning
- 127.0.0.1-bound host ports
- Backup sidecar (pg_dump → R2, MinIO mirror → R2, age-encrypted `.env` → R2)
- Volume management and restart policies
- Image building / Dockerfiles
- `.env` structure and `.env.example`

### Deployment philosophy

Same as the Hetzner plan. Single `docker-compose.yml` with `profiles: [prod]` for prod-only services. No separate compose files for home vs. Hetzner. When Hetzner verification clears, migration is: copy `.env`, run `docker compose --profile prod up -d` on the Hetzner box, update the Cloudflare Tunnel target to point at the Hetzner internal IPs. See Phase 6.

---

## Planning Order

Four phases. Each must complete before the next starts.

1. **Phase 1**: DNS migration (livedns → Cloudflare DNS hosting)
2. **Phase 2**: Cloudflare Tunnel setup and docker-compose wiring
3. **Phase 3**: Application config changes (CORS, trusted proxies, app endpoints)
4. **Phase 4**: Go-live, verification, and operational runbook

**Total tasks: 18.** Realistic effort: **1–2 focused days** from zero to first live request.

---

## Phase 1 — DNS Setup ✅ (Domain on Cloudflare Registrar)

**Status**: Tasks 1 and 2 are **N/A**. `follow-nav.com` was purchased directly from Cloudflare Registrar, so the zone is already active on Cloudflare DNS with no nameserver migration needed. Only Task 3 (initial DNS records) applies.

**Note**: The original plan assumed the domain was at a third-party registrar (livedns.co.il) and required a nameserver migration. Cloudflare Registrar skips that entire step — zones bought through Cloudflare are Active immediately.

---

### Task 1: ~~Add domain to Cloudflare as a free zone~~ ✅ Done automatically

Skipped. Cloudflare Registrar domains are pre-attached to the account as Active zones at purchase time.

Verify in dashboard: `follow-nav.com` should appear under Websites with status **Active**.

---

### Task 2: ~~Update nameservers at external registrar~~ ✅ N/A

Skipped. No external registrar to update — Cloudflare is both the registrar and DNS host.

---

### Task 3: Add DNS records in Cloudflare

**Story Points**: 1

**Description**

Create the production DNS records. Tunnel hostnames are added by Cloudflare automatically in Phase 2 (Task 5), but the Flutter web app record and apex redirect should be set now.

| Subdomain | Type | Target | Cloudflare Proxy |
|-----------|------|--------|------------------|
| `app.follow-nav.com` | CNAME | Cloudflare Pages target | Proxied (orange) |
| apex `follow-nav.com` | redirect rule or CNAME | `app.follow-nav.com` | Proxied |

The `api`, `gateway`, and `storage` subdomains will be created automatically by `cloudflared` tunnel configuration in Phase 2. Do not create them manually — Cloudflare will own those records.

**Acceptance Criteria**

- `app.follow-nav.com` resolves correctly and Cloudflare Pages serves the Flutter web build.
- `https://follow-nav.com` redirects to `https://app.follow-nav.com`.

---

## Phase 2 — Cloudflare Tunnel Setup

**Goal**: Create the tunnel, add cloudflared to docker-compose, remove Caddy from the home-PC workflow, wire tunnel hostnames to internal services.

---

### Task 4: Create the Cloudflare Tunnel in Zero Trust dashboard

**Story Points**: 1

**Description**

The tunnel is created once in Cloudflare's dashboard. The resulting token is stored in `.env` and used by the cloudflared container.

1. Go to [one.dash.cloudflare.com](https://one.dash.cloudflare.com) → Networks → Tunnels → Create a tunnel.
2. Select "Cloudflared" connector type.
3. Name the tunnel `follow-home` (descriptive, not secret).
4. Copy the tunnel token shown on the "Install connector" page. This is a long base64 string starting with `eyJ...`. Store it as `CLOUDFLARE_TUNNEL_TOKEN` in the host `.env` file (not `.env.example`).
5. Do NOT install the system service via the dashboard instructions — Docker Compose manages the daemon instead.
6. Continue to the "Public Hostnames" tab after the token is saved. Configure three public hostnames:

| Subdomain | Domain | Service | Path |
|-----------|--------|---------|------|
| `api` | `follow-nav.com` | HTTP `follow-api:8080` | (none) |
| `gateway` | `follow-nav.com` | HTTP `follow-image-gateway:8090` | (none) |
| `storage` | `follow-nav.com` | HTTP `minio:9000` | (none) |

Service type is **HTTP** (not HTTPS) because TLS terminates at the Cloudflare edge; internal traffic travels over the Docker bridge network unencrypted, which is correct and safe.

Cloudflare will automatically create three CNAME DNS records pointing each subdomain at `<tunnel-id>.cfargotunnel.com` with the orange-cloud (proxied) state.

**Acceptance Criteria**

- Tunnel `follow-home` appears in Zero Trust dashboard with status "Inactive" (no connector running yet).
- Three public hostnames are configured in the dashboard.
- Three CNAME records (`api`, `gateway`, `storage`) appear in the Cloudflare DNS zone, each pointing at `<tunnel-id>.cfargotunnel.com`, proxied.
- `CLOUDFLARE_TUNNEL_TOKEN` is written to the host `.env` (not committed).

---

### Task 5: Add cloudflared service to docker-compose.yml

**Story Points**: 2

**Description**

Add the cloudflared container under `profiles: [prod]`. Remove or repurpose Caddy. The cloudflared daemon establishes outbound connections to Cloudflare's edge and proxies inbound requests to internal services by service name on the Docker bridge network.

**Remove Caddy from the home-PC prod profile**

Caddy solved two problems on Hetzner: TLS termination and reverse proxying. Cloudflare Tunnel solves both differently (TLS at the edge, hostname routing in Zero Trust config). Keeping Caddy adds a hop, consumes memory, and requires maintaining a Caddyfile that the tunnel makes redundant. Remove it for the home-PC deployment.

The `caddy_data` and `caddy_config` volumes should be removed from the volumes block as well, since Caddy no longer runs. The `Caddyfile` and `Caddyfile.local` files stay in the repo as the Hetzner-path artifacts — do not delete them.

**cloudflared service definition**:

```yaml
cloudflared:
  image: cloudflare/cloudflared:latest
  profiles: [prod]
  restart: unless-stopped
  command: tunnel --no-autoupdate run
  environment:
    - TUNNEL_TOKEN=${CLOUDFLARE_TUNNEL_TOKEN}
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
        cpus: "0.5"
```

**Why `--no-autoupdate`**: auto-update rewrites the running binary mid-operation and is incompatible with Docker containers (the update mechanism expects a systemd service or a writable filesystem in specific locations). Pin the image version explicitly (see note below) and update by rebuilding.

**Image version pinning**: `cloudflare/cloudflared:latest` is acceptable for MVP but consider pinning to a specific release tag (e.g., `cloudflare/cloudflared:2025.4.0`) once the stack is stable. Treat version bumps as deliberate changes. For now, `latest` is fine — cloudflared has an excellent stability record and breaking changes are rare.

**Network access**: cloudflared must be on the `internal` network so it can reach `follow-api:8080`, `follow-image-gateway:8090`, and `minio:9000` by service name. It does not need host network access because all egress goes over the standard Docker bridge.

**Volumes block cleanup**: remove `caddy_data` and `caddy_config` from the top-level `volumes:` block.

**Files Affected**

- `docker-compose.yml` — add `cloudflared` service under `profiles: [prod]`, remove `caddy` service and caddy volumes.

**Acceptance Criteria**

- `docker compose --profile prod up -d` starts cloudflared, does NOT start caddy.
- `docker compose up -d` (no profile) starts no prod-only services.
- `docker compose logs cloudflared` shows the tunnel connecting: lines containing `Connection established` or `Registered tunnel connection`.
- Zero Trust dashboard shows tunnel `follow-home` status changes from "Inactive" to "Healthy".
- `curl https://api.follow-nav.com/health` from a machine that is NOT the home PC returns HTTP 200.
- `curl https://gateway.follow-nav.com/health` returns HTTP 200.

---

### Task 6: Add `CLOUDFLARE_TUNNEL_TOKEN` to `.env.example`

**Story Points**: 1

**Description**

Document the new env var. The actual token must never be committed.

```bash
# ── Cloudflare Tunnel ────────────────────────────────────────────
# Tunnel token from Zero Trust dashboard → Networks → Tunnels →
# <tunnel name> → Configure → Install connector. The token is a
# long base64 string. Keep it in the password manager.
# Generate a new one: Zero Trust → Tunnels → <tunnel> → Configure
# → Rotate token.
CLOUDFLARE_TUNNEL_TOKEN=<tunnel-token-from-zero-trust-dashboard>
```

**Files Affected**

- `.env.example`

**Acceptance Criteria**

- `.env.example` contains the `CLOUDFLARE_TUNNEL_TOKEN` entry with a comment.
- The actual token does not appear in any committed file.

---

### Task 7: Set `MINIO_EXTERNAL_ENDPOINT` for home-PC prod

**Story Points**: 1

**Description**

The Hetzner plan (Task 11b, already done) changed `MINIO_EXTERNAL_ENDPOINT` to pass through directly from `.env`. For the home PC, set it in the host `.env`:

```bash
MINIO_EXTERNAL_ENDPOINT=storage.follow-nav.com
MINIO_USE_SSL=false            # internal: follow-api ↔ minio:9000 is plain HTTP on Docker network
MINIO_EXTERNAL_USE_SSL=true    # external: presigned URLs emit https://storage.follow-nav.com
```

**Why two flags**: follow-api creates two MinIO clients from the config — an internal client that connects to `minio:9000` on the private Docker network (plain HTTP, no cert), and a presign client that generates URLs for end-user clients (HTTPS via Cloudflare Tunnel). With Cloudflare terminating TLS at the edge, these two values must differ. `MINIO_EXTERNAL_USE_SSL` defaults to `MINIO_USE_SSL` when unset, so existing Caddy-in-front deployments (where both were `true`) remain unchanged. The split mirrors the existing `MINIO_ENDPOINT` / `MINIO_EXTERNAL_ENDPOINT` pattern.

**follow-image-gateway is unaffected** — it only does internal `PutObject`, never generates presigned URLs for users, so it reads `MINIO_USE_SSL=false` alone and is correct.

With `MINIO_EXTERNAL_USE_SSL=true`, the MinIO Go SDK infers port 443 from the bare hostname. Presigned download URLs issued by follow-api will embed `https://storage.follow-nav.com/...`. These URLs are given to the Flutter app, which downloads images directly — the download goes through the `storage` tunnel hostname to internal MinIO.

**No code change required** at this point — the code split was already landed in follow-api. These are `.env` values set on the host.

**Acceptance Criteria**

- `follow-api` logs show presigned URLs starting with `https://storage.follow-nav.com/`.
- The Flutter app successfully downloads a route image using a presigned URL issued by the running follow-api.
- `curl -L "https://storage.follow-nav.com/<presigned-url-path>"` from an external machine downloads the image binary.

---

## Phase 3 — Application Config Changes

**Goal**: Update CORS, trusted proxy IPs, and the Flutter app's API endpoint constants so the platform works correctly behind Cloudflare's edge.

---

### Task 8: Configure CORS origins in follow-api for production hostnames

**Story Points**: 1

**Description**

follow-api's CORS middleware must allow requests from `https://app.follow-nav.com` (Flutter web) and `https://gateway.follow-nav.com` is an upload target (not a browser origin, so no CORS required there). The mobile app sends no Origin header, so mobile traffic is unaffected.

Check the current CORS configuration:

```bash
grep -r "CORS\|cors\|AllowedOrigins\|allowed_origins" \
  follow-api/internal/ follow-api/design/ --include="*.go" -l
```

Locate where origins are configured (likely `internal/api/server/` or `internal/shared/config/`). Add `https://app.follow-nav.com` to the allowed origins list.

If CORS origins are hardcoded, move them to an env var `CORS_ALLOWED_ORIGINS` (comma-separated) so the same binary works on both laptop and production without recompilation.

**Files Affected**

- Wherever CORS origins are configured in `follow-api` (grep to locate exact file).
- `.env.example` if the env var approach is adopted.

**Acceptance Criteria**

- Browser request from `https://app.follow-nav.com` to `https://api.follow-nav.com/api/v1/routes` returns `Access-Control-Allow-Origin: https://app.follow-nav.com`.
- Preflight OPTIONS request returns HTTP 204 with the correct CORS headers.
- Laptop dev workflow unchanged (localhost origins still allowed in development mode).

---

### Task 9: Configure trusted proxy IPs for Cloudflare edge

**Story Points**: 2

**Description**

Cloudflare proxies all tunnel traffic through its edge servers and adds `CF-Connecting-IP` (original client IP) and `X-Forwarded-For` headers. follow-api's request logging and any rate-limiting that uses client IP must trust these headers only from Cloudflare's IP ranges, not from arbitrary callers.

If follow-api uses a standard reverse proxy trust mechanism (common in Go HTTP frameworks), configure it to trust Cloudflare's published IP ranges.

**Step 1**: Check if follow-api uses `X-Forwarded-For` or `X-Real-IP` for client IP extraction:

```bash
grep -r "X-Forwarded-For\|X-Real-IP\|CF-Connecting-IP\|RemoteAddr\|TrustProxy\|trusted" \
  follow-api/internal/ --include="*.go" -l
```

**Step 2**: Cloudflare publishes its IP ranges at:
- IPv4: https://www.cloudflare.com/ips-v4
- IPv6: https://www.cloudflare.com/ips-v6

These change infrequently. For MVP, hardcoding the list as a config value is acceptable. Cloudflare also sends the `CF-Connecting-IP` header with the true client IP regardless of trusted-proxy config — this can be used directly if the framework doesn't have a trust mechanism.

**Step 3**: If rate limiting uses `RemoteAddr` instead of the forwarded IP, the rate limiter will see Cloudflare edge IPs (a handful of shared IPs) instead of client IPs, making rate limiting ineffective per-client. Fix by using `CF-Connecting-IP` or the trusted `X-Forwarded-For` chain.

Add a `TRUSTED_PROXY_CIDRS` env var (comma-separated CIDR list) if not already present.

**Files Affected**

- follow-api — rate limit middleware or request logging middleware that reads client IP (grep to identify exact files).
- `.env.example` if env var is added.

**Acceptance Criteria**

- Request logs show the real client IP (not a Cloudflare edge IP like `104.x.x.x`).
- Rate limiting counts per-client correctly when requests flow through the tunnel.
- No regression in the rate-limiting middleware tests.

---

### Task 10: Update `GATEWAY_BASE_URL` for home-PC prod

**Story Points**: 1

**Description**

`GATEWAY_BASE_URL` is used by follow-api to construct the upload URL embedded in `create-waypoints` and `replace-image/prepare` responses. The Flutter app uses this URL to PUT images directly to follow-image-gateway.

In the home-PC prod `.env`:

```bash
GATEWAY_BASE_URL=https://gateway.follow-nav.com
```

The trailing `/api/v1/upload` path is appended by follow-api's gateway URL generator (`follow-api/internal/infrastructure/gateway/`). Confirm the generator does not hardcode the scheme or port.

**No code change expected** — the env var was already wired through docker-compose.yml in Phase 2 of the Hetzner plan.

**Acceptance Criteria**

- A `create-waypoints` response contains `upload_url` values starting with `https://gateway.follow-nav.com/`.
- The Flutter app can PUT an image to that URL and receive HTTP 200 from the gateway.

---

### Task 11: Update Flutter app API endpoint constants

**Story Points**: 1

**Description**

The Flutter app has API base URL constants that need to point at the production hostnames. Locate the config file:

```bash
grep -r "localhost\|fly\.io\|BASE_URL\|apiUrl\|baseUrl\|kApiBase" \
  follow-app/lib/ --include="*.dart" -l
```

Update the production values:

| Constant | Value |
|----------|-------|
| API base URL | `https://api.follow-nav.com` |
| Gateway base URL | `https://gateway.follow-nav.com` |

Ensure there is a clear dev/prod split (likely already exists given the MVVM plan). Do not hardcode the production URL as the only value — the dev workflow (localhost) must still work.

**Files Affected**

- follow-app — wherever API base URLs are configured (grep to locate exact file).

**Acceptance Criteria**

- `flutter analyze` passes with no errors.
- A production build of the app targets `https://api.follow-nav.com`.
- A debug build still targets `http://localhost:8080` (or the configured dev URL).
- The app successfully creates an anonymous user via `POST https://api.follow-nav.com/api/v1/users/anonymous`.

---

## Phase 4 — Go-Live, Verification, and Operations

**Goal**: Start the full prod stack, verify the end-to-end flow, confirm backup works from the home machine, and establish the operational runbook.

---

### Task 12: Configure the host `.env` for prod

**Story Points**: 1

**Description**

The host `.env` (not committed, `chmod 600`) needs the following prod-specific values set or confirmed. Use the password manager as the source of truth for secrets.

```bash
# Production overrides (on top of the dev defaults)
MINIO_EXTERNAL_ENDPOINT=storage.follow-nav.com
MINIO_USE_SSL=false            # internal HTTP to minio:9000
MINIO_EXTERNAL_USE_SSL=true    # external HTTPS for presigned URLs
GATEWAY_BASE_URL=https://gateway.follow-nav.com
CLOUDFLARE_TUNNEL_TOKEN=<from-zero-trust-dashboard>

# These should already be set from Phase 2 Hetzner work:
# R2_ENDPOINT, R2_ACCESS_KEY, R2_SECRET_KEY, R2_BACKUP_BUCKET
# AGE_KEY (age public key for .env encryption)
# JWT_SECRET, FOLLOW_API_ED25519_PRIVATE_KEY, FOLLOW_API_ED25519_PUBLIC_KEY
# POSTGRES_USER, POSTGRES_PASSWORD, POSTGRES_DB
# MINIO_ROOT_USER, MINIO_ROOT_PASSWORD, MINIO_ACCESS_KEY_ID, MINIO_SECRET_ACCESS_KEY
# MINIO_BUCKET_NAME
```

**Acceptance Criteria**

- `.env` exists, `chmod 600`, owned by the running user.
- `docker compose config` resolves all variables without warnings about missing values.

---

### Task 13: First prod start and smoke test

**Story Points**: 2

**Description**

Start the full stack and verify every service reaches healthy state.

```bash
# Build images first
docker compose --profile prod build

# Start everything
docker compose --profile prod up -d

# Watch health convergence
docker compose ps
docker compose logs -f --tail=50
```

Wait for all services to report `healthy` in `docker compose ps`. The gateway takes up to 30 seconds (libvips + ONNX model loading).

**Smoke test sequence**:

```bash
# 1. Health endpoints via tunnel (from a different machine or mobile hotspot)
curl https://api.follow-nav.com/health
curl https://gateway.follow-nav.com/health

# 2. Anonymous user creation
curl -X POST https://api.follow-nav.com/api/v1/users/anonymous \
  -H "Content-Type: application/json" \
  -d '{"device_id": "smoke-test-device"}'
# Expect: 201 with JWT

# 3. Route prepare
curl -X POST https://api.follow-nav.com/api/v1/routes/prepare \
  -H "Authorization: Bearer <jwt-from-step-2>" \
  -H "Content-Type: application/json"
# Expect: 201 with route_id

# 4. SSE stream connection (verify heartbeat arrives within 35 seconds)
curl -N -H "Authorization: Bearer <jwt>" \
  "https://api.follow-nav.com/api/v1/routes/<route-id>/status/stream"
# Expect: event stream with heartbeat events every 30s

# 5. Presigned URL check
# Create a route with waypoints and verify the upload_url starts with
# https://gateway.follow-nav.com/
```

**Acceptance Criteria**

- All containers show `healthy` in `docker compose ps`.
- cloudflared logs show `Connection established` (not `Failed to connect`).
- All 5 smoke test steps pass from a machine that is NOT on the home network.
- Zero Trust dashboard shows tunnel `follow-home` as "Healthy" with at least one active connector.

---

### Task 14: Verify backup sidecar works from home

**Story Points**: 1

**Description**

The backup sidecar is unchanged from the Hetzner plan (Phase 2, Tasks 12–13). It uses outbound HTTPS to reach Cloudflare R2. The home PC's iptables allows `ESTABLISHED,RELATED` return traffic and Docker's OUTPUT chain is not restricted. Outbound HTTPS should work without any changes.

Verify manually:

```bash
docker compose --profile prod exec backup /usr/local/bin/backup.sh run-now
```

Check R2 for new objects:
- `postgres/<timestamp>.dump`
- `minio/follow-images/...` (mirrored objects)
- `env/<timestamp>.env.age`
- `_last_success.txt` (updated timestamp)

Decrypt the `.env` backup to confirm it recovers correctly:

```bash
age --decrypt -i /path/to/private-key.txt \
  env-backup-downloaded.env.age > .env.recovered
diff .env .env.recovered
```

**Acceptance Criteria**

- `backup.sh run-now` exits 0.
- All three backup artifacts appear in R2.
- `_last_success.txt` timestamp is within the last 5 minutes.
- `.env` backup decrypts to an exact copy of the host `.env`.
- Nightly cron fires at the configured time (check next morning).

---

### Task 15: Configure Docker to start on boot

**Story Points**: 1

**Description**

The `restart: unless-stopped` policy on all services means Docker restarts containers after a daemon restart. What needs verification is that the Docker daemon itself starts on boot.

```bash
# Verify Docker daemon is enabled (systemd)
systemctl is-enabled docker
# Should return "enabled". If not:
systemctl enable docker
```

After a power loss, the sequence is:
1. UPS supplies power, PC boots.
2. systemd starts Docker daemon.
3. Docker starts all containers with `restart: unless-stopped` that were running before shutdown.
4. cloudflared reconnects to Cloudflare edge (outbound dial).
5. Tunnel becomes healthy again. No manual intervention needed.

**Acceptance Criteria**

- `systemctl is-enabled docker` returns `enabled`.
- Simulate: `docker compose --profile prod stop && docker compose --profile prod start` — all services return to healthy state without manual intervention (verifies restart policy, not boot).
- Full power-cycle test (optional but recommended): reboot the machine and confirm all services are healthy and the tunnel is active within 3 minutes of boot.

---

### Task 16: Set up uptime monitoring

**Story Points**: 1

**Description**

The home PC can go offline (ISP outage, power beyond UPS runtime, kernel panic). External monitoring catches this and alerts before a customer notices.

**UptimeRobot free tier** monitors up to 50 endpoints with 5-minute check intervals. Sufficient for MVP.

Configure three monitors:
1. HTTPS monitor: `https://api.follow-nav.com/health` — check every 5 minutes.
2. HTTPS monitor: `https://gateway.follow-nav.com/health` — check every 5 minutes.
3. Keyword monitor on `_last_success.txt` in R2 (if R2 public access can be configured) OR a simple heartbeat from the backup script to healthchecks.io.

For the backup monitor, add a curl ping to the backup script after a successful run:

```bash
# In backup.sh, after writing _last_success.txt:
# HEALTHCHECKS_UUID is set in .env
if [ -n "${HEALTHCHECKS_UUID:-}" ]; then
    curl -fsS --retry 3 \
      "https://hc-ping.com/${HEALTHCHECKS_UUID}" > /dev/null 2>&1 || true
fi
```

[healthchecks.io](https://healthchecks.io) free tier supports up to 20 checks and sends alerts if a ping is not received within the expected window. Create one check with a 25-hour period and 1-hour grace period. If the nightly backup fails (or the machine is down), the alert fires the next morning.

**Files Affected**

- `scripts/backup.sh` — add optional healthchecks.io ping (guard with `[ -n "${HEALTHCHECKS_UUID:-}" ]` so it is no-op without the env var).
- `.env.example` — add `HEALTHCHECKS_UUID` with comment.

**Acceptance Criteria**

- UptimeRobot shows both health endpoints as "Up".
- healthchecks.io shows the backup check as "Up" after the first nightly run.
- An intentional test (stop the stack for 6 minutes) triggers an UptimeRobot alert email.

---

### Task 17: Firewall verification (no changes needed — document)

**Story Points**: 1

**Description**

This is a verification and documentation task, not a change task.

The current iptables ruleset:
- Default INPUT policy: DROP
- `ESTABLISHED,RELATED` return traffic: ACCEPT
- SSH: via fwknop SPA (only opens 22/tcp after a valid knock)
- Docker chains: intact (Docker manages its own chains for container-to-container and NAT)

cloudflared requires only **outbound** TCP to Cloudflare's anycast IPs on port 7844 (QUIC/UDP also used if available, but falls back to TCP). The `OUTPUT` chain is not restricted. Docker's bridge network allows all inter-container traffic on the `internal` network. No rule additions are needed.

Verify cloudflared can reach Cloudflare's edge:

```bash
# From the cloudflared container
docker compose --profile prod exec cloudflared \
  cloudflared tunnel info follow-home
```

Verify no external port is exposed:

```bash
# From a machine outside the home network (e.g., mobile hotspot)
nmap -p 80,443,8080,8090,9000 <home-public-ip>
# ALL ports should show filtered (dropped by iptables INPUT default-DROP)
# Tunnel access works via https:// only (Cloudflare edge handles 443)
```

**This is a selling point**: the iptables ruleset that was designed for SSH-only access now also handles public service exposure. No new attack surface on the host.

**Acceptance Criteria**

- `nmap` scan from outside shows all ports filtered.
- Public services are accessible only via `https://api.follow-nav.com` and `https://gateway.follow-nav.com`.
- No new iptables rules were added.

---

### Task 18: Operational runbook

**Story Points**: 2

**Description**

Document the day-to-day operational procedures for a home-hosted service. This is the single reference for anything that can go wrong.

**Create `docs/runbook-home-pc.md`** in the coordination repo with the following sections:

---

#### Viewing logs

```bash
# All services, follow live
docker compose --profile prod logs -f

# Single service
docker compose logs -f follow-api
docker compose logs -f cloudflared
docker compose logs -f backup

# Last N lines
docker compose logs --tail=100 follow-image-gateway
```

---

#### Restarting services

```bash
# Restart one service (e.g., after a config change)
docker compose --profile prod restart follow-api

# Full stack restart (e.g., after a kernel update)
docker compose --profile prod down && docker compose --profile prod up -d

# Force rebuild (e.g., after a code change)
docker compose --profile prod build follow-api
docker compose --profile prod up -d follow-api
```

---

#### Rotating the Cloudflare Tunnel token

The tunnel token is a long-lived credential. Rotate it if it is suspected to be compromised.

1. Go to Zero Trust dashboard → Networks → Tunnels → `follow-home` → Configure → Rotate token.
2. Copy the new token.
3. Update `CLOUDFLARE_TUNNEL_TOKEN` in the host `.env`.
4. `docker compose --profile prod restart cloudflared`.
5. Update the token in the password manager.

The tunnel ID and DNS records do not change when the token rotates.

---

#### Recovering from power loss (within UPS runtime)

Normal boot sequence (Docker daemon auto-starts, `restart: unless-stopped` restores all containers). Expected recovery time: 2–3 minutes. No manual action needed.

If the machine was off longer than the UPS runtime (hard power cut):
1. Same as above — automatic. Docker restarts containers in dependency order (postgres/valkey/minio → follow-api/follow-image-gateway → backup/cloudflared).
2. Check `docker compose ps` after 3 minutes. All should be `healthy`.
3. Check Zero Trust dashboard — tunnel should show "Healthy".

---

#### Recovering from ISP outage

cloudflared automatically reconnects when the internet comes back. No action needed. The SSE streamer's 2-minute max duration means any in-flight route creation will timeout and the client will need to retry, but no data is lost (postgres and valkey are persistent).

---

#### Recovering from disk failure

The 4 TB Samsung 990 PRO is a single disk (no RAID). If it fails:

1. The R2 backup has: all PostgreSQL data, all MinIO images, an encrypted copy of `.env`.
2. Restore sequence:
   a. Boot a replacement disk (or the same Hetzner VPS once account is verified).
   b. Decrypt `.env` backup: `age --decrypt -i <private-key> env-backup.age > .env`
   c. `chmod 600 .env`
   d. `docker compose --profile prod up -d` — starts with empty volumes.
   e. Restore postgres: `mc cat r2/follow-backups/postgres/<latest>.dump | pg_restore -h localhost -U $POSTGRES_USER -d $POSTGRES_DB`
   f. Mirror MinIO back: `mc mirror r2/follow-backups/minio/follow-images/ local/follow-images/`
   g. Verify data integrity.
3. Update CORS config and app endpoints if moving to a different host.

---

#### Migrating to Hetzner (when account verification clears)

See Phase 6 (Task 19) — this is the intentional exit path. Summary:

1. Provision the Hetzner server.
2. Copy the same `.env` (no changes needed for Cloudflare Tunnel — the tunnel is host-agnostic).
3. `git clone` the repo, `docker compose --profile prod up -d`.
4. In Zero Trust dashboard, update the tunnel's public hostname services to point at the Hetzner-internal Docker network IPs (or keep the same `follow-api:8080` service names — they work because cloudflared runs inside the same Docker network on Hetzner too).
5. Wait for the old home-PC tunnel connector to drop (it will once you stop Docker there).
6. Verify the tunnel routes traffic to Hetzner.
7. Done. DNS records do not change. App does not need updating. Customer sees zero interruption.

---

#### Checking backup health manually

```bash
# Trigger a backup now
docker compose --profile prod exec backup /usr/local/bin/backup.sh run-now

# Check last success timestamp in R2
docker compose --profile prod exec backup \
  mc cat r2/follow-backups/_last_success.txt
```

---

#### When to escalate to Hetzner (risk thresholds)

| Event | Action |
|-------|--------|
| ISP outage > 4 hours | Consider expediting Hetzner migration |
| Second ISP outage in one week | Migrate to Hetzner immediately |
| Home PC hardware failure | Restore to Hetzner from R2 backups |
| Pilot customer SLA negotiation | Discuss Hetzner as the committed infra |
| Residential ISP contacts you about traffic | Switch to Hetzner (unlikely but possible) |

**Files Affected**

- `docs/runbook-home-pc.md` (new, in coordination repo)

**Acceptance Criteria**

- Runbook file exists and is committed.
- Every recovery scenario above has been mentally walked through (optional: simulate disk recovery using a test postgres + mc restore).

---

## Phase 5 (Reference) — Migration to Hetzner

This is not a blocking phase for home-PC deployment. It documents the exit path so the migration when Hetzner verification clears is a 1-hour task, not a re-planning exercise.

**What changes when moving to Hetzner**:

| Item | Home PC | Hetzner |
|------|---------|---------|
| Tunnel token | Same (or rotate for hygiene) | Same tunnel, same token |
| `.env` | Copy as-is | Copy as-is, change nothing |
| docker-compose.yml | Identical | Identical |
| DNS records | Unchanged | Unchanged |
| App build | Unchanged | Unchanged |
| Caddy | Not running | Start it if you want Caddy instead of cloudflared — but cloudflared works on Hetzner too |

**The migration is**: `git clone` + copy `.env` + `docker compose --profile prod up -d` on the Hetzner box. The Cloudflare Tunnel auto-discovers the new connector. The old connector (home PC) drops when you `docker compose --profile prod down` there. No DNS changes. No app changes. No customer-visible interruption if you migrate during a low-traffic window.

If you want to switch from cloudflared to Caddy on Hetzner (for full Hetzner-native deployment as originally planned):
1. Stop cloudflared: `docker compose --profile prod stop cloudflared`.
2. Start Caddy: same `docker-compose.yml` already has the Caddy service definition.
3. Update DNS records in Cloudflare: change `api`, `gateway`, `storage` from CNAME (tunnel) to A records pointing at the Hetzner public IP, set DNS-only (grey cloud) for Caddy's ACME challenge to work.
4. Caddy auto-issues TLS. Done.

---

## Risks and Mitigations

| Risk | Probability | Impact | Mitigation |
|------|-------------|--------|------------|
| ISP outage | Medium (fiber is reliable but not 100%) | High (service down) | Uptime monitoring alerts; nightly backups to R2; migration to Hetzner if recurring |
| Dynamic IP assignment | Low (tunnels are IP-agnostic) | None | Cloudflare Tunnel reconnects automatically; DuckDNS for SSH is unchanged |
| Power beyond UPS runtime | Low | Medium (unplanned downtime, no data loss) | Auto-restart on boot; R2 backups ensure no data loss |
| Home PC hardware failure | Very low | High | R2 backups cover postgres + MinIO + .env; Hetzner migration path documented in runbook |
| Residential ISP ToS violation | Very low (outbound tunnels are normal traffic; no exposed ports) | Medium | Most residential ISPs have no practical objection to outbound tunnel traffic; only becomes an issue at very high sustained bandwidth (not expected at MVP scale) |
| Cloudflare Tunnel free tier limits | Low | Low | 100 MB upload limit is fine for 3–15 MB phone images; no bandwidth cap on downloads; SSE keep-alive resolved (30s heartbeat) |
| Heat and noise (24/7 i7-11700) | Low (adequate airflow assumed) | Low | Monitor CPU temperature; i7-11700 at typical server loads (5–20% utilization) runs cool; no GPU workload |
| Cloudflare free plan service changes | Very low | Medium | Cloudflare has not changed free Tunnel terms since launch; worst case: subscribe to Cloudflare Teams paid tier (~$7/user/month) |

---

## Acceptance Criteria — Full Platform

The deployment is considered production-ready when all of the following pass:

- [ ] `https://api.follow-nav.com/health` returns HTTP 200 from an external network.
- [ ] `https://gateway.follow-nav.com/health` returns HTTP 200 from an external network.
- [ ] Anonymous user creation succeeds via `https://api.follow-nav.com`.
- [ ] Route create-waypoints response contains `upload_url` values starting with `https://gateway.follow-nav.com/`.
- [ ] Image upload to gateway via presigned URL succeeds from the Flutter app.
- [ ] SSE stream at `https://api.follow-nav.com/api/v1/routes/<id>/status/stream` delivers heartbeat events every 30 seconds (verified with `curl -N`).
- [ ] Presigned download URLs start with `https://storage.follow-nav.com/` and serve images.
- [ ] `nmap` from outside the home network shows all host ports filtered.
- [ ] `docker compose --profile prod exec backup /usr/local/bin/backup.sh run-now` exits 0 and R2 artifacts are present.
- [ ] UptimeRobot shows both health endpoints as "Up".
- [ ] Docker daemon is enabled in systemd (`systemctl is-enabled docker` returns `enabled`).
- [ ] Zero Trust dashboard shows tunnel `follow-home` as "Healthy".
