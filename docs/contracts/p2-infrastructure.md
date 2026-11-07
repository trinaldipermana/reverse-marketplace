# P2 shared infrastructure proposal

Status: SQLite inventory and commerce storage implemented; shared application wiring remains open. Tracking: [#11](https://github.com/trinaldirizki/reverse-marketplace/issues/11). The pinned SQLite driver and migration runner were delivered in Phase 2; the additive commerce migration follows in phases 3/4. Shared app wiring is separate.

## Infrastructure boundaries

| Topic | Proposal | Affected owners |
| --- | --- | --- |
| Database | SQLite for the one-process mock demo, one shared connection pool | P1, P2, P4 |
| Migrations | One ordered sequence under migrations; P2 coordinates numbering and commerce/discovery tables, owners supply their own table requirements | P1, P2, P4 |
| Persistence | Component queries live with their owner; internal/store provides connection/transaction infrastructure | All backend owners |
| App wiring | internal/app constructs dependencies and mounts owner-supplied handlers; appoint one coordinator before editing shared wiring | All owners |
| Clock and IDs | Inject clock and ID generation into P2 services for deterministic expiry/idempotency tests | P2; shared conventions reviewed with P1/P4 |
| HTTP conventions | net/http-compatible handlers, explicit seller context, common response envelopes | P2, P3, P4 |

The implemented mockpos.OpenSQLite helper configures the pinned mattn/go-sqlite3 driver, foreign keys, bounded busy timeout, WAL and immediate transactions. P2 commerce also acquires a write lock before allocation reads. Keep LLM/network calls outside database transactions. The real application should open this shared pool once and inject it.

## P2 schema groups

1. Sellers/policies, inventory/overrides, mock stock ledger.
2. Discovery runs, queries, run-local post snapshots, query/post provenance.
3. Leads/offers, reservations/checkouts, mock payment receipts, commerce idempotency results.
4. Discovery matches and initial runtime notification intents.

P1 owns runtime/action tables; P4 owns canonical marketplace posts/messages. P2 snapshots post data for discovery evidence and preserves P4 IDs, not a second canonical feed. Runtime notification acceptance/recovery depends on #6; message-delivery internals are encapsulated by P1/P4.

## Transaction requirements

- Lead and initial notification intent commit together; notify P1 only after commit, reusing the same event ID.
- Reservation stock check, reservation/checkout insertion, and lead reserved transition commit together.
- Mock payment, source stock decrement, stock movement, reservation commit, checkout paid, and lead paid commit together.
- Expiry/cancellation releases the reservation and returns the lead to active atomically.
- Idempotency result and corresponding mutation commit together.

One lead per seller/platform/post; one live reservation and pending checkout per lead; unique payment receipt per checkout. Retain immutable offer/payment terms and current availability checks. Initial migration SQL must include constraints and contention/rollback tests.

## Remaining application coordination

Confirm the app wiring coordinator and shared migration coordination with P1/P4 in #11. Applied migrations are immutable and use the single schema_migrations sequence. Changing the deployed database engine later requires an explicit migration plan; component constructors do not open competing databases.
