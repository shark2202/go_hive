#!/usr/bin/env bash
# 每 2 小时一个 slot 版：主机四二进制 + manifest + 台账。
# 完整六平台归档/发布仍由 scripts/release.sh 在门槛（G1/G2/G3）执行。
#
# 用法: scripts/release_slot.sh [slot-id]
#   slot-id 形如 20261121-12；省略时按当前时间向下取偶数小时。
# 产物: dist/slots/<slot-id>/hive-<version>+build.<slot>-darwin-arm64.tar.gz
# 台账: dist/slots/cadence.json（watchdog 读取）
set -uo pipefail

REPO=/Users/mac/hive
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

write_cadence() { # status head artifact detail
  python3 - "$CADENCE" "$slot_id" "$1" "$2" "$3" "$4" <<'PY'
import json, sys, time
path, slot, status, head, artifact, detail = sys.argv[1:7]
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
    "slot_seconds": 7200,
})
json.dump(data, open(path, "w"), ensure_ascii=False, indent=1)
PY
}

head=$(git -C "$REPO" rev-parse --short HEAD 2>/dev/null || echo "?")
dirty=$(git -C "$REPO" status --porcelain --untracked-files=no -- go_hive/go-hive 2>/dev/null | wc -l | tr -d ' ')
if [ "${dirty:-0}" != "0" ]; then
  write_cadence blocked_dirty "$head" "" "go_hive/go-hive 有 ${dirty} 个未提交改动，slot 版不构建"
  notify "Hive slot 版被跳过" "$slot_id：主线 go_hive/go-hive 有未提交改动"
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

python3 - "$out/manifest.json" "$slot_id" "$head" "$version" "$build_version" "$artifact" "$sha" "$size" <<'PY'
import json, sys, time
path, slot, head, version, build_version, artifact, sha, size = sys.argv[1:9]
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
    "note": "slot build; six-platform release archives remain gated by scripts/release.sh",
}, open(path, "w"), ensure_ascii=False, indent=1)
PY

write_cadence ok "$head" "$artifact" "sha256=$sha bytes=$size"
notify "Hive slot 版完成" "$slot_id · ${version} · ${head}"

# 只保留最近 KEEP 个 slot，避免每 2 小时累积占盘
list=$(mktemp)
ls -1d "$SLOTS"/20*-* 2>/dev/null | sort >"$list"
count=$(wc -l <"$list" | tr -d ' ')
if [ "${count:-0}" -gt "$KEEP" ]; then
  head -n $((count - KEEP)) "$list" | while read -r old; do rm -rf "$old"; done
fi
rm -f "$list"

echo "release-slot: ok $artifact ($size bytes)"
