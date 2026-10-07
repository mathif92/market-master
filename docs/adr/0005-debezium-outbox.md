# ADR-0005: Replace the outbox polling relay with Debezium CDC

## Status

Proposed — future work. Not implemented; the in-process relay in
`pkg/outbox` remains the active mechanism.

## Context

The transactional outbox (ADR-0003, layer 3) solves the dual-write
problem: state change + event row commit in one transaction
(`outbox.Writer.Insert`). The second half — getting the row to Kafka —
is currently a **polling relay** (`outbox.Relay`): every 200 ms it
selects unpublished rows `FOR UPDATE SKIP LOCKED`, publishes, marks
`published_at`, and retries on failure (`attempts`/`last_error`).

The relay works and is covered by `make smoke`, but it trades away
latency (poll interval), adds polling load and bookkeeping columns, and
is code each service must run.

**Debezium** is a log-based CDC platform (Kafka Connect source
connectors). For Postgres it streams the WAL through a logical
replication slot (`pgoutput`), turning every committed row change into
a Kafka message with millisecond latency, no polling, no table reads.

## Decision (when this is picked up)

Replace only the relay half of the outbox. **The write path is
unchanged**: handlers keep calling `outbox.Writer.Insert` inside the
business transaction — nobody ever publishes to Kafka from a request
handler.

1. **Schema** (`pkg/outbox`): `payload BYTEA` → `payload TEXT` (or
   JSONB), because Debezium's JSON converter would wrap BYTEA in a
   `{"base64":…}` envelope; TEXT lets the router pass the
   `evt.Encode` bytes through byte-identical, so every consumer keeps
   working with zero changes. Drop `published_at`/`attempts`/
   `last_error` — the replication slot LSN is the checkpoint.
2. **Postgres**: run with `wal_level=logical`; a REPLICATION role; one
   publication per database. There are **three outbox tables** (order,
   payment, logistics — the services that emit) → three connectors and
   three replication slots.
3. **Kafka Connect**: add a `debezium/connect` service to
   `deploy/docker-compose.yml` and register one Postgres connector per
   database via the REST API, using the built-in
   **`io.debezium.transforms.outbox.EventRouter` SMT**:

   ```jsonc
   {
     "connector.class": "io.debezium.connector.postgresql.PostgresConnector",
     "plugin.name": "pgoutput",
     "table.include.list": "public.outbox",
     "snapshot.mode": "initial",
     "transforms": "outbox",
     "transforms.outbox.type": "io.debezium.transforms.outbox.EventRouter",
     "transforms.outbox.table.field.event.key": "partition_key",
     "transforms.outbox.route.topic.replacement": "market.orders.v1",
     "topic.creation.enable": "false"
   }
   ```

   - Key = `partition_key` (`order_id`) → partition
     `hash(key) mod 12` on the existing 12-partition topic (ADR-0002).
   - Value = the `payload` column = the existing `evt` envelope.
   - `topic.creation.enable=false` (or pre-create the topic with 12
     partitions): Connect auto-creates topics with broker-default
     partition counts, which would violate ADR-0002.
   - All events go to the single topic, so a static
     `route.topic.replacement` beats column-based routing.
4. **Code removal**: `outbox.Relay`, `Publisher` and the
   `NewRelay(...).Run(ctx)` calls in the three `cmd/server` mains.
   `EnsureSchema` stays (new DDL).
5. **Retention**: rows currently accumulate forever. If a cleanup
   `DELETE` is added, configure the SMT's delete behavior to discard
   the resulting delete events (verify the option name against the
   pinned Debezium version).
6. **Docs**: update README's outbox bullet ("relay publishes with FOR
   UPDATE SKIP LOCKED") and AGENTS conventions accordingly.

## Consequences

- Latency drops from ≤200 ms to WAL speed (~ms); polling loop, retry
  bookkeeping and its failure modes disappear from the services.
- Delivery stays **at-least-once** (slot advance vs. publish); the
  existing consumer idempotency (event_id dedupe + state machines)
  continues to absorb duplicates — no consumer changes.
- New operational surface: Kafka Connect workers, three replication
  slots, connector lifecycle monitoring.
  **Primary risk: a stopped connector stops advancing its slot → WAL
  accumulates → disk fills.** Monitor slot lag
  (`pg_replication_slots`) and set `max_replication_slots`/WAL limits
  deliberately.
- Initial snapshot (`snapshot.mode=initial`) re-publishes existing
  outbox rows on first boot — harmless for idempotent consumers, but
  use `no_data` if cutover must not replay history.
- CDC on the same tables comes free for later uses (e.g. streaming
  `stock_movements` to analytics).

## Alternatives considered

- **Keep the polling relay** (status quo): simplest, already tested;
  adequate latency for this domain. This ADR is an optimization, not
  a fix — revisit when sub-100 ms lag or more CDC streams are needed.
- **`LISTEN`/`NOTIFY` push relay**: lower latency without new infra,
  but more in-house code (payload size limits, reconnect handling)
  than it saves.
- **Kafka Connect JDBC source**: still polls the table — same shape as
  the relay, just moved out of process.
- **Debezium Server instead of Kafka Connect**: fewer moving parts,
  but SMT support (the OutboxEventRouter) is the deciding feature and
  is first-class on Kafka Connect; verify Server capability before
  choosing it.
