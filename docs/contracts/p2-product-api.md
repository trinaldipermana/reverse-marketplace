# P2 product API proposal

Status: Draft for P2/P3 agreement. Tracking: [#11](https://github.com/trinaldirizki/reverse-marketplace/issues/11). The phase-2 inventory/policy implementation and examples are specified in [P2 inventory integration](p2-inventory.md); the remaining routes below are proposals.

P2 owns discovery, inventory/POS, policy, lead, checkout, and mock payment backends. P3 owns their UI. P4 marketplace routes are outside this contract. Internal commerce records are not frontend response types.

## Common conventions

Proposed base prefix: `/api`. Seller identity comes from shared middleware; local demo transport is `X-Seller-ID`. Seller-resource lookups are always scoped to that context. Opaque checkout-token routes do not need a seller header. This is demo behavior, not production authentication.

Use string IDs, integer rupiah, currency `IDR`, quantity one, UTC RFC3339 timestamps, snake_case JSON, and explicit nulls for unknown values. Success uses `{ "data": ... }`; errors use `{ "error": { "code": "...", "message": "...", "retryable": false } }`. Paginated collections put `items` and `next_cursor` inside data. The response never exposes raw model/provider errors or hidden pricing policy.

Proposed errors: invalid input 400; missing/scoped-out resource 404; invalid state, stock, version, or idempotency conflict 409; rate limit 429; temporary dependency failure 503. Idempotency keys accompany mutation requests as headers. Same key with different canonical input is a conflict.

## Discovery button and polling

| Route | Request | Response |
| --- | --- | --- |
| POST /api/discovery/run | `{}` and Idempotency-Key | 202 with run_id, status, reused, status_url |
| GET /api/discovery/runs/{id} | Seller context | Status, stage, timestamps, counts, warnings, truncation, notification summary |
| GET /api/discovery/runs/{id}/results | cursor, limit | Paginated post/item matches with safe reasons, lead_id, new/existing outcome, query provenance |
| GET /api/discovery/runs/{id}/queries | Seller context | Generated query text, source item IDs, status, result counts, safe errors |

Queries are generated from current inventory; the user need not enter keywords. One active run per seller. Repeated clicks return the active run; retrying the same idempotency key returns the original run.

Example start response:

```json
{"data":{"run_id":"run_demo","status":"queued","reused":false,"status_url":"/api/discovery/runs/run_demo"}}
```

Run status: queued/running/completed/partial/failed. Stage: inventory/query_generation/search/matching/lead_creation/notification. Status counts distinguish distinct posts, eligible matches, newly created leads, existing leads, failed queries/posts, and pending/accepted/failed notifications. P1 acceptance is not proof that a message was sent. A run may complete with zero results; errors and partial coverage remain distinguishable.

P3 may poll approximately every second while active, disable duplicate starts, show progress/partial results, and display clear empty outcomes. Do not render a failed model call as “no buying posts.”

## Inventory and mock POS

| Route | Request | Response |
| --- | --- | --- |
| GET /api/inventory | cursor, limit | Items with title, attributes, list_price_idr, active, version, physical/reserved/available quantities, location |
| POST /api/inventory | Item facts, list price, initial stock | 201 with created item |
| PATCH /api/inventory/{id} | Editable item fields and expected_version | Updated item/version |
| POST /api/inventory/{id}/stock-adjustments | delta, reason, expected_version; Idempotency-Key | Updated stock/version and adjustment ID |

Do not hard-delete referenced items. Deactivate instead. Physical stock cannot fall below live reservations. Item attributes and known condition drive discovery and grounded offer descriptions.

## Seller policy

GET/PATCH `/api/seller/policy` is an explicitly seller-scoped editor. Proposed editable fields: floor_price_idr, max_discount_bps, message_cap, rate_limit_per_minute, and expected_version for edits. Manual approval is optional pending scope agreement. Item overrides can follow a separate agreed extension.

Private price settings may be shown in this seller editor; they never appear in buyer checkout, ordinary lead/action views, or model prompts.

## Lead views

GET `/api/leads` supports state, discovery_run_id, cursor, and limit filters. GET `/api/leads/{id}` returns lead ID/state/version, buyer/thread/post references, original buying intent, primary item, safe match reason, current offer, current checkout status/expiry, and safe P1 action summaries through an agreed read adapter.

Lead states: active/reserved/escalated/paid/lost. Action logs are P1-owned; P2 does not create a parallel runtime log table. The cross-owner read adapter is still to agree.

Optional seller intervention routes: POST `/api/leads/{id}/resume` and `/close`, with expected_version and reason. These do not imply that seller hand-back scope is approved.

## Checkout and mock pay

P3 renders `/checkout/{token}`. P2 provides:

| Route | Request | Response |
| --- | --- | --- |
| GET /api/checkouts/{token} | Opaque token | Safe item facts, stored price/quantity/total, status, expiry |
| POST /api/checkouts/{token}/pay | `{}` and Idempotency-Key | Receipt ID, paid status, stored total, paid_at |

No request can override price, item, quantity, or seller. No card/payment-provider details are collected. An already paid checkout returns its original receipt. A late pay fails without stock consumption. Server status is authoritative; frontend countdowns are presentation only.

Checkout URLs are returned to P1 for delivery. Token lookup/response storage details belong to P2 persistence, not FE business logic. Tokens must not appear in ordinary lead/action logs.

## Before implementation

P3 confirms route/envelope conventions, seller context, polling, inventory edit payloads, policy visibility, and checkout states. P1 confirms the action-history read adapter separately. Exact field structs and OpenAPI/schema fixtures should be committed with the first HTTP slice. No existing clients require migration because these routes are not implemented yet.
