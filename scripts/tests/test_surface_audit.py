"""surface_audit.py 的单元测试。

全部在临时目录里造样本，不读取真实仓库状态；这样门禁自身的回归不会
被 skills 路由修没修、worktree 干不干净所左右。
"""

from __future__ import annotations

import io
import json
import sys
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import surface_audit as sa  # noqa: E402


def write(root: Path, rel: str, content: str) -> Path:
    path = root / rel
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding="utf-8")
    return path


def make_root(tmp: Path, *, go_routes: str = "", frontend: str = "", python: str = "") -> Path:
    root = tmp / "repo"
    write(root, "go_hive/go-hive/internal/server/app.go", go_routes or "package server\n")
    write(root, "go_hive/go-hive/internal/quality/size_test.go",
          "package quality\n\nvar fileWaivers = map[string]sizeWaiver{\n}\n\n"
          "var funcWaivers = map[string]sizeWaiver{\n}\n")
    if frontend:
        write(root, "go_hive/go-hive/frontend/src/api/sample.ts", frontend)
    if python:
        write(root, "core/framework/server/routes_sample.py", python)
    return root


class NormalizeTests(unittest.TestCase):
    def test_template_and_query_normalization(self):
        self.assertEqual(
            sa.normalize_path("/skills/${encodeURIComponent(name)}?verbose=1"),
            "/skills/{param}",
        )
        self.assertEqual(sa.normalize_path("/skills/${name}/body#x"), "/skills/{param}/body")
        self.assertEqual(sa.normalize_path("skills/${name}", add_api_prefix=True), "/api/skills/{param}")
        self.assertEqual(sa.normalize_path("/api/skills/"), "/api/skills")

    def test_python_and_go_param_shapes(self):
        self.assertEqual(sa.normalize_path("/api/x/{id:[0-9]+}"), "/api/x/{param}")
        self.assertEqual(sa.normalize_path("/api/x/:id"), "/api/x/{param}")
        self.assertEqual(sa.normalize_path("/api/x/{id...}"), "/api/x/{param}")

    def test_route_matching_is_segment_wise(self):
        self.assertTrue(sa.route_matches("/api/skills/{param}", "/api/skills/{skill_name}"))
        self.assertTrue(sa.route_matches("/api/skills/abc", "/api/skills/{skill_name}"))
        self.assertFalse(sa.route_matches("/api/skills/{param}/body", "/api/skills/{param}"))
        self.assertFalse(sa.route_matches("/api/skills/scopes", "/api/skills/{param}/body"))


