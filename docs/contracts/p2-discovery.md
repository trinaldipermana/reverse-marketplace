# P2 synchronous discovery integration

Status: Implemented for review. Tracking: [#34](https://github.com/trinaldirizki/reverse-marketplace/issues/34).

## Request and result

`POST /api/matches/search` accepts exactly `{ "sellerId": "seller_demo" }`. It performs discovery synchronously and returns a direct frontend-compatible result with `requestsScanned`, `listingsScanned`, `matches`, `warnings`, `truncated`, and diagnostic `runId`. No polling or seller header is required for this endpoint.

Each match contains matchId/leadId, canonical threadId, matchScore (0–100 display value), matchReasons, buyer/post facts, product facts/pricing, sellerId, leadState and notificationStatus. Match ID is the stable lead ID. Buyer display name falls back to buyer ID; product URLs are empty when no trusted external listing URL exists. No fictional personal or listing facts are generated.

Errors use the common safe code/message/retryable envelope: invalid request 400, missing seller 404, concurrent discovery for that seller 409, unavailable model/connector/storage 503. A valid empty result is distinct from failure; partial useful results include warnings. `requestsScanned` counts unique posts admitted to matching, up to the configured cap, rather than counting duplicated search hits.

## Pipeline and ownership

1. Load available active inventory, excluding unexpired stock holds.
2. Call P1's GenerateJSON through ModelAdapter to generate short inventory-derived queries. There is no static seller keyword registry.
3. Call P4 SearchPost with bounded pagination. P4 AND-matches all query terms, so synonyms are separate queries. Initial searches leave City empty to retain unknown-location posts.
4. Preserve each query → post association while deduplicating canonical PostRef snapshots.
5. Call GenerateJSON once per unique admitted post to extract buying intent and rank candidate inventory. Buying extraction and item ranking are combined in this stage.
6. Validate current seller ownership, listing/stock, requirements, quantity, confidence/score, currency, legal price eligibility, geography, and known deadlines in Go. Select one primary item and retain alternatives.
7. Use commerce.CreateLead to persist one lead and stable initial runtime event per seller/platform/post. Notify P1 after commit using the existing event ID.

Discovery does not message buyers or implement manual conversation starts. P1 owns optional manual starts and the negotiation runtime; P4 owns messaging. The separate `/api/conversations` frontend mock is not implemented by this service.

## Model and hard checks

ModelAdapter reuses the shared llm.JSONRequest/GenerateJSON client. Prompt/schema constants are in internal/discovery/model.go for the MVP. The model sees public item facts and original post data, never seller floors/discount rules. Listing descriptions and buyer text are untrusted data, not developer instructions.

Structured results preserve explicit quantity, unknown values, non-IDR currency, and requirements. Numeric requirement values use the attribute's unit without a suffix (ram_gb_min value 16); categorical synonyms normalize to inventory values (bekas → used). Invalid provider output fails explicitly.

One unit and IDR are supported. Budget filtering uses the same overflow-safe commerce.MinimumOfferPrice calculation as offers/checkout. Unknown hard attributes are not assumed satisfied. Numeric _min/_max bounds and exact normalized text attributes are checked against current item facts.

Use trusted source coordinates for Haversine distance when both endpoints have them. Otherwise normalized known city equality is the explicit mock fallback; it is not a fabricated distance. Unknown city/geometry is rejected as NEEDS_LOCATION. Source coordinates stay trusted, while the buyer-requested city extracted from the text is preserved separately from source city metadata. Contradictory known cities reject with LOCATION_CONFLICT before lead creation or P1 notification. An unknown extracted city stays unknown; the source city can still supply the normal fallback. Model coordinates are not invented or used to override source data.

Existing leads stay unchanged, including paid/lost/escalated states. A previously accepted event is not re-enqueued. Failed/pending events reuse the same ID, and are paused when the lead is not active. If a new run selects another item for an existing lead, the response preserves the authoritative existing item and marks its fresh score as unavailable rather than attaching the other item's score/reason.

## Bounds and persistence

Defaults: 90-second request deadline, 4 queries, 20 posts/page, 1 page/query, 20 unique posts matched, 30 inventory items, intent threshold 0.8, match threshold 0.75, maximum known-coordinate distance 50km. Limits are configurable within bounded constructor validation. Truncated work is reported. Search/model/storage calls do not run while a database write transaction is held.

Additive migration 0003 stores discovery_runs, discovery_queries, run-local discovery_posts, discovery_query_posts and discovery_matches. All extracted intents, including ignored nonbuying results, are retained separately from extraction failures. Extra retrieved posts outside the match budget remain provenance snapshots with pending evaluation.

Only one running request per seller is allowed. Request cancellation stops new model/search work; a short independent cleanup saves final run state. RecoverInterruptedRuns is called once at process startup before serving work, not on every constructor/request.

Notification status accepted means P1 accepted the event, not that a buyer message was sent or that work is durable. P1's current worker deduplicates in memory and loses execution state on restart. P2 retains event IDs/delivery status; it does not silently replay historically accepted events after restart or claim durable buyer outreach. Full application recovery/wiring remains separate integration work.

## Construction and local demo

Construct discovery.New with the shared DB, commerce service, P4 MarketplaceDiscovery adapter, QueryPlanner, PostMatcher, LeadNotifier and Options. ModelAdapter implements planner/matcher; runtime.Worker implements notifier. No competing database or LLM client is opened by the service.

```sh
set -a
source .env
set +a
go run ./internal/discovery/cmd/demo
```

The local harness serves discovery, P2 commerce routes and P4's opt-in mock-post route at localhost:8082, with seeded synthetic inventory/posts. `-db` changes its SQLite file. It uses the real LLM client and a real P1 worker whose runner only probes context loading; it deliberately does not pretend to run buyer-facing negotiation. Use the application owner's actual TurnRunner in shared app wiring.

```sh
curl -X POST http://127.0.0.1:8082/api/matches/search \
  -H 'Content-Type: application/json' \
  -d '{"sellerId":"seller_demo"}'
```

The server write timeout permits the synchronous discovery budget. P3's current Vite fixture middleware intercepts the same route; P3 must disable that fixture and proxy to this backend when integrating. No frontend implementation is modified here.

## Verification

- Unit/integration tests: dynamic query inputs, overlap/provenance, one match per post, nonbuying/low confidence, hard constraints versus high model scores, empty inventory, changed stock, multiple items/one lead, currency/quantity/location checks, partial/all dependency failures, cursor loops/caps, cancelled/concurrent requests, notification retry, terminal lead guards, and safe HTTP bodies/errors.
- Real P4 mock store + commerce + P1 worker integration tests confirm committed context and stable lead/event identity across repeated discovery.
- `go run ./internal/discovery/cmd/smoke` uses synthetic data and the live shared LLM client to verify one buying lead, ignored selling post, P1 context load, and repeat deduplication. It prints only a safe summary and requires exported credentials; no key is committed or printed.
