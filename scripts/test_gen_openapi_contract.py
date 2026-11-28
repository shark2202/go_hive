#!/usr/bin/env python3
"""Focused guard tests for the generated OpenAPI/AsyncAPI contracts.

Run with:
    uv run python go_hive/scripts/test_gen_openapi_contract.py
"""

from __future__ import annotations

import copy
import inspect
import sys
import unittest
from pathlib import Path
from unittest import mock

SCRIPT_DIR = Path(__file__).resolve().parent
if str(SCRIPT_DIR) not in sys.path:
    sys.path.insert(0, str(SCRIPT_DIR))

import check_compat_contracts as compat  # noqa: E402
import check_openapi_contract as checker  # noqa: E402
import gen_openapi_contract as gen  # noqa: E402


class GeneratedContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.openapi = gen.build_openapi()
        cls.asyncapi = gen.build_asyncapi()
        cls.manifest = gen.load_json(gen.API_ROUTES_PATH)
        cls.snapshots = gen.load_snapshots()[1]

    def test_generation_is_deterministic(self) -> None:
        self.assertEqual(gen.render_json(self.openapi), gen.render_json(gen.build_openapi()))
        self.assertEqual(gen.render_json(self.asyncapi), gen.render_json(gen.build_asyncapi()))

    def test_every_target_route_is_covered_exactly_once(self) -> None:
        routes = self.manifest["go"]["routes"]
        expected = {(route["method"].upper(), route["path"]) for route in routes}
        actual = {
            (method.upper(), path)
            for path, item in self.openapi["paths"].items()
            for method in item
            if method in checker.HTTP_METHODS
        }
        self.assertEqual(expected, actual)

    def test_every_snapshot_is_wired_into_its_operation(self) -> None:
        routes = self.manifest["go"]["routes"]
        for snapshot in self.snapshots:
            route = gen.match_route(routes, snapshot["method"], snapshot["path"])
            self.assertIsNotNone(route, snapshot["endpoint"])
            assert route is not None
            operation = self.openapi["paths"][route["path"]][route["method"].lower()]
            status = str(snapshot["payload"]["status"])
            self.assertIn(status, operation["responses"], snapshot["endpoint"])
            self.assertIn(snapshot["endpoint"], operation["responses"][status]["x-snapshot-endpoints"])

    def test_generated_documents_pass_the_checker(self) -> None:
        errors, _ = checker.validate_documents(self.openapi, self.asyncapi)
        self.assertEqual(errors, [])

    def test_plain_error_failures_reuse_error_response(self) -> None:
        routes = self.manifest["go"]["routes"]
        snapshot = next(item for item in self.snapshots if item["endpoint"] == "credentials_detail_missing")
        route = gen.match_route(routes, snapshot["method"], snapshot["path"])
        assert route is not None
        operation = self.openapi["paths"][route["path"]][route["method"].lower()]
        schema = operation["responses"]["404"]["content"]["application/json"]["schema"]
        self.assertEqual(schema, {"$ref": "#/components/schemas/ErrorResponse"})

    def test_routes_without_snapshots_are_partial_and_schema_free(self) -> None:
        operation = self.openapi["paths"]["/api/agents"]["delete"]
        self.assertEqual(operation["x-contract-status"], "partial")
        self.assertNotIn("content", operation["responses"]["default"])

    def test_websocket_stays_design_only(self) -> None:
        channel = self.asyncapi["channels"]["channelWebsocket"]
        self.assertEqual(channel["x-contract-status"], "design-only")
        self.assertFalse(channel["x-production"])
        self.assertEqual(self.asyncapi["x-contract"]["websocket"]["status"], "design-only")
        route_paths = {route["path"] for route in self.manifest["go"]["routes"]}
        self.assertNotIn(channel["address"], route_paths)

    def test_sse_channels_point_at_real_routes(self) -> None:
        route_keys = {
            (route["method"].upper(), route["path"]) for route in self.manifest["go"]["routes"]
        }
        for key, stream in checker.SSE_CHANNEL_KEYS.items():
            channel = self.asyncapi["channels"][key]
            method, path = channel["x-route"].split(" ", 1)
            self.assertIn((method, path), route_keys)
            self.assertEqual(channel["x-stream"], stream)

    def test_removing_an_operation_is_detected(self) -> None:
        mutated = copy.deepcopy(self.openapi)
        del mutated["paths"]["/api/agents"]["delete"]
        errors, _ = checker.validate_documents(mutated, self.asyncapi)
        self.assertTrue(any("missing operation DELETE /api/agents" in error for error in errors), errors)

    def test_removing_a_snapshot_response_is_detected(self) -> None:
        mutated = copy.deepcopy(self.openapi)
        del mutated["paths"]["/api/health"]["get"]["responses"]["200"]
        mutated["paths"]["/api/health"]["get"]["responses"]["default"] = {
            "description": "removed",
            "x-contract-status": "partial",
        }
        errors, _ = checker.validate_documents(mutated, self.asyncapi)
        self.assertTrue(any("snapshot health status 200 missing" in error for error in errors), errors)


class CompatGateIntegrationTests(unittest.TestCase):
    """The canonical documents are only protected if ``make ci`` runs the checker.

    ``make ci`` -> ``compat-check`` -> ``check_compat_contracts.py`` -> C13.
    These tests guard the wiring (a dropped call would silently retire the gate)
    and the exit-code mapping (checker 1/2 must stay a gate FAIL).
    """

    def setUp(self) -> None:
        self._saved_results = list(compat.results)
        compat.results.clear()

    def tearDown(self) -> None:
        compat.results[:] = self._saved_results

    def c13_rows(self) -> list[dict]:
        return [row for row in compat.results if row["id"] == "C13"]

    def test_compat_gate_runs_the_checker_and_passes(self) -> None:
        compat.check_generated_api_contracts(compat.REPO)
        rows = self.c13_rows()
        self.assertEqual(len(rows), 1, rows)
        self.assertEqual(rows[0]["status"], "PASS", rows[0]["detail"])
        self.assertIn("operations=226", rows[0]["detail"])
        self.assertIn("snapshots=107/107", rows[0]["detail"])

    def test_compat_gate_maps_checker_failure_to_fail(self) -> None:
        output = (
            "[contract-check] FAILED: openapi operations=226 paths=181\n"
            "[contract-check]   drift: go_hive/contracts/openapi.json differs from generator output"
        )
        with mock.patch.object(compat, "run_script", return_value=(1, output)):
            compat.check_generated_api_contracts(compat.REPO)
        row = self.c13_rows()[0]
        self.assertEqual(row["status"], "FAIL")
        self.assertIn("exit 1", row["detail"])
        self.assertIn("drift", row["detail"])

    def test_compat_gate_fails_when_the_checker_is_missing(self) -> None:
        with mock.patch.object(compat, "run_script", side_effect=AssertionError("must not run")):
            compat.check_generated_api_contracts(compat.REPO / "does-not-exist")
        self.assertEqual(self.c13_rows()[0]["status"], "FAIL")

    def test_compat_gate_is_wired_into_main(self) -> None:
        self.assertIn("check_generated_api_contracts(root)", inspect.getsource(compat.main))


if __name__ == "__main__":
    unittest.main()
