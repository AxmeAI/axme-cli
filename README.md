# AXME CLI

Run async workflows in 2 minutes.

```bash
axme login
axme examples run human/cli
```

```
  Intent submitted
  Waiting for approval...
  Approved
  Completed

Result: deployment approved
```

Try it:

```bash
curl -fsSL https://raw.githubusercontent.com/AxmeAI/axme-cli/main/install.sh | sh
```

Works with AXME Cloud or your own agent runtime.

Main project: [github.com/AxmeAI/axme](https://github.com/AxmeAI/axme)

---

**Go CLI for the AXME platform.** Manage intent lifecycle, configure runtime contexts, inspect audit logs, and operate the platform from the terminal — single binary, no runtime dependencies.

> **Alpha** - CLI surface is stabilizing. Install, log in, and run your first example in under 5 minutes. [Quick Start](https://cloud.axme.ai/alpha/cli) · [hello@axme.ai](mailto:hello@axme.ai)

---

## What You Can Do With the CLI

- **Authenticate and onboard** — `axme login` is the primary cloud alpha entry point; direct key/token input is still supported
- **Manage contexts** — configure and switch between multiple gateway environments (local, staging, production)
- **Work with intents** — list, get, watch, cancel, retry, and resume intents in real time
- **Operate agents and registry** — register, list, and resolve agent identities
- **Stream logs and traces** — follow live intent event streams from the terminal
- **Diagnose** — run `doctor` to check config, connectivity, and auth health
- **Quota** — `axme quota show` to view limits and usage; `axme quota upgrade-request` to request a corporate-tier upgrade

---

## Install and Update

Install or update to the latest release (Linux/macOS, no Go required):

```bash
curl -fsSL https://raw.githubusercontent.com/AxmeAI/axme-cli/main/install.sh | sh
# Open a new terminal, or run the "source" command shown by the installer
```

The same command installs if you don't have the CLI, and upgrades to the latest release if you do. After install the CLI periodically checks for new versions in the background and prompts you to update.

Install a specific released version:

```bash
curl -fsSL https://raw.githubusercontent.com/AxmeAI/axme-cli/main/install.sh | AXME_VERSION=v0.2.2 sh
```

Update explicitly from within the CLI:

```bash
axme update
```

Manual downloads:

- GitHub Releases: https://github.com/AxmeAI/axme-cli/releases
- Linux/macOS archives are published as `tar.gz`
- Windows archives are published as `zip`

Go-based install remains available for contributors and local development:

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

Preferred cloud path — email-based passwordless login:

```bash
axme login
# Enter your email → receive OTP → enter code
# CLI automatically selects or prompts for workspace
axme whoami
axme quota show
```

`axme login` sends a one-time code to your email. Enter it in the CLI — no browser required, no API key to copy. On success the CLI stores credentials securely (OS keyring on desktop; `~/.config/axme/secrets.json` mode 0600 on servers and SSH sessions) and selects your workspace.

The `access_token` (JWT) has a 15-minute TTL. The CLI automatically refreshes it using the stored `refresh_token` (30-day TTL) before each command — you only need to re-login after 30 days of inactivity.

Direct/manual context setup remains available:

```bash
axme context set default \
  --base-url "https://api.cloud.axme.ai" \
  --api-key "AXME_API_KEY_FROM_CLOUD_ALPHA" \
  --actor-token "OPTIONAL_USER_OR_SESSION_TOKEN" \
  --org-id "org_..." \
  --workspace-id "ws_..." \
  --owner-agent "agent://your-service" \
  --environment "production"

axme context use default
axme status
axme whoami
```

`--api-key` maps to `x-api-key` (service-account/workspace key from AXME Cloud alpha); `--actor-token` maps to `Authorization: Bearer ...` for actor-scoped routes.

---

## Create and Control Sequence

The sequence diagram below shows what happens at the network level when you run `axme run` — from CLI to gateway to scheduler:

![Create and Control Sequence](https://raw.githubusercontent.com/AxmeAI/axme-docs/main/docs/diagrams/intents/02-create-and-control-sequence.svg)

*The CLI sets idempotency keys and correlation IDs on every request. The gateway authenticates, validates, and persists the intent. Status is polled and streamed back to the terminal.*

---

## Rate Limits and Quotas

All API calls from the CLI are subject to platform rate limits.

Alpha quota tiers:

| Tier | intents/day | actors | service accounts |
|---|---|---|---|
| email_verified | 500 | 20 | 10 |
| corporate | 5 000 | 200 | 50 |

`email_verified` tier is applied automatically on first login via email OTP.
For `corporate`, run `axme quota upgrade-request` to submit a review request.

<details>
<summary>Rate Limit and Quota Model (diagram)</summary>

![Rate Limit and Quota Model](https://raw.githubusercontent.com/AxmeAI/axme-docs/main/docs/diagrams/api/04-rate-limit-and-quota-model.svg)

*When a rate limit is hit, the CLI displays a `429 Too Many Requests` error with a `Retry-After` value. Retry the command after the indicated wait window.*

</details>

---

## Command Reference

### Authentication and Login
```bash
axme login                           # email OTP login (primary path — no browser, no key copy)
axme login --browser                 # browser-assisted flow (legacy, requires existing key/context)
axme login --api-key <key>           # non-interactive: store API key directly
axme login --actor-token <jwt>       # store an actor JWT for actor-scoped routes
axme whoami                          # show current identity, context, and active sessions
axme session list                    # list active sessions
axme session revoke <session_id>     # revoke a specific session
axme session revoke --current        # revoke the current session (equivalent to logout on this device)
axme logout                          # clear credentials for the active context
```

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

### Human Tasks

Resolve human-in-the-loop steps from the terminal. AXME supports 8 task types: `approval`, `confirmation`, `review`, `assignment`, `form`, `clarification`, `manual_action`, `override`.

```bash
axme tasks list                       # list pending human tasks
axme tasks get <intent_id>            # show task details and allowed outcomes
axme tasks approve <intent_id>        # approve (approval, review)
axme tasks reject <intent_id>         # reject (approval, review, override)
axme tasks confirm <intent_id>        # confirm (confirmation)
axme tasks complete <intent_id>       # complete (manual_action)
axme tasks assign <intent_id>         # assign (assignment)
axme tasks submit <intent_id> \       # generic: any outcome + data
  --outcome provided \
  --comment "Here is the info you requested"
```

### Scenarios

```bash
axme scenarios apply scenario.json              # provision agents + submit intent
axme scenarios apply scenario.json --watch       # same + stream lifecycle events
```

### Agents and Registry
```bash
axme agents list
axme agents register --name "<name>"
axme agents show <address>
axme agents delete <service_account_id>
axme agents keys create --agent-id <sa_id>
axme agents keys revoke --agent-id <sa_id> --key-id <sak_id>
```

<details>
<summary>Advanced: Policies and Overrides</summary>

### Agent Send Policies
```bash
axme agents policy get <address>                              # show send policy (default: open)
axme agents policy set <address> <open|allowlist|denylist>    # set send policy mode
axme agents policy add <address> <sender_pattern>             # add pattern to allowlist/denylist
axme agents policy remove <address> <entry_id>                # remove pattern entry
```

### Org Receive Policy (Cross-Org Intent Delivery)

Control which external organizations can send intents to your org. Default: `closed` (no cross-org inbound).

```bash
axme org receive-policy get                                   # show org receive policy
axme org receive-policy set <open|allowlist|closed>           # set receive policy mode
axme org receive-policy add <sender_pattern>                  # add sender to allowlist
axme org receive-policy remove <entry_id>                     # remove allowlist entry
```

### Agent Receive Override

Per-agent exception to the org receive policy. Enables "public agents" in a closed org.

```bash
axme agents receive-override get <address>                                       # show override (default: use_org_default)
axme agents receive-override set <address> <open|allowlist|closed|use_org_default>  # set override
axme agents receive-override add <address> <sender_pattern>                      # add to agent allowlist
axme agents receive-override remove <address> <entry_id>                         # remove entry
```

</details>

### Organizations and Workspaces
```bash
axme org list                        # list organizations in your account
axme workspace list                  # list workspaces visible to your account
axme workspace use <workspace_id>    # switch active workspace (persisted to context)
```

### Workspace Member Management
```bash
# Org-level members (invitation, role change, removal from org)
axme member list                                     # list org members
axme member add <actor_id> --role <role>             # add actor to org + workspace
axme member remove <member_id>                       # remove member from org entirely
axme member update-role <member_id> --role <role>    # change member role

# Workspace-scoped access (include/exclude without touching org membership)
axme workspace members list                          # list members in current workspace
axme workspace members include <actor_id> --role <role>   # grant workspace access to an org member
axme workspace members exclude <member_id>           # revoke workspace access (member stays in org)
```

`axme workspace members include/exclude` operates within the workspace only — it does not add or remove the user from the organization. Use `axme member add/remove` for org-level changes.

### Operations
```bash
axme logs <intent_id>              # fetch audit log for an intent
axme trace <intent_id>             # distributed trace view
axme status                        # gateway and service health
axme doctor                        # config, connectivity, and auth check
axme version                       # CLI version and build info
axme update                        # update CLI to the latest release
```

Add `--json` to any command for machine-readable output.

### Service Accounts and Keys
```bash
axme service-accounts create --name <name>
axme service-accounts list
axme service-accounts keys create --service-account-id <sa_id>
axme service-accounts keys revoke --service-account-id <sa_id> --key-id <sak_id>
```

`axme service-accounts ...` uses the active account session plus workspace API key. The CLI resolves the effective organization/workspace from your selected personal context unless you override it explicitly.

### Quota
```bash
# View your current limits and usage
axme quota show

# Request a corporate-tier upgrade (reviewed within 1 business day)
axme quota upgrade-request \
  --company "Acme Corp" \
  --justification "Running a production pilot with ~50 agents"

# Optional: request a specific tier (default: corporate)
axme quota upgrade-request --company "..." --justification "..." --tier corporate
```

`axme quota show` output example:
```
DIMENSION                   USED  LIMIT  USAGE%
intents per day             48    50     96% ⚠
actors total                3     5      60%
service accounts per workspace  1     2      50%

overage_mode=block  hard_enforcement=true

To request higher limits: axme quota upgrade-request --company "..." --justification "..."
```

Platform-operator workflows are intentionally not part of the public `axme` user CLI. Internal operators should use the dedicated tooling and contract documentation in `axme-security-ops`.

---

<details>
<summary>Capacity and Latency Budget (diagram)</summary>

For teams doing performance analysis or capacity planning:

![Capacity and Latency Budget](https://raw.githubusercontent.com/AxmeAI/axme-docs/main/docs/diagrams/operations/04-capacity-latency-budget.svg)

*The CLI adds negligible latency overhead. Gateway p99 is the dominant term.*

</details>

---

## Repository Structure

```
axme-cli/
├── cmd/
│   └── axme/                  # main entry point and command tree
├── docs/
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
| [axme-examples](https://github.com/AxmeAI/axme-examples) | End-to-end examples and scenario walkthroughs |
| [axme-docs](https://github.com/AxmeAI/axme-docs) | Full API reference and integration guides |
| Runtime operations (private) | Deployment and operational runbooks are maintained internally |

---

## Contributing

Bug reports and feature requests: open an issue. See [CONTRIBUTING.md](CONTRIBUTING.md).

---

[hello@axme.ai](mailto:hello@axme.ai) · [Security](SECURITY.md) · [License](LICENSE)
