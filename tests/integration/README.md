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
- The root `docker-compose.yml` is used; all services are started and stopped
  by the test suite automatically
- No manual infrastructure setup is required

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
API_URL=http://localhost:8080 \
GATEWAY_URL=http://localhost:8090 \
go test -tags=integration -v -count=1 ./...
```

### Docker mode (CI/CD)

The test suite starts the full stack via Docker Compose, runs all tests, and
tears everything down (including volumes) when done.

```bash
cd tests/integration
INTEGRATION_TEST_MODE=docker go test -tags=integration -v -count=1 ./...
```

---

## Environment Variables

These variables control the test suite itself. Service-internal configuration
(database URL, MinIO credentials, etc.) is handled by the compose file.

| Variable               | Default               | Description                                  |
|------------------------|-----------------------|----------------------------------------------|
| `INTEGRATION_TEST_MODE`| `local`               | `local` or `docker`                          |
| `API_URL`              | `http://localhost:8080` | Base URL for `follow-api` (local mode only) |
| `GATEWAY_URL`          | `http://localhost:8090` | Base URL for `follow-image-gateway` (local) |
| `VALKEY_ADDRESS`       | `localhost:6379`      | Valkey address (local mode only)             |

---

## Test Cases

Test cases will be listed here as they are added.

| Test File | Test Name | Description |
|-----------|-----------|-------------|
| _(placeholder)_ | _(placeholder)_ | _(will be filled as tests are added)_ |

---

## Troubleshooting

### Port conflicts (local mode)

If the default ports (8080, 8090, 6379, 5432, 9000) are in use, override them:

```bash
VALKEY_ADDRESS=localhost:16379 \
API_URL=http://localhost:18080 \
GATEWAY_URL=http://localhost:18090 \
go test -tags=integration -v -count=1 ./...
```

Note: `POSTGRES_HOST_PORT` and `MINIO_HOST_PORT` are only relevant in docker
mode — in local mode, you point `follow-api` at its own database via its
config/env.

### Service not reachable after 60s

The test suite waits up to 60 seconds for each service's `/health/` endpoint.
If this timeout is hit:

1. Check that the service compiled successfully (look for build errors in the
   test output).
2. Confirm infrastructure (PostgreSQL, MinIO, Valkey) is reachable — `follow-api`
   will not start healthy if its database is down.
3. Run the service manually to inspect startup errors:
   ```bash
   cd /home/yoseforb/pkg/follow/follow-api
   go run ./cmd/server -port 8080 -log-level debug
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

### Docker mode: stale containers

If a previous test run crashed without cleanup, stale containers may block
port binding. Remove them manually:

```bash
docker compose -f /home/yoseforb/pkg/follow/docker-compose.yml \
  down -v --remove-orphans
```
