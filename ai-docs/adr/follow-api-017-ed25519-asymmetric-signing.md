# ADR-017: Ed25519 Asymmetric Signing for Service-to-Service Authentication

**Date:** 2026-01-29
**Status:** Proposed

## Context

The image-gateway must validate that upload requests are legitimate (originated from follow-api) without calling follow-api. Each upload URL contains a JWT token that the gateway must verify. The verification must be:

- Fast (no network round-trip)
- Secure (compromise of gateway does not compromise API's signing capability)
- Simple (no certificate authority or mTLS infrastructure)

## Decision

Use Ed25519 key pairs for asymmetric signing:

- follow-api signs JWT upload tokens with its Ed25519 private key
- image-gateway verifies tokens with follow-api's Ed25519 public key
- Each service has its own independent key pair

JWT token structure (signed by follow-api with Ed25519 private key):

```json
{
  "sub": "image-upload",
  "iss": "follow-api",
  "image_id": "550e8400-e29b-41d4-a716-446655440000",
  "storage_key": "images/550e8400/photo.webp",
  "content_type": "image/jpeg",
  "max_file_size": 10485760,
  "iat": 1738141200,
  "exp": 1738142100
}
```

**Note:** The JWT intentionally omits `route_id`, `waypoint_id`, and marker coordinates. The image-gateway is domain-agnostic and needs only image-related claims to authorize and process the upload. follow-api maintains the image-to-waypoint-to-route mapping in PostgreSQL.

## Alternatives Considered

1. **HMAC-SHA256 (symmetric):** Both services share the same secret. Simpler but has a critical flaw: if the gateway is compromised, the attacker has the signing key and can forge upload tokens. Secret rotation requires coordinated deployment of both services.

2. **mTLS (mutual TLS):** Stronger authentication but requires certificate authority infrastructure, certificate rotation, and more complex deployment. Overkill for two services communicating via Redis.

3. **API key with follow-api verification call:** Gateway calls follow-api to verify each token. Introduces network dependency, latency, and a single point of failure. Defeats the purpose of asymmetric signing.

## Consequences

### Positive
- Each service has its own key pair -- compromise of one does not compromise the other's signing ability
- Token verification is a pure cryptographic operation (microseconds, no I/O)
- Ed25519 keys are small (32 bytes private, 32 bytes public) and easy to distribute
- No service discovery or network calls needed for authentication
- Standard JWT libraries support Ed25519 (EdDSA algorithm)

### Negative
- Requires key generation and distribution as part of deployment
- Key rotation requires updating environment variables on both services
- Ed25519 is less widely supported than RSA in older JWT libraries (not an issue for Go)

## References

- JWT token structure: `#7.1-ed25519-asymmetric-signing` in architecture document
- Ed25519 signing: https://pkg.go.dev/crypto/ed25519
- Golang JWT libraries: https://github.com/golang-jwt/jwt
