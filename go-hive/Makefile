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
#   make cross      CGO_ENABLED=0 交叉编译 6 个平台到 dist/
#   make parity          每个 Test 必须带 // Parity: 或 // Parity-exempt:
#   make parity-targets  // Parity: 引用的 Python 测试必须真实存在
#   make ci              依次执行 fmt-check vet test cross parity parity-targets
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

.PHONY: all test test-v vet fmt-check build cross parity parity-targets ci clean help

all: ci

help:
	@echo "go-hive targets:"
	@echo "  make test        go test ./...（离线、CGO_ENABLED=0）"
	@echo "  make test-v      同上，-v"
	@echo "  make vet         go vet ./..."
	@echo "  make fmt-check   gofmt -l 检查，非空即失败"
	@echo "  make build       go build ./...，有 cmd 入口时产出 dist/hive"
	@echo "  make cross       交叉编译 6 平台到 dist/"
	@echo "  make parity      校验每个 Test 的 // Parity: 注解"
	@echo "  make parity-targets 校验注解引用的 Python 测试真实存在"
	@echo "  make ci          fmt-check + vet + test + cross + parity + parity-targets"

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

race:
	$(OFFLINE) $(GO) test -race ./...

ci: fmt-check vet test cross parity parity-targets

clean:
	rm -rf $(DIST)
