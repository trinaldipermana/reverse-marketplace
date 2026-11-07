# P2 commerce and mock purchase integration

Status: Implemented for review. Tracking: [commerce request #8](https://github.com/trinaldirizki/reverse-marketplace/issues/8), [checkout/payment #20](https://github.com/trinaldirizki/reverse-marketplace/issues/20).

## Scope

`commerce.Service` implements the existing `contracts.Commerce` interface: lead/item/rules reads, immutable offers, checkout, and agent state transitions. It also provides seller-scoped lead reads, checkout token APIs, mock payment, expiry/cancellation, and an inventory service connected to actual reservations. No tool names or model arguments change.

P1 owns runtime/tools and message delivery coordination with P4. P3 owns the checkout page and other UI. Discovery and actual P1 notification dispatch are later work; CreateLead currently persists the notification intent atomically and leaves it pending.

## Construction

```go
// Open once in the shared app, apply the single migration sequence, inject db.
service, err := commerce.New(db, commerce.Options{
    CheckoutOrigin: "http://127.0.0.1:5173",
})
// Check err, then inject service into P1 and mount service.Handler() in the app.
// service.Inventory() uses authoritative reservation SQL, not NoReservations.
```

The configured checkout origin must be an HTTP(S) origin with no credentials, path, query, or fragment. The returned link is `/checkout/{token}` at that origin. P3 serves the page; P2 serves the token read/pay API. The default constructor origin is localhost:8080; the component harness explicitly defaults its frontend origin to localhost:5173.

`Options.Now` and `Options.NewID` support deterministic service tests. Checkout bearer tokens always use independent cryptographic random bytes, even when IDs are injected. Do not log tokens or trusted checkout URLs.

## Local component harness

```sh
go run ./internal/commerce/cmd/demo \
  -addr 127.0.0.1:8082 \
  -checkout-origin http://127.0.0.1:5173
```

Default SQLite file: reverse-marketplace-p2-commerce.sqlite in the OS temporary directory; `-db` selects another file. The harness migrates, seeds missing inventory and one demo lead, serves the combined APIs, and sweeps expiry once per second. Seeding preserves existing stock, edits, and paid/lost states. Use a fresh DB path for a fresh scenario.

This harness does not run P1, emit buyer messages, start discovery, or render UI. A seeded notification remains pending until discovery/runtime wiring is added. The old mockpos demo remains available for inventory-only testing; use this combined harness when checkout/real holds are involved.

## Leads, policy, and offers

`CreateLead(ctx, CreateLeadInput)` returns `(LeadRecord, created, error)`. It validates seller/item scope and available stock, inserts an active lead, and inserts exactly one stable pending runtime event in the same transaction. Unique seller/platform/post identity returns the existing lead without reopening it or creating another event.

Effective item pricing uses seller defaults plus nullable item-policy overrides. Discount is integer basis points and minimum price uses exact overflow-safe rounded-up arithmetic. Floor/discount remain private server fields. The seller policy editor currently edits defaults; item-specific override editing UI/API is not included.

`RecordOffer` validates current item ownership/status, one-unit quantity, active lead, stock, and effective minimum under a write transaction. It records immutable terms and the current lead item together. Latest offer order uses a monotonic database sequence, including offers created at the same clock time. Same key/input replays the recorded offer; different input with the same key rejects.

`TransitionLead` permits agent active → escalated/lost only. Same target and normalized original reason is an idempotent no-op; changed reason rejects. Payment, reservation, expiry and cancellation use dedicated operations. Seller hand-back is not implemented.

## Checkout lifecycle

CreateCheckout takes only persisted lead/offer identity plus action metadata. It validates the current offer, seller/item ownership, one-unit terms, active item, current effective price policy and available stock, then commits the hold, immutable checkout and reserved lead state atomically. One active hold/pending checkout per lead is enforced by indexes.

A live repeated call returns the same checkout/link. Replaying a creation action after expiry commits release and returns CHECKOUT_EXPIRED; replay after payment/cancellation returns INVALID_STATE. It never reports an old pending snapshot or creates another hold on that key.

The reservation lasts 30 minutes. At `now == expires_at`, payment is expired. Expiry and cancellation release the hold and return a still-reserved lead to active together; they never reopen a paid/lost lead. A fresh checkout after release needs a new action key.

PayCheckout uses only token and action key. Stored terms determine the amount. A single transaction writes receipt, physical stock decrement, payment ledger movement, committed reservation, paid checkout, paid lead, and replay result. Repeated payment, even with another key, returns the original receipt and does not consume stock twice.

GetCheckout/PayCheckout lazily expire a due pending checkout; ExpireCheckouts is also safe for a scheduled sweep. A late-pay business error does not roll back its expiry release. The caller owns scheduling outside the standalone harness.

The database stores a token hash for lookup and restricted raw token/action response for stable link recovery. This is an internal MVP storage choice, not encrypted secret storage. Public views and normal logs omit bearer values.

## Product APIs

Seller-context routes require `X-Seller-ID` in the current component API; discovery separately uses sellerId in its body. Header selection is demo context, not production authentication.

| Route | Response |
| --- | --- |
| GET /api/leads?state=active&limit=20&cursor=... | Seller-scoped ID-order page, data.items and data.next_cursor |
| GET /api/leads/{id} | Explicit safe lead/intent/offer/checkout-status projection, no token/URL or private price policy |
| GET /api/checkouts/{token} | Safe stored item/price/quantity/status/expiry projection; receipt when paid |
| POST /api/checkouts/{token}/pay | Empty body or `{}`, Idempotency-Key header; original stored receipt |

Inventory/policy routes are included through `service.Inventory().Handler()`. Their reservation count is real in this combined service. Checkout bearer routes do not require seller headers and cannot accept browser-supplied amount/item/quantity overrides.

Success envelopes are `{ "data": ... }`; failures have code/message/retryable. Missing resources return 404, invalid arguments 400, business/state/stock/key conflicts 409, and masked service failures 503. Lead pages do not yet include P1 action timelines; that read-adapter agreement is separate.

## Migration and verification

Additive `0002_p2_commerce.sql` creates item overrides, leads, offers, reservations, checkouts, payments, action replays, and pending runtime notifications. It does not modify applied migration 0001 or P1/P4 tables. Foreign keys enforce seller scope and same-lead offer/reservation checkout associations. Partial indexes prevent duplicate active holds and pending checkouts.

Tests cover real P1 context and tool wrappers, one-unit purchase through HTTP pay, POS hold protection, last-unit contention, duplicate/concurrent payments, expiry/pay races, stale offers/policies, immutable transitions, failed-write rollback, token-safe lead views, and Phase-2 database upgrade preserving stock/ledger/idempotency. No LLM or real payment provider is needed for those checks.
