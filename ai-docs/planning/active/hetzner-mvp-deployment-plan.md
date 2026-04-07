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
                +---------------------------+----------------------------+
                |                           |                            |
   app.follow.example                api.follow.example          upload.follow.example
   (CNAME, proxied)                  (A, DNS-only)               (A, DNS-only)
                |                           |                            |
                v                           v                            v
       Cloudflare Pages         +-----------+----------------------------+
       (Flutter web build)      |   Hetzner VPS (single box)             |
                                |                                        |
                                |   Caddy (TLS termination, ports 80/443)|
                                |     |                                  |
                                |     +--> follow-api:8080 (internal)    |
                                |     +--> follow-image-gateway:8090     |
                                |                                        |
                                |   postgres, valkey, minio (internal)   |
                                |   backup sidecar (cron) -> Cloudflare R2
                                +----------------------------------------+
```

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

**Total tasks: 37.** Realistic effort: **2-3 focused days**, +1 day buffer for cert/CORS/SSE surprises.

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

Create the four DNS records that map the customer-facing hostnames to the right places.

| Subdomain | Type | Target | Cloudflare Proxy |
|-----------|------|--------|------------------|
| `app.follow.example` | CNAME | Cloudflare Pages target | **Proxied (orange)** — Pages is on CF edge already |
| `api.follow.example` | A | Hetzner public IPv4 | **DNS-only (grey)** — Caddy needs real client IP for HTTP-01 cert challenge |
| `upload.follow.example` | A | Hetzner public IPv4 | **DNS-only (grey)** — same reason; also avoids CF free-plan 100MB request size cap |
| apex `follow.example` | redirect / CNAME | `app.follow.example` | Proxied |

The Hetzner IP may not exist yet at this point — that is fine, A records can be created with a placeholder and updated in Phase 4.

**Acceptance Criteria**

- All four records exist.
- Proxy state per the table.
- DNS propagation verified with `dig api.follow.example` from outside the local network.

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

### Task 8: Review REAPER_SCAN_INTERVAL / REAPER_STALE_THRESHOLD

**Story Points**: 1

**Description**

Root compose currently sets `REAPER_SCAN_INTERVAL=1s` and `REAPER_STALE_THRESHOLD=2s` on `follow-api`. These look like test-tuned values bleeding into the root file. Confirm whether they are intentional production settings or whether they need to move to a dev/test override and the root file should use less aggressive defaults.

**Files Affected**

- `docker-compose.yml` (lines 130-131)
- Possibly `follow-api/configs/config.yaml` for the new defaults

**Acceptance Criteria**

- Decision documented in this plan or as an ADR amendment.
- Root compose values match the intended production behavior.

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
```

The actual hostnames come from Phase 1. The 15MB cap matches the gateway's 10MB image limit with headroom for multipart overhead.

**Files Affected**

- `Caddyfile` (new, repo root)

**Acceptance Criteria**

- File exists and is mounted into the Caddy container.
- `docker compose --profile prod up -d caddy` boots healthy.
- Caddy validates the file at startup (no syntax errors).

---

### Task 11: Remove host port publishing for app services in prod

**Story Points**: 1

**Description**

In production, `follow-api` and `follow-image-gateway` should not be reachable from the public internet directly — only via Caddy. Remove (or scope to dev only) the `ports:` blocks that publish 8080 and 8090 to the host. The internal compose network still lets Caddy reach them by service name.

The cleanest way is to keep the `ports:` blocks for laptop convenience but bind them to `127.0.0.1` only in production. Easier alternative: use `profiles: [dev]` on the `ports:` block (compose 2.20+).

**Files Affected**

- `docker-compose.yml`

**Acceptance Criteria**

- After deploy on Hetzner, `curl http://<hetzner-ip>:8080/health` from outside the box fails.
- `curl https://api.follow.example/health` succeeds (via Caddy).
- Laptop dev workflow unchanged: `docker compose up -d` still exposes 8080/8090 on localhost.

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
   - `mc mirror --remove local/follow-images r2/$R2_BACKUP_BUCKET/minio/follow-images/`
   - Log success/failure to stdout (caught by Docker logging driver).

Use `set -euo pipefail`. Exit non-zero on any failure so the container restarts and the failure is visible in logs/monitoring.

**Files Affected**

- `scripts/backup.sh` (new)

**Acceptance Criteria**

