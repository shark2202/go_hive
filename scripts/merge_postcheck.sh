#!/usr/bin/env bash
# 合并解冲突后的收尾检查（2026-11-22 双次事故：python 解冲突把 Go 文件
# 留成 gofmt-dirty，wave-33/wave-37 两次 fmt-check 红）。
#
# 用法：每次用脚本解完 merge 冲突、提交前运行；在 go-hive 下检查
# gofmt -l，非空即失败并要求先 gofmt -w。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT/go-hive" || exit 2

dirty=$(gofmt -l internal/ cmd/ 2>/dev/null || true)
if [ -n "$dirty" ]; then
  echo "merge-postcheck: gofmt 需要处理以下文件（先 gofmt -w 再提交）："
  echo "$dirty"
  exit 1
fi
echo "merge-postcheck: gofmt clean"
