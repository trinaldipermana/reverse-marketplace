# Web application

P3 owns the seller dashboard, inventory and mock POS screens, lead and action views, seller policy screens, and checkout. Keep product API calls in `src/api`, shared UI in `src/components`, and screen code in `src/features`. P3 and P2 agree on the product API contract before wiring up these screens.

The seller dashboard prototype uses React and Vite.

## Run locally

```sh
npm install
npm run dev
```

The match flow calls `POST /api/matches/search`, previews one best product–buyer match with a Shopee listing link, and calls `POST /api/conversations` when the seller starts a DM. These endpoints are mocked by the Vite development server until the P2 API contract is ready.

The buyer demo at `/buyer` includes a social-post composer. Posts appear in the feed immediately. To send them to an API, set `VITE_BUYER_POSTS_API_URL` to its POST endpoint. The current request body is `{ "content", "author", "source" }`; the demo shows a local-only status while the endpoint is unset. The request contract can be updated when the API is provided.

Run `npm run build` to create a production bundle.
