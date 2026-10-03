# ADR-0004: REST at the edge, gRPC between services

## Status

Accepted

## Context

Six services need to talk to each other synchronously at well-defined
points (price snapshot at order creation, order facts at payment time,
shipping address at dispatch time), while external clients expect a simple
HTTP API.

## Decision

- **External API = REST/JSON**, served only by the gateway (8080). The
  gateway resolves the market from `Host` (or `X-Tenant-Slug`), validates
  JWTs, strips inbound identity headers, and routes by path prefix.
- **Internal API = gRPC**, contracts in `api/proto` managed by buf:
  - `identity.v1`: `ResolveTenant`, `GetUser`
  - `catalog.v1`: `ValidateOrderLines` (availability + price snapshot)
  - `order.v1`: `GetOrder` (amount, status, customer, destination)
- Codegen: `make proto` → `gen/**` (never hand-edit); `buf lint` runs in
  `make lint`; breaking-change checks are available via `buf breaking`.
- Interceptors propagate `X-Tenant-ID` / `X-User-ID` metadata
  (`pkg/grpcx`); services derive tenant from it for RLS.
- Kafka payloads stay JSON (schema_version in the envelope) for debuggability;
  Protobuf payloads are a possible later migration reusing the same protos.

## Consequences

- Adding a service-to-service call = proto change + `make proto`, versioned
  in a `v1` package so services deploy independently.
- Domain services do not expose REST ports publicly in production — only
  the gateway is ingress-reachable (K8s Service/Ingress in the EKS target).
- Debugging internals uses `grpcurl` + reflection (enabled on all gRPC
  servers).

## Alternatives considered

REST between services (no contracts, stringly-typed, no codegen);
all-gRPC at the edge (worse browser/client ergonomics);
an API gateway product from day one (ops weight we don't need yet).
