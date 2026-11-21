# go-hive — 离线、无 cgo 的构建与质量门禁
#
# 所有目标默认在离线、CGO_ENABLED=0 的环境下运行：
#   CGO_ENABLED=0  纯 Go（modernc.org/sqlite），不链接 C 运行时
#   GOPROXY=off    不访问网络
#   GOSUMDB=off    不校验 sumdb（依赖已在本地 module cache）
#   GOFLAGS=-mod=mod
#
# 目标：
#   make test       运行全部测试
#   make test-v     详细输出
#   make vet        go vet
#   make fmt-check  gofmt 检查（不修改文件）
#   make build      编译全部包；若 cmd/ 下存在 main 包则产出二进制
#   make embed-ui   构建前端并把 dist 嵌入单二进制（-tags embed_ui）
#   make cross      CGO_ENABLED=0 交叉编译 6 个平台到 dist/
#   make parity          每个 Test 必须带 // Parity: 或 // Parity-exempt:
#   make parity-targets  // Parity: 引用的 Python 测试必须真实存在
#   make ci              依次执行 fmt-check vet test cross parity parity-targets wiring-check compat-check arch-check
#
# 入口约定：./cmd/... 是 CLI 入口目录。当前该目录尚未落地 main 包，
# 因此 build/cross 只做编译验证，不会产出二进制；等 cmd/hive 落地后
# 同一个目标会自动开始产出 dist/hive-<os>-<arch>[.exe]。

GO        ?= go
GOFMT     ?= gofmt
DIST      ?= dist
PARITY    ?= ../scripts/check_parity.sh
PARITY_TARGETS ?= ../scripts/check_parity_targets.sh
# Python 参考树根目录（含 tests/）。默认指向本仓库内的 core/；
# 独立检出（只有 go_hive）时找不到，parity-targets 自动 SKIP。
PY_ROOT   ?= ../../core
OFFLINE    = CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod
PLATFORMS  = darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64

.PHONY: all test test-v vet fmt-check build embed-ui cross parity parity-targets specs-check tool-contract-check tool-contract-report tool-surface-check browser-tool-check tool-output-check cli-surface-check compat-check upgrade-compat-check ci clean help

all: ci

help:
	@echo "go-hive targets:"
	@echo "  make test        go test ./...（离线、CGO_ENABLED=0）"
	@echo "  make test-v      同上，-v"
	@echo "  make vet         go vet ./..."
	@echo "  make fmt-check   gofmt -l 检查，非空即失败"
	@echo "  make build       go build ./...，有 cmd 入口时产出 dist/hive"
	@echo "  make embed-ui    构建前端（必要时 npm）并嵌入单二进制 dist/hive-embed"
	@echo "  make cross       交叉编译 6 平台到 dist/"
	@echo "  make parity      校验每个 Test 的 // Parity: 注解"
	@echo "  make parity-targets 校验注解引用的 Python 测试真实存在"
	@echo "  make tool-contract-check  校验 contracts/tool-definitions.json 自洽"
	@echo "  make tool-contract-report 对拍冻结契约与当前工具面（只报告，不阻断）"
	@echo "  make tool-output-check    工具输出 golden 产物自洽检查（docs/e2e-assets/tool-output）"
	@echo "  make cli-surface-check    CLI 对外面契约重采 + 对拍（递归 --help + 退出码探针）"
	@echo "  make compat-check         冻结契约自洽门禁（env/config/storage/DB schema/API 路由/工具面）"
	@echo "  make upgrade-compat-check 升级兼容门禁（PREV_SLOT=<上一交付二进制目录> [NEW_SLOT=...]）"
	@echo "  make ci          fmt-check + vet + test + cross + parity + parity-targets + wiring + compat-check + arch-check"

test:
	$(OFFLINE) $(GO) test ./...

test-v:
	$(OFFLINE) $(GO) test -v ./...

vet:
	$(OFFLINE) $(GO) vet ./...

fmt-check:
	@unformatted="$$( $(GOFMT) -l . )"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt 需要处理以下文件："; \
		echo "$$unformatted"; \
		exit 1; \
	fi; \
	echo "gofmt: OK"