- `bash -n scripts/backup.sh` passes.
- `shellcheck scripts/backup.sh` passes.
- `run-now` mode works end-to-end against laptop postgres + MinIO + a real R2 bucket.

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
- Process documented in `scripts/RESTORE.md` (Task 37).

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

**Story Points**: 2

**Description**

SSE + CORS + cross-origin + long-lived connection is the most likely thing to surprise you. Test it BEFORE Hetzner so you debug locally.

Process:

1. Edit `/etc/hosts` to point `api.follow.example` and `app.follow.example` at `127.0.0.1`.
2. Run the prod profile locally with self-signed Caddy certs.
3. Run the Flutter web app (configured to point at `https://api.follow.example`).
4. Trigger a route creation flow that opens an SSE stream.
5. Verify the SSE connection establishes, events flow, no CORS errors in browser console, no premature disconnects.

**Acceptance Criteria**

- SSE handshake succeeds from the browser.
- Progress events arrive in order.
- Terminal `ready` event arrives.
- No CORS errors anywhere in the browser console or server logs.

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

- **CX22** (2 vCPU, 4GB RAM, 40GB SSD) — minimum viable, may be tight under image processing load.
- **CX32** (4 vCPU, 8GB RAM, 80GB SSD) — recommended starting point. ~€6/month. Headroom for libvips + ONNX + postgres + everything.
- **CCX13** (dedicated vCPU) — only if pipeline benchmarks show vCPU contention is real.

Pick a location close to the pilot customer. Use a Debian or Ubuntu LTS image (Caddy and Docker both have first-class packages).

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

As the `follow` user, clone the coordination repo (which contains the root `docker-compose.yml`, `Caddyfile`, `scripts/backup.sh`). The sub-repos (`follow-api`, `follow-image-gateway`, `follow-pkg`) are co-located inside it, so all of them come along when you clone.

```bash
cd ~
git clone <coordination-repo-url> follow
cd follow
# Verify all sub-repos are present (they have their own .git dirs)
ls follow-api follow-image-gateway follow-pkg
```

**Acceptance Criteria**

- Repo cloned to `/home/follow/follow`.
- All five sub-repos present.
- `git status` clean in each.

---

### Task 28: Create real `.env` on the box

**Story Points**: 1

**Description**

Create `/home/follow/follow/.env` with REAL production values. NEVER committed. Source values from the password manager (Task 23 keys, Task 15 R2 credentials, freshly generated DB/MinIO passwords).

```bash
# Key production differences from .env.example:
# - HOST_IP = Hetzner public IPv4 (or domain — but IP is simpler for now)
# - Strong unique passwords for POSTGRES_PASSWORD, MINIO_ROOT_PASSWORD, etc.
# - Real JWT_SECRET and Ed25519 keypair from Task 23
# - Real R2_* credentials from Task 15

chmod 600 /home/follow/follow/.env
chown follow:follow /home/follow/follow/.env
```

**Acceptance Criteria**

- File exists with all required vars from `.env.example`.
- Permissions are `600`, owner `follow:follow`.
- `cat .env` as any other user fails.
- File is in `.gitignore` (verify it does not appear in `git status`).

---

### Task 29: Point DNS A records at the Hetzner IP

**Story Points**: 1

**Description**

If Phase 1 used a placeholder IP, update `api.follow.example` and `upload.follow.example` A records to the real Hetzner IPv4. Wait for propagation.

```bash
dig +short api.follow.example
dig +short upload.follow.example
# Both should return the Hetzner IP
```

**Acceptance Criteria**

- Both A records resolve to the Hetzner IP from outside the local network.
- TTL set to a reasonable value (300s during cutover, can raise to 3600s after).

---

## Phase 5 — First Deploy & Verify

### Task 30: First deploy on the box

**Story Points**: 2

**Description**

Bring up the full prod profile on Hetzner.

```bash
cd /home/follow/follow
docker compose --profile prod up -d
docker compose ps
docker compose logs -f --tail=100
```

Watch the logs for the first 60-90 seconds. Postgres needs to initialize, migrations run, valkey/MinIO come up, app services join the network, Caddy starts requesting certs. Anything failing here is almost always an env var typo or a port collision.

**Acceptance Criteria**

- All services reach healthy state.
- No restart loops.
- `docker compose ps` shows everything `Up (healthy)`.

---

### Task 31: Verify Caddy got Let's Encrypt certs

**Story Points**: 2

**Description**

