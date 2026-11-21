#!/usr/bin/env bash
# 每 2 小时一个 slot 版：主机四二进制 + manifest + 台账。
# 完整六平台归档/发布仍由 scripts/release.sh 在门槛（G1/G2/G3）执行。
#
# 用法: scripts/release_slot.sh [slot-id]
#   slot-id 形如 20261121-12；省略时按当前时间向下取偶数小时。
# 产物: dist/slots/<slot-id>/hive-<version>+build.<slot>-darwin-arm64.tar.gz
# 台账: dist/slots/cadence.json（watchdog 读取）
set -uo pipefail

# 仓库根按脚本位置解析：从主检出运行得到 /Users/mac/hive，从工作树运行得到
# 该工作树——避免在工作树里误用别处的源码/产物。
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO_ROOT="$REPO/go_hive"
MODULE="$GO_ROOT/go-hive"
BRIDGE="$GO_ROOT/browserbridge"
SLOTS="$GO_ROOT/dist/slots"
CADENCE="$SLOTS/cadence.json"
KEEP=6
export CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod
export COPYFILE_DISABLE=1

notify() {
  command -v osascript >/dev/null 2>&1 || return 0
  osascript -e "display notification \"${2//\"/\\\"}\" with title \"${1//\"/\\\"}\"" >/dev/null 2>&1 || true
}

