# MVP folder structure

The MVP uses one Go module and one server with a shared database. The server entry point belongs in [cmd/server](../cmd/server). [internal/app](../internal/app) assembles the services and mounts handlers supplied by their owners. [web](../web) contains the frontend.

| Path | Responsibility |
| --- | --- |
| `internal/llm`, `internal/runtime`, `internal/runtime/tools` | P1 owns the shared model adapter, lead turns, event processing, action records, limits, and the six conversation tool wrappers. |
| `internal/discovery`, `internal/commerce`, `internal/commerce/mockpos` | P2 owns intent extraction, matching, commerce operations, product HTTP handlers, mock inventory, and mock payment. |
| `web/src` | P3 owns the dashboard, inventory, lead, seller policy, and checkout screens that call P2 product APIs. |
| `internal/marketplace`, `internal/marketplace/mock`, `internal/demo` | P4 owns marketplace APIs, the mock connector, search, messaging, and demo orchestration. |
| `internal/domain`, `internal/contracts` | Shared domain types, service interfaces, and event formats after owner agreement. |
| `internal/store`, `migrations` | Shared database infrastructure and one ordered migration sequence. Component persistence code lives with its owner. |
| `prompts`, `schemas`, `evals` | P2 prompt assets, structured model output schemas, and expected intent, matching, and conversation behavior. |
| `testdata`, `tests/integration`, `scripts` | Seed and scenario fixtures, cross-component checks, and local demo commands. |

Discovery calls P4's `SearchPost` to get candidate buyer requests. P2 filters for buying intent, matches eligible inventory, and creates leads. The P1 runtime starts turns for new leads and buyer replies. Its six tool wrappers call P2 commerce methods and P4 message delivery through agreed interfaces. `SearchPost` is part of discovery.

Go validates proposed actions and controls lead state. P4 delivery reads approved outbound actions from the outbox and retries with the same idempotency key. P2 checks stock atomically when reserving and commits mock payment, stock consumption, reservation, checkout, and paid lead state in one database transaction.

Before integration, record shared agreements under [docs/contracts](contracts). The [spec](../INITIAL_SPEC.md#implementation-defaults-and-open-decisions) tracks open choices for the database engine and migration owner, frontend stack, model adapter, API schemas, event recovery, approval, and seller hand-back.
