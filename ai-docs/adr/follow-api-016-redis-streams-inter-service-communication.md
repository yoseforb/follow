# ADR-016: Redis Streams for Inter-Service Communication

**Date:** 2026-01-29
**Status:** Proposed

## Context

follow-api and follow-image-gateway need asynchronous communication for job distribution (API to gateway) and result delivery (gateway to API). The gateway is stateless and has no database. Communication must survive service restarts and support horizontal scaling.

## Decision

Use Redis Streams with consumer groups for all inter-service communication:

- `image:process` stream: follow-api publishes jobs, gateway consumes
- `image:result` stream: gateway publishes results, follow-api consumes
- `image:status:{id}` hashes: gateway writes progress, follow-api reads for SSE

## Alternatives Considered

1. **HTTP callbacks (gateway calls follow-api):** Fragile -- requires retry logic, service discovery, and handling follow-api downtime. follow-api would need a callback endpoint, adding coupling.

2. **RabbitMQ or NATS:** Full-featured message brokers but introduce significant new infrastructure. Redis is simpler, already well-understood, and Redis Streams provide exactly the semantics needed (persistent messages, consumer groups, acknowledgment).

3. **gRPC streaming:** More complex setup, requires proto definition maintenance, and doesn't provide message persistence. If follow-api is down when gateway finishes, the result is lost.

4. **Shared PostgreSQL polling:** Gateway writes results to a shared table, follow-api polls. Rejected because it couples the gateway to PostgreSQL (violating stateless design) and polling introduces latency.

## Consequences

### Positive
- Messages persist in Redis across service restarts (AOF enabled)
- Consumer groups enable horizontal scaling of both services
- Built-in retry via XPENDING/XCLAIM for orphaned messages
- No service discovery needed -- both services connect to Redis
- Redis Hashes provide lightweight progress tracking with TTL auto-cleanup
- Low latency -- XREADGROUP with BLOCK provides near-real-time delivery

### Negative
- Adds Redis as infrastructure dependency
- Redis memory usage grows with message volume (mitigated by stream trimming and hash TTL)
- Redis is NOT the source of truth -- data must be committed to PostgreSQL for durability
- Redis cluster adds operational complexity if scaling beyond single instance

## References

- Redis Streams documentation: https://redis.io/docs/data-types/streams/
- Redis Data Structures: `#6-redis-data-structures` in architecture document
