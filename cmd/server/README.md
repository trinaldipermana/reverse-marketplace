# Local demo API

From the repository root, export `OPENAI_API_KEY` and a local operator
`DEMO_RESET_TOKEN`. Provide the approved negotiation instructions as a text
file and the approved scope classifier configuration as a JSON file.

```sh
go run ./cmd/server -turn-instructions /path/to/instructions.txt -scope-config /path/to/scope.json
```

The scope configuration contains the P2-approved `instructions`, `schema_name`,
and `schema` JSON object. Set `decision_field` to the field in that schema
which returns `in_scope`, `off_topic`, or `mixed`. The app requires these
assets at startup and uses the guarded P1 runtime for both initial outreach and
buyer replies. Mixed requests stay blocked until the commerce-only projection
is agreed.

The server binds to `127.0.0.1:8080`. Use `-port` to change the port and set
`DEMO_BASE_URL` to the corresponding origin for the simulator. Each run uses a
fresh, temporary SQLite database and process-local marketplace and runtime
state. A successful authorized reset installs a fresh run and cancels the
previous run. Pending reply acknowledgments remain volatile.

See the [demo runbook](../../docs/demo.md) for scenario commands. The server
provides the existing discovery, marketplace, seller, and checkout APIs. The
seller dashboard and buyer checkout page are separate P3 integration work.
