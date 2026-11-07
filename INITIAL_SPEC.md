# Reverse marketplace MVP specification

Version: 0.4. Updated: 7 November 2026. Track: Autonomous & Adaptive AI. Backend: Go.

Implementation baseline. Open decisions and proposed implementation details are labeled separately.

Buyers publish needs; seller agents find suitable requests, offer real inventory, negotiate within seller limits, and provide a checkout link. Sellers intervene when an exception requires judgment. The hackathon MVP should demonstrate this complete journey using a mock marketplace, mock inventory, and a local mock payment page.

## Team responsibilities

| Person | Owns | First deliverable | Depends on |
| --- | --- | --- | --- |
| P1 runtime | Shared LLM client, event queue/worker, conversation turns, guarded tool wrappers, serialization, runtime limits, action logging | Run a lead-created turn and a buyer-reply turn with stub tools | P2 commerce contracts; P4 marketplace contract |
| P2 backend and commerce | Discovery, intent extraction, matching, connector contracts, GetItem, RecordOffer, CreateCheckout, TransitionLead, product APIs, mock POS backend, mock payment backend, prompts, schemas, evals | Shared types, commerce/product APIs, mock inventory and payment services, seeded discovery result | Shared DB/migration agreement; P4 feed; P1 runtime hooks |
| P3 UI | Seller dashboard, inventory/POS screens, lead and action views, checkout/payment screens, frontend API integration | Dashboard and checkout UI wired to P2 APIs | P2 product API contracts; P1 action records |
| P4 demo | Mock marketplace/feed, post and message storage, SendAgentMessage, buyer-reply notifications to P1, buyer simulator, seed scenarios, pitch | A post and reply flow emitting agreed inbound events | Shared types; P1 event hooks |

P1 implements the six tool wrappers; P2 implements their commerce methods and P4 implements message delivery. P2 owns inventory connector contracts, the mock POS backend, product-related APIs, and mock payment execution. P3 owns the corresponding UI and calls P2 APIs. P2 owns the payment transaction that decrements inventory, commits the reservation, and marks the checkout and lead paid. P4 retains mock marketplace APIs and message storage/delivery. Shared migration ownership still needs alignment.

## Integration requirements

- P1, P2, P3, and P4 use the same tool names, lead states, and shared domain types.
- Service owners define request/response types and error behavior before consumers integrate. Consumers may use interface stubs during development.
- Database changes use one ordered migration sequence across services. Migration ownership is an open decision.
- Application source repository: `https://github.com/trinaldirizki/reverse-marketplace`.

## System requirements

- Discovery is ordinary code, with LLM calls for intent extraction and ranking. The conversation agent starts only after a lead exists.
- Marketplace and inventory integrations use interfaces. Mock implementations are the default; real platforms are extensions.
- The conversation agent runs once per buyer reply and proposes actions through tools. It never sends messages directly.
- Go code owns guardrails, message delivery, reservations, payment, and all lead state changes.
- Lead states are `active`, `reserved`, `escalated`, `paid`, and `lost`. Negotiating means `active`; it is not another persisted state.
- Reservations live in our database. Available stock is source stock minus active reservations.
- Checkout reserves stock for 30 minutes. Payment is a mock action on our own checkout page, with no payment provider.
- P2 owns `GetItem`, `RecordOffer`, `CreateCheckout`, and `TransitionLead`. P4 owns the mock marketplace and `SendAgentMessage`. P2 also owns product APIs, mock POS, and mock payment. P3 owns the dashboard, POS/inventory screens, and checkout/payment UI.

## Demo outcome and scope

A buyer posts: “I need a used laptop for coding, 16GB RAM, under 6 juta, in Jakarta this week.” A seller with suitable stock responds, explains the match, negotiates a safe discount, creates checkout, and completes a mock purchase. The dashboard shows the decisions, tool actions, reservation, and payment result.

A second short scenario shows adaptation: a buyer requests a price below the permitted floor or asks for an exception, and the system refuses that action or escalates to the seller. A non-buying post is ignored.

