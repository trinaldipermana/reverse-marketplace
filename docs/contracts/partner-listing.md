# Fixed-price partner listing (opt-in)

Refs #52, #53, #54, #55 and #56. This additive contract is for the hackathon
partner-link journey. Existing negotiable discovery and mock checkout stay the
default until the shared app explicitly enables the mode.

## Trusted read

```go
core.GetPartnerListing(ctx, sellerID, itemID, approvedHosts)
// commerce.PartnerListing{SellerID, ItemID, Name, PriceIDR, URL}
```

The helper loads the current seller-scoped inventory item. It requires an active
item, available stock and a positive integer IDR list price. Destination comes
only from `item.Attributes["partner_listing_url"]`, stored in the existing
`inventory_items.attributes_json`; no migration is required. Existing POS create
and patch operations accept this attribute. Preserve other attributes when
patching (attributes are a replacement map).

Approved hostnames are explicit operator configuration, with exact matching
(case insensitive). HTTPS only; no user info, fragment, whitespace, or non-443
port. A subdomain is not approved by approving its parent. The existing URL,
including its query string, is returned unchanged. Do not print it in the safe
activity log. This read performs no network request or checkout/payment mutation.

The helper returns the existing scoped item-read error for a missing item;
`InvalidArgument` for unavailable, mismatched or invalid/unapproved listing data.
Messages never echo the destination. Empty host configuration approves nothing.
The helper validates configured provenance and seller/item binding; the demo team
must verify that the stored destination actually describes that listing.

## Discovery consumer

```go
discovery.Options{
    FixedPrice: true,
    PartnerHosts: []string{"approved-marketplace.example"},
}
```

Configure the same approved hosts for the delivery reader and discovery. These
values come from app configuration, never buyer text or model output. In this
mode, the candidate must have a valid partner listing and its full list price
must fit the buyer's stated budget. Existing stock/spec/location/policy checks
still apply. Missing listing yields persisted `PARTNER_LISTING` rejection;
a price above budget yields `BUDGET`. An existing lead with a different primary
item is revalidated before it can be projected or notified in this mode.

A selected match projects the trusted URL in the existing `product.url`, the
fixed IDR value in `product.priceIDR`, and existing seller/item/lead/thread IDs.
Default discovery continues returning an empty product URL. This adds no HTTP
endpoint or changes to the six conversation tools. Listing reads are current
inventory terms, not an immutable offer; consumers must reread and validate
before delivery and implement fixed-price enforcement in Go as tracked in #54.

## Fixture and rollout

The existing seed already has `seller_demo` / `item_thinkpad`, used ThinkPad
T480, 16 GB RAM, Jakarta, Rp 5,800,000. **The actual approved partner URL is still
pending**; there is intentionally no fabricated seed URL or permissive default.
Test fixtures use `partner.example` only and are not demo destinations.

P2 supplies this library contract. P4/shared-app assembly supplies operator
configuration, approved URL installation on each reset, and safe delivery (#53).
P1 consumes the trusted listing with fixed-price tool enforcement (#54). P3
consumes real discovery results and confirmed activity (#55). Agree host/fixture
configuration in #52 before activating the shared mode. App/server wiring and
full browser rehearsal are not provided by this change.
