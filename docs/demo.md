# Demo scenarios

The P4 [scenario fixtures](../testdata/scenarios) use English buyer posts and replies for the presentation. The buyer posts are in [marketplace.json](../testdata/seeds/marketplace.json), with Jakarta locations and prices in Indonesian rupiah. The CLI buyer simulator uses the existing mock marketplace HTTP routes to publish posts, send replies, and read threads. P2 owns seller, inventory, and policy seeding.

## Local setup

Start the [local demo API](../cmd/server/README.md) with the approved P2 instructions and classifier configuration. The simulator defaults to `http://127.0.0.1:8080`. Set `DEMO_BASE_URL` if you change the port.

Export the same `DEMO_RESET_TOKEN` for the server and simulator. The loopback-only reset seeds a new SQLite database and marketplace, swaps them in together, and cancels the previous run. The new run starts with the original seller and inventory and empty threads, leads, reservations, replay keys, and delivery receipts. Old work is confined to the retired run while it shuts down.

From the repository root, start each scenario with these commands.

```sh
./scripts/demo.sh reset
export DEMO_RUN_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
```

Keep the timestamp and fixture the same when retrying a post. Reply retries reuse the fixture's external reply ID and must target the same seller message. The simulator prints the server response so the presenter can inspect the saved post, reply event, or thread.

## Purchase

1. Run `./scripts/demo.sh post purchase` to publish a request for a used laptop for coding with at least 16 GB of RAM. The buyer needs it in Jakarta this week and has a budget under Rp 6 million.
2. Trigger discovery from the seller dashboard when its API binding is available. To exercise the backend directly, run:

   ```sh
   curl --fail-with-body -sS -H 'Content-Type: application/json' \
     --data '{"sellerId":"seller_demo"}' "${DEMO_BASE_URL:-http://127.0.0.1:8080}/api/matches/search"
   ```

   Confirm one lead for the seller and post and one initial outreach. Use `./scripts/demo.sh thread purchase` to read the buyer-visible thread.
3. Run the counteroffer command with the actual agent message ID from the thread. The seeded ThinkPad T480 lists for Rp 5.8 million. The 10% discount cap allows Rp 5.22 million, and the seller's Rp 5.4 million floor sets the effective minimum. The buyer counteroffers at that minimum. Confirm the agreed price in the thread and dashboard before sending the acceptance. If the counteroffer already led to a valid checkout link, proceed to checkout.

   ```sh
   ./scripts/demo.sh reply purchase counteroffer "$INITIAL_OFFER_MESSAGE_ID"
   ./scripts/demo.sh thread purchase
   ./scripts/demo.sh reply purchase accept "$COUNTEROFFER_MESSAGE_ID"
   ./scripts/demo.sh thread purchase
   ```

4. Complete payment on the checkout page when its API binding is available. For a backend run, use the token from the delivered `/checkout/{token}` link:

   ```sh
   base=${DEMO_BASE_URL:-http://127.0.0.1:8080}
   curl --fail-with-body -sS -H 'Idempotency-Key: demo-pay' -X POST "$base/api/checkouts/$CHECKOUT_TOKEN/pay"
   curl --fail-with-body -sS -H 'X-Seller-ID: seller_demo' "$base/api/leads/$LEAD_ID"
   curl --fail-with-body -sS -H 'X-Seller-ID: seller_demo' "$base/api/inventory/item_thinkpad"
   ```

   Verify the paid lead and one stock decrement. Repeating payment should return the original result with the same stock count.
5. Reset, verify the original stock is restored and prior threads and actions are cleared, then repeat the scenario.

## Exception and non-buying

- **Exception.** Start a fresh run and publish with `./scripts/demo.sh post exception`. Trigger discovery, read `./scripts/demo.sh thread exception`, then run `./scripts/demo.sh reply exception below-floor "$INITIAL_OFFER_MESSAGE_ID"`. Verify the requested Rp 5.1 million price never appears as a seller offer or checkout. Show the resulting action in the dashboard, such as a rejected offer, a compliant counteroffer, or an escalation.
- **Non-buying.** Start a fresh run and publish with `./scripts/demo.sh post non_buying`. Trigger discovery and verify the selling post can match the laptop search terms and creates zero leads after intent filtering.

## Verification and rehearsal

Run the fixture checks with the following command.

```sh
go test ./internal/app ./internal/demo/... ./tests/integration -run 'TestBuyer|TestDemo|TestReplyCommand|TestPostCommand' -count=1
```

The tests cover CLI fixture requests, authorized reset, and a scripted HTTP run through real discovery, guarded negotiation, mock checkout, repeat payment, and reseeding. Dashboard delivery status, the live model rehearsal, and the fallback recording are tracked in [#17](https://github.com/trinaldirizki/reverse-marketplace/issues/17). Restart-safe intake and delivery are tracked in [#10](https://github.com/trinaldirizki/reverse-marketplace/issues/10).

Rehearse the [MVP acceptance checks](../INITIAL_SPEC.md#acceptance-checks) with the integrated services. In particular, exercise duplicate events, contention for the last unit, and checkout expiry followed by a new reply and fresh checkout. Verify validation and delivery outcomes in the dashboard. If manual approval ships, confirm the system waits for approval and rechecks current facts.

Label deterministic fixture runs separately from runs with the live model. Time the live rehearsal against the [hackathon run of show](showcase/hackathon-run-of-show.md) and capture a fallback recording after the purchase and exception results are verified.
