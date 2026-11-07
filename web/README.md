# Web application

P3 owns the seller dashboard, inventory and mock POS screens, lead and action views, seller policy screens, and checkout. Keep product API calls in `src/api`, shared UI in `src/components`, and screen code in `src/features`. P3 and P2 agree on the product API contract before wiring up these screens.

The seller dashboard prototype uses React and Vite.

## Run locally

```sh
npm install
npm run dev
```

The seller dashboard uses real discovery by default. Start the shared Go server
from the repository root, using your locally configured OpenAI credentials:

```sh
export DEMO_RESET_TOKEN=local-demo-reset
# OPENAI_API_KEY must already be set; do not commit it.
go run ./cmd/server -port 8080 \
  -turn-instructions prompts/seller_negotiation.txt \
  -scope-config prompts/buyer_turn_scope.json
```

In another terminal, run Vite from `web/`. `DEMO_API_ORIGIN` sets the proxy target
for **all** `/api` calls and defaults to `http://127.0.0.1:8080`:

```sh
DEMO_API_ORIGIN=http://127.0.0.1:8080 npm run dev
```

`VITE_SELLER_ID` selects the seller and defaults to `seller_demo`, matching the
seed. Changes to environment variables require restarting Vite. Production
hosting must provide the same-origin `/api` reverse proxy separately; Vite's
proxy is only for local development.

Publish a buyer request before searching; a fresh server has seeded inventory
but an empty marketplace. The purchase fixture in `docs/demo.md` provides one:

```sh
# From the repository root, against your own server:
export DEMO_BASE_URL=http://127.0.0.1:8080
export DEMO_RUN_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
./scripts/demo.sh post purchase
```

The seller search calls synchronous `POST /api/matches/search` with `sellerId`
in the body. Returned matches populate the inbox with actual thread and lead
IDs. Discovery starts P1 automatically, so **Open conversation** only opens the
saved conversation and never calls `/api/conversations` in real mode. Active
thread messages and seller-scoped lead state refresh every 1.5 seconds; API
errors remain visible. Checkout-ready and paid states come from the lead API.
No local seller message composer is shown because there is no authorized manual
seller-send endpoint in this integration. Send replies using the buyer tooling.

To use the old static seller prototype explicitly, start Vite with
`VITE_SELLER_MOCK=true npm run dev`. Only this mode enables the match/conversation
mock middleware and sample inbox; buyer API calls still use the configured proxy.

The buyer demo at `/buyer` retains its intended fixed demo thread. Its post/reply
adapters use the mock marketplace API. Live buyer-side agent-message rendering,
checkout-page routing, and Discovery Backstage same-run wiring are separate work;
this seller integration does not claim the full browser purchase flow is complete.

The opt-in judge screen is available at `/hackathon`. It saves one buyer post, runs discovery once for `seller_demo`, and follows the returned lead and thread. Its `/api/hackathon/*` requests are rewritten to the existing `/api/*` routes and use the configured Vite API proxy (`DEMO_API_ORIGIN`, default `http://127.0.0.1:8080`). Other screens keep using their existing `/api/*` routes. Start the unified server using [`cmd/server/README.md`](../cmd/server/README.md). “Link sent” appears only after the checkout URL is present in a saved seller message in the buyer thread.

Run `npm run build` to create a production bundle.
