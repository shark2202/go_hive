#!/usr/bin/env bash
# R15 repro: capture bridge /command + hive-browser CLI returns for the coordinate
# actions (click / hover / key) against the real bridge + the fake extension.
# Usage: reproduce.sh <repo-root> <out-dir>
set -euo pipefail

root=$(cd "$1" && pwd)
out=$(cd "$(dirname "$2")" && mkdir -p "$2" && cd "$2" && pwd)
go_hive="$root/go_hive"
ws_port=19929
http_port=19930
control_port=19931
tmp=$(mktemp -d "${TMPDIR:-/tmp}/probe-actions.XXXXXX")
pids=()
cleanup() {
  for pid in "${pids[@]:-}"; do kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; done
  rm -rf "$tmp"
}
trap cleanup EXIT

echo "== build =="
(cd "$go_hive/browserbridge" && CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go build -o "$tmp/browserbridge" ./cmd/browserbridge)
(cd "$go_hive/go-hive" && CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go build -o "$tmp/hive-browser" ./cmd/hive-browser)
"$tmp/browserbridge" -version > "$out/bridge.version.txt" 2>/dev/null || true

echo "== bridge + fake extension =="
mkdir -p "$tmp/bridge-home" "$tmp/cli-home"
HIVE_HOME="$tmp/bridge-home" "$tmp/browserbridge" \
  -ws "127.0.0.1:$ws_port" -http "127.0.0.1:$http_port" -control "127.0.0.1:$control_port" \
  -no-legacy -no-sweep -no-idle-exit >"$tmp/bridge.log" 2>&1 &
pids+=($!)
for _ in $(seq 1 100); do curl -fsS -m 1 "http://127.0.0.1:$http_port/health" >/dev/null 2>&1 && break; sleep 0.1; done

uv run --offline --project "$root/tools" python "$go_hive/scripts/e2e_browser_panel_fakeext.py" --port "$ws_port" \
  >"$tmp/fakeext.log" 2>&1 &
pids+=($!)
for _ in $(seq 1 100); do
  curl -fsS -m 1 "http://127.0.0.1:$http_port/status" 2>/dev/null | grep -q '"connected":true' && break
  sleep 0.1
done
curl -fsS -m 1 "http://127.0.0.1:$http_port/status" | grep -q '"connected":true' || { echo "fake ext not attached"; cat "$tmp/bridge.log" "$tmp/fakeext.log"; exit 1; }

uv run --offline --project "$root/tools" python "$go_hive/scripts/e2e_browser_panel_fakeext.py" \
  --control-port "$control_port" --create-context e2e-profile > "$out/create-context.json"

base="http://127.0.0.1:$http_port"
call_bridge() { # name, params-json
  local name=$1 params=$2
  curl -sf --max-time 25 -X POST "$base/command" -H 'Content-Type: application/json' \
    -d "$(jq -cn --arg t browser_interact --argjson p "$params" '{type: $t, params: $p}')" > "$out/bridge-$name.json"
  python3 -c "
import json,sys
b=json.load(open('$out/bridge-$name.json'))
r=b.get('result', b)
print('$name', 'OK' if r.get('ok') else 'FAIL', 'keys='+','.join(sorted(r.keys())))"
}

echo "== bridge /command probes =="
call_bridge click-coord '{"action":"left_click","coordinate":[0.5,0.5],"tab_id":101,"auto_snapshot_mode":"off"}'
call_bridge hover-coord '{"action":"hover","coordinate":[0.25,0.5],"tab_id":101}'
call_bridge key-coord '{"action":"key","text":"Enter","coordinate":[0.5,0.5],"tab_id":101}'

echo "== hive-browser CLI probes =="
cli() { HIVE_HOME="$tmp/cli-home" "$tmp/hive-browser" "$@"; }
set +e
cli interact --action left_click --coordinate 0.5,0.5 --tab 101 --json --bridge "$base" > "$out/cli-click-coord.json" 2>"$out/cli-click-coord.err"; echo "click rc=$?"
cli interact --action hover --coordinate 0.25,0.5 --tab 101 --json --bridge "$base" > "$out/cli-hover-coord.json" 2>"$out/cli-hover-coord.err"; echo "hover rc=$?"
cli interact --action key --text Enter --coordinate 0.5,0.5 --tab 101 --json --bridge "$base" > "$out/cli-key-coord.json" 2>"$out/cli-key-coord.err"; echo "key rc=$?"
set -e
python3 - "$out" <<'PY'
import json, pathlib, sys
d = pathlib.Path(sys.argv[1])
for name in ("cli-click-coord", "cli-hover-coord", "cli-key-coord"):
    p = d / f"{name}.json"
    raw = p.read_text() if p.exists() else ""
    if not raw.strip():
        print(name, "EMPTY", (d / f"{name}.err").read_text()[:200])
        continue
    b = json.loads(raw)
    print(name, "OK" if b.get("ok") else "FAIL", "keys=" + ",".join(sorted(b.keys())))
PY
echo "evidence in $out"
# Keep the spilled JPEGs alongside the JSON (their saved_to paths point into the
# probe's temp dir, which is removed on exit).
python3 - "$out" <<'PY'
import json, pathlib, shutil, sys
out = pathlib.Path(sys.argv[1])
dest = out / "artifacts"
dest.mkdir(exist_ok=True)
for name in ("cli-click-coord", "cli-hover-coord", "cli-key-coord"):
    p = out / f"{name}.json"
    if not p.is_file():
        continue
    body = json.loads(p.read_text())
    src = pathlib.Path(body.get("saved_to") or "")
    if src.is_file():
        shutil.copy2(src, dest / src.name)
        print("artifact:", src.name, src.stat().st_size, "B")
PY
