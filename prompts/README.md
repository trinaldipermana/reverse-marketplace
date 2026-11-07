# Model prompts

P2 maintains discovery intent and ranking prompts. P1 owns the buyer-scope and
seller-negotiation runtime prompts, conversation execution, and tool wrappers.
These runtime assets are tracked in
[issue #35](https://github.com/trinaldirizki/reverse-marketplace/issues/35).
The named prompt catalog lives in the [root README](../README.md#named-system-prompts). The files below contain the
complete canonical prompt content; the catalog links to them instead of keeping
a second hand-maintained copy.

- `seller_negotiation.txt` provides English six-tool negotiation instructions.
- `buyer_turn_scope.json` is the complete startup classifier configuration:
  instructions, schema name, strict output schema, and `decision_field: scope`.
  It matches the guarded server configuration introduced by P4 in PR #48;
  it introduces no service or frontend API.
- `../evals/buyer_turn_scope.json` records expected decisions for eight synthetic
  scope cases. These check meaning rather than exact buyer-facing wording.

Pass the runtime prompt files explicitly:

```sh
go run ./cmd/server -turn-instructions prompts/seller_negotiation.txt -scope-config prompts/buyer_turn_scope.json
```

The server also requires its existing OpenAI and demo reset configuration.
These files do not enable a server or classifier by merely existing. Mixed
requests remain blocked until a safe commerce-only projection is added.
Only P4's approved English message forms may be sent; the prompt cannot add
unsupported explanations, warranty promises, or delivery terms. The off-topic
redirect belongs to the guard and validated P4 sender.

## Opt-in fixed-price partner-link draft

`fixed_price_partner_link.txt` is a separate file-backed system prompt for
[#54](https://github.com/trinaldirizki/reverse-marketplace/issues/54) and
[#56](https://github.com/trinaldirizki/reverse-marketplace/issues/56). It covers
trusted fixed-price outreach and a buyer requesting the existing partner listing.
It does not call the four mutation tools, change the lead from active, or claim
checkout, payment, stock reservation or conversion. Link sent requires confirmed
delivery; pending remains pending.

P4 supplied the two exact approved message originals in
[#53](https://github.com/trinaldirizki/reverse-marketplace/issues/53#issuecomment-6703378861):
`I found a match at a fixed price of {listing_price}.` and
`Here is the partner listing: {partner_listing_link}`. The prompt preserves those
placeholders; P4 renders them from the seller/item-scoped trusted listing in Go.
No new URL field, tool arguments or launch selector are chosen here. The approved
URL fixture and runtime/delivery implementation binding are still integration
dependencies in #52/#53/#56. Use this asset together with the opt-in Go policy;
a prompt file alone does not enforce the mode. No new-mode live run is claimed.
The existing `seller_negotiation.txt`, `buyer_turn_scope.json`, purchase startup
command and existing evals remain unchanged.

Run the deterministic asset-to-guard check without network access:

```sh
go test ./tests/integration -run TestPromptAssetsBindScopeGuard -count=1
```

For opt-in live synthetic checks, first export the existing `OPENAI_API_KEY`
and optional `OPENAI_MODEL` securely in your terminal, then run:

```sh
RUN_PROMPT_EVAL=1 go test ./tests/integration -run 'TestPromptLive(Scope|Purchase)$' -count=1 -v -timeout 5m
```

The scope check calls the actual model on the eight cases. The purchase check
uses the actual model, ScopeGuard, six tool wrappers, temporary SQLite commerce,
and the local guarded marketplace sender to publish an initial offer and a
checkout after acceptance. It sends no external buyer messages. It does not
exercise the shared server/UI, production history loader, payment, or the full
below-floor/mixed flows. A passing sample is not a guarantee of semantic
classification on every request; Go remains authoritative for tool enforcement.

Prompt structure follows the official OpenAI guidance on
[prompt engineering](https://developers.openai.com/api/docs/guides/prompt-engineering)
and [structured outputs](https://developers.openai.com/api/docs/guides/structured-outputs).
