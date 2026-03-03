# axme-cli

Go CLI for Axme Cloud control surface and intent operations.

## Status

CLI-first alpha implementation.

`login` web redirect/device flow is intentionally deferred until `axme-cloud-landing` domain auth form is connected to API. For now, use token/API-key based login.

## Build

```bash
go build -o ./bin/axme ./cmd/axme
./bin/axme version
```

## Quick setup

```bash
./bin/axme context set default \
  --base-url "https://axme-gateway.example.com" \
  --api-key "..." \
  --bearer-token "..." \
  --org-id "org_..." \
  --workspace-id "ws_..." \
  --owner-agent "agent://ops" \
  --environment "staging"

./bin/axme context use default
./bin/axme status
./bin/axme whoami
```

Env fallbacks are also supported:

- `AXME_PORTAL_BASE_URL` (or `AXME_GATEWAY_BASE_URL`)
- `AXME_GATEWAY_API_KEY`
- `AXME_PORTAL_SCOPED_BEARER_TOKEN`
- `AXME_ORG_ID`
- `AXME_WORKSPACE_ID`
- `AXME_OWNER_AGENT`

## Command groups

- `login`, `whoami`, `context`, `logout`
- `init`, `examples`, `run`
- `intents list|get|watch|cancel|retry|resume`
- `logs`, `trace`
- `agents list|register|resolve`
- `keys list|create|revoke`
- `status`, `doctor`, `version`
- `raw`

Use `--json` for machine-readable output in any command.

## Examples

```bash
./bin/axme examples
./bin/axme run approval-resume
./bin/axme intents list --status WAITING --limit 20
./bin/axme intents watch intent_123 --follow
./bin/axme raw GET /v1/capabilities
```

## Tests

```bash
go test ./...
```
