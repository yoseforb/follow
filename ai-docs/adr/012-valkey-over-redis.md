# ADR-012: Use Valkey Instead of Redis

**Date:** 2026-02-10
**Status:** Accepted
**Deciders:** Architecture Team
**Supplements:** ADR follow-api-016 (Redis Streams for Inter-Service Communication)

---

## Context

ADR follow-api-016 established Redis Streams with consumer groups as the inter-service communication mechanism between follow-api and follow-image-gateway. That decision selected the Streams protocol, data structures (XADD, XREADGROUP, XCLAIM, HSET), and messaging patterns. This ADR addresses the choice of **server implementation** for that protocol.

In March 2024, Redis Ltd. changed the Redis server license from BSD-3-Clause to a dual RSL/SSPL license. This change:

1. **Restricts cloud hosting**: The new license prohibits offering Redis as a managed service without a commercial agreement, creating legal uncertainty for self-hosted and cloud deployments.
2. **Breaks open-source commitment**: The Follow platform values truly open-source dependencies with permissive licenses. RSL/SSPL is not OSI-approved.
3. **Fragments the ecosystem**: Major cloud providers (AWS, Google Cloud, Oracle) have moved away from Redis in response to the license change.

In response, the Linux Foundation launched **Valkey** -- a community-driven fork of Redis 7.2.4, the last BSD-licensed version. Valkey has rapidly matured with strong industry backing and full protocol compatibility.

---

## Decision

Use **Valkey** as the message broker and transient data store for the follow-image-gateway, replacing Redis in all infrastructure references.

### Why Valkey

1. **Truly open source (BSD-3-Clause)**: No licensing ambiguity. OSI-approved license. No restrictions on hosting or deployment model.

2. **Full Redis protocol compatibility**: Valkey implements the complete Redis protocol, including:
   - Streams: XADD, XREADGROUP, XACK, XCLAIM, XPENDING, XTRIM
   - Hashes: HSET, HGET, HGETALL, EXPIRE
   - Consumer groups: XGROUP CREATE, XGROUP DELCONSUMER
   - All data structures used by ADR follow-api-016

3. **Zero code changes**: The `redis/go-redis/v9` client library works unchanged with Valkey. The client speaks the Redis protocol (RESP), which Valkey fully implements. No import changes, no API changes, no configuration changes beyond the server endpoint.

4. **Strong governance**: Maintained by the Linux Foundation with contributions from AWS, Google, Oracle, Ericsson, Snap, and others. This is not a single-company project -- it has broad industry commitment.

5. **Active development**:
   - Valkey 7.2.5 (June 2024): First stable release, security patches
   - Valkey 8.0 (September 2024): Performance improvements, new features
   - Valkey 8.1 (2025): Continued evolution beyond the Redis fork point

6. **Industry adoption**: AWS ElastiCache, Google Cloud Memorystore, and other managed services have adopted Valkey as their backend, ensuring long-term viability.

### What Changes

| Aspect | Before | After |
|--------|--------|-------|
| Server binary | `redis-server` | `valkey-server` |
| Docker image | `redis:7` | `valkey/valkey:8` |
| Infrastructure docs | "Redis" | "Valkey" |
| Client library | `redis/go-redis/v9` | `redis/go-redis/v9` (unchanged) |
| Connection string | `redis://host:6379` | `redis://host:6379` (unchanged) |
| API/protocol name | Redis Streams | Redis Streams (unchanged) |
| Default port | 6379 | 6379 (unchanged) |

### What Does NOT Change

- **All Go code**: No changes to imports, client initialization, or commands
- **Protocol name**: "Redis Streams" remains the API/protocol name (like how "Elasticsearch" API is used by OpenSearch)
- **Messaging patterns**: All patterns from ADR follow-api-016 remain identical
- **Data structures**: `image:process`, `image:result`, `image:status:{id}` -- all unchanged
- **Configuration keys**: `REDIS_*` environment variables can remain as-is (protocol-level naming)