build:
	@mkdir -p $(DIST)
	$(OFFLINE) $(GO) build ./...
	@if find ./cmd -name '*.go' 2>/dev/null | grep -q .; then \
		echo "building ./cmd/..."; \
		$(OFFLINE) $(GO) build -o $(DIST)/hive ./cmd/hive; \
	else \
		echo "build: ./cmd 下暂无 main 包，仅完成全部包的编译验证"; \
	fi

# 单二进制带 dashboard：先把 frontend/dist 复制到 embed 包内（go:embed 不能
# 引用包目录之外的路径），再以 -tags embed_ui 编译。dist 是 gitignore 的
# 构建产物，脚本会在缺失时先运行 npm ci/build；预构建的 dist 可用
# FRONTEND_DIST 指定，跳过 npm 可用 SKIP_FRONTEND_BUILD=1。
embed-ui:
	@bash ../scripts/embed_ui.sh

cross:
	@mkdir -p $(DIST)
	@set -e; \
	for platform in $(PLATFORMS); do \
		os="$${platform%/*}"; \
		arch="$${platform#*/}"; \
		ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		echo "== $$os/$$arch =="; \
		CGO_ENABLED=0 GOOS="$$os" GOARCH="$$arch" GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
			$(GO) build ./...; \
		if [ -n "$$(find ./cmd -name '*.go' 2>/dev/null)" ]; then \
			CGO_ENABLED=0 GOOS="$$os" GOARCH="$$arch" GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
				$(GO) build -o "$(DIST)/hive-$$os-$$arch$$ext" ./cmd/hive; \
			echo "  -> $(DIST)/hive-$$os-$$arch$$ext"; \
		fi; \
	done; \
	echo "cross: 全部平台编译通过（无 main 包时不产出二进制）"

parity:
	@bash $(PARITY) .

parity-targets:
	@bash $(PARITY_TARGETS) . "$(PY_ROOT)"

# 工具契约冻结（不等于 parity 门禁）：manifest 是抓取到的 Python 基准面，
# 本仓库工具面是待对齐方。check 只校验产物自洽（排序/唯一/覆盖率/文档数字），
# report 全量打印差异但退出码不受差异影响 —— 差异是当前事实，不是回归。
TOOL_CONTRACT ?= ../contracts/tool-definitions.json
tool-contract-check:
	@python3 ../scripts/freeze_tool_contract.py check

tool-contract-report:
	@test -f "$(TOOL_CONTRACT)" || (echo "tool-contract-report: $(TOOL_CONTRACT) missing（先运行 scripts/freeze_tool_contract.py build）"; exit 1)
	$(OFFLINE) $(GO) test ./internal/cli -run TestToolContractFreeze -v

# 扁平默认工具面（tool_surface_flat.json）与冻结契约的漂移检查：重新抽取
# contracts/tool-definitions.json 的默认三层并与内嵌产物逐字比对。
tool-surface-check:
	@if command -v uv >/dev/null 2>&1; then \
		cd .. && uv run --no-project python scripts/gen_tool_surface_flat.py --check; \
	else \
		echo "tool-surface-check: SKIP — uv 不可用"; \
	fi

# 浏览器工具定义（browser_tool_defs.json）与上游 gcu FastMCP 注册的漂移检查：
# 重新从 tools/src/gcu/browser/tools/*.py 抽取 22 个 active 工具的
# name/description/inputSchema，与内嵌产物逐字比对（含 per-tool sha256）。
browser-tool-check:
	@if command -v uv >/dev/null 2>&1; then \
		cd .. && uv run python scripts/gen_browser_tool_defs.py --check; \
	else \
		echo "browser-tool-check: SKIP — uv 不可用"; \
	fi

# 工具输出 golden（docs/e2e-assets/tool-output/）的自洽检查：从已提交的
# raw 抓取重新规范化，results.json / diff.json / summary.txt 任一漂移即失败。
# 重新抓取两侧运行时见 go_hive/scripts/capture_tool_output.py（需要两侧可执行）。
tool-output-check:
	@if command -v uv >/dev/null 2>&1; then \
		cd .. && uv run --no-project python scripts/tool_output_golden.py report --check; \
	else \
		echo "tool-output-check: SKIP — uv 不可用"; \
	fi

