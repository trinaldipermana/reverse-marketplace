# Reverse Marketplace

Reverse Marketplace is a Go-backed hackathon MVP for the Autonomous & Adaptive AI track. Buyers post what they need. Seller agents match those requests to available inventory, negotiate within seller limits, and share a checkout link. Sellers step in when a request needs their judgment.

## Demo

A buyer in Jakarta asks for a used laptop with 16 GB RAM under Rp6 million. The demo follows the request through discovery, offer, negotiation, reservation, and mock payment. The seller dashboard shows the lead and the actions taken. A second scenario shows the system rejecting a below-floor offer or escalating an exception. The demo uses a mock marketplace, mock inventory, and a local mock checkout.

## Getting started

Read the [MVP specification](./INITIAL_SPEC.md) and the [folder structure and ownership guide](./docs/architecture.md). Use the spec's [build sequence](./INITIAL_SPEC.md#build-sequence-and-integration-gates) and [acceptance checks](./INITIAL_SPEC.md#acceptance-checks) as each component is implemented.

With Go 1.26 or newer, run `make check` to build and test the Go packages in the scaffold.

The seller dashboard prototype is in [`web/`](./web/README.md).

## Team ownership

| Role | Owns | Proposed GitHub label |
| --- | --- | --- |
| P1 Runtime | Agent turns, event worker, tool wrappers, runtime limits, and action logs | `area:runtime` |
| P2 Commerce | Discovery and matching, product APIs, inventory and mock POS backend, reservations, and mock payment | `area:commerce` |
| P3 UI | Seller dashboard, inventory and POS screens, and checkout UI | `area:ui` |
| P4 Demo | Mock marketplace, search and messaging, buyer simulator, and demo scenarios | `area:demo` |

## How we work

- Find your work in [GitHub Issues](https://github.com/trinaldirizki/reverse-marketplace/issues). Area labels show which part of the project an issue affects. The assignee is the person responsible for moving it forward. Use one primary assignee per issue, even when several areas are involved.
- Start from `main`, work on a separate branch, and open a pull request into `main`. Link the issue, describe what changed, and include how you verified it. Get one teammate’s approval before merging.
- If you find a bug, blocker, or compatibility problem, update the existing issue or open a new one. Apply the relevant area labels. If ownership is unclear, leave the issue unassigned and add `needs-triage`.

See [AGENTS.md](./AGENTS.md) for the rules agents follow.

## P1 OpenAI client

The shared model client lives in `internal/llm/`. It uses the OpenAI Responses API
through Go's standard HTTP library; no external dependencies are required.

Use Go 1.26 or newer. Copy `.env.example` to `.env` if you do not already have
a local `.env`, then set your OpenAI API key. The example contains no credentials.
Configuration uses these environment variables:

```dotenv
OPENAI_API_KEY=your-local-key
OPENAI_MODEL=gpt-6.1-sol
```

`OPENAI_MODEL` defaults to `gpt-6.1-sol`. The client reads exported environment
variables; it does not automatically load `.env`. If your local `.env` contains
shell-compatible assignments, load it in your terminal before running:

```sh
set -a
source .env
set +a
go run ./internal/llm/cmd/smoke
```

The smoke command makes one live model request using synthetic input. It checks
strict JSON output and prints the model and success status. It does not print
the API key or send project documents. `.env` is ignored by Git.

Run local tests without a key or network access:

```sh
go test ./...
```

### Calling the client

```go
cfg, err := llm.ConfigFromEnv()
if err != nil {
    return err
}
client, err := llm.NewOpenAIClient(cfg)
if err != nil {
    return err
}
response, err := client.Generate(ctx, llm.Request{
    Messages: []llm.ChatMessage{
        {Role: "developer", Content: "Answer briefly in the buyer's language."},
        {Role: "user", Content: "Halo!"},
    },
})
```

Import `github.com/trinaldirizki/reverse-marketplace/internal/llm` from within
this module. `Generate` returns text and tool calls; it does not execute tools or
deliver messages. For tool continuation, pass the returned `Continuation`, tool
results keyed by `ToolCall.ID`, and the tool definitions into the next request.
Do not repeat the previous messages: the continuation already contains them.
The continuation retains protocol items, including encrypted reasoning, in memory;
do not log it. `GenerateJSON` accepts a JSON schema and a non-nil output pointer.

Requests disable server-side response storage (`store: false`) and parallel tool
calls. This storage setting does not replace OpenAI's broader data retention policies.
Each call has a 20-second timeout, bounded by the caller's context. Refusals,
incomplete output and API errors return errors without upstream error bodies.
Automatic retries, concrete business tools, business guardrails and the worker
are deferred.

### GenerateJSON for discovery

[Issue #6](https://github.com/trinaldirizki/reverse-marketplace/issues/6) tracks
the P2 handoff. The consuming service can depend on this small interface:

```go
type JSONGenerator interface {
    GenerateJSON(context.Context, llm.JSONRequest, any) error
}
```

`llm.JSONRequest` contains `Instructions`, `Input`, `SchemaName` and `Schema`
(`json.RawMessage`). Supply a strict JSON schema supported by OpenAI and a non-nil
output pointer. P2 owns prompts, schemas and business validation; P1 owns provider
configuration and execution. Application composition passes the configured shared
client to discovery and runtime. The client imports neither service.

Success decodes the model's JSON into the output pointer. Refusals return
`llm.ErrRefusal`; non-completed responses return `llm.ErrIncomplete`. Cancellation
and deadline errors wrap the context error. HTTP errors return `*llm.APIError`
with a status code, without the upstream error body. Invalid configuration,
requests or decoded output return errors; consumers must not treat errors as
valid empty matches. Strict output is requested from the provider; this client
does not implement a local JSON Schema or business-rule validator.

API reference: [function calling](https://developers.openai.com/api/docs/guides/function-calling)
and [structured outputs](https://developers.openai.com/api/docs/guides/structured-outputs).

## P2 inventory API integration

The standalone inventory and policy harness runs with `go run ./internal/commerce/mockpos/cmd/demo` on localhost:8082. See [P2 inventory integration](docs/contracts/p2-inventory.md) for API examples, SQLite/CGO requirements, seeding, and the reservation adapter boundary. It does not run checkout, discovery, or the full marketplace.
