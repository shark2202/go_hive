#!/bin/bash
# 09:00 晨报：汇总夜间推进、质量门禁、时间台账与剩余缺口。
# 生成到 go_hive/docs/morning-report-<date>.md，并触发 macOS 通知。
set -u

REPO=/Users/mac/hive
STAMP="$(date '+%F %H:%M %Z')"
OUT="$REPO/go_hive/docs/morning-report-$(date +%F).md"
LATEST="$REPO/go_hive/docs/morning-report-latest.md"

cd "$REPO" || exit 1

# 先刷新 surface 快照（失败不阻断晨报）
if [ -f go_hive/scripts/refresh_surface_matrix.py ]; then
  uv run --no-project python go_hive/scripts/refresh_surface_matrix.py >/tmp/morning-refresh.log 2>&1 || true
fi

HEAD="$(git rev-parse --short HEAD)"
BRANCH="$(git branch --show-current)"
COMMITS_NIGHT="$(git log --since='2026-11-20 20:00' --oneline | wc -l | tr -d ' ')"
COMMITS_TODAY="$(git log --since='2026-11-21 00:00' --oneline | wc -l | tr -d ' ')"
WORKTREES="$(git worktree list | wc -l | tr -d ' ')"
DIRTY="$(git status --porcelain | grep -v '^??' | wc -l | tr -d ' ')"
SERVE="$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 http://127.0.0.1:8788/api/health || true)"
BRIDGE="$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 http://127.0.0.1:14830/health || true)"
LATEST_CI="$(ls -t /tmp/main_ci_*.log 2>/dev/null | head -1)"
LATEST_QC="$(ls -t /tmp/qc_*.log 2>/dev/null | head -1)"
PARITY_LINE="$(grep -h 'check_parity: OK' "$LATEST_CI" 2>/dev/null | tail -1)"
TOTAL_LINE="$(grep -h '^TOTAL' "$LATEST_CI" 2>/dev/null | tail -1)"
QC_LINE="$(grep -h '8/8 PASS' "$LATEST_QC" 2>/dev/null | tail -1)"

{
  echo "# go_hive 晨报 · $STAMP"
  echo
  echo "> 生成方式：夜间自主推进；数据来自 git、/tmp 门禁日志、看门狗与时间台账。未验证项不写成通过。"
  echo
  echo "## 目标"
  echo
  echo "按 \`go_hive/00-product-goals.md\`，以 Hive 当前 commit 为基准推进 API / tool / 内置 Skill / env / configuration.json 的逐字与功能一致。"
  echo
  echo "## 状态快照"
  echo
  echo "| 项 | 值 |"
  echo "| --- | --- |"
  echo "| HEAD | \`$HEAD\`（${BRANCH}） |"
  echo "| 夜间提交（20:00 起） | $COMMITS_NIGHT |"
  echo "| 今日提交（00:00 起） | $COMMITS_TODAY |"
  echo "| worktree / 未提交 | $WORKTREES / $DIRTY |"
  echo "| serve 8788 / bridge 14830 | $SERVE / $BRIDGE |"
  echo "| 最近 CI 结论 | ${PARITY_LINE:-未找到} |"
  echo "| 最近测试总数 | ${TOTAL_LINE:-未找到} |"
  echo "| 最近 QC | ${QC_LINE:-未找到} |"
  echo
  echo "## 夜间提交"
  echo
  echo '```'
  git log --since='2026-11-20 20:00' --oneline --decorate | head -60
  echo '```'
  echo
  echo "## 质量门禁证据"
  echo
  echo "- 最新 CI 日志：\`${LATEST_CI:-未找到}\`"
  echo "- 最新 QC 日志：\`${LATEST_QC:-未找到}\`"
  echo "- QC 报告：\`$REPO/go_hive/QC_REPORT.md\`"
  echo
  echo "## 时间与效率"
  echo
  echo '```'
  tail -8 go_hive/TIME_EVENTS.csv 2>/dev/null
  echo '```'
  echo
  echo "## 告警"
  echo
  echo '```'
  tail -12 go_hive/ALERTS.md 2>/dev/null
  echo '```'
  echo
  echo "## 契约/待办快照"
  echo
  for f in go_hive/docs/tool-contract-recheck.json go_hive/contracts/tool-definitions.json; do
    if [ -f "$f" ]; then
      echo "### \`$f\`"
      echo '```json'
      head -40 "$f"
      echo '```'
    fi
  done
  echo
  echo "## 已知未对齐（以文档为准）"
  echo
  echo "- Tool 名称形状：分组 vs Python 扁平（若夜间未完成，仍不能宣布逐字一致）。"
  echo "- API：14 端点探针中的 tools/SSE/snapshot/stats/credentials 差异。"
  echo "- Config：worker_llm / vision_fallback / queen_llm 的生产消费路径、Python-only 键。"
  echo "- 未验证：Windows 实机、81 个凭据集成、真实视觉模型、LongMemEval 真实分数、GitHub Actions 发布。"
  echo
  echo "## 下一步"
  echo
  echo "1. 复核夜间合并的 lane 与契约复测数字。"
  echo "2. 若 Tool 扁平化未完成，优先收口；否则转向 API 响应契约与 config 运行时消费。"
  echo "3. 任何新增/变化的差异更新到 \`docs/tool-contract-diff.md\`、\`docs/api-contract-diff.md\`。"
} > "$OUT"

cp "$OUT" "$LATEST"

SHORT="$(printf 'HEAD %s，夜间提交 %s，CI %s，QC %s，serve %s/桥 %s' "$HEAD" "$COMMITS_NIGHT" "${PARITY_LINE:-未知}" "${QC_LINE:-未知}" "$SERVE" "$BRIDGE")"
if command -v osascript >/dev/null 2>&1; then
  osascript -e "display notification \"${SHORT//\"/\\\"}\" with title \"go_hive 09:00 晨报\"" >/dev/null 2>&1 || true
fi
printf '%s\n' "$OUT"