# CLI 对外面契约（contracts/cli-surface.json）：递归采集两侧 `--help`
# （子命令树 / flags / 取值 / help 文案 / 默认值）并跑一组无副作用退出码
# 探针，再与冻结契约对拍。Python 参考侧需要能导入 framework/gcu 的解释器；
# 找不到时脚本只核对 Go 侧并打印 SKIP 原因。全量重采约 2–3 分钟，不进 ci。
cli-surface-check:
	@if command -v uv >/dev/null 2>&1; then \
		cd .. && uv run --no-project python scripts/cli_surface_contract.py --check; \
	else \
		echo "cli-surface-check: SKIP — uv 不可用"; \
	fi

# 接线守护：关键运行时调用点（内存注入、事件日志、会话工具安装）缺失即失败。
# 纯 stdlib，无外网也能跑。
WIRING_CHECK ?= ../scripts/wiring_check.py
wiring-check:
	@python3 $(WIRING_CHECK) --root .

# 校验 internal/credentials/specs_gen.go 与上游 Python 注册表一致。
# 需要 uv 和同级 tools/ 树；缺失时跳过（与 parity-targets 的 PY_ROOT 约定一致）。
specs-check:
	@if command -v uv >/dev/null 2>&1 && [ -d ../../tools/src/aden_tools/credentials ]; then \
		cd .. && uv run --no-project python scripts/gen_credential_specs.py --check; \
	else \
		echo "specs-check: SKIP — uv 或 ../../tools/src/aden_tools/credentials 不可用"; \
	fi

race:
	$(OFFLINE) $(GO) test -race ./...

# 冻结契约的自洽门禁：只读已提交的 contracts/ 与源码（stdlib、离线），
# 覆盖 env/config 清单、配置键锚点、存储字段、tracker DB schema 版本、
# API 路由/响应形状、工具定义与工具面。冻结产物的逐字节重生成（freeze_*
# --check）仍由冻结车道负责——行号会随代码移动漂移，不适合作为每次提交的
# 硬门禁；本目标检查的是语义面（清单/计数/锚点/版本/形状）。
# python3 不可用时明确 BLOCKED 并退出 1：没验过就不能算绿。
compat-check:
	@if command -v python3 >/dev/null 2>&1; then \
		python3 ../scripts/check_compat_contracts.py; \
	else \
		echo "compat-check: BLOCKED — python3 unavailable; contracts NOT verified"; \
		exit 1; \
	fi

# 升级兼容门禁：PREV_SLOT 放上一交付的二进制目录（解压的发布包或
# dist/slots/<slot>），NEW_SLOT 放候选二进制目录（省略则现场构建）。
# 在 v1/v2/v3 夹具 home 上验证迁移、读 API、旧二进制回读 0 写入、幂等。
# 需要真实二进制与本机端口，所以不进 ci；slot cadence 会自动调用它。
PREV_SLOT ?=
NEW_SLOT ?=
upgrade-compat-check:
	@test -n "$(PREV_SLOT)" || (echo "upgrade-compat-check: set PREV_SLOT=<dir with the previous hive binary>"; exit 2)
	@bash ../scripts/upgrade_compat_check.sh --old-dir "$(PREV_SLOT)" \
		$(if $(NEW_SLOT),--new-dir "$(NEW_SLOT)",) --work "$${WORK:-$$(mktemp -d "$${TMPDIR:-/tmp}/upgrade-compat.XXXXXX")}"

# 架构文档同步门禁：architecture/model.yaml + go list 重渲染全部生成物并逐字节比较。
# 纯 stdlib + 本地 go，离线可跑；产物有一处漂移即 exit 1。
arch-check:
	@if command -v python3 >/dev/null 2>&1; then \
		cd .. && python3 scripts/gen_arch.py --check; \
	else \
		echo "arch-check: BLOCKED — python3 unavailable; architecture docs NOT verified"; \
		exit 1; \
	fi

ci: fmt-check vet test cross parity parity-targets wiring-check compat-check arch-check

clean:
	rm -rf $(DIST) internal/server/uiembed/dist
