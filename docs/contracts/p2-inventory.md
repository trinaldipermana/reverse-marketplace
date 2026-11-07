# P2 inventory and mock POS integration

Status: Phase-2 implementation for review. Tracking: [#15](https://github.com/trinaldirizki/reverse-marketplace/issues/15). Infrastructure and P3 API agreement remain tracked in [#11](https://github.com/trinaldirizki/reverse-marketplace/issues/11).

## Delivered scope

`internal/commerce/mockpos` provides persisted inventory, seller policies, seeded items, stock adjustments, and product HTTP handlers. It implements GetItem/GetRules for P1 context loading, but is not the complete Commerce interface: leads/offers, checkout/payment, discovery, and runtime integration come in later phases. Item-level policy overrides are not implemented in this slice; GetRules returns the seller default after checking item ownership.

SQLite storage uses the [mattn/go-sqlite3 driver](https://github.com/mattn/go-sqlite3), which requires CGO and a C compiler. The dependency is pinned in go.mod. `OpenSQLite` configures foreign keys, WAL, immediate transactions, a bounded busy timeout, and a shared pool. The real app should open one database and inject it into all components. No P1/P4/app package is changed.

`migrations.Apply` embeds the ordered SQL files and records applied names in schema_migrations. SQL files are immutable once merged. `0001_p2_inventory.sql` creates sellers, seller_policies, inventory_items, stock_movements, and pos_actions. Migrations apply atomically and repeat safely. The migration coordinator/driver/numbering are proposals for the shared app until team agreement; this slice does not wire that app.

## Local demo

```sh
go run ./internal/commerce/mockpos/cmd/demo -addr 127.0.0.1:8082
```

Default database: reverse-marketplace-p2-inventory.sqlite in the OS temporary directory. Use `-db /absolute/path/to/inventory.sqlite` for another file. The command seeds missing demo records without resetting edits/stock. Stop with Ctrl-C. This is a component integration harness, not the marketplace server.

```sh
curl -H 'X-Seller-ID: seller_demo' \
  'http://127.0.0.1:8082/api/inventory?limit=20'
```

The demo has no checkout and explicitly selects NoReservations. Before any checkout integration, supply a real ReservedStock function that reads unexpired active holds on the caller's transaction. Missing readers are rejected by the constructor; reservation-read errors block mutations. The demo command must not be used as a substitute for the eventual shared checkout service.

## API conventions

X-Seller-ID is a local-demo seller selector, not production authentication. Responses are JSON `{ "data": ... }` or `{ "error": { "code": "...", "message": "...", "retryable": false } }`. Prices are integer IDR; versions are positive integers. IDs are opaque strings. All mutations require Idempotency-Key (1–200 characters), scoped to seller. Repeat same operation/input/key returns its original response, even if the current item version later changes. Reusing a key with different arguments returns 409 IDEMPOTENCY_CONFLICT.

Bodies reject unknown fields, multiple JSON values, and malformed input. Maximum request body is 1 MiB. Requests are seller-scoped; another seller's item appears not found. Internal DB errors map to a safe 503 without exposing SQL/driver details. No CORS configuration is imposed: P3 can proxy `/api` to this local harness during integration.

| Route | Behavior |
| --- | --- |
| GET /api/inventory?limit=20&cursor=... | Stable ID-order pagination, default limit 20, max 100; data contains items and next_cursor |
| GET /api/inventory/{id} | One seller-scoped item |
| POST /api/inventory | Create active item; 201, including on replay |
| PATCH /api/inventory/{id} | Version-checked item edit/deactivation |
| POST /api/inventory/{id}/stock-adjustments | Version-checked delta and ledger write |
| GET /api/seller/policy | Seller-only policy editor data |
| PATCH /api/seller/policy | Version-checked policy edit |

Item response fields: id, seller_id, name, description, attributes (string map), list_price_idr, currency, physical_stock, reserved_stock, available_stock, city, active, version. Inventory responses do not include private policy fields. Inactive items remain in the editor list; discovery must filter them out.

### Create item

```json
{"name":"Used coding laptop","description":"16GB RAM","attributes":{"ram_gb":"16"},"list_price_idr":6000000,"initial_stock":2,"city":"Jakarta"}
```

Name is required; price is positive and at most 1,000,000,000,000 IDR. Stock is 0–1,000,000. New active listings must have a list price at or above the seller floor. Initial nonzero stock writes an opening ledger entry atomically.

### Edit item

```json
{"expected_version":1,"name":"Updated title","active":false}
```

Editable fields: name, description, attributes, list_price_idr, city, active. At least one field is required. Attributes replace the map; stock uses the dedicated adjustment operation. Stale versions return 409 VERSION_CONFLICT. Deactivate rather than deleting referenced items.

### Adjust stock

```json
{"expected_version":1,"delta":-1,"reason":"Walk-in POS sale"}
```

Delta must be nonzero and keep physical stock within bounds and at least the current reservation count. A held-stock conflict currently returns 409 INVALID_STATE with a safe explanation. The write lock is acquired before reading version/availability; item stock/version, movement, and idempotency result commit together. Failure anywhere rolls back everything.

### Seller policy

Response fields: floor_price_idr, max_discount_bps, message_cap, rate_limit_per_minute, version. These private settings are returned only by the seller editor, not inventory/public buyer projections.

```json
{"expected_version":1,"max_discount_bps":1000,"message_cap":12}
```

Discount uses basis points (1000 = 10%). Floor is nonnegative, message cap is 1–1000, rate limit is 1–10000 per minute. A policy that would place an active item's list price below its floor is rejected. Operational settings are exposed to P1; this component does not implement its message counters/rate limiter.

## Transaction and integration rules

- ReservedStock must query the injected sql.Tx, scope by seller/item, and use the supplied clock time. Do not open another connection/network call from that callback.
- Apply migrations before constructing services or seeding. Do not instantiate a private per-owner database in the shared app.
- Migrations do not create P1 action tables or P4 marketplace tables.
- SeedDemo is additive and idempotent; it preserves existing stock, edits, and policy.
- Request handlers call the same service methods used by Go consumers; no alternate stock-write path exists in HTTP.
- Schema holds one physical source for mock inventory. Real external inventory connectors and payment decrement/recovery are later work.

## Verification

Tests cover repeat migrations/seeding, persistence across reopen, seller isolation, pagination, input/policy validation, optimistic edits, duplicate and conflicting keys, simultaneous stock edits, reservation-count/expiry behavior through an injected SQL reader, cancellation, forced ledger/action-write rollback, and full HTTP inventory/policy operations. No live LLM request is needed for these tests.
