from __future__ import annotations

import argparse
from dataclasses import dataclass
import json
import sys
from typing import Any

from .client import GatewayClient
from .config import CliConfig, build_config


class CliUsageError(Exception):
    pass


@dataclass(frozen=True)
class RequestSpec:
    method: str
    path: str
    params: dict[str, Any] | None = None
    payload: dict[str, Any] | None = None


def _parse_json_object(raw: str | None, *, flag_name: str) -> dict[str, Any] | None:
    if raw is None:
        return None
    try:
        parsed = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise CliUsageError(f"{flag_name} must be valid JSON object: {exc}") from exc
    if not isinstance(parsed, dict):
        raise CliUsageError(f"{flag_name} must decode to a JSON object")
    return parsed


def _resolve_org_id(args: argparse.Namespace, config: CliConfig, *, required: bool) -> str | None:
    org_id = getattr(args, "org_id", None) or config.org_id
    if required and not org_id:
        raise CliUsageError("org_id is required (use --org-id or AXME_ORG_ID)")
    return org_id


def _resolve_workspace_id(args: argparse.Namespace, config: CliConfig, *, required: bool) -> str | None:
    workspace_id = getattr(args, "workspace_id", None) or config.workspace_id
    if required and not workspace_id:
        raise CliUsageError("workspace_id is required (use --workspace-id or AXME_WORKSPACE_ID)")
    return workspace_id


