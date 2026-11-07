# P4 marketplace discovery proposal

Draft for P1 and P2 review. P1 loads canonical posts through lookup. P2 uses lookup and search for discovery, and owns intent filtering, lead deduplication, and initial turn creation. Search is a read-only connector operation outside the six conversation tools. [Issue #7](https://github.com/trinaldirizki/reverse-marketplace/issues/7)

## Go contracts

```go
GetPost(ctx context.Context, ref domain.PostRef) (domain.Post, error)
SearchPost(ctx context.Context, req contracts.SearchPostRequest) (contracts.SearchPostResult, error)
ReadPosts(ctx context.Context, cursor string, limit int) (contracts.ReadPostsResult, error)
```

- `contracts.PostLookup` exposes lookup. `contracts.MarketplaceDiscovery` adds search. `contracts.PostFeed` exposes forward reads for connector ingestion.
- A `domain.PostRef` is the pair `Platform` and `ExternalPostID`. Values are case-sensitive, nonempty UTF-8 strings with no surrounding whitespace. Search and lookup return the same reference and original post content. P2 can deduplicate by this pair while retaining which queries found the post.
- A `domain.Post` carries its reference, buyer ID, thread ID, original text, creation timestamp and optional location. An empty city or nil coordinates represent unknown values.
- Lookup of a well-formed missing reference returns `NOT_FOUND`. Invalid input returns `INVALID_ARGUMENT`. Consumers can inspect `*contracts.Error` with `errors.As`. Adapters report connector failure as `DEPENDENCY_UNAVAILABLE` and preserve context cancellation and deadlines for `errors.Is`. Empty result sets are successful with a non-nil empty `Posts` slice.

## Mock search and feed behavior

- `SearchPostRequest` carries `Query`, `City`, `Cursor`, and `Limit`. Lowercase whitespace-separated query terms match substrings in the lowercased original text. All terms must match in any order. Empty query text lists recent posts.
- City equality ignores surrounding whitespace and case. An explicit city filter excludes posts with unknown city. An empty city filter includes them. Invalid UTF-8 in query or city returns `INVALID_ARGUMENT`.
- Results sort by creation time descending, then platform and external post ID ascending. The opaque search cursor binds the last post to the normalized query and city. The next page may use a different valid limit. Posts inserted after the cursor boundary in sort order can appear on a later page. Start a fresh search to see inserts before the boundary.
- A search cursor is empty when the search is exhausted. A feed checkpoint stays populated at the end and can be reused to find later inserts. Feed order follows seed order and then successful creates, so backdated posts remain visible to a feed poller. Persist checkpoints only after downstream ingestion succeeds durably.
- A zero limit defaults to 20. Valid explicit limits are 1–100. Search and feed cursors are distinct and scoped to the mock store instance. A rebuilt store starts with an empty cursor.

## Opt-in mock create route

Pass the store created by `mock.NewPostStore(seed...)` to `marketplace.NewPostHandler` for an in-process fixture or local demo. The returned handler serves `POST /api/mock/posts`. This route is an additive draft for the mock, with no app registration.

Send a single JSON object up to 64 KiB with one `Idempotency-Key` header. Supply the source identities, original text and creation time. Location and each of its fields are optional. A response omits unknown location fields and formats the time in UTC.

```json
{
  "platform": "mock",
  "external_post_id": "post-1",
  "buyer_id": "buyer-1",
  "thread_id": "thread-1",
  "text": "Butuh laptop bekas untuk coding di Jakarta",
  "created_at": "2026-11-07T08:00:00Z",
  "location": {"city": "Jakarta", "latitude": -6.2, "longitude": 106.8}
}
```

Both first create and an identical replay return `201` with the original record. Equivalent timestamps denote the same instant and replay equivalently. Keys are scoped to the store and a key reused with different valid content returns `409 IDEMPOTENCY_CONFLICT`. A new key with an existing post reference also returns `409`, including seed references. The mock allows one canonical post per platform and thread. Creating a different post for that binding returns `409 IDEMPOTENCY_CONFLICT`, and a conflicting seed returns the same Go error code. Different platforms may reuse a thread ID. A failed create does not reserve the key. Validation errors use `400 INVALID_ARGUMENT`, oversized bodies use `413 INVALID_ARGUMENT`, and classified store failures use `503 DEPENDENCY_UNAVAILABLE`. Error bodies use `code` and `message`.

The mock keeps posts, thread bindings, replay keys, and feed order in process memory. SQLite/driver selection, the shared migration sequence, restart verification, and app wiring depend on the infrastructure agreement. The team can use this adapter to review the proposed contract and run consumer fixtures now. [Issue #11](https://github.com/trinaldirizki/reverse-marketplace/issues/11) · [Issue #15](https://github.com/trinaldirizki/reverse-marketplace/issues/15)
