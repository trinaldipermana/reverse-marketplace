# Discovery Backstage

Standalone local discovery viewer for presenters. The default page shows the
whole flow in five columns: Inventory → Generate search queries → Search posts
→ Match items → Save lead. Completed results stay visible as later stages run.
All lists are scrollable; each selected match pairs its original post with its
item. Progress has no artificial presentation delay.

## Run

Requires Python 3 and a running discovery backend with its corresponding SQLite
database. No frontend build, Python packages or credentials are needed by the
viewer. The backend performs any real model calls.

```sh
# Start the existing component harness from the repository root first.
# OPENAI_API_KEY and model settings must already be in your environment.
go run ./internal/discovery/cmd/demo \
  -addr 127.0.0.1:8082 -db /tmp/reverse-marketplace-local-demo.sqlite

# In another terminal, from the repository root:
python3 tools/discovery-view/server.py \
  --db /tmp/reverse-marketplace-local-demo.sqlite \
  --backend http://127.0.0.1:8082
```

Open http://127.0.0.1:8766/. `/overview` is an alias of the default view. Use `--port` for another viewer port
and `--runtime-label` to identify the actual backend runtime.

The component harness defaults to a P1 context-load probe: runtime acceptance
is not proof of conversation execution or a delivered buyer message. The board
shows discovery through saved leads only. Do not label the harness as a full
shared-app purchase rehearsal.

`Discover buyers` calls the backend's synchronous discovery once. The viewer
reads persisted evidence while it runs. `Replay` walks through saved evidence
without triggering a backend request. Refreshing the viewer only reads data.
Current stock and lead state are not historical inventory snapshots. The local
seller is `seller_demo`. Inventory images use `attributes.image_url`; bundled
laptop illustrations are demo artwork, not product photographs. They appear
only when the stored item attribute references the appropriate `/product-*.svg`.

The shared `cmd/server` currently uses a fresh temporary database per run/reset.
This viewer must be given that exact database to observe it; passing an unrelated
component database will show unrelated runs. Automatic dashboard opening and
same-run correlation remain integration work in #50. This change does not
connect the product dashboard to the viewer or change the buyer demo thread.

## Safety and verification

The server binds to loopback, reads SQLite in read-only mode, serves an explicit
static-file allowlist and projects only the discovery fields needed by the UI.
It excludes private pricing policies, action results, payment tokens and API keys.
Only its explicit discovery proxy action can trigger normal backend writes.

```sh
python3 -m unittest discover -s tools/discovery-view -p test_server.py
node --check tools/discovery-view/overview.js
```
