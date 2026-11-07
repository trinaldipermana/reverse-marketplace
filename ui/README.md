# Seller dashboard UI

React + Vite prototype for the seller-side reverse marketplace workflow.

## Run locally

```sh
npm install
npm run dev
```

The match flow calls `POST /api/matches/search`, then previews one best product–buyer match. **Start DM with buyer** calls `POST /api/conversations` and opens a blank conversation so the seller can write the first message.

Both endpoints are mocked by the Vite dev server in `vite.config.js` for the hackathon demo. Replace that middleware with the real matching and conversation API when the backend is available. The dashboard uses illustrative data.

The sample Alesis Nitro Max match links to a Shopee product page so the seller can inspect the marketplace listing from the preview and sale details.

## Build

```sh
npm run build
```
