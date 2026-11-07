# Proposed marketplace buyer replies

P4 owns the mock thread and reply store. P1 owns lead resolution and conversation turns. The proposal and remaining integration decisions are tracked in [issue #10](https://github.com/trinaldirizki/reverse-marketplace/issues/10). The local mock uses the canonical post lookup from [issue #7](https://github.com/trinaldirizki/reverse-marketplace/issues/7).

## Thread and HTTP identity

The mock post store enforces one canonical post per platform and thread when seeding or creating posts. Multiple seller leads can share that thread. Create a `mock.Threads` using a `contracts.PostLookup` and call `OpenThread(ctx, post.PostRef)` after saving a post. The thread store binds the platform-qualified thread to the post and buyer from lookup. Opening the same binding again succeeds. A different post or buyer for the same platform and thread is a conflict. The same thread ID can exist on different platforms. Post save and thread registration are separate mock calls. The integrated registration contract and P2 consumer review remain open.

Use `marketplace.NewThreadHandler(threads, "mock")` to serve one configured platform. Registering that handler in the shared app remains an integration step. The request path contains the thread ID, while the handler configuration supplies its platform.

- `POST /api/mock/threads/{id}/replies` takes `external_reply_id` and `text`. Optional fields are `reply_to_message_id` and `location`, with optional `city`, `latitude`, and `longitude` inside `location`. The store supplies the post, buyer, platform, and UTC creation time. The platform and external reply ID form the replay identity. There is no separate header. Reuse with the same thread, text, reply target, and location returns the original message and event with HTTP 200. Creation returns HTTP 201. Both responses contain `message` and `event_id`.
- `GET /api/mock/threads/{id}` returns `{"messages":[]}` for an empty thread and orders populated history oldest-first, then by message ID for equal timestamps. The public message fields are `id`, `platform`, `external_post_id`, `buyer_id`, `thread_id`, `direction` (`buyer` or `agent`), `text`, and `created_at`, plus `external_reply_id`, `reply_to_message_id`, and `location` when present. Internal lead and seller IDs are excluded.
- HTTP errors have `code` and `message`. Invalid input yields 400, unknown threads 404, conflicting replay 409, and a body larger than 64 KiB 413. The Go store reports typed contract errors and preserves context cancellation and deadlines.

A reply to a known agent message in the same thread carries that message's internal lead and seller IDs as routing hints. A reply without a known target is saved with no routing hint. P1 must validate any hint against current P2 lead context before scheduling a turn and decide how to record unmatched or ambiguous events. The public thread can contain messages involving multiple sellers. P1's lead-scoped history and prompt visibility remain an agreement to record in #10.

## Relay and recovery

`SubmitBuyerReply` stores the message and its pending notification under one in-memory lock. HTTP success acknowledges that write. An explicit `RelayBuyerReplies` call snapshots the pending replies and calls `BuyerReplyIntake.AcceptBuyerReply` with the original event ID. Agent messages are excluded from the buyer feed and relay. New replies submitted during a pass are handled on a subsequent pass.

A durable acknowledgment means P1 recorded the event ID and its routing outcome or queued work in storage that survives runtime restart. A lost response, intake failure, or volatile acknowledgment leaves the event pending. The intake must deduplicate by event ID so a retry after it commits can acknowledge the original work. An intake that enqueues in memory returns `BuyerReplyAcceptedVolatile`.

`ReadBuyerReplies` returns saved inbound replies in insertion order with an opaque checkpoint scoped to the store. The default page size is 20, with a maximum of 100. A read at the end preserves the checkpoint. A polling consumer advances its checkpoint only after durable ingestion.

`marketplacetest.NewConversation` seeds a canonical post and opens its thread. `marketplacetest.SendStub` lets consumer tests inject a send outcome without publishing to buyer history.

The mock verifies in-process replay and lost-acknowledgment behavior. It loses history, pending events, and cursors on restart. Shared database, migration, and app wiring decisions are tracked in [issue #11](https://github.com/trinaldirizki/reverse-marketplace/issues/11) and [issue #15](https://github.com/trinaldirizki/reverse-marketplace/issues/15). Restart recovery and one serialized turn per eligible reply await persistent storage and P1 intake. Guarded outbound delivery is a separate slice of #10.
