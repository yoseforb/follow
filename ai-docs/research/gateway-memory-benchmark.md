# Gateway Memory Investigation and Benchmarks

Reference document for Hetzner server provisioning decisions.

**Date**: 2026-04-13

---

## Context

The follow-image-gateway processes images through a 4-stage pipeline (validate, analyze, transform, upload) using libvips for image processing and ONNX Runtime for ML inference (SCRFD face detection + YOLOv11 license plate detection). We needed to estimate real memory usage for Hetzner server provisioning.

## Test Setup

- **Test**: `TestSeedSearchData` -- creates 60 routes with ~140 images across 6 anonymous users, processing them sequentially through the full pipeline
- **Test images**: 27 stock photos cycled through waypoints (2-3 images per route)
- **Dev machine**: 11th Gen Intel Core i7-11700 (16 cores), 128 GiB RAM
- **Monitoring tools**: `docker stats` (Docker mode), `/proc/<pid>/status` VmRSS/VmHWM (local mode), `GODEBUG=gctrace=1` (Go GC tracing)
- **Test modes**: Docker (via testcontainers + docker-compose) and Local (systemd infra + Go subprocess services)

## Baseline Memory (Idle, No Traffic)

| Service | Idle Memory |
|---------|-------------|
| follow-image-gateway | ~100 MiB |
| follow-api | ~11 MiB |
| PostgreSQL 17 | ~40 MiB |
| MinIO | ~92 MiB |
| Valkey 8 | ~9 MiB |
| **Total** | **~252 MiB** |

## Single Route Processing (3 Images, Behavioral Flow Test)

| Service | Peak Memory | Notes |
|---------|-------------|-------|
| follow-image-gateway | 1.126 GiB | ML models loaded + libvips processing |
| follow-api | ~22 MiB | SSE streaming + Valkey consuming |
| PostgreSQL | ~40 MiB | Barely changed |
| MinIO | ~91 MiB | Stable |
| Valkey | ~10 MiB | Stable |
| **Total** | **~1.29 GiB** | |

## Sustained Load (60 Routes, ~140 Images) -- Before Fix

Without memory management intervention, the gateway RSS grew monotonically:

| Time (approx) | Gateway RSS | Notes |
|---------------|-------------|-------|
| Idle | ~98 MiB | Before any images |
| ~4s | 1.5 GiB | First routes processing |
| ~16s | 2.5 GiB | Sustained processing |
| ~36s | 3.0 GiB | Still climbing |
| ~56s | **3.375 GiB** | Peak (Docker stats) |

Local mode confirmed: VmHWM = 3,420,172 kB (~3.26 GiB), VmRSS oscillating 2.99-3.17 GiB.

Other services remained stable: API ~34 MiB, PostgreSQL ~38 MiB, MinIO grew to ~217 MiB (storing 140 WebP images), Valkey ~11 MiB.

## Root Cause: glibc malloc Fragmentation, Not a Memory Leak

Go GC traces (`GODEBUG=gctrace=1`) proved the Go heap was flat at 20-31 MiB throughout the entire 70-second test:

| GC # | Time | Heap Before GC | Live Heap After GC |
|------|------|---------------|-------------------|
| 4 | 1.3s | 33 MB | 27 MB |
| 12 | 12.8s | 50 MB | 20 MB |
| 24 | 31.0s | 58 MB | 28 MB |
| 36 | 50.9s | 51 MB | 23 MB |
| 48 | 69.9s | 47 MB | 24 MB |

The 3.26 GiB RSS was entirely **CGO memory** -- ONNX Runtime and libvips allocate through C malloc, outside Go's heap. glibc's default allocator retains freed memory in its heap arena rather than returning it to the OS via `madvise(MADV_DONTNEED)`.

## Fix: In-Code C Memory Management

Implemented in `internal/shared/cmem/malloc_trim.go` (commit `2273fba`):

1. **`InitMallocTuning()`** -- called once at startup before `vips.Startup()`. Sets `mallopt(M_MMAP_THRESHOLD, 65536)` so C allocations >64KB use `mmap` instead of `sbrk`. mmap'd memory is always returned to the OS on free.

2. **`TrimCHeap()`** -- called in `resultPublisher()` after each pipeline job completes. Calls `malloc_trim(0)` to ask glibc to return freed heap pages to the OS.

## Results After Fix

Three experiments were conducted:

| Experiment | Peak RSS (VmHWM) | RSS Pattern | Routes Completed |
|------------|-----------------|-------------|------------------|
| Baseline (no fix) | **3.26 GiB** | Monotonically growing | 59/60 |
| Env var `MALLOC_TRIM_THRESHOLD_=0` | **1.31 GiB** | Sawtooth 460MB-1.2GiB | 60/60 PASS |
| In-code `mallopt` + `malloc_trim` | **1.26 GiB** | Sawtooth 362MB-1.1GiB | 60/60 PASS |
| Docker `mem_limit: 2g` (no fix) | N/A | Thrashed, timed out | 9/60 |

