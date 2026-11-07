# Web application

P3 owns the seller dashboard, inventory and mock POS screens, lead and action views, seller policy screens, and checkout. Keep product API calls in `src/api`, shared UI in `src/components`, and screen code in `src/features`. P3 and P2 agree on the product API contract before wiring up these screens.

The seller dashboard prototype uses React and Vite.

## Run locally

```sh
npm install
npm run dev
```

The match flow calls `POST /api/matches/search`, previews one best product–buyer match with a Shopee listing link, and calls `POST /api/conversations` when the seller starts a DM. These endpoints are mocked by the Vite development server until the P2 API contract is ready.

Run `npm run build` to create a production bundle.