class ExtractionTests(unittest.TestCase):
    def test_frontend_templates_helpers_generics_and_upload(self):
        source = """
const queenPath = (queenId: string) =>
  `/queen/${encodeURIComponent(queenId)}/skills`;

export const skillsApi = {
  listAll: () => api.get<{ queens: Array<{ id: string; name: string }> }>("/skills"),
  detail: (name: string) => api.get<Detail>(`/skills/${encodeURIComponent(name)}?x=1`),
  forQueen: (id: string) => api.get<Scope>(queenPath(id)),
  create: (id: string) => api.post<Row>(queenPath(id), {}),
  body: (id: string, name: string) =>
    api.get<Detail>(`${queenPath(id)}/${encodeURIComponent(name)}/body`),
  upload: (fd: FormData) => api.upload<{ ok: boolean }>("/skills/upload", fd),
  rows: (table: string) => api.get(`/tables/${table}/rows${qs ? `?${qs}` : ""}`),
};
"""
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "repo"
            write(root, "frontend/src/api/skills.ts", source)
            routes, unresolved = sa.extract_frontend_routes([root / "frontend/src/api"])

        found = {(route.method, route.path) for route in routes}
        self.assertIn(("GET", "/api/skills"), found)
        self.assertIn(("GET", "/api/skills/{param}"), found)
        self.assertIn(("GET", "/api/queen/{param}/skills"), found)
        self.assertIn(("POST", "/api/queen/{param}/skills"), found)
        self.assertIn(("GET", "/api/queen/{param}/skills/{param}/body"), found)
        self.assertIn(("POST", "/api/skills/upload"), found)
        self.assertIn(("GET", "/api/tables/{param}/rows"), found)
        self.assertEqual(unresolved, 0)
        self.assertNotIn(("GET", "/api/tables/{param}/rows{param}"), found)

    def test_go_handlers_include_handle_and_skip_tests(self):
        source = """
package server
func register(mux *http.ServeMux) {
    mux.HandleFunc("GET /api/skills", handleSkills)
    mux.Handle("DELETE /api/queen/{queen_id}/skills/{skill_name}", handleDelete())
    mux.HandleFunc("/", handleSPA)
}
"""
        with tempfile.TemporaryDirectory() as tmp:
            server = Path(tmp) / "server"
            write(server, "app.go", source)
            write(server, "app_test.go", 'package server\n// mux.HandleFunc("GET /api/fake")\n')
            routes = sa.extract_go_routes(server)

        found = {(route.method, route.path) for route in routes}
        self.assertIn(("GET", "/api/skills"), found)
        self.assertIn(("DELETE", "/api/queen/{param}/skills/{param}"), found)
        self.assertNotIn(("GET", "/api/fake"), found)
        self.assertEqual(len(routes), 2)

    def test_python_multiline_router_and_module_constant(self):
        source = '''
PATH = "/api/runtime-logs"

def register_routes(app):
    r.add_get("/api/skills", handle_list)
    app.router.add_post(
        "/api/skills/upload",
        handle_upload,
    )
    app.router.add_delete(
        "/api/skills/{skill_name}",
        handle_delete,
    )
    app.router.add_get(PATH, handle_logs)
    app.router.add_get(UNRESOLVED, handle_unknown)
'''
        with tempfile.TemporaryDirectory() as tmp:
            server = Path(tmp) / "server"
            write(server, "routes_sample.py", source)
            routes, unresolved = sa.extract_python_routes(server)

        found = {(route.method, route.path) for route in routes}
        self.assertIn(("GET", "/api/skills"), found)
        self.assertIn(("POST", "/api/skills/upload"), found)
        self.assertIn(("DELETE", "/api/skills/{param}"), found)
        self.assertIn(("GET", "/api/runtime-logs"), found)
        self.assertEqual(unresolved, 1)


