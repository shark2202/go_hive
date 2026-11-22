#!/usr/bin/env bash
# 合并解冲突后的收尾检查（2026-11-22 双次事故：python 解冲突把 Go 文件
# 留成 gofmt-dirty，wave-33/wave-37 两次 fmt-check 红）。
#
# 用法：每次用脚本解完 merge 冲突、提交前运行；在 go-hive 下检查
# gofmt -l，非空即失败并要求先 gofmt -w。
#
# 另加 wiring-check：代码搬家/拆文件后接线锚点失配已发生三次
# （w27 锚点、w43 loop_judge、w51 chat_runtime_colony），在这里挡住第 4 次。
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

if [ -f "$ROOT/scripts/wiring_check.py" ]; then
  if ! python3 "$ROOT/scripts/wiring_check.py" --root "$ROOT/go-hive" >/tmp/merge_postcheck_wiring.log 2>&1; then
    echo "merge-postcheck: wiring-check 失败（搬家后锚点失配？），详见 /tmp/merge_postcheck_wiring.log："
    tail -8 /tmp/merge_postcheck_wiring.log
    exit 1
  fi
  tail -1 /tmp/merge_postcheck_wiring.log
fi
