# axme-cli

Command-line interface for Axme Cloud enterprise and portal operations.

## Status

Active CLI implementation for alpha CLI-first scope.

## Install (local development)

```bash
python -m pip install -e .
```

This provides the `axme` command from `pyproject.toml`.

## Configuration

CLI supports explicit flags or environment variables:

- `--base-url` or `AXME_PORTAL_BASE_URL` (fallback: `AXME_GATEWAY_BASE_URL`, then `http://127.0.0.1:8100`)
- `--api-key` or `AXME_GATEWAY_API_KEY`
- `--bearer-token` or `AXME_PORTAL_SCOPED_BEARER_TOKEN`
- `--default-org-id` or `AXME_ORG_ID`
- `--default-workspace-id` or `AXME_WORKSPACE_ID`

Example:

```bash
export AXME_PORTAL_BASE_URL="https://axme-gateway-uc2bllq3la-uc.a.run.app"
export AXME_GATEWAY_API_KEY="..."
export AXME_PORTAL_SCOPED_BEARER_TOKEN="..."
export AXME_ORG_ID="..."
export AXME_WORKSPACE_ID="..."
```

## Quick Start

```bash
axme health
axme portal-navigation --org-id "$AXME_ORG_ID" --workspace-id "$AXME_WORKSPACE_ID"
axme portal-personal-overview --org-id "$AXME_ORG_ID" --workspace-id "$AXME_WORKSPACE_ID"
axme deliveries-operations --org-id "$AXME_ORG_ID" --workspace-id "$AXME_WORKSPACE_ID" --window-hours 24
```

All commands return JSON:

```json
{
  "status_code": 200,
  "ok": true,
  "body": { "...": "..." }
}
```

Use `--compact` for one-line JSON output.

## Command Groups

### Organizations and Workspaces

- `org-create`
- `org-get`
- `org-update`
- `workspace-create`
- `workspace-list`
- `workspace-update`

### Members and Access

- `member-list`
- `member-add`
- `member-update`
- `member-remove`
- `access-request-create`
- `access-request-list`
- `access-request-get`
- `access-request-review`

### Service Accounts

- `service-account-create`
- `service-account-list`
- `service-account-get`
- `service-account-key-create`
- `service-account-key-revoke`

### Portal and Delivery Operations

- `portal-navigation`
- `portal-request-queue`
- `portal-personal-overview`
- `portal-enterprise-overview`
- `deliveries-operations`
- `deliveries-reconcile`

### Raw API

Use raw requests when a dedicated command is not yet exposed:

```bash
axme raw GET /v1/access-requests --query org_id="$AXME_ORG_ID" --query state=pending
axme raw POST /v1/deliveries/reconcile --data-json '{"org_id":"...","workspace_id":"...","target_status":"dead_lettered"}'
```

## Tests

Run CLI tests:

```bash
python -m unittest discover -s tests -p "test_*.py"
```
