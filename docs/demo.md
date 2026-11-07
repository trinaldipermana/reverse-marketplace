# Demo scenarios

The P4 [scenario fixtures](../testdata/scenarios) reference the buyer posts in [marketplace.json](../testdata/seeds/marketplace.json). The buyer simulator publishes those posts through the mock marketplace HTTP API. P2 owns seller, inventory, and policy seeding.

## Local setup

Start the integrated demo server with the mock marketplace routes, P1 runtime, P2 commerce services, and P3 dashboard and checkout surfaces. The CLI defaults to `http://127.0.0.1:8080`. Set `DEMO_BASE_URL` if the local server uses a different address.

The wiring owner registers `POST /api/demo/reset` with `demo.RegisterReset` only in the opt-in demo configuration on a loopback listener. Supply the operator token from that configuration as `DEMO_RESET_TOKEN` in the CLI environment. The reset operation must block or fence old work before atomically clearing run state and restoring the agreed P2 seeds. That includes posts, messages, replay keys, lead and action history, pending events, delivery receipts, reservations, checkouts, and run-specific stock changes. A successful reset returns 204 once the new run can accept traffic.

From the repository root, start each scenario with these commands.

```sh
./scripts/demo.sh reset
export DEMO_RUN_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
```

Keep the timestamp and fixture the same when retrying a post. Reply retries reuse the fixture's external reply ID and must target the same seller message. The simulator prints the server response so the presenter can inspect the saved post, reply event, or thread.

## Purchase

1. Run `./scripts/demo.sh post purchase` to publish the Jakarta request for a used coding laptop with at least 16 GB of RAM, under Rp6 million this week.
2. Trigger discovery from the seller dashboard. Confirm one lead for the seller and post and one initial outreach. Use `./scripts/demo.sh thread purchase` to read the buyer-visible thread.
3. Run the counteroffer command with the actual agent message ID from the thread. The proposed commerce seed lists the item at Rp5.8 million, with a Rp5.2 million floor and 10% discount cap. That yields a minimum of Rp5.22 million, so the Rp5.4 million buyer counteroffer is permitted. Confirm the agreed price in the thread and dashboard before sending the acceptance. If the counteroffer already led to a valid checkout link, proceed to checkout.

   ```sh
   ./scripts/demo.sh reply purchase counteroffer "$INITIAL_OFFER_MESSAGE_ID"
   ./scripts/demo.sh thread purchase
   ./scripts/demo.sh reply purchase accept "$COUNTEROFFER_MESSAGE_ID"
   ./scripts/demo.sh thread purchase
   ```

4. Open the delivered link and complete the simulated payment on the local checkout page. Verify the paid lead and one stock decrement in the dashboard. Repeating the same payment should return the original result with the same stock count.
5. Reset, verify the original stock is restored and prior threads and actions are cleared, then repeat the scenario.

## Exception and non-buying

- **Exception.** Start a fresh run and publish with `./scripts/demo.sh post exception`. Trigger discovery, read `./scripts/demo.sh thread exception`, then run `./scripts/demo.sh reply exception below-floor "$INITIAL_OFFER_MESSAGE_ID"`. Verify the requested Rp5.1 million price never appears as a seller offer or checkout. Show the rejected action or escalation in the dashboard.
- **Non-buying.** Start a fresh run and publish with `./scripts/demo.sh post non_buying`. Trigger discovery and verify the selling post can match the laptop search terms and creates zero leads after intent filtering.

## Verification and rehearsal

Run the fixture checks with the following command.

```sh
go test ./internal/demo/... ./tests/integration -run 'TestBuyer|TestDemo|TestReplyCommand|TestPostCommand' -count=1
```

These checks exercise the CLI, fixture loading, request/replay shapes, and local reset admission through in-process HTTP test doubles. The application reset transaction, old worker fencing, durable replay, live checkout/payment and dashboard behavior are integration gates tracked in [#17](https://github.com/trinaldirizki/reverse-marketplace/issues/17).

Rehearse the [MVP acceptance checks](../INITIAL_SPEC.md#acceptance-checks) with the integrated services. In particular, exercise duplicate events, contention for the last unit, and checkout expiry followed by a new reply and fresh checkout. Verify validation and delivery outcomes in the dashboard. If manual approval ships, confirm the system waits for approval and rechecks current facts.

Label deterministic fixture runs separately from runs with the live model. Time the live rehearsal against the [hackathon run of show](showcase/hackathon-run-of-show.md) and capture a fallback recording after the purchase and exception results are verified.
