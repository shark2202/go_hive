#!/bin/bash
# Push the current go_hive subtree to the public mirror github.com/shark2202/go_hive.
# Usage: publish_go_hive.sh [source_commit]
#   - pulls the mirror first (HTTP/1.1: this host's HTTP/2 git transport is flaky)
#   - exports tracked go_hive files at the source commit
#   - drops .github/workflows (the token has no workflow scope)
#   - commits the snapshot and pushes
# The mirror is a snapshot repo: each run adds one commit unless nothing changed.
set -eu

SRC=/Users/mac/hive
PUB=/Users/mac/.publish-go-hive/repo
MARKER=/Users/mac/.publish-go-hive/last-published
GIT="git -c http.version=HTTP/1.1"

COMMIT="${1:-$(git -C "$SRC" rev-parse HEAD)}"

if [ ! -d "$PUB/.git" ]; then
  echo "publish: mirror clone missing at $PUB" >&2
  exit 2
fi

cd "$PUB"
GIT_TERMINAL_PROMPT=0 $GIT pull --rebase --quiet || true

EXPORT=/tmp/go-hive-publish-export
rm -rf "$EXPORT"; mkdir -p "$EXPORT"
git -C "$SRC" archive "$COMMIT" go_hive | tar -x -C "$EXPORT"
rm -rf "$EXPORT/go_hive/.github/workflows"

# Replace the tree but keep the mirror's git dir.
find . -mindepth 1 -maxdepth 1 ! -name .git -exec rm -rf {} +
rsync -a "$EXPORT/go_hive/" ./

git add -A
if git diff --cached --quiet; then
  echo "publish: no changes for $COMMIT"
else
  git -c user.name="${PUBLISH_USER_NAME:-go_hive publisher}" \
      -c user.email="${PUBLISH_USER_EMAIL:-shark2202@users.noreply.github.com}" \
      commit -q -m "sync: go_hive @ $(git -C "$SRC" rev-parse --short "$COMMIT") ($(date '+%F %H:%M %Z'))"
fi
GIT_TERMINAL_PROMPT=0 $GIT push --quiet origin main
echo "$COMMIT" > "$MARKER"
echo "publish: pushed $(git log --oneline -1)"