now_epoch=$(date +%s)
if [ $# -ge 1 ]; then
  slot_id="$1"
else
  hour=$(date +%H); hour=$((10#$hour / 2 * 2))
  slot_id="$(date +%Y%m%d)-$(printf '%02d' "$hour")"
fi
slot_hour=${slot_id##*-}
case "$slot_hour" in 08|10|12|14|16|18|20|22) ;; *)
  echo "release-slot: $slot_id 不在 08..22 偶数小时窗口，跳过"; exit 0 ;;
esac

mkdir -p "$SLOTS"
lockdir="$SLOTS/.lock.d"
if ! mkdir "$lockdir" 2>/dev/null; then
  oldpid=$(cat "$lockdir/pid" 2>/dev/null || echo "")
  if [ -n "$oldpid" ] && kill -0 "$oldpid" 2>/dev/null; then
    echo "release-slot: 另一个 slot 构建在进行（pid $oldpid），跳过 $slot_id"; exit 0
  fi
  rm -rf "$lockdir"
  mkdir "$lockdir" 2>/dev/null || { echo "release-slot: 锁竞争，跳过 $slot_id"; exit 0; }
fi
echo $$ >"$lockdir/pid"
trap 'rm -rf "$lockdir"' EXIT

write_cadence() { # status head artifact detail [compat]
  python3 - "$CADENCE" "$slot_id" "$1" "$2" "$3" "$4" "${5:-}" <<'PY'
import json, sys, time
path, slot, status, head, artifact, detail, compat = sys.argv[1:8]
try:
    data = json.load(open(path))
except Exception:
    data = {}
data.update({
    "last_slot": slot,
    "last_time": time.strftime("%Y-%m-%d %H:%M:%S"),
    "last_epoch": int(time.time()),
    "last_status": status,
    "last_head": head,
    "last_artifact": artifact,
    "last_detail": detail,
    "last_compat": compat,
    "slot_seconds": 7200,
})
json.dump(data, open(path, "w"), ensure_ascii=False, indent=1)
PY
}

head=$(git -C "$REPO" rev-parse --short HEAD 2>/dev/null || echo "?")
dirty=$(git -C "$REPO" status --porcelain --untracked-files=no -- go_hive/go-hive 2>/dev/null | wc -l | tr -d ' ')
if [ "${dirty:-0}" != "0" ]; then
  write_cadence blocked_dirty "$head" "" "go_hive/go-hive 有 ${dirty} 个未提交改动，slot 版不构建"
  notify "Hive slot 版被跳过" "${slot_id}：主线 go_hive/go-hive 有未提交改动"
  echo "release-slot: blocked_dirty ($dirty)"; exit 1
fi

version=$(grep -m1 'const Version' "$MODULE/internal/version/version.go" | cut -d'"' -f2)
build_version="${version}+build.${slot_id//-/}"
out="$SLOTS/$slot_id"
rm -rf "$out"; mkdir -p "$out"
log="$out/build.log"

echo "release-slot: $slot_id version=$build_version head=$head"
{
  date '+%Y-%m-%d %H:%M:%S'
  ( cd "$MODULE" && go build -tags embed_ui -trimpath -o "$out/hive" ./cmd/hive ) &&
  ( cd "$MODULE" && go build -trimpath -o "$out/hive-cred" ./cmd/hive-cred ) &&
  ( cd "$MODULE" && go build -trimpath -o "$out/hive-browser" ./cmd/hive-browser ) &&
  ( cd "$BRIDGE" && go build -trimpath -o "$out/browserbridge" ./cmd/browserbridge )
} >"$log" 2>&1
rc=$?
if [ $rc -ne 0 ]; then
  write_cadence failed "$head" "" "构建失败，见 $log"
  notify "Hive slot 版失败" "$slot_id 构建失败"
  echo "release-slot: build failed rc=$rc"; exit $rc
fi

artifact="$out/hive-${build_version}-darwin-arm64.tar.gz"
tar -czf "$artifact" -C "$out" hive hive-cred hive-browser browserbridge >>"$log" 2>&1 || {
  write_cadence failed "$head" "" "打包失败，见 $log"; echo "release-slot: tar failed"; exit 1; }
sha=$(shasum -a 256 "$artifact" | awk '{print $1}')
size=$(stat -f%z "$artifact" 2>/dev/null || wc -c <"$artifact")

# 门禁：新 slot 的二进制必须能无损升级上一 slot 写出的 home，且上一 slot
# 二进制能在升级后的 home 上回读而不写入。失败 → 状态 compat_failed + 告警，
# 看门狗（watchdog_report.sh）按 last_status 再次告警。
prev_slot_dir="$(ls -1d "$SLOTS"/20*-* 2>/dev/null | sort | while read -r d; do
  [ "$(basename "$d")" = "$slot_id" ] && continue
  [ -x "$d/hive" ] && echo "$d"
done | tail -1)"
compat_status="skipped"
compat_detail="no previous slot with a hive binary"
# 孤儿测试 serve 会占住 889x 端口（lane 探针被 kill 后 PPID=1，HIVE_HOME 在 scratch）。
# 只清 HIVE_HOME 指向 cache/scratch 的进程；主 serve 与用户 home 不受影响。
for pid in $(lsof -nP -iTCP -sTCP:LISTEN 2>/dev/null | awk '$9 ~ /:(88|89)[0-9][0-9]$/ {print $2}' | sort -u); do
  home="$(ps eww -p "$pid" 2>/dev/null | tr " " "\n" | grep "^HIVE_HOME=" | head -1)"
  case "$home" in
    *cache/scratch/*) kill "$pid" 2>/dev/null && echo "release-slot: killed orphan test serve pid=$pid ($home)" >>"$log" ;;
  esac
done
# 端口基址：默认 8890；被占则自动上移（或 HIVE_SLOT_COMPAT_PORT_BASE 覆盖）。
pick_port_base() {
  local base="${HIVE_SLOT_COMPAT_PORT_BASE:-8890}" i
  for _ in 1 2 3 4 5 6; do
    for i in 0 1 2 3 4 5 6 7 8 9; do
      if lsof -nP -iTCP:$((base + i)) -sTCP:LISTEN >/dev/null 2>&1; then break; fi
      [ "$i" = 9 ] && { echo "$base"; return 0; }
    done
    base=$((base + 20))
  done
  echo "${HIVE_SLOT_COMPAT_PORT_BASE:-8890}"
}
if [ -n "$prev_slot_dir" ]; then
  compat_work="$SLOTS/.compat/$slot_id"
  rm -rf "$compat_work"; mkdir -p "$compat_work"
  compat_port_base="$(pick_port_base)"
  echo "release-slot: upgrade-compat vs $(basename "$prev_slot_dir") (port-base $compat_port_base)" >>"$log"
  if bash "$GO_ROOT/scripts/upgrade_compat_check.sh" \
      --old-dir "$prev_slot_dir" --new-dir "$out" \
      --work "$compat_work" --out "$out/compat.json" \
      --label "slot-$slot_id" --port-base "$compat_port_base" >>"$log" 2>&1; then
    compat_status="ok"
    compat_detail="PASS vs $(basename "$prev_slot_dir")"
  else
    compat_status="compat_failed"
    compat_detail="FAILED vs $(basename "$prev_slot_dir"); see $out/compat.json"
  fi
fi

python3 - "$out/manifest.json" "$slot_id" "$head" "$version" "$build_version" "$artifact" "$sha" "$size" "$compat_status" "$compat_detail" <<'PY'
import json, sys, time
path, slot, head, version, build_version, artifact, sha, size, compat_status, compat_detail = sys.argv[1:11]
json.dump({
    "slot": slot,
    "built_at": time.strftime("%Y-%m-%d %H:%M:%S"),
    "head": head,
    "version": version,
    "build_version": build_version,
    "platform": "darwin-arm64",
    "artifact": artifact,
    "sha256": sha,
    "bytes": int(size),
    "binaries": ["hive", "hive-cred", "hive-browser", "browserbridge"],
    "upgrade_compat": {"status": compat_status, "detail": compat_detail},
    "note": "slot build; six-platform release archives remain gated by scripts/release.sh",
}, open(path, "w"), ensure_ascii=False, indent=1)
PY

if [ "$compat_status" = "compat_failed" ]; then
  write_cadence compat_failed "$head" "$artifact" "sha256=$sha bytes=$size; $compat_detail" "$compat_status"
  notify "Hive slot 版升级兼容门禁失败" "${slot_id}：${compat_detail}"
  echo "release-slot: compat_failed $artifact ($compat_detail)"
  exit 1
fi

write_cadence ok "$head" "$artifact" "sha256=$sha bytes=$size; compat=$compat_status ($compat_detail)" "$compat_status"
notify "Hive slot 版完成" "$slot_id · ${version} · ${head} · compat=$compat_status"

# 只保留最近 KEEP 个 slot，避免每 2 小时累积占盘（compat 工作区同步清理）
list=$(mktemp)
ls -1d "$SLOTS"/20*-* 2>/dev/null | sort >"$list"
count=$(wc -l <"$list" | tr -d ' ')
if [ "${count:-0}" -gt "$KEEP" ]; then
  head -n $((count - KEEP)) "$list" | while read -r old; do
    rm -rf "$old"
    rm -rf "$SLOTS/.compat/$(basename "$old")"
  done
fi
rm -f "$list"

echo "release-slot: ok $artifact ($size bytes)"