def _build_request_spec(args: argparse.Namespace, config: CliConfig) -> RequestSpec:
    command = args.command
    if command == "health":
        return RequestSpec(method="GET", path="/health")

    if command == "org-create":
        return RequestSpec(
            method="POST",
            path="/v1/organizations",
            payload={
                "name": args.name,
                "requested_by_actor_id": args.requested_by_actor_id,
                "legal_name": args.legal_name,
                "primary_domain": args.primary_domain,
                "metadata": _parse_json_object(args.metadata_json, flag_name="--metadata-json"),
            },
        )

    if command == "org-get":
        org_id = _resolve_org_id(args, config, required=True)
        return RequestSpec(method="GET", path=f"/v1/organizations/{org_id}")

    if command == "org-update":
        org_id = _resolve_org_id(args, config, required=True)
        payload = {
            "name": args.name,
            "legal_name": args.legal_name,
            "primary_domain": args.primary_domain,
            "status": args.status,
            "metadata": _parse_json_object(args.metadata_json, flag_name="--metadata-json"),
        }
        if not any(value is not None for value in payload.values()):
            raise CliUsageError("org-update requires at least one field to patch")
        return RequestSpec(method="PATCH", path=f"/v1/organizations/{org_id}", payload=payload)

    if command == "workspace-create":
        org_id = _resolve_org_id(args, config, required=True)
        return RequestSpec(
            method="POST",
            path=f"/v1/organizations/{org_id}/workspaces",
            payload={
                "org_id": org_id,
                "name": args.name,
                "environment": args.environment,
                "region": args.region,
            },
        )

    if command == "workspace-list":
        org_id = _resolve_org_id(args, config, required=True)
        return RequestSpec(method="GET", path=f"/v1/organizations/{org_id}/workspaces")

    if command == "workspace-update":
        org_id = _resolve_org_id(args, config, required=True)
        workspace_id = _resolve_workspace_id(args, config, required=True)
        payload = {
            "name": args.name,
            "environment": args.environment,
            "region": args.region,
            "status": args.status,
        }
        if not any(value is not None for value in payload.values()):
            raise CliUsageError("workspace-update requires at least one field to patch")
        return RequestSpec(
            method="PATCH",
            path=f"/v1/organizations/{org_id}/workspaces/{workspace_id}",
            payload=payload,
        )

    if command == "member-list":
        org_id = _resolve_org_id(args, config, required=True)
        workspace_id = _resolve_workspace_id(args, config, required=False)
        return RequestSpec(
            method="GET",
            path=f"/v1/organizations/{org_id}/members",
            params={"workspace_id": workspace_id},
        )

    if command == "member-add":
        org_id = _resolve_org_id(args, config, required=True)
        workspace_id = _resolve_workspace_id(args, config, required=True)
        return RequestSpec(
            method="POST",
            path=f"/v1/organizations/{org_id}/members",
            payload={
                "actor_id": args.actor_id,
                "role": args.role,
                "workspace_id": workspace_id,
            },
        )

    if command == "member-update":
        org_id = _resolve_org_id(args, config, required=True)
        payload = {"role": args.role, "status": args.status}
        if not any(value is not None for value in payload.values()):
            raise CliUsageError("member-update requires --role and/or --status")
        return RequestSpec(
            method="PATCH",
            path=f"/v1/organizations/{org_id}/members/{args.member_id}",
            payload=payload,
        )

    if command == "member-remove":
        org_id = _resolve_org_id(args, config, required=True)
        return RequestSpec(method="DELETE", path=f"/v1/organizations/{org_id}/members/{args.member_id}")

    if command == "service-account-create":
        org_id = _resolve_org_id(args, config, required=True)
        workspace_id = _resolve_workspace_id(args, config, required=True)
        return RequestSpec(
            method="POST",
            path="/v1/service-accounts",
            payload={
                "org_id": org_id,
                "workspace_id": workspace_id,
                "name": args.name,
                "description": args.description,
                "created_by_actor_id": args.created_by_actor_id,
            },
        )

    if command == "service-account-list":
        org_id = _resolve_org_id(args, config, required=True)
        workspace_id = _resolve_workspace_id(args, config, required=False)
        return RequestSpec(
            method="GET",
            path="/v1/service-accounts",
            params={"org_id": org_id, "workspace_id": workspace_id},
        )

    if command == "service-account-get":
        return RequestSpec(method="GET", path=f"/v1/service-accounts/{args.service_account_id}")

    if command == "service-account-key-create":
        return RequestSpec(
            method="POST",
            path=f"/v1/service-accounts/{args.service_account_id}/keys",
            payload={
                "created_by_actor_id": args.created_by_actor_id,
                "expires_at": args.expires_at,
            },
        )

    if command == "service-account-key-revoke":
        return RequestSpec(
            method="POST",
            path=f"/v1/service-accounts/{args.service_account_id}/keys/{args.key_id}/revoke",
        )

    if command == "access-request-create":
        org_id = _resolve_org_id(args, config, required=False)
        workspace_id = _resolve_workspace_id(args, config, required=False)
        if args.request_type in {"join_organization", "elevated_role"} and not org_id:
            raise CliUsageError("join_organization/elevated_role require org_id")
        return RequestSpec(
            method="POST",
            path="/v1/access-requests",
            payload={
                "request_type": args.request_type,
                "requester_actor_id": args.requester_actor_id,
                "org_id": org_id,
                "workspace_id": workspace_id,
                "requested_role": args.requested_role,
                "company_name": args.company_name,
                "justification": args.justification,
                "expires_at": args.expires_at,
            },
        )

    if command == "access-request-list":
        org_id = _resolve_org_id(args, config, required=False)
        workspace_id = _resolve_workspace_id(args, config, required=False)
        return RequestSpec(
            method="GET",
            path="/v1/access-requests",
            params={"org_id": org_id, "workspace_id": workspace_id, "state": args.state},
        )

    if command == "access-request-get":
        return RequestSpec(method="GET", path=f"/v1/access-requests/{args.access_request_id}")

    if command == "access-request-review":
        return RequestSpec(
            method="POST",
            path=f"/v1/access-requests/{args.access_request_id}/review",
            payload={
                "decision": args.decision,
                "reviewer_actor_id": args.reviewer_actor_id,
                "review_comment": args.review_comment,
            },
        )

    if command == "portal-navigation":
        org_id = _resolve_org_id(args, config, required=True)
        workspace_id = _resolve_workspace_id(args, config, required=False)
        return RequestSpec(
            method="GET",
            path="/v1/portal/enterprise/navigation",
            params={"org_id": org_id, "workspace_id": workspace_id},
        )

    if command == "portal-request-queue":
        org_id = _resolve_org_id(args, config, required=True)
        workspace_id = _resolve_workspace_id(args, config, required=False)
        return RequestSpec(
            method="GET",
            path="/v1/portal/enterprise/request-queue",
            params={
                "org_id": org_id,
                "workspace_id": workspace_id,
                "state": args.state,
                "limit": args.limit,
            },
        )

    if command == "portal-personal-overview":
        org_id = _resolve_org_id(args, config, required=True)
        workspace_id = _resolve_workspace_id(args, config, required=True)
        return RequestSpec(
            method="GET",
            path="/v1/portal/personal/overview",
            params={"org_id": org_id, "workspace_id": workspace_id},
        )

    if command == "portal-enterprise-overview":
        org_id = _resolve_org_id(args, config, required=True)
        workspace_id = _resolve_workspace_id(args, config, required=True)
        return RequestSpec(
            method="GET",
            path="/v1/portal/enterprise/overview",
            params={"org_id": org_id, "workspace_id": workspace_id},
        )

    if command == "deliveries-operations":
        org_id = _resolve_org_id(args, config, required=True)
        workspace_id = _resolve_workspace_id(args, config, required=False)
        return RequestSpec(
            method="GET",
            path="/v1/deliveries-operations",
            params={
                "org_id": org_id,
                "workspace_id": workspace_id,
                "window_hours": args.window_hours,
            },
        )

    if command == "deliveries-reconcile":
        org_id = _resolve_org_id(args, config, required=True)
        workspace_id = _resolve_workspace_id(args, config, required=False)
        return RequestSpec(
            method="POST",
            path="/v1/deliveries/reconcile",
            payload={
                "org_id": org_id,
                "workspace_id": workspace_id,
                "max_pending_age_seconds": args.max_pending_age_seconds,
                "limit": args.limit,
                "target_status": args.target_status,
                "reason": args.reason,
            },
        )

    if command == "raw":
        params: dict[str, str] = {}
        for item in args.query:
            if "=" not in item:
                raise CliUsageError("raw --query must use key=value format")
            key, value = item.split("=", 1)
            params[key] = value
        return RequestSpec(
            method=args.method.upper(),
            path=args.path,
            params=params or None,
            payload=_parse_json_object(args.data_json, flag_name="--data-json"),
        )

    raise CliUsageError(f"unknown command: {command}")


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="axme", description="Axme Cloud operations CLI")
    parser.add_argument("--base-url", default=None, help="Gateway base URL (or AXME_PORTAL_BASE_URL)")
    parser.add_argument("--api-key", default=None, help="Gateway API key (or AXME_GATEWAY_API_KEY)")
    parser.add_argument("--bearer-token", default=None, help="Scoped bearer token (or AXME_PORTAL_SCOPED_BEARER_TOKEN)")
    parser.add_argument("--default-org-id", default=None, help="Default org id (or AXME_ORG_ID)")
    parser.add_argument("--default-workspace-id", default=None, help="Default workspace id (or AXME_WORKSPACE_ID)")
    parser.add_argument("--timeout", type=float, default=20.0, help="HTTP timeout in seconds")
    parser.add_argument("--compact", action="store_true", help="Print compact JSON without indentation")

    subparsers = parser.add_subparsers(dest="command", required=True)

    subparsers.add_parser("health", help="Check gateway health endpoint")

    org_create = subparsers.add_parser("org-create", help="Create organization")
    org_create.add_argument("--name", required=True)
    org_create.add_argument("--requested-by-actor-id", required=True)
    org_create.add_argument("--legal-name")
    org_create.add_argument("--primary-domain")
    org_create.add_argument("--metadata-json")

    org_get = subparsers.add_parser("org-get", help="Get organization")
    org_get.add_argument("--org-id")

    org_update = subparsers.add_parser("org-update", help="Patch organization")
    org_update.add_argument("--org-id")
    org_update.add_argument("--name")
    org_update.add_argument("--legal-name")
    org_update.add_argument("--primary-domain")
    org_update.add_argument("--status", choices=["pending", "active", "suspended", "archived"])
    org_update.add_argument("--metadata-json")

    workspace_create = subparsers.add_parser("workspace-create", help="Create workspace")
    workspace_create.add_argument("--org-id")
    workspace_create.add_argument("--name", required=True)
    workspace_create.add_argument("--environment", required=True, choices=["sandbox", "staging", "production"])
    workspace_create.add_argument("--region")

    workspace_list = subparsers.add_parser("workspace-list", help="List organization workspaces")
    workspace_list.add_argument("--org-id")

    workspace_update = subparsers.add_parser("workspace-update", help="Patch workspace")
    workspace_update.add_argument("--org-id")
    workspace_update.add_argument("--workspace-id")
    workspace_update.add_argument("--name")
    workspace_update.add_argument("--environment", choices=["sandbox", "staging", "production"])
    workspace_update.add_argument("--region")
    workspace_update.add_argument("--status", choices=["active", "suspended", "archived"])

    member_list = subparsers.add_parser("member-list", help="List members")
    member_list.add_argument("--org-id")
    member_list.add_argument("--workspace-id")

    member_add = subparsers.add_parser("member-add", help="Add member")
    member_add.add_argument("--org-id")
    member_add.add_argument("--workspace-id")
    member_add.add_argument("--actor-id", required=True)
    member_add.add_argument(
        "--role",
        required=True,
        choices=["org_owner", "org_admin", "workspace_admin", "member", "billing_viewer", "security_auditor"],
    )

    member_update = subparsers.add_parser("member-update", help="Update member role/status")
    member_update.add_argument("--org-id")
    member_update.add_argument("--member-id", required=True)
    member_update.add_argument("--role", choices=["org_owner", "org_admin", "workspace_admin", "member", "billing_viewer", "security_auditor"])
    member_update.add_argument("--status", choices=["active", "invited", "suspended", "removed"])

    member_remove = subparsers.add_parser("member-remove", help="Remove member")
    member_remove.add_argument("--org-id")
    member_remove.add_argument("--member-id", required=True)

    service_account_create = subparsers.add_parser("service-account-create", help="Create service account")
    service_account_create.add_argument("--org-id")
    service_account_create.add_argument("--workspace-id")
    service_account_create.add_argument("--name", required=True)
    service_account_create.add_argument("--description")
    service_account_create.add_argument("--created-by-actor-id", required=True)

    service_account_list = subparsers.add_parser("service-account-list", help="List service accounts")
    service_account_list.add_argument("--org-id")
    service_account_list.add_argument("--workspace-id")

    service_account_get = subparsers.add_parser("service-account-get", help="Get service account")
    service_account_get.add_argument("--service-account-id", required=True)

    service_account_key_create = subparsers.add_parser("service-account-key-create", help="Create service account key")
    service_account_key_create.add_argument("--service-account-id", required=True)
    service_account_key_create.add_argument("--created-by-actor-id", required=True)
    service_account_key_create.add_argument("--expires-at", help="ISO8601 timestamp")

    service_account_key_revoke = subparsers.add_parser("service-account-key-revoke", help="Revoke service account key")
    service_account_key_revoke.add_argument("--service-account-id", required=True)
    service_account_key_revoke.add_argument("--key-id", required=True)

    access_create = subparsers.add_parser("access-request-create", help="Create access request")
    access_create.add_argument("--request-type", required=True, choices=["create_organization", "join_organization", "elevated_role"])
    access_create.add_argument("--requester-actor-id", required=True)
    access_create.add_argument("--org-id")
    access_create.add_argument("--workspace-id")
    access_create.add_argument("--requested-role", choices=["org_owner", "org_admin", "workspace_admin", "member", "billing_viewer", "security_auditor"])
    access_create.add_argument("--company-name")
    access_create.add_argument("--justification")
    access_create.add_argument("--expires-at", help="ISO8601 timestamp")

    access_list = subparsers.add_parser("access-request-list", help="List access requests")
    access_list.add_argument("--org-id")
    access_list.add_argument("--workspace-id")
    access_list.add_argument("--state", choices=["pending", "under_review", "approved", "rejected", "waitlisted", "expired"])

    access_get = subparsers.add_parser("access-request-get", help="Get access request")
    access_get.add_argument("--access-request-id", required=True)

    access_review = subparsers.add_parser("access-request-review", help="Review access request")
    access_review.add_argument("--access-request-id", required=True)
    access_review.add_argument("--decision", required=True, choices=["approve", "reject", "waitlist"])
    access_review.add_argument("--reviewer-actor-id", required=True)
    access_review.add_argument("--review-comment")

    portal_nav = subparsers.add_parser("portal-navigation", help="Get portal navigation")
    portal_nav.add_argument("--org-id")
    portal_nav.add_argument("--workspace-id")

    portal_queue = subparsers.add_parser("portal-request-queue", help="Get portal request queue")
    portal_queue.add_argument("--org-id")
    portal_queue.add_argument("--workspace-id")
    portal_queue.add_argument("--state", choices=["pending", "under_review", "approved", "rejected", "waitlisted", "expired"])
    portal_queue.add_argument("--limit", type=int, default=200)

    portal_personal = subparsers.add_parser("portal-personal-overview", help="Get personal overview")
    portal_personal.add_argument("--org-id")
    portal_personal.add_argument("--workspace-id")

    portal_enterprise = subparsers.add_parser("portal-enterprise-overview", help="Get enterprise overview")
    portal_enterprise.add_argument("--org-id")
    portal_enterprise.add_argument("--workspace-id")

    deliveries_ops = subparsers.add_parser("deliveries-operations", help="Get delivery operations summary")
    deliveries_ops.add_argument("--org-id")
    deliveries_ops.add_argument("--workspace-id")
    deliveries_ops.add_argument("--window-hours", type=int, default=24)

    deliveries_reconcile = subparsers.add_parser("deliveries-reconcile", help="Reconcile pending deliveries")
    deliveries_reconcile.add_argument("--org-id")
    deliveries_reconcile.add_argument("--workspace-id")
    deliveries_reconcile.add_argument("--max-pending-age-seconds", type=int, default=300)
    deliveries_reconcile.add_argument("--limit", type=int, default=500)
    deliveries_reconcile.add_argument("--target-status", choices=["delivered", "dead_lettered"], default="dead_lettered")
    deliveries_reconcile.add_argument("--reason")

    raw = subparsers.add_parser("raw", help="Send raw request")
    raw.add_argument("method")
    raw.add_argument("path")
    raw.add_argument("--query", action="append", default=[], help="query param key=value (repeatable)")
    raw.add_argument("--data-json", help="JSON object request body")

    return parser


def main(argv: list[str] | None = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv)
    config = build_config(
        base_url=args.base_url,
        api_key=args.api_key,
        bearer_token=args.bearer_token,
        org_id=args.default_org_id,
        workspace_id=args.default_workspace_id,
        timeout_seconds=args.timeout,
    )
    try:
        spec = _build_request_spec(args, config)
    except CliUsageError as exc:
        print(f"usage error: {exc}", file=sys.stderr)
        return 2

    client = GatewayClient(config)
    status_code, body, raw = client.request(
        method=spec.method,
        path=spec.path,
        params=spec.params,
        payload=spec.payload,
    )
    output = {
        "status_code": status_code,
        "ok": status_code < 400,
        "body": body if body else {"raw": raw} if raw else {},
    }
    if args.compact:
        print(json.dumps(output, separators=(",", ":"), ensure_ascii=True))
    else:
        print(json.dumps(output, indent=2, ensure_ascii=True))
    return 0 if status_code < 400 else 1
