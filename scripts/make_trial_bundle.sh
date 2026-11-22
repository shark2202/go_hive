#!/usr/bin/env bash
# 组装试用包含：最新 slot 归档 + install.sh + 试用文档 + 校验和。
# 用法：bash go_hive/scripts/make_trial_bundle.sh [slot_id]
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO_HIVE="$ROOT/go_hive"
SLOTS="$GO_HIVE/dist/slots"
SLOT="${1:-$(ls "$SLOTS" | grep -E '^2026[0-9]{4}-[0-9]{2}$' | sort | tail -1)}"
SRC="$SLOTS/$SLOT"
[ -d "$SRC" ] || { echo "slot not found: $SRC" >&2; exit 1; }
OUT="$GO_HIVE/dist/trial/$SLOT"
rm -rf "$OUT"; mkdir -p "$OUT"

# slot 内容（二进制/归档/manifest/compat）
if git -C "$ROOT" rev-parse --show-toplevel >/dev/null 2>&1; then :; fi
cp -R "$SRC"/. "$OUT"/

# 试用文档与安装脚本
mkdir -p "$OUT/docs"
cp "$GO_HIVE/docs/trial/KNOWN-GAPS.md" "$OUT/docs/"
cp "$GO_HIVE/docs/trial/QUICKSTART.md" "$OUT/docs/"
[ -f "$GO_HIVE/install.sh" ] && cp "$GO_HIVE/install.sh" "$OUT/" || true

# 校验和
( cd "$OUT" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 shasum -a 256 > SHA256SUMS )

# 清单
{
  echo "slot=$SLOT"
  echo "built_at=$(date '+%Y-%m-%d %H:%M:%S %Z')"
  echo "head=$(git -C "$ROOT" rev-parse --short HEAD)"
  echo "version=$(grep -o 'Version = \"[^\"]*\"' "$GO_HIVE/go-hive/internal/version/version.go" | head -1 | cut -d'"' -f2)"
  echo "files=$(find "$OUT" -type f | wc -l | tr -d ' ')"
} > "$OUT/TRIAL-MANIFEST.txt"

echo "trial bundle: $OUT"
tail -n +1 "$OUT/TRIAL-MANIFEST.txt"