MVP includes discovery, intent extraction, matching, a tool-using conversation loop, deterministic guardrails, reservations, checkout, a seller dashboard, a mock feed, seed data, and a buyer simulator. Real marketplace credentials, real payment processing, shipping, refunds, cross-currency pricing, and production authentication are outside the initial scope. Each buyer is assumed to purchase one unit per lead.

## Architecture and execution flow

```text
Mock marketplace → Discovery → Intent filter → Inventory search
                                               ↓
                                      Rerank and distance gate
                                               ↓
                                          Create lead
                                               ↓
Buyer reply → Inbound event → Conversation runtime → Proposed tool action
                                                       ↓
                                             Go policy validation
                                                       ↓
                                      Persist state and outbound action
                                                       ↓
                                      Marketplace connector delivery

Seller dashboard → Policies, leads, action history
Checkout page → Reservation and mock payment → Inventory decrement
```

1. Start discovery against stored marketplace posts and deduplicate by platform and external post ID. Seller-triggered discovery is the proposed demo default; automatic polling remains an extension.
2. Extract buying intent into validated structured JSON. Skip non-buying or low-confidence posts.
3. Search each seller's inventory, check availability and hard constraints, then rerank eligible candidates.
4. Check score threshold and seller service radius. Create at most one lead per seller and buyer request.
5. Run an initial agent turn to propose an offer for the new lead.
6. Receive each buyer reply once and execute a serialized turn for that lead.
7. Validate each proposed action, record the result, and deliver approved messages through the connector.
8. On checkout creation, reserve an agreed item at a validated price and return an owned checkout URL.
9. On successful mock payment, commit inventory consumption and mark the lead paid. On expiry, release the reservation and return the lead to active.

The initial offer is a lead-created turn; subsequent turns are triggered by buyer replies. Bounded internal model/tool steps may occur within one turn.

## Connector contracts

Marketplace operations: `SearchPost` for discovery, read posts using a cursor, read replies using a cursor, reply to a post, and send a DM. Posts and replies include platform, external ID, buyer ID, thread ID, text, creation time, and optional location. Connector capabilities indicate whether DM is supported; MVP can negotiate in a public mock thread.

### Marketplace SearchPost API

P4 implements `SearchPost`; P2 calls it during discovery to find candidate buyer posts. It is a read-only marketplace API, outside the six conversation-agent tools. Searching does not create leads or trigger agent turns; P2 owns intent filtering, matching, and deduplication afterward.

Proposed Go contract, with shared type names to align in implementation:

```go
SearchPost(ctx context.Context, req SearchPostRequest) (SearchPostResult, error)
```

- Request: `Query` (search text), optional `City`, `Cursor`, and bounded `Limit`.
- Result: `Posts` (normalized marketplace posts) and `NextCursor` (empty when exhausted).
- Each post includes platform, external post ID, buyer/thread identity, text, creation time, and optional location, as above.
- An empty query lists recent posts. The mock uses case-insensitive text matching and an optional city filter with stable pagination. No results returns an empty list; connector failures return an error.
- P4 exposes this capability through its marketplace service; any HTTP route is an implementation detail to agree with P2. Search results are candidates, not confirmed buying requests.

All outbound operations receive an action ID as an idempotency key. The mock connector must deduplicate it. Persist cursors only after the corresponding events are durably ingested. Retries must not create duplicate messages.

Inventory operations: search by structured query, get item by source/item ID, and decrement quantity using a payment/commit ID as an idempotency key. Normalize seller ID, title, attributes, description, source stock, price, currency, and location. Search results are candidates; retrieve current stock before reservation.

The mock inventory must support idempotent decrements. A future external connector that cannot guarantee this needs reconciliation before it can safely support automatic payment completion.

## Structured intent and matching

Illustrative intent result:

```json
{
  "is_buying_request": true,
  "confidence": 0.97,
  "item_query": "used laptop for coding",
  "attributes": {"ram_gb_min": 16},
  "location": {"city": "Jakarta", "latitude": null, "longitude": null},
  "budget": {"max_amount": 6000000, "currency": "IDR"},
  "urgency": {"text": "this week", "needed_by": null}
}
```

The confidence value above is illustrative. Validate schema and currency; preserve unknowns rather than inventing facts. Resolve relative deadlines using the post timestamp and buyer timezone when known. “6 juta” means 6,000,000 IDR in the demo context.

