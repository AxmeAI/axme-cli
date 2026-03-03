from __future__ import annotations

from dataclasses import dataclass
import os


@dataclass(frozen=True)
class CliConfig:
    base_url: str
    api_key: str | None
    bearer_token: str | None
    org_id: str | None
    workspace_id: str | None
    timeout_seconds: float


def _clean(value: str | None) -> str | None:
    if value is None:
        return None
    stripped = value.strip()
    return stripped or None


def build_config(
    *,
    base_url: str | None,
    api_key: str | None,
    bearer_token: str | None,
    org_id: str | None,
    workspace_id: str | None,
    timeout_seconds: float,
) -> CliConfig:
    resolved_base_url = (
        _clean(base_url)
        or _clean(os.getenv("AXME_PORTAL_BASE_URL"))
        or _clean(os.getenv("AXME_GATEWAY_BASE_URL"))
        or "http://127.0.0.1:8100"
    )
    resolved_api_key = _clean(api_key) or _clean(os.getenv("AXME_GATEWAY_API_KEY"))
    resolved_bearer_token = _clean(bearer_token) or _clean(os.getenv("AXME_PORTAL_SCOPED_BEARER_TOKEN"))
    resolved_org_id = _clean(org_id) or _clean(os.getenv("AXME_ORG_ID"))
    resolved_workspace_id = _clean(workspace_id) or _clean(os.getenv("AXME_WORKSPACE_ID"))
    return CliConfig(
        base_url=resolved_base_url.rstrip("/"),
        api_key=resolved_api_key,
        bearer_token=resolved_bearer_token,
        org_id=resolved_org_id,
        workspace_id=resolved_workspace_id,
        timeout_seconds=timeout_seconds,
    )
