from __future__ import annotations

import json
from typing import Any
from urllib.error import HTTPError
from urllib.parse import urlencode
from urllib.request import Request, urlopen

from .config import CliConfig


class GatewayClient:
    def __init__(self, config: CliConfig):
        self._config = config

    def request(
        self,
        *,
        method: str,
        path: str,
        params: dict[str, Any] | None = None,
        payload: dict[str, Any] | None = None,
    ) -> tuple[int, dict[str, Any], str]:
        normalized_path = path if path.startswith("/") else f"/{path}"
        query = ""
        if params:
            normalized_params = {key: str(value) for key, value in params.items() if value is not None}
            if normalized_params:
                query = urlencode(normalized_params)
        url = f"{self._config.base_url}{normalized_path}"
        if query:
            url = f"{url}?{query}"

        headers = {"accept": "application/json"}
        body = json.dumps(payload).encode("utf-8") if payload is not None else None
        if payload is not None:
            headers["content-type"] = "application/json"
        if self._config.api_key:
            headers["x-api-key"] = self._config.api_key
        if self._config.bearer_token:
            headers["authorization"] = f"Bearer {self._config.bearer_token}"

        request = Request(url=url, method=method.upper(), headers=headers, data=body)
        try:
            with urlopen(request, timeout=self._config.timeout_seconds) as response:
                raw = response.read().decode("utf-8")
                parsed = json.loads(raw) if raw else {}
                return response.status, parsed, raw
        except HTTPError as exc:
            raw = exc.read().decode("utf-8")
            try:
                parsed = json.loads(raw) if raw else {}
            except json.JSONDecodeError:
                parsed = {"raw": raw}
            return exc.code, parsed, raw