The first cert issuance is the most likely thing to fail because it depends on DNS, firewall, and Caddy all being correct simultaneously.

```bash
# From outside the box:
curl -v https://api.follow.example/health
curl -v https://upload.follow.example/health

# Cert chain should be Let's Encrypt, NOT self-signed.
echo | openssl s_client -showcerts -servername api.follow.example -connect api.follow.example:443 2>/dev/null | openssl x509 -noout -issuer
# Expected: issuer=C = US, O = Let's Encrypt, CN = R3 (or similar)
```

If issuance fails, common causes:
- DNS not propagated yet (Task 29) — wait 5-10 minutes.
- ufw blocking port 80 — Caddy needs HTTP-01 challenge access.
- Caddy proxied (orange) instead of DNS-only (grey) on the A records — flip them.
- Rate limit hit (Let's Encrypt has prod rate limits — use staging endpoint while debugging).

**Acceptance Criteria**

- Both hostnames serve valid Let's Encrypt certs.
- `curl https://api.follow.example/health` returns 200 OK.
- `curl https://upload.follow.example/health` returns 200 OK.
- Caddy data volume contains the cert files (`docker compose exec caddy ls /data/caddy/certificates`).

---

### Task 32: Run cross-repo integration test suite against the live box

**Story Points**: 2

**Description**

The cross-repo integration tests in `tests/integration/` already support a "remote target" mode (or close to it). Run them with `API_URL=https://api.follow.example GATEWAY_URL=https://upload.follow.example` to smoke-test the live deployment.

If the suite assumes local infrastructure access (postgres, valkey directly), skip those tests and run only the API-level ones. Even just the anonymous-user creation + route creation + image upload + status streaming flow is enough to catch most deployment issues.

**Acceptance Criteria**

- API-level integration tests pass against the Hetzner box.
- No CORS, cert, or networking errors observed.
- Logs on the box show the test traffic arriving at `follow-api` and `follow-image-gateway`.

---

## Phase 6 — Cutover & cleanup

### Task 33: Verify backup cron actually fires

**Story Points**: 2

**Description**

Either wait for the first nightly run (set the cron to run within the next hour to speed this up), OR trigger manually:

```bash
ssh follow@<hetzner-ip>
docker compose --profile prod exec backup /usr/local/bin/backup.sh run-now
# Then verify in Cloudflare dashboard that today's dump appeared in R2.
```

After the first manual verification, leave it overnight and confirm the next morning that the cron-triggered run also succeeded.

**Acceptance Criteria**

- Manual `run-now` succeeds against R2.
- Next-morning cron run succeeds (verified by R2 object timestamp).
- Backup logs visible via `docker compose logs backup`.

---

### Task 34: Manually test full Flutter app flow against Hetzner

**Story Points**: 2

**Description**

End-to-end manual smoke test from the deployed Flutter web app against the Hetzner backend. This is the final gate before declaring the box production-ready.

Test scenarios:

1. Open `https://app.follow.example` in a browser.
2. Create an anonymous user (POST /users/anonymous).
3. Prepare a route, create waypoints with 3-5 images.
4. Upload images via the gateway (PUT /upload).
5. Watch SSE stream emit progress events.
6. Verify route transitions to `ready`.
7. Publish the route.
8. Open the route in navigation mode, verify presigned download URLs work.
9. Test image replacement on a waypoint.
10. Repeat the same flow on a real Android phone (not just web).

**Acceptance Criteria**

- All 10 scenarios pass.
- No errors in browser console.
- No errors in Hetzner logs.
- Images visible in MinIO via presigned URLs.
- SSE events arrive in real time.

---

### Task 35: Drop fly.io

**Story Points**: 1

**Description**

ONLY after Task 34 passes. Stop and delete the fly.io app, cancel the fly.io subscription, remove fly.io secrets from any password manager entries (after archiving).

**Acceptance Criteria**

- fly.io app stopped.
- fly.io billing canceled.
- No remaining DNS records pointing at fly.dev.
- No remaining code references to fly.io (already cleaned in Task 21).

---

### Task 36: Set up basic monitoring

**Story Points**: 2

**Description**

Minimal viable monitoring for one box:

1. **UptimeRobot** (free tier, 50 monitors) — HTTP(S) checks every 5 minutes on:
   - `https://api.follow.example/health`
   - `https://upload.follow.example/health`
   - `https://app.follow.example` (Pages)
   Alert via email and SMS to the user.

2. **Disk space alert** — simple cron on the box that checks `df` and emails if any partition >85% full. (Or use UptimeRobot's keyword monitor on a custom `/health/disk` endpoint if one exists.)

3. **Log review habit** — once a week, `docker compose logs --since 7d | grep -i error`. Not automated yet, but on the calendar.

Future (post-MVP): Grafana Cloud free tier scrapes Prometheus metrics from `follow-api`'s `/health/metrics` endpoint. Out of scope for this plan.

**Acceptance Criteria**

- UptimeRobot configured with 3 monitors.
- Test alert verified (intentionally take a service down, confirm alert received).
- Disk space alert mechanism in place.

---

### Task 37: Document the runbook

**Story Points**: 2

**Description**

Write `ai-docs/operations/hetzner-runbook.md` (new file). Contents:

- **Box access**: SSH command, where keys live, how to reset SSH access if locked out.
- **Deploy / redeploy**: `git pull && docker compose --profile prod up -d --build`.
- **Logs**: where they live, how to tail, how to grep for errors, how rotation works.
- **Restart a single service**: `docker compose restart follow-api`.
- **Update the Caddyfile**: edit, reload with `docker compose exec caddy caddy reload --config /etc/caddy/Caddyfile`.
- **Rotate JWT/Ed25519 keys**: procedure (generate new pair, update `.env`, restart, verify clients).
- **Restore from backup**: full procedure (postgres + MinIO, with the exact mc and pg_restore commands). Reference `scripts/RESTORE.md` from Task 19.
- **Emergency rollback**: `git checkout <prev-tag> && docker compose --profile prod up -d`.
- **Common failure modes and their fixes**: cert renewal failure, postgres OOM, valkey memory exhaustion, gateway pipeline stalled.

Also write `scripts/RESTORE.md` documenting the restore drill from Task 19 in detail.

**Acceptance Criteria**

- `ai-docs/operations/hetzner-runbook.md` exists, covers all bullets above.
- `scripts/RESTORE.md` exists, contains a tested restore procedure.
- A second person (or future-you in 6 months) can recover from any of the documented failures using only the runbook.

---

## Risks & Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Let's Encrypt rate limit hit during debugging | Cert issuance blocked for 1 week | Use Let's Encrypt **staging** endpoint (`acme_ca https://acme-staging-v02.api.letsencrypt.org/directory` in Caddyfile) until issuance is reliable, then switch to prod |
| Cloudflare Pages domain handshake forgotten | App serves the wrong response | Task 4 explicitly calls out the two-sided handshake; verified before Phase 5 |
| Backup script silently broken | Customer data loss on host failure | Task 19 (restore drill) is mandatory and gates Phase 6 |
| First-deploy env var typo | Stack fails to boot | Task 17 (local prod profile test) catches this before touching Hetzner |
| Postgres OOM under image processing load | DB crash, data loss risk | Task 6 (resource limits) caps everything; CX32 has headroom |
| SSE long-lived connections die through Caddy | Status streaming broken in production | Task 22 verifies SSE locally through self-signed Caddy before Hetzner |
| ufw blocks port 80, Let's Encrypt HTTP-01 fails | Cert issuance fails | Task 25 explicitly opens 80 and 443 |
| User locks themselves out via SSH hardening | Manual recovery via Hetzner console | Test the hardened SSH config from a SECOND terminal before closing the original session |

---

## Definition of Done

The plan is complete when:

- [ ] All 37 tasks marked done.
- [ ] Customer can access `https://app.follow.example` over HTTPS and complete the full route lifecycle (create, upload, navigate).
- [ ] Backups have run successfully for at least 2 consecutive nights, verified in R2.
- [ ] A test restore from the most recent backup has been performed within the last 7 days.
- [ ] fly.io is fully decommissioned.
- [ ] Monitoring alerts are firing on intentional outages.
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
| 3. App code changes | 4 | 6 | 1-2 hours |
| 4. Hetzner provisioning | 6 | 7 | 1-2 hours |
| 5. Deploy & verify | 3 | 6 | 1-2 hours |
| 6. Cutover & cleanup | 5 | 9 | 2-3 hours |
| **Total** | **37** | **55** | **~3 days focused** |

The realistic worst case is 4-5 days, with the extra day absorbed by debugging Let's Encrypt cert issuance, CORS edge cases, and the SSE-through-domain test. Everything else is mechanical.
