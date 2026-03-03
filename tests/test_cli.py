from __future__ import annotations

import io
import json
import unittest
from unittest.mock import patch

from axme_cli import cli


class CliTests(unittest.TestCase):
    def _run(self, argv: list[str]) -> tuple[int, str, str]:
        stdout = io.StringIO()
        stderr = io.StringIO()
        with patch("sys.stdout", stdout), patch("sys.stderr", stderr):
            code = cli.main(argv)
        return code, stdout.getvalue(), stderr.getvalue()

    def test_health_command_calls_health_endpoint(self) -> None:
        with patch("axme_cli.cli.GatewayClient.request", return_value=(200, {"service": "ok"}, "")) as mocked:
            code, stdout, stderr = self._run(["health"])
        self.assertEqual(code, 0)
        self.assertEqual(stderr, "")
        mocked.assert_called_once_with(method="GET", path="/health", params=None, payload=None)
        payload = json.loads(stdout)
        self.assertEqual(payload["status_code"], 200)
        self.assertTrue(payload["ok"])

    def test_org_create_builds_expected_payload(self) -> None:
        with patch("axme_cli.cli.GatewayClient.request", return_value=(200, {"ok": True}, "")) as mocked:
            code, _, _ = self._run(
                [
                    "org-create",
                    "--name",
                    "Acme Corp",
                    "--requested-by-actor-id",
                    "actor://owner",
                    "--legal-name",
                    "Acme Corporation LLC",
                    "--metadata-json",
                    '{"tier":"enterprise"}',
                ]
            )
        self.assertEqual(code, 0)
        mocked.assert_called_once()
        call = mocked.call_args.kwargs
        self.assertEqual(call["method"], "POST")
        self.assertEqual(call["path"], "/v1/organizations")
        self.assertEqual(call["payload"]["name"], "Acme Corp")
        self.assertEqual(call["payload"]["requested_by_actor_id"], "actor://owner")
        self.assertEqual(call["payload"]["metadata"], {"tier": "enterprise"})

    def test_workspace_create_uses_default_org_id(self) -> None:
        with patch("axme_cli.cli.GatewayClient.request", return_value=(200, {"ok": True}, "")) as mocked:
            code, _, _ = self._run(
                [
                    "--default-org-id",
                    "11111111-1111-1111-1111-111111111111",
                    "workspace-create",
                    "--name",
                    "Sandbox A",
                    "--environment",
                    "sandbox",
                ]
            )
        self.assertEqual(code, 0)
        call = mocked.call_args.kwargs
        self.assertEqual(
            call["path"],
            "/v1/organizations/11111111-1111-1111-1111-111111111111/workspaces",
        )
        self.assertEqual(call["payload"]["org_id"], "11111111-1111-1111-1111-111111111111")

    def test_access_request_join_without_org_fails_usage(self) -> None:
        code, _, stderr = self._run(
            [
                "access-request-create",
                "--request-type",
                "join_organization",
                "--requester-actor-id",
                "actor://user",
            ]
        )
        self.assertEqual(code, 2)
        self.assertIn("require org_id", stderr)

    def test_raw_query_and_payload(self) -> None:
        with patch("axme_cli.cli.GatewayClient.request", return_value=(200, {"ok": True}, "")) as mocked:
            code, _, _ = self._run(
                [
                    "raw",
                    "POST",
                    "/v1/deliveries/reconcile",
                    "--query",
                    "org_id=abc",
                    "--query",
                    "limit=10",
                    "--data-json",
                    '{"target_status":"dead_lettered"}',
                ]
            )
        self.assertEqual(code, 0)
        call = mocked.call_args.kwargs
        self.assertEqual(call["params"], {"org_id": "abc", "limit": "10"})
        self.assertEqual(call["payload"], {"target_status": "dead_lettered"})

    def test_http_error_returns_nonzero(self) -> None:
        with patch("axme_cli.cli.GatewayClient.request", return_value=(403, {"detail": "forbidden"}, "")):
            code, stdout, _ = self._run(["health"])
        self.assertEqual(code, 1)
        payload = json.loads(stdout)
        self.assertFalse(payload["ok"])
        self.assertEqual(payload["status_code"], 403)


if __name__ == "__main__":
    unittest.main()
