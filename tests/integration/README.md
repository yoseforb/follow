# Follow Platform — Cross-Repo Integration Tests

End-to-end integration tests that exercise the full Follow platform stack:
`follow-api`, `follow-image-gateway`, Valkey, MinIO, and PostgreSQL together.

---

## Prerequisites

### All modes

- Go 1.23 or later
- `testdata/` directory already populated with real images (checked in)

### Local mode (default)

Infrastructure services must be running before starting the tests.
The test suite starts `follow-api` and `follow-image-gateway` automatically,
but the following must already be up (typically via systemd or the root
`docker-compose.yml`):

- **PostgreSQL** on `localhost:5432`
- **MinIO** on `localhost:9000` (bucket `follow-images` must exist)
- **Valkey** on `localhost:6379`

### Docker mode (CI/CD)

- Docker Engine and Docker Compose plugin installed and running
- Two compose files are merged together by the test harness (via
  `testcontainers-go`'s multi-file support):
  - The root `docker-compose.yml` — base service definitions.
  - `tests/integration/docker-compose.test.yml` — test-only override
    that adds three `follow-api` env vars (`RATE_LIMIT_ENABLED=false`
    and aggressive reclaimer tuning) so the suite can hammer the
    anonymous-user endpoint without tripping 429s and so recovery
    tests don't wait minutes. The root compose and
    `follow-api/configs/config.yaml` defaults are untouched.
- `tests/integration/.env` provides the full configuration (ports, container
  names, network name, host IP, credentials, test Ed25519 keypair). This file
  is **committed** because it contains dummy values only. CI gets it for free
  on checkout; no manual infrastructure or secret setup is required.

---

## Running Tests

### Local mode (default)

`follow-api` and `follow-image-gateway` are compiled and started automatically
by `TestMain`. Go compilation adds ~30-60 s to the first run (subsequent runs
use the build cache).

```bash
cd tests/integration
go test -tags=integration -v -count=1 ./...
```

Override service addresses if your local infrastructure uses non-default ports:

```bash
VALKEY_ADDRESS=localhost:6379 \
API_URL=http://localhost:8085 \
GATEWAY_URL=http://localhost:8095 \
go test -tags=integration -v -count=1 ./...
```

### Docker mode (CI/CD)

The test suite loads `tests/integration/.env`, starts the full stack via
Docker Compose with those values, runs all tests, and tears everything down
(including volumes) when done.

```bash
cd tests/integration
INTEGRATION_TEST_MODE=docker go test -tags=integration -v -count=1 ./...
```

All configuration — ports, container names, network name, host IP,
credentials, Ed25519 keypair — comes from `tests/integration/.env`. To
change something (e.g. shift test ports off the defaults), edit that file.
No code changes and no command-line overrides are needed.

---

## Environment Variables

### Local mode

These variables control where the test suite looks for the services that
`TestMain` launches as subprocesses:

| Variable               | Default                 | Description                           |
|------------------------|-------------------------|---------------------------------------|
| `INTEGRATION_TEST_MODE`| `local`                 | `local` or `docker`                   |
| `API_URL`              | `http://localhost:8085` | Base URL for `follow-api`             |
| `GATEWAY_URL`          | `http://localhost:8095` | Base URL for `follow-image-gateway`   |
| `VALKEY_ADDRESS`       | `localhost:6379`        | Valkey address                        |

### Docker mode

All configuration comes from `tests/integration/.env`. Relevant keys:

| Key                                       | Purpose                                          |
|-------------------------------------------|--------------------------------------------------|
| `POSTGRES_HOST_PORT` / `VALKEY_HOST_PORT` | Host ports exposed by compose (offset from dev)  |
| `MINIO_HOST_PORT` / `MINIO_CONSOLE_HOST_PORT` | MinIO host ports                             |
| `API_HOST_PORT` / `GATEWAY_HOST_PORT`     | App service host ports                           |
| `*_CONTAINER_NAME`                        | `*-test` suffixed names — avoid dev-stack clash  |
| `NETWORK_NAME`                            | Test-only compose network name                   |
| `HOST_IP`                                 | Forced to `localhost` so presigned URLs resolve  |
| `POSTGRES_*` / `MINIO_*` / `JWT_SECRET`   | Test-only credentials (safe to commit)           |
| `FOLLOW_API_ED25519_{PRIVATE,PUBLIC}_KEY` | Test-only Ed25519 keypair (raw 32-byte seed b64) |

---

## Test Cases

Test cases will be listed here as they are added.

| Test File | Test Name | Description |
|-----------|-----------|-------------|
| _(placeholder)_ | _(placeholder)_ | _(will be filled as tests are added)_ |

---

## Troubleshooting

### Port conflicts (local mode)

Local mode defaults to 8085 (follow-api), 8095 (follow-image-gateway) and
6379 (Valkey). If those are in use, override via env vars:

```bash
VALKEY_ADDRESS=localhost:16379 \
API_URL=http://localhost:18085 \
GATEWAY_URL=http://localhost:18095 \
go test -tags=integration -v -count=1 ./...
```

In local mode, PostgreSQL and MinIO are not managed by the test suite — you
point `follow-api` at its own database and object store via its own config
or env.

### Port conflicts (docker mode)

Docker mode host ports are controlled by `tests/integration/.env`. Defaults
are offset into the 25xxx–29xxx range so they don't clash with either the
dev stack (5432, 6379, 8080, 8090, 9000) or systemd-managed services. If
something on your machine still collides, edit the `*_HOST_PORT` values in
`.env` and re-run.

### Service not reachable (local mode)

Local mode waits up to 60 seconds for each subprocess's `/health/`
endpoint. In docker mode this timeout does not apply — container
readiness is governed by the `healthcheck:` blocks in
`docker-compose.yml` and `compose.Wait(true)` in `setupDocker`. If
either mode fails to reach a healthy state:

1. Check that the service compiled successfully (look for build errors in the
   test output).
2. Confirm infrastructure (PostgreSQL, MinIO, Valkey) is reachable — `follow-api`
   will not start healthy if its database is down.
3. Run the service manually to inspect startup errors:
   ```bash
   cd /home/yoseforb/pkg/follow/follow-api
   go run ./cmd/server -port 8085 -log-level debug
   ```
4. In docker mode, inspect container logs directly:
   ```bash
   docker logs follow-api-test
   docker logs follow-image-gateway-test
   ```

### Infrastructure not running (local mode)

If Valkey is unreachable, `TestMain` will exit within 30 seconds with:

```
valkey not reachable after 30s
```

Start the infrastructure stack from the project root:

```bash
cd /home/yoseforb/pkg/follow
docker compose up -d postgres valkey minio createbuckets
```

### Docker mode: stale containers from a crashed run

Normally you don't need to do anything. `setupDocker` uses a stable
compose project identifier (`follow-integration-test`) and calls
`composeStack.Down(ctx, compose.RemoveVolumes(true))` before each
`Up()`, so any leftovers from a previous Ctrl+C'd or crashed run are
wiped automatically on the next `go test` invocation.

If you need to force a manual wipe anyway (e.g. you want to reclaim
disk from the named volumes without running the suite), target the
test project by name with **both** compose files:

```bash
docker compose \
  -f /home/yoseforb/pkg/follow/docker-compose.yml \
  -f /home/yoseforb/pkg/follow/tests/integration/docker-compose.test.yml \
  -p follow-integration-test \
  down -v --remove-orphans
```

The `-p follow-integration-test` flag is **required** — without it,
compose will target whatever project name your current directory +
`.env` resolve to (often the dev stack), which will not match the
test containers and will silently no-op.
