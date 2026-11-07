# Proposed marketplace conversation contract

P4 owns the mock thread and reply store. P1 owns lead resolution and conversation turns. The proposal and remaining integration decisions are tracked in [issue #10](https://github.com/trinaldirizki/reverse-marketplace/issues/10). The local mock uses the canonical post lookup from [issue #7](https://github.com/trinaldirizki/reverse-marketplace/issues/7).

## Thread and HTTP identity

The mock post store enforces one canonical post per platform and thread when seeding or creating posts. Multiple seller leads can share that thread. Create a `mock.Threads` using a `contracts.PostLookup` and call `OpenThread(ctx, post.PostRef)` after saving a post. The thread store binds the platform-qualified thread to the post and buyer from lookup. Opening the same binding again succeeds. A different post or buyer for the same platform and thread is a conflict. The same thread ID can exist on different platforms. Post save and thread registration are separate mock calls. The integrated registration contract and P2 consumer review remain open.

Use `marketplace.NewThreadHandler(threads, "mock")` to serve one configured platform. Registering that handler in the shared app remains an integration step. The request path contains the thread ID, while the handler configuration supplies its platform.

- `POST /api/mock/threads/{id}/replies` takes `external_reply_id` and `text`. Optional fields are `reply_to_message_id` and `location`, with optional `city`, `latitude`, and `longitude` inside `location`. The store supplies the post, buyer, platform, and UTC creation time. The platform and external reply ID form the replay identity. There is no separate header. Reuse with the same thread, text, reply target, and location returns the original message and event with HTTP 200. Creation returns HTTP 201. Both responses contain `message` and `event_id`.
- `GET /api/mock/threads/{id}` returns `{"messages":[]}` for an empty thread and orders populated history oldest-first, then by message ID for equal timestamps. The public message fields are `id`, `platform`, `external_post_id`, `buyer_id`, `thread_id`, `direction` (`buyer` or `agent`), `text`, and `created_at`, plus `external_reply_id`, `reply_to_message_id`, and `location` when present. Internal lead and seller IDs are excluded.
- HTTP errors have `code` and `message`. Invalid input yields 400, unknown threads 404, conflicting replay 409, and a body larger than 64 KiB 413. The Go store reports typed contract errors and preserves context cancellation and deadlines.

A reply to a known agent message in the same thread carries that message's internal lead and seller IDs as routing hints. A reply without a known target is saved with no routing hint. P1 must validate any hint against current P2 lead context before scheduling a turn and decide how to record unmatched or ambiguous events. The public thread can contain messages involving multiple sellers. Use `LeadPromptHistory(ctx, threadRef, leadID, sellerID)` for the exact lead and seller. It preserves message identity, direction, and order, excludes unmatched and competing seller messages, and redacts checkout links in both directions. The application cuts that history through the current buyer message before passing it to P1.

## Relay and recovery

`SubmitBuyerReply` stores the message and its pending notification under one in-memory lock. HTTP success acknowledges that write. An explicit `RelayBuyerReplies` call snapshots the pending replies and calls `BuyerReplyIntake.AcceptBuyerReply` with the original event ID. Agent messages are excluded from the buyer feed and relay. New replies submitted during a pass are handled on a subsequent pass.

A durable acknowledgment means P1 recorded the event ID and its routing outcome or queued work in storage that survives runtime restart. A lost response, intake failure, or volatile acknowledgment leaves the event pending. The intake must deduplicate by event ID so a retry after it commits can acknowledge the original work. An intake that enqueues in memory returns `BuyerReplyAcceptedVolatile`.

`ReadBuyerReplies` returns saved inbound replies in insertion order with an opaque checkpoint scoped to the store. The default page size is 20, with a maximum of 100. A read at the end preserves the checkpoint. A polling consumer advances its checkpoint only after durable ingestion.

`marketplacetest.NewConversation` seeds a canonical post and opens its thread. `marketplacetest.SendStub` lets consumer tests inject a send outcome without publishing to buyer history.

The mock verifies in-process replay and lost-acknowledgment behavior. It loses history, pending events, and cursors on restart. Restart recovery and one serialized turn per eligible reply await persistent storage and P1 intake. P4 migration allocation and app wiring remain open in [issue #11](https://github.com/trinaldirizki/reverse-marketplace/issues/11) and [issue #15](https://github.com/trinaldirizki/reverse-marketplace/issues/15).

## Guarded agent delivery

`marketplace.AgentSender.SendAgentMessage` is a draft Go boundary for P1’s `send_message(text)` wrapper. P1 supplies a stable action ID, the authorized lead, seller and platform-qualified thread, and the connector channel. To deliver a checkout, P1 also supplies the checkout ID returned by `create_checkout` in the current turn. Only `Text` is model input. The mock supports public post replies. A direct message request returns `AGENT_CHANNEL_UNSUPPORTED`.

The demo sender accepts these exact Go-owned English templates.

| Text | Lead state and required facts |
| --- | --- |
| `Thanks for your request.` | `active`, with current seller and lead item policy |
| `I can help with product fit, price, stock, or checkout.` | `active`, with current seller and lead item policy, used by the scope guard |
| `I can offer this item for {price}.` | `active`, with a current in-stock offer |
| `Checkout for {price}: {checkout_link}` | `reserved`, with the current offer and pending checkout from this turn |

P4 reads P2’s lead, item, and rules and verifies the scope against the canonical mock thread. It renders the integer IDR price from the stored offer, checks the effective minimum, and verifies that checkout identity, offer, item, quantity, price, and deadline agree. The stored checkout URL must use the configured HTTP(S) origin with an ASCII hostname or IP address and have the exact path `/checkout/{token}` with a nonempty URL-safe token. A reserved checkout may hold the last unit. P4 obtains the bearer URL from the trusted P2 record and includes it in the buyer-facing message. Keep checkout-bearing message text out of P1 prompts, action summaries, and dashboard projections. The connector receipt contains the action ID, opaque marketplace message ID, and publication time.

After validation, the mock records an immutable pending draft. It appends the public message and saves the receipt together under one lock. That lock also enforces the current per-lead cap and a rolling one-minute rate limit per seller and platform, based on published messages. The mock publishes each stored offer and checkout at most once per lead. A message cap returns `AGENT_MESSAGE_CAP_REACHED` for P1 to surface to the seller. A rate limit returns retryable `AGENT_RATE_LIMITED`. Agent messages never enter the buyer reply feed.

After an ambiguous or retryable publication failure, the caller can retry the same request. P4 reconciles a published receipt before reading current commerce facts and again after a concurrent validation or staging failure. If the action has a draft and no receipt, P4 checks current facts again and requires them to render the original terms. Publication checks the checkout deadline again. Conflicting action ID reuse returns `IDEMPOTENCY_CONFLICT`. A `delivered` result is receipt-backed and may end the P1 turn. A `pending` result has a saved draft and an unconfirmed publication. A `rejected` result has a terminal policy or validation error for that attempt. A caller handles a dependency failure before staging through the typed error.

The fixture test uses P1’s existing in-memory worker and tool loop, test-only intake and tool wrappers, and the P2 commerce fake. It exercises duplicate discovery and reply notifications, the initial offer, a checkout tool call that publishes nothing, and a same-turn send that publishes the link and stops the loop. The mock retains drafts and receipts for the lifetime of the store. Production integration still needs a durable P1 action and intake contract, a P4 migration slot and app registration, agreed approval and seller notification behavior, and a shared consistency rule for commerce changes between validation and publication. Track P1/P4 agreement in [issue #10](https://github.com/trinaldirizki/reverse-marketplace/issues/10) and commerce facts in [issue #8](https://github.com/trinaldirizki/reverse-marketplace/issues/8).

## Fixed-price demo delivery

The shared app can construct `NewFixedPriceAgentSender` with the same approved
hosts as fixed-price discovery. It consumes the existing `AgentMessageRequest`
and `AgentDeliveryResult`. For an active lead, pass the original template with a
stable action ID and no checkout ID:

- `I found a match at a fixed price of {listing_price}.`
- `Here is the partner listing: {partner_listing_link}`

The sender loads the lead's current seller-owned item and uses the trusted
[partner listing validation](./partner-listing.md). It renders the fixed IDR
price or inserts the stored HTTPS URL, preserving the query string. The mock
allows one delivered message of each of these two kinds per lead. Retrying the
same action returns the original matching receipt when published. An
unconfirmed result is pending and contains no receipt. The buyer thread
contains the rendered message. The receipt contains only action ID, message ID,
and publication time.

## Fixed-price internal checkout

The fixed-price checkout demo uses `NewFixedPriceCheckoutAgentSender`. The
sender accepts `Sorry, I can't lower the listed price.` for an active lead,
with no checkout ID. The existing offer and checkout templates still use
stored terms, and the sender requires the stored offer price to match the
seller item's current listing price. A checkout link still requires the live,
matching internal reservation and a receipt-backed publication.

P1 establishes explicit buyer acceptance before allowing checkout. P2 owns
fixed-price enforcement when recording and reserving the offer. This sender
can be wired after those controls are available. See [#56](https://github.com/trinaldirizki/reverse-marketplace/issues/56).
