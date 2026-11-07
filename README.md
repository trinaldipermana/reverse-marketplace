# Reverse Marketplace

Reverse Marketplace is a Go-backed hackathon MVP for the Autonomous & Adaptive AI track. Buyers post what they need. Seller agents match those requests to available inventory, negotiate within seller limits, and share a checkout link. Sellers step in when a request needs their judgment.

## Demo

A buyer in Jakarta asks for a used laptop with 16 GB RAM under Rp6 million. The demo follows the request through discovery, offer, negotiation, reservation, and mock payment. The seller dashboard shows the lead and the actions taken. A second scenario shows the system rejecting a below-floor offer or escalating an exception. The demo uses a mock marketplace, mock inventory, and a local mock checkout.

## Getting started

Read the [MVP specification](./INITIAL_SPEC.md), then use its [build sequence](./INITIAL_SPEC.md#build-sequence-and-integration-gates) and [acceptance checks](./INITIAL_SPEC.md#acceptance-checks) to plan and verify your work. Local run commands will be added once the app is runnable.

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