The in-code fix is slightly better than the env var (1.26 vs 1.31 GiB peak) because `mallopt(M_MMAP_THRESHOLD)` prevents fragmentation proactively rather than only trimming after the fact.

Key observation: with the fix, RSS actively drops between image batches (down to 362 MiB between routes), confirming memory is genuinely freed and returned to the OS.

## Docker mem_limit Experiment

With `mem_limit: 2g` and `memswap_limit: 2g` on the gateway container (without the malloc fix):

- The container did NOT get OOM killed (exitCode=0)
- But image processing timed out at route 10/60 -- kernel memory pressure + glibc not releasing pages created a death spiral of reclaim overhead
- Conclusion: a 2 GiB cgroup limit requires the malloc fix to be usable

## Revised Memory Budget (With Fix)

| Service | Realistic Peak | Notes |
|---------|---------------|-------|
| follow-image-gateway | **1.3 GiB** | Down from 3.4 GiB with malloc fix |
| PostgreSQL | 512 MiB | shared_buffers for production tuning |
| MinIO | 220 MiB | Grows linearly with stored images |
| follow-api | 35 MiB | Trivial even under load |
| Valkey | 11 MiB | Rock solid |
| OS + Docker | 512 MiB | Kernel, Docker daemon, page cache |
| **Total** | **~2.6 GiB** | |

## Hetzner Server Sizing Recommendation

**Recommended: CPX31 (Shared Regular Performance, AMD EPYC)**

- 4 vCPU shared, 8 GB RAM, 160 GB SSD -- ~11 EUR/month
- 8 GB leaves ~5.4 GiB headroom over the 2.6 GiB measured peak
- Shared vCPU is fine: image processing bursts last seconds, then idles for hours
- Live-resizable to CPX41 if needed

**CPX21 (4 GB RAM) is viable** with the malloc fix (2.6 GiB peak, 1.4 GiB headroom), but leaves little room for PostgreSQL shared_buffers tuning or unexpected traffic.

**Dedicated vCPU is unnecessary** -- the workload is bursty (seconds of processing per route creation), not sustained. Paying for guaranteed cores that idle 99% of the time is wasteful at MVP scale.

## CPX31 Benchmark Reference (Geekbench 6.3.0)

The CPX31 uses AMD EPYC Zen 2 (Rome, 2019) processors:

| Metric | Score |
|--------|-------|
| Single-Core | 1450 |
| Multi-Core (4 cores) | 4767 |
| Object Detection (most relevant) | 805 single / 2863 multi |
| Background Blur (relevant to pipeline) | 2050 single / 6873 multi |

Development machine (i7-11700, 16 cores) is significantly faster. Route processing that takes ~1.3s locally will likely take 4-6s on CPX31. This is acceptable for MVP -- users wait for SSE "complete" after uploading, and a few extra seconds is imperceptible.

## Alternatives Considered

- **`sync.Pool` for Go objects**: Would not help -- Go heap is only 25 MiB, the problem is C-side memory.
- **ONNX session pooling**: Sessions are already singletons with mutex-protected reuse.
- **libvips image pooling**: Not feasible -- each image has different dimensions/content.
- **jemalloc/tcmalloc**: Alternative C allocators that handle fragmentation better, but add build complexity. The mallopt + malloc_trim approach achieves similar results with zero dependencies.
- **Reducing pipeline worker count**: Fewer concurrent jobs = less peak CGO memory, but halves throughput. Not needed with the malloc fix.
- **`GOMEMLIMIT`**: Only controls Go heap GC pressure, has no effect on CGO allocations. Irrelevant here since Go heap is 25 MiB.
- **`debug.FreeOSMemory()`**: Go-side equivalent of returning heap pages to OS. Minor effect since Go heap is tiny.

## Test Infrastructure Added

- `gatewayEnv()` in `tests/integration/main_test.go`: forwards `GATEWAY_GOMEMLIMIT`, `GATEWAY_GODEBUG`, `GATEWAY_MALLOC_TRIM` to the gateway subprocess in local mode.
- `COMPOSE_EXTRA_FILES` env var in `setupDocker()`: accepts additional compose override files for Docker mode stress testing.
- `docker-compose.memlimit.yml`: overlay that sets `mem_limit: 2g` + `memswap_limit: 2g` + `GODEBUG=gctrace=1` on the gateway container.
- `waitForRouteReady()`: polls GET /api/v1/routes/{id} for route_status=ready before publish, fixing the Valkey-to-PostgreSQL eventual consistency race in the seed test.
