# P2 commerce contract proposal

Status: Draft for P1/P2 agreement. Tracking: [#11](https://github.com/trinaldirizki/reverse-marketplace/issues/11). Consumer request and confirmation discussion: [#8](https://github.com/trinaldirizki/reverse-marketplace/issues/8).

This change provides types, a service interface, and fake adapters for integration planning. It implements no production commerce, persistence, HTTP routes, or runtime wiring. Existing components have no implementations to migrate. A teammate must review the open contract choices before merging or integrating this proposal.

## Ownership and packages

| Package | Responsibility |
| --- | --- |
| internal/domain | Proposed shared Item, Rules, Intent, LeadRecord, Offer, Checkout, and state types |
| internal/contracts | Proposed Commerce interface and typed service errors |
| internal/commerce/commercefake | Configurable P2 test double and isolated demo read fixtures |
| internal/commerce | Future authoritative commerce implementation |

P1 owns conversation/tool execution; P2 owns commerce validation and state changes. P4 owns marketplace identity, posts/messages, and delivery. P3 consumes explicit product API projections, not these internal records. P2 does not depend on whether P1/P4 delivery is immediate or queued.

## Service surface

```go
type Commerce interface {
    GetLead(ctx context.Context, leadID string) (domain.LeadRecord, error)
    GetItem(ctx context.Context, sellerID, itemID string) (domain.Item, error)
    GetRules(ctx context.Context, sellerID, itemID string) (domain.Rules, error)
    RecordOffer(ctx context.Context, leadID, itemID string, priceIDR int64, qty int, actionKey string) (domain.Offer, error)
    CreateCheckout(ctx context.Context, leadID, offerID, actionKey string) (domain.Checkout, error)
    TransitionLead(ctx context.Context, leadID string, target domain.LeadState, reason string) (domain.LeadRecord, error)
}
```

These names align with the initial runtime proposal. GetLead/GetRules and payload details still need P1 confirmation in #8. No six-tool names or model arguments change.

## Data conventions

- IDs are strings, money is integer IDR, quantity is one, and timestamps use time.Time serialized as UTC RFC3339 by future HTTP handlers.
- Lead states are active, reserved, escalated, paid, lost. Checkout states are pending, paid, expired, cancelled.
- LeadRecord carries seller/post/buyer/thread identity, primary item, validated intent, state/version, and optional current offer/checkout.
- Item carries seller ownership, inventory facts, location, list price, current computed available stock, and version. Available stock excludes unexpired active reservations.
- Unknown budget/deadline and coordinates remain nil. Structured attributes are normalized string values; attribute names and matching schema still need alignment in discovery work.
- Rules uses effective typed FloorPriceIDR/MaxDiscountBPS and operational limits; 1000 basis points means 10%. Item overrides are resolved by the future P2 service.
- Private rules stay outside model prompts and public API projections. Price fields and bearer checkout URL are excluded from default JSON serialization as a defensive measure. A dedicated delivery operation may explicitly read Checkout.URL; product responses need explicit projections.

## Required production semantics

These are contracts for the later implementation, not behavior supplied by the fake:

1. GetItem/GetRules are seller-scoped. GetLead is an internal trusted runtime lookup; seller-facing HTTP handlers must scope lead reads separately.
2. RecordOffer validates current ownership, state, price, quantity, and stock. It persists immutable terms, does not reserve stock, and does not send a message.
3. CreateCheckout validates an existing current offer belonging to the lead. Item, price, and quantity come from that record. It atomically reserves stock for 30 minutes and moves the lead to reserved. Repeated creation returns an existing live checkout where applicable.
4. TransitionLead validates the state machine; it cannot bypass reservation release/payment transactions. P1 stops the turn after escalation or closure.
5. Same action key and same arguments replay the stored result. Different arguments with the same key are rejected. Keys and writes commit atomically.
6. Model-output validation does not replace P2 checks. No LLM or external network call is made while holding a database write transaction.

## Errors

Use errors.As to inspect *contracts.Error.Code and Retryable; do not parse message strings. Error() contains only the code and a safe message. Cause unwraps for errors.Is/As and must not be exposed. Context cancellation/deadline remains recognizable.

Business codes: NOT_FOUND, INVALID_ARGUMENT, INVALID_STATE, PRICE_BELOW_MINIMUM, OUT_OF_STOCK, OFFER_SUPERSEDED, CHECKOUT_EXPIRED, VERSION_CONFLICT, IDEMPOTENCY_CONFLICT. DEPENDENCY_UNAVAILABLE describes service/infrastructure failure. Retryable is not permission to rerun the model and generate a new operation key.

A blocked-price message never reveals the numeric seller floor. The error object is not a public HTTP response type; handlers will map it to agreed safe error envelopes.

## Using the fake

```go
fake := commercefake.NewSeeded()
lead, err := fake.GetLead(ctx, commercefake.LeadID)
// Handle err before using lead.
item, err := fake.GetItem(ctx, lead.SellerID, lead.ItemID)
// Handle err before using item.
_ = item

fake.RecordOfferFunc = func(ctx context.Context, leadID, itemID string,
    priceIDR int64, qty int, actionKey string) (domain.Offer, error) {
    // Script this test's expected result; this callback is not price enforcement.
    return domain.Offer{ID: "offer_test", LeadID: leadID, ItemID: itemID,
        PriceIDR: priceIDR, Qty: qty}, nil
}
```

NewSeeded supplies fresh seller-scoped lead/item/rules read fixtures. It does not pretend to record offers or reserve stock. Configure write callbacks explicitly. Unconfigured methods fail with DEPENDENCY_UNAVAILABLE (not retryable). Cancelled contexts fail before callbacks run. Configure callbacks before concurrent use; callback state synchronization is the consumer test's responsibility.

A fake is not evidence that production price/stock/checkout guarantees work. Later service/database tests must verify those guarantees.

## Open agreements and next slices

- P1: confirm retrieval names and records, typed Rules versus the earlier map, and shared package ownership in #8.
- TransitionLead currently preserves the four-argument baseline. Before production implementation, agree a stable action-key parameter or explicit idempotent repeat-target semantics; expected-version metadata is also open. The fake does not choose either behavior.
- P1 GenerateJSON is available in internal/llm via OpenAIClient.GenerateJSON and llm.JSONRequest. P2 supplies schemas and business validation; use that shared client rather than defining a duplicate request type. OnLeadDiscovered acknowledgement/recovery remains tracked in [#6](https://github.com/trinaldirizki/reverse-marketplace/issues/6).
- P4: provide GetPost/SearchPost identity and payload details in [#7](https://github.com/trinaldirizki/reverse-marketplace/issues/7). This proposal defines no competing marketplace/LLM structs.
- Team: choose database engine, ordered migration owner, and app wiring owner. Proposed: SQLite, P2 migration coordination, component-owned repositories. No database driver, connection setup, or migration is added here.
- P3: agree product HTTP schemas in the next slice; these internal structs are not frozen frontend payloads.

Verification for this slice: callback metadata forwarding, safe missing-configuration failures, cancellation before side effects, fixture isolation and seller scoping, typed error classification/unwrap, and accidental private-field JSON exposure. Production valid/rejected offers and checkout semantics remain open acceptance items in #8.

Related proposals: [product APIs](p2-product-api.md) and [shared infrastructure](p2-infrastructure.md).