Apply hard constraints before ranking: seller ownership, active listing, stock, required attributes, currency, and whether a permitted offer can fit the buyer's budget. Rank eligible items by suitability, price, location, and urgency. Store score and a short reason for the dashboard. The LLM cannot override hard constraints.

Use seeded coordinates for the demo distance calculation. If location is unknown, request clarification or hold the lead for review; do not fabricate distance. A strict MVP may support only the seeded Jakarta area.

## Agent tools and runtime

The conversation agent exposes exactly these six tools:

| Tool and model arguments | Service call | Enforced behavior |
| --- | --- | --- |
| `get_item(item_id)` | P2 `GetItem` | Item must belong to this lead's seller |
| `make_offer(item_id, price)` | P2 `RecordOffer` | Price must meet the effective minimum; stock and seller ownership must be valid. Records an offer without sending a message |
| `send_message(text)` | P4 `SendAgentMessage` | Applies message rules; fills `{price}` and `{checkout_link}` from stored offer and checkout data |
| `create_checkout()` | P2 `CreateCheckout` | Requires an existing offer; price, item, and quantity come from that stored offer, never model arguments |
| `escalate_to_seller(reason)` | P2 `TransitionLead` | Code moves the lead to `escalated` and stops the turn |
| `close_lead(reason)` | P2 `TransitionLead` | Code moves the lead to `lost` and stops the turn |

Seller ID, lead ID, offer ID, quantity, and action keys are runtime/service context, not additional model arguments. Price is integer IDR. Quantity comes from the stored offer; the initial MVP uses one unit. Inventory search belongs to discovery, outside the conversation tool set.

Tools expose business operations, not raw SQL, arbitrary HTTP, or direct model access to connectors. A successful `send_message` ends the turn. `create_checkout` reserves stock and permits delivery of its link in the same turn; it does not itself send a message. After reservation, block further negotiation mutations.

Resolve `{price}` from the stored offer and `{checkout_link}` from a live checkout for this lead. Reject missing data, unknown placeholders, and model-authored payment URLs. Render commercial facts in code so free text cannot contradict the validated offer or invent a payment destination. P2 services revalidate current policy, stock, ownership, and state even after P1 wrapper checks.

Each turn loads seller policy, lead state, trusted inventory facts, conversation history, and prior action results. Buyer posts, replies, and listing descriptions are untrusted input and cannot change policy or tool authority. Validate tool arguments, cap steps, and persist tool outcomes. After a rejected action, the model may correct it within the bounded turn or escalate. A model error must not send an unvalidated fallback message.

## Deterministic guardrails

- Effective minimum price is `max(item_floor, ceil(list_price × (1 − max_discount)))`; store monetary amounts as integer rupiah for the IDR demo.
- Rate limits apply per seller/platform and optionally per buyer. Configuration owns the values.
- Deduplicate inbound events and outbound actions. Do not repeat the same initial offer for the same lead.
- Enforce a per-lead outbound message cap in code. At the cap, stop automatic sends and surface the lead to the seller.
- Offers must reference existing seller-owned items and trusted attributes. Reject unsupported promises about condition, delivery, or warranty.
- Permit only the configured checkout origin and a valid checkout token for payment links. Reject external payment instructions and arbitrary payment destinations.
- Default proposed behavior is automatic action within limits. If the team retains optional `manual` mode, queue buyer-facing actions for approval and revalidate policy, price, stock, and expiry before execution.
- Recheck stock atomically when reserving, even when matching already checked it.
- Never let model output directly write a state, alter seller policy, or mark an order paid.

Escalated leads pause automatic negotiation. If seller resume/close controls are included, only an explicit seller action can resume or close them. The escalation tool ends the turn; do not schedule another model-generated acknowledgement after that transition.

## Lead and checkout state rules

### Lead states