---

## Alternatives Considered

### Alternative 1: Continue with Redis (New License)

**Approach**: Accept the RSL/SSPL license and continue using Redis.

**Pros**:
- No migration effort
- Redis has longer track record
- Redis Ltd. continues active development

**Rejected because**:
- **License risk**: RSL/SSPL is not OSI-approved. Legal uncertainty for deployment models, especially if Follow ever offers hosted services.
- **Philosophical misalignment**: The Follow platform is built on open-source principles. Depending on source-available (not open-source) infrastructure contradicts this.
- **Ecosystem fragmentation**: Major cloud providers are moving away from Redis, reducing community momentum and long-term support options.

### Alternative 2: NATS

**Approach**: Replace Redis Streams with NATS JetStream.

**Rejected because**: Already evaluated in ADR follow-api-016. Lacks persistent streams with consumer groups and hash data structures for progress tracking.

### Alternative 3: RabbitMQ

**Approach**: Replace Redis Streams with RabbitMQ.

**Rejected because**: Already evaluated in ADR follow-api-016. Heavier infrastructure, no hash data structures, queue-based not stream-based.

### Alternative 4: KeyDB

**Approach**: Use KeyDB, another Redis fork focused on multithreading.

**Rejected because**: Single-company project (Snap Inc.), smaller community, no Linux Foundation governance. Valkey is the clear industry winner.

### Alternative 5: Dragonfly

**Approach**: Use Dragonfly, a Redis-compatible in-memory store.

**Rejected because**: BSL license (not truly open source). Same licensing concern as Redis. Overkill for our messaging volume.

---

## Consequences

### Positive

1. **True open-source license**: BSD-3-Clause with no restrictions on deployment or hosting.
2. **Zero migration cost**: Drop-in server replacement. No code changes, no client library changes, no configuration changes.
3. **Strong community governance**: Linux Foundation stewardship ensures no single company can change the license.
4. **Industry momentum**: Major cloud providers backing Valkey ensures long-term viability and managed service availability.
5. **Future-proof**: Valkey is actively developed and will continue to receive features and security patches independently of Redis.

### Negative

1. **Younger project**: Valkey forked in March 2024. While it inherits Redis's decade of stability, the fork itself is relatively young.
2. **Potential protocol divergence**: Over time, Valkey and Redis may diverge in features. The `go-redis/v9` client may eventually need Valkey-specific handling for new features.
3. **Documentation references**: Most Redis Streams tutorials and documentation reference "Redis". Developers must mentally map "Redis" documentation to Valkey (trivial since the API is identical today).
4. **Naming confusion**: The client library is still called `go-redis`, configuration may still reference `REDIS_*` prefixes. This is cosmetic but may cause initial confusion.

### Mitigations

- **Protocol divergence**: Monitor Valkey release notes. The `go-redis` maintainers have explicitly committed to Valkey support.
- **Documentation**: Internal documentation (this ADR, CLAUDE.md) clearly states "Valkey" for infrastructure, "Redis Streams" for the protocol/API.
- **Naming**: Keep `REDIS_*` environment variable prefixes for now -- they describe the protocol, not the server implementation.

---

## Docker Compose Configuration

```yaml
services:
  valkey:
    image: valkey/valkey:8
    ports:
      - "6379:6379"
    volumes:
      - valkey-data:/data
    command: valkey-server --appendonly yes
```

---

## References

- Valkey project: https://valkey.io/
- Valkey GitHub: https://github.com/valkey-io/valkey
- Linux Foundation announcement: https://www.linuxfoundation.org/press/linux-foundation-launches-open-source-valkey-community
- Redis license change: https://redis.io/blog/redis-adopts-dual-source-available-licensing/
- go-redis Valkey support: https://github.com/redis/go-redis
- ADR follow-api-016: `ai-docs/adr/follow-api-016-redis-streams-inter-service-communication.md`
