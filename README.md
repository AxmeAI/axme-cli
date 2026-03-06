# axme-cli

**Go CLI for the AXME platform.** Manage intent lifecycle, configure runtime contexts, inspect audit logs, and operate the platform from the terminal — single binary, no runtime dependencies.

> **Alpha** · CLI surface is stabilizing. Not recommended for production scripting yet.  
> Alpha access: https://cloud.axme.ai/alpha · Contact and suggestions: [hello@axme.ai](mailto:hello@axme.ai)

---

## What Is AXME?

AXME is a coordination infrastructure for durable execution of long-running intents across distributed systems.

It provides a model for executing **intents** — requests that may take minutes, hours, or longer to complete — across services, agents, and human participants.

## AXP — the Intent Protocol

At the core of AXME is **AXP (Intent Protocol)** — an open protocol that defines contracts and lifecycle rules for intent processing.

AXP can be implemented independently.  
The open part of the platform includes:

- the protocol specification and schemas
- SDKs and CLI for integration
- conformance tests
- implementation and integration documentation

## AXME Cloud

**AXME Cloud** is the managed service that runs AXP in production together with **The Registry** (identity and routing).

It removes operational complexity by providing:

- reliable intent delivery and retries  
- lifecycle management for long-running operations  
- handling of timeouts, waits, reminders, and escalation  
- observability of intent status and execution history  

State and events can be accessed through:

- API and SDKs  
- event streams and webhooks  
- the cloud console

---

## What You Can Do With the CLI

- **Manage contexts** — configure and switch between multiple gateway environments (local, staging, production)
- **Work with intents** — list, get, watch, cancel, retry, and resume intents in real time
- **Operate agents and registry** — register, list, and resolve agent identities
- **Stream logs and traces** — follow live intent event streams from the terminal
- **Diagnose** — run `doctor` to check config, connectivity, and auth health

---

## Install

```bash
go install github.com/AxmeAI/axme-cli/cmd/axme@latest
```

Or build from source:

```bash
git clone https://github.com/AxmeAI/axme-cli.git
cd axme-cli
go build -o ./bin/axme ./cmd/axme
./bin/axme version
```

---

## Quick Setup

Before using the CLI, configure a context pointing to your AXME gateway:

```bash
axme context set default \
  --base-url "https://gateway.axme.ai" \
  --api-key "YOUR_PLATFORM_API_KEY" \
  --actor-token "OPTIONAL_USER_OR_SESSION_TOKEN" \
  --org-id "org_..." \
  --workspace-id "ws_..." \
  --owner-agent "agent://your-service" \
  --environment "production"

axme context use default
axme status        # check connectivity
axme whoami        # verify identity
```

`--api-key` maps to `x-api-key`; `--actor-token` maps to `Authorization: Bearer ...` for routes that require actor context.

---

## Create and Control Sequence

The sequence diagram below shows what happens at the network level when you run `axme intents create` — from CLI to gateway to scheduler:

![Create and Control Sequence](docs/diagrams/02-create-and-control-sequence.svg)

*The CLI sets idempotency keys and correlation IDs on every request. The gateway authenticates, validates, and persists the intent. Status is polled and streamed back to the terminal.*

---

## Rate Limits and Quotas

All API calls from the CLI are subject to platform rate limits. The quota model below shows how limits are applied per org, workspace, and API key:

![Rate Limit and Quota Model](docs/diagrams/04-rate-limit-and-quota-model.svg)

*When a rate limit is hit, the CLI displays a `429 Too Many Requests` error with a `Retry-After` value. Use `--rate-limit-wait` to auto-retry within the wait window.*

---

## Command Reference

### Context Management
```bash
axme context set <name> [flags]    # configure a new context
axme context use <name>            # switch active context
axme context list                  # list all configured contexts
axme context show                  # show active context
```

### Intents
```bash
axme intents list [--status <status>] [--limit <n>]
axme intents get <intent_id>
axme intents watch <intent_id>     # stream live state events
axme intents cancel <intent_id>
axme intents retry <intent_id>
axme intents resume <intent_id>
```

### Agents and Registry
```bash
axme agents list
axme agents register --nick "@name" --display-name "..."
axme agents resolve "@name"
```

### Operations
```bash
axme logs <intent_id>              # fetch audit log for an intent
axme trace <intent_id>             # distributed trace view
axme raw <method> <path> [body]    # raw API call (for debugging)
axme status                        # gateway and service health
axme doctor                        # config, connectivity, and auth check
axme version                       # CLI version and build info
```

Add `--json` to any command for machine-readable output.

---

## Capacity and Latency Budget

For teams doing performance analysis or capacity planning:

![Capacity and Latency Budget](docs/diagrams/04-capacity-latency-budget.svg)

*The CLI adds negligible latency overhead. Gateway p99 is the dominant term. Use `axme raw` with `--trace` to capture full timing breakdowns.*

---

## Repository Structure

```
axme-cli/
├── cmd/
│   └── axme/                  # main entry point and command tree
├── docs/
│   ├── diagrams/              # Diagram copies for README embedding
│   └── commands/              # Per-command reference pages
└── go.mod
```

---

## Tests

```bash
go test ./...
```

---

## Related Repositories

| Repository | Role |
|---|---|
| [axme-sdk-go](https://github.com/AxmeAI/axme-sdk-go) | Go SDK that the CLI is built on |
| [axme-docs](https://github.com/AxmeAI/axme-docs) | Full API reference and integration guides |
| Runtime operations (private) | Deployment and operational runbooks are maintained internally |

---

## Contributing & Contact

- Bug reports and feature requests: open an issue in this repository
- Alpha access: https://cloud.axme.ai/alpha · Contact and suggestions: [hello@axme.ai](mailto:hello@axme.ai)
- Security disclosures: see [SECURITY.md](SECURITY.md)
- Contribution guidelines: [CONTRIBUTING.md](CONTRIBUTING.md)