| State | Definition |
| --- | --- |
| `active` | The lead is open for initial outreach or negotiation. The agent may respond and make validated offers; no live checkout reservation exists. |
| `reserved` | A checkout has reserved the agreed item and quantity for 30 minutes. The buyer can pay through that checkout; further automated negotiation is paused. The current turn may still deliver the checkout link. |
| `escalated` | The lead needs seller attention because the agent cannot resolve a request or a guardrail requires intervention. Automatic negotiation stops. Seller resume/close controls are proposed; without them the lead stays escalated for the demo. |
| `paid` | Mock payment completed and stock consumption committed. The purchase is complete; this state is terminal for the MVP. |
| `lost` | The buyer declined or the lead was explicitly closed without a completed purchase. Automatic negotiation stops; this state is terminal for the MVP. |

A newly created lead starts as `active`. “Negotiating” is a description of `active`, not a separate state. Go application services own every transition; the LLM can only request permitted actions through tools. Reservation expiry or cancellation returns a `reserved` lead to `active` after releasing its stock.

### Allowed lead transitions

| Current state | Trigger | Next state |
| --- | --- | --- |
| active | Valid checkout and successful reservation | reserved |
| active | Exception or seller review required | escalated |
| active | Buyer declines or seller closes | lost |
| reserved | Payment commits before expiry | paid |
| reserved | Expiry or cancellation releases stock | active |
| escalated | Explicit seller resume, if hand-back is included | active |
| escalated | Explicit seller close, if seller controls are included | lost |

`paid` and `lost` are terminal for the MVP. One live reservation per lead. Escalation while reserved is deferred for the MVP: cancel/release the checkout first, then escalate. Checkout has separate statuses: `pending` means awaiting payment with a live reservation; `paid` means payment committed; `expired` means its deadline passed and stock was released; `cancelled` means explicitly invalidated and stock was released.

Reservation creation locks the relevant stock allocation, expires stale reservations, checks `source_stock − active_reservations >= requested_quantity`, and inserts a reservation and checkout together. Repeated `create_checkout` calls return the existing live checkout unless explicitly replacing it.

Payment and expiry compete through a locked/conditional transition. Only one can win. A repeated pay request returns the original result. Paying an expired or cancelled checkout fails without decrementing stock. Use an unguessable checkout token; the payment endpoint derives amount and item from stored checkout data.

For the MVP, keep mock inventory, reservations, checkout, and leads in the same database so decrement, reservation commit, checkout payment, and lead transition can be one transaction. During commit, decrement source stock and remove the reservation from the active set together; otherwise availability would be subtracted twice. External inventory requires an idempotent commit workflow and recovery, not a cross-service transaction assumption.

## Minimum data model

| Entity | Key fields |
| --- | --- |
| Seller and policy | Seller ID, location/radius, approval mode, discounts/floors, rate/message limits |
| Inventory item | Seller ID, source/item ID, attributes, list price, floor, currency, stock, location |
| Marketplace post | Platform/external ID, buyer/thread ID, text, timestamp, intent result |
| Lead | Seller, post, buyer/thread, candidate/agreed item, state, version, match reason |
| Conversation event | Lead, direction, external event ID, text, timestamp |
| Agent turn and action | Trigger ID, status, proposed tool arguments, validation result, delivery status, reason |
| Reservation | Lead/item/quantity, expiry, active/committed/released status |
| Checkout | Reservation, immutable amount/currency, token, pending/paid/expired/cancelled status |

Unique constraints cover external event identity, seller/post lead identity, trigger/turn identity, and action/commit idempotency keys. A small database outbox records approved outbound messages before delivery. Keep failed deliveries visible and retryable.

## Proposed HTTP surface

Proposed routes and responsibilities; request/response schemas remain to be defined. P2 owns product routes; P4 owns mock marketplace routes. P3 consumes these APIs and renders the checkout page.

- `POST /api/mock/posts` and `POST /api/mock/threads/{id}/replies`: create feed activity.
- `GET /api/mock/threads/{id}`: read offers and replies.
- `POST /api/discovery/run`: trigger a deterministic discovery pass for demo control.
- `GET /api/leads` and `GET /api/leads/{id}`: dashboard state, history, actions, and reasons.
- `GET /api/inventory`: current inventory and availability.
- `GET /api/seller/policy` and `PATCH /api/seller/policy`: demo seller settings.
- `POST /api/leads/{id}/resume` and `POST /api/leads/{id}/close`: proposed seller intervention controls; hand-back scope remains open.
- Optional if manual approval is retained: `POST /api/actions/{id}/approve` and `POST /api/actions/{id}/reject`.
- `GET /checkout/{token}` and `POST /api/checkouts/{token}/pay`: mock payment.
- `POST /api/demo/reset`: local demo-only reset and seed.

