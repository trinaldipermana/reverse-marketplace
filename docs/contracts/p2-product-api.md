# P2 product API proposal

Status: Draft for P2/P3 agreement. Tracking: [#11](https://github.com/trinaldirizki/reverse-marketplace/issues/11). The phase-2 inventory/policy implementation and examples are specified in [P2 inventory integration](p2-inventory.md); the remaining routes below are proposals.

P2 owns discovery, inventory/POS, policy, lead, checkout, and mock payment backends. P3 owns their UI. P4 marketplace routes are outside this contract. Internal commerce records are not frontend response types.

## Common conventions

Base prefix: `/api`. Discovery accepts `sellerId` in the request body. The implemented inventory/policy component currently uses `X-Seller-ID`; its separate contract is documented in p2-inventory.md. All lookups remain seller-scoped. Checkout uses its opaque token. These seller selectors are demo conventions, not production authentication.

Use string IDs, integer rupiah, currency `IDR`, quantity one, UTC RFC3339 timestamps, snake_case JSON, and explicit nulls for unknown values. Success uses `{ "data": ... }`; errors use `{ "error": { "code": "...", "message": "...", "retryable": false } }`. Paginated collections put `items` and `next_cursor` inside data. The response never exposes raw model/provider errors or hidden pricing policy.

Proposed errors: invalid input 400; missing/scoped-out resource 404; invalid state, stock, version, or idempotency conflict 409; rate limit 429; temporary dependency failure 503. Idempotency keys accompany mutation requests as headers. Same key with different canonical input is a conflict.

## Synchronous discovery

`POST /api/matches/search` accepts `{ "sellerId": "seller_demo" }`, runs discovery within the request, and returns `200 OK` with matches and summary counts. Use the frontend's existing route and request casing. No polling endpoints or background-run requirement are part of the initial discovery API. `/api/discovery/run` in the earlier route proposal is superseded by this route.

Queries are generated from current inventory; no seller-maintained keyword input is needed. P2 searches P4, preserves query/post provenance, extracts intent and matches, validates results, creates leads, and notifies P1 before returning. Do not wait for P1 to finish negotiation or send a message.

Discovery uses a frontend-compatible direct response rather than the inventory response envelope:

```json
{
  "requestsScanned": 12,
  "listingsScanned": 3,
  "matches": [{
    "matchId": "match_demo",
    "leadId": "lead_demo",
    "matchScore": 92,
    "matchReasons": ["16GB RAM and list price within budget"],
    "buyer": {"id": "buyer_demo", "post": "Need a coding laptop under 6 juta"},
    "product": {"id": "item_thinkpad", "name": "Used ThinkPad T480", "priceIDR": 5800000},
    "notificationStatus": "accepted"
  }],
  "warnings": []
}
```

`requestsScanned` counts unique posts considered; `listingsScanned` counts eligible inventory items. Internal scores remain 0–1; matchScore is a rounded 0–100 display value. Additional presentation fields must come from trusted source data or P3 formatting, not fabricated buyer/item facts. Exact optional buyer/product presentation fields remain to align with P3.

No matches is a successful empty array. Failures remain distinguishable from empty results; partial successful work includes safe warnings. Enforce a bounded request deadline and persist committed results so a retry does not repeat outreach. Do not return a queued run ID that requires polling. P3 shows one loading state until the request completes and handles explicit error/partial outcomes.

Discovery automatically notifies P1 for newly persisted leads. A manual conversation-start option belongs to P1's runtime flow; P2 does not implement `/api/conversations` or make automatic discovery depend on that action. P1 owns coordination with P3 for manual starts. P2 still deduplicates leads and reuses stable event IDs for its own notifications.

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

Discovery is synchronous with sellerId in the body. P3 confirms the remaining response presentation fields, inventory edit payloads, policy visibility, and checkout states. P1 confirms the action-history read adapter separately. Exact field structs and OpenAPI/schema fixtures should be committed with the first HTTP slice. No existing clients require migration because these routes are not implemented yet.
