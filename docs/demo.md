# Demo scenarios

P4 owns the [scenario fixtures](../testdata/scenarios) and demo orchestration. Keep reusable seller, stock, and marketplace data under [testdata/seeds](../testdata/seeds) and load it through the demo reset flow.

1. Publish a Jakarta buyer's request for a used laptop with at least 16 GB RAM and a budget of Rp6 million. Discover a match, negotiate an offer, reserve stock at checkout, and complete mock payment. Show the lead and action history in the seller dashboard.
2. Ask for a price below the seller's floor or request an exception. Show the rejected action or escalated lead in the dashboard.
3. Publish a post that does not express buying intent. Verify that it produces no lead.

The [MVP acceptance checks](../INITIAL_SPEC.md#acceptance-checks) define the cases to rehearse before the demo.