Agent tools call application services directly, not public admin endpoints. Polling is sufficient for dashboard updates in the MVP. Display item, buyer need, agreed price, available stock, lead state, last action, and escalation reason. Log concise decision summaries and tool outcomes rather than hidden model reasoning.

## Build sequence and integration gates

### Gate 1 Shared skeleton

Agree IDs, enums, connector contracts, tool schemas, event format, and service ownership. Start one Go service with a shared database and seeded seller/inventory. P4 can post and reply; P3 can display a seeded lead; P1 can process a stub turn; P2 can extract intent from fixtures.

### Gate 2 Complete purchase path

Connect post → discovery → lead → initial offer → buyer reply → checkout → mock pay → paid. Begin with deterministic agent/tool fixtures if needed to unblock integration, then run the same flow with the live LLM. Keep the distinction visible during testing.

### Gate 3 Autonomous negotiation and exceptions

Run live extraction, ranking, and conversation. Demonstrate a safe counteroffer, a rejected below-floor action, an escalation, stock contention, and checkout expiry. Show the corresponding decisions on the dashboard.

### Gate 4 Demo freeze

Freeze features for the final quarter of available time. Reset and rehearse with seed data, test model credentials and connectivity, prepare a recording, and finalize the pitch. Exact timing depends on hours remaining.

## Acceptance checks

1. A relevant Indonesian buying post yields one lead and one grounded offer. A selling/non-buying post yields no lead.
2. Duplicate polling, buyer events, or delivery retries do not duplicate leads, turns, messages, reservations, or stock decrements.
3. A buyer request or prompt injection cannot bypass price, seller ownership, payment-link, approval, or state rules.
4. A legal discount succeeds; an illegal discount cannot be sent even if the model proposes it.
5. Two concurrent checkouts for the last unit yield exactly one reservation.
6. Mock payment marks the lead paid and decrements stock exactly once. Repeated payment is harmless.
7. Expiry restores availability and makes the lead active; a subsequent reply can create a fresh checkout. A late pay fails.
8. If manual approval is included, manual mode sends nothing before approval; approval checks current facts again.
9. Escalation pauses automation and shows a concrete seller action. Message caps and rate limits are enforced.
10. The full seeded flow can be reset and repeated, and the dashboard exposes validation and delivery failures.

Use focused Go tests for policy math, transitions, idempotency, and payment/reservation races, plus one end-to-end seeded purchase. Maintain a small intent/matching/conversation eval set with expected decisions and tool behavior; do not require exact wording.

## Implementation defaults and open decisions

These defaults are implementation proposals, not additional confirmed requirements.

| Topic | Default or open decision |
| --- | --- |
| Database | SQLite for one local service; use PostgreSQL if the team already has it ready |
| Currency and location | IDR, seeded Jakarta coordinates |
| Demo scale | Two sellers, three to five items each, one shared feed |
| Negotiation channel | Public mock thread; DM remains in the connector interface |
| Approval | Automatic within policy for the purchase path; manual approval scope needs team agreement |
| Seller hand-back | Escalation always stops automation; resume/close UI scope needs team agreement |
| Runtime scheduling | One Go process and channel worker; event status and recovery details need P1 alignment |
| Migrations | Owner not yet assigned; agree one migration sequence across services |
| Payment commit | P2 owns the atomic inventory/reservation/paid transaction; P3 calls its API from the payment UI |
| Model access | One shared provider adapter; select model and SDK after credentials and team constraints are known |
| UI stack | Team's fastest familiar stack; choose once before dashboard/feed implementation |
| Thresholds and caps | Configurable, calibrated against seed scenarios before the demo |

Open integration decisions: frontend stack, model configuration, migration ownership, P2/P3 API request/response schemas, manual approval scope, seller resume/close controls, and event recovery behavior.