class MissingAndAllowlistTests(unittest.TestCase):
    def test_missing_detection_respects_methods_and_wildcards(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = make_root(
                Path(tmp),
                go_routes='package server\n'
                          'func register(mux *http.ServeMux) {\n'
                          '  mux.HandleFunc("GET /api/skills/{skill_name}", h)\n'
                          '}\n',
                frontend='api.get("/skills/abc"); api.post("/skills", {});',
                python='r.add_get("/api/skills/{skill_name}", h)\n'
                       'r.add_post("/api/skills", h)\n'
                       'r.add_get("/api/runtime-logs", h)\n',
            )
            result = sa.audit(root)

        self.assertEqual(result["counts"]["frontend_missing"], 1)
        self.assertEqual(result["frontend_missing"][0]["method"], "POST")
        self.assertEqual(result["counts"]["python_missing"], 2)
        self.assertEqual(
            {(item["method"], item["path"]) for item in result["python_missing"]},
            {("POST", "/api/skills"), ("GET", "/api/runtime-logs")},
        )
        self.assertFalse(result["ok"])

    def test_allowlist_marks_known_not_applicable_and_unblocks(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = make_root(
                Path(tmp),
                python='r.add_get("/api/browser/tabs", h)\n',
            )
            allowlist = root / "allow.json"
            allowlist.write_text(json.dumps({
                "python_to_go": [{
                    "path": "/api/browser/tabs",
                    "methods": ["GET"],
                    "owner": "browser-bridge lane",
                    "reason": "独立进程承载",
                }],
            }), encoding="utf-8")

            blocked = sa.audit(root)
            allowed = sa.audit(root, allowlist)

        self.assertFalse(blocked["ok"])
        self.assertTrue(allowed["ok"])
        self.assertEqual(allowed["python_missing"][0]["status"], "allowlisted")
        self.assertEqual(allowed["python_missing"][0]["reason"], "独立进程承载")
        self.assertEqual(allowed["counts"]["python_pending"], 0)

    def test_allowlist_without_reason_does_not_exempt(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = make_root(Path(tmp), python='r.add_get("/api/skills", h)\n')
            allowlist = root / "allow.json"
            allowlist.write_text(json.dumps({
                "python_to_go": [{"path": "/api/skills", "reason": "   "}],
            }), encoding="utf-8")
            result = sa.audit(root, allowlist)

        self.assertFalse(result["ok"])
        self.assertEqual(result["python_missing"][0]["status"], "pending")

    def test_pending_prefix_only_labels_owner(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = make_root(
                Path(tmp),
                python='r.add_get("/api/skills", h)\nr.add_get("/api/skills/scopes", h)\n',
            )
            allowlist = root / "allow.json"
            allowlist.write_text(json.dumps({
                "pending": [{"prefix": "/api/skills", "owner": "skills-api lane"}],
            }), encoding="utf-8")
            result = sa.audit(root, allowlist)

        self.assertEqual(result["counts"]["python_pending"], 2)
        self.assertTrue(all(item["owner"] == "skills-api lane" for item in result["python_missing"]))
        self.assertFalse(result["ok"])

    def test_strict_exit_codes(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = make_root(Path(tmp), python='r.add_get("/api/skills", h)\n')
            allowlist = root / "allow.json"
            allowlist.write_text(json.dumps({
                "python_to_go": [{"path": "/api/skills", "reason": "上游专有"}],
            }), encoding="utf-8")

            buffer = io.StringIO()
            with redirect_stdout(buffer):
                blocked_exit = sa.main(["--root", str(root), "--strict"])
                allowed_exit = sa.main(
                    ["--root", str(root), "--strict", "--allowlist", str(allowlist)]
                )

        self.assertEqual(blocked_exit, 1)
        self.assertEqual(allowed_exit, 0)


class HygieneTests(unittest.TestCase):
    def test_empty_dir_lane_file_and_dangling_waiver_are_blocking(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = make_root(Path(tmp))
            (root / "go_hive/go-hive/internal/platform/term").mkdir(parents=True)
            (root / ".opcteam/telemetry").mkdir(parents=True)
            write(root, "go_hive/go-hive/internal/server/batch3_lane2.go", "package server\n")
            write(
                root,
                "go_hive/go-hive/internal/quality/size_test.go",
                "package quality\n\n"
                "var fileWaivers = map[string]sizeWaiver{\n"
                '  "internal/ghost.go": {2000, "旧文件"},\n'
                "}\n\n"
                "var funcWaivers = map[string]sizeWaiver{\n"
                '  "internal/also_ghost.go::Run": {300, "旧函数"},\n'
                "}\n",
            )
            result = sa.audit(root)

        self.assertEqual(result["counts"]["empty_dirs"], 1)
        self.assertEqual(result["counts"]["lane_named_files"], 1)
        self.assertEqual(result["counts"]["dangling_waivers"], 2)
        self.assertFalse(result["ok"])
        statuses = {item["path"]: item["status"] for item in result["hygiene"]["empty_dirs"]}
        self.assertEqual(statuses["go_hive/go-hive/internal/platform/term"], "pending")

    def test_integration_batch_directory_is_a_documented_convention(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = make_root(Path(tmp))
            write(root, "go_hive/go-hive/internal/tools/integrations/batch2_http.go", "package integrations\n")
            result = sa.audit(root)

        self.assertEqual(result["counts"]["lane_named_files"], 0)
        self.assertEqual(result["counts"]["lane_named_convention"], 1)
        self.assertTrue(result["ok"])

    def test_waived_lane_file_is_not_blocking(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = make_root(Path(tmp))
            write(root, "go_hive/go-hive/internal/server/batch9_lane1.go", "package server\n")
            write(
                root,
                "go_hive/go-hive/internal/quality/size_test.go",
                "package quality\n\n"
                "var fileWaivers = map[string]sizeWaiver{\n"
                '  "internal/server/batch9_lane1.go": {1800, "批次整批落地，冻结"} ,\n'
                "}\n\nvar funcWaivers = map[string]sizeWaiver{\n}\n",
            )
            result = sa.audit(root)

        self.assertEqual(result["counts"]["lane_named_files"], 0)
        self.assertEqual(result["hygiene"]["lane_named_files"][0]["status"], "waived")
        self.assertTrue(result["ok"])

    def test_allowlisted_empty_dir_is_not_blocking(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = make_root(Path(tmp))
            (root / "go_hive/go-hive/parity/fixtures").mkdir(parents=True)
            allowlist = root / "allow.json"
            allowlist.write_text(json.dumps({
                "empty_dirs": [{"path": "go_hive/go-hive/parity/fixtures", "reason": "fixtures 由脚本生成"}],
            }), encoding="utf-8")
            result = sa.audit(root, allowlist)

        self.assertEqual(result["counts"]["empty_dirs"], 0)
        self.assertEqual(result["hygiene"]["empty_dirs"][0]["status"], "allowlisted")
        self.assertTrue(result["ok"])

    def test_hive_go_runtime_empty_dirs_are_ignored(self):
        """HIVE_HOME（.hive_go）是运行时状态，运行期间的空目录不算落地残渣。"""
        with tempfile.TemporaryDirectory() as tmp:
            root = make_root(Path(tmp))
            # 真实布局：仓库根的 go_hive/.hive_go，以及 internal/server 下的夹具。
            (root / "go_hive/.hive_go/memories/global").mkdir(parents=True)
            (root / "go_hive/.hive_go/credentials/credentials").mkdir(parents=True)
            (root / "go_hive/go-hive/internal/server/.hive_go/_misc/session_1/logs").mkdir(parents=True)
            # 普通空目录仍然阻断；非 .hive_go 的 .hive 不能被顺手跳过。
            (root / "go_hive/go-hive/internal/platform/term").mkdir(parents=True)
            (root / "go_hive/go-hive/internal/platform/.hive/state").mkdir(parents=True)
            result = sa.audit(root)

        self.assertEqual(
            {item["path"] for item in result["hygiene"]["empty_dirs"]},
            {
                "go_hive/go-hive/internal/platform/term",
                "go_hive/go-hive/internal/platform/.hive/state",
            },
        )
        self.assertEqual(result["counts"]["empty_dirs"], 2)
        self.assertFalse(result["ok"])

    def test_hive_go_skip_keeps_empty_dir_allowlist_semantics(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = make_root(Path(tmp))
            (root / "go_hive/.hive_go/memories/global").mkdir(parents=True)
            (root / "go_hive/go-hive/parity/fixtures").mkdir(parents=True)
            allowlist = root / "allow.json"
            allowlist.write_text(json.dumps({
                "empty_dirs": [{
                    "path": "go_hive/go-hive/parity/fixtures",
                    "reason": "fixtures 由脚本生成",
                }],
            }), encoding="utf-8")
            result = sa.audit(root, allowlist)

        self.assertEqual(result["counts"]["empty_dirs"], 0)
        self.assertEqual(
            [(item["path"], item["status"]) for item in result["hygiene"]["empty_dirs"]],
            [("go_hive/go-hive/parity/fixtures", "allowlisted")],
        )
        self.assertTrue(result["ok"])


class ReportTests(unittest.TestCase):
    def test_markdown_lists_counts_and_missing_paths(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = make_root(Path(tmp), python='r.add_get("/api/skills", h)\n')
            result = sa.audit(root)
        markdown = sa.render_markdown(result)

        self.assertIn("# API / 仓库表面审计", markdown)
        self.assertIn("前端 → Go 缺失", markdown)
        self.assertIn("Python → Go 缺失（按前缀分组）", markdown)
        self.assertIn("`/api/skills`", markdown)
        self.assertIn("存在未豁免缺口", markdown)


if __name__ == "__main__":
    unittest.main()
