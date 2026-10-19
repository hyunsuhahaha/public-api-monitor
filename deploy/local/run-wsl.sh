#!/usr/bin/env bash
# Runs pamon in WSL until stopped (deploy/local/start-wsl.ps1 starts it hidden from Windows).
# The database lives on the Linux disk (~/.pamon), not under /mnt/c: SQLite over the
# Windows mount is slow and its locks are unreliable. Rebuilds from the repo on each start,
# restarts after a crash (with a pause), keeps one rotated log.
set -u
REPO="$(cd "$(dirname "$0")/../.." && pwd)"
DATA="$HOME/.pamon"
mkdir -p "$DATA"
export PATH="$HOME/go-sdk/go/bin:$PATH" GOFLAGS=-buildvcs=false
echo $$ > "$DATA/run.pid"
trap 'kill "${CHILD:-}" 2>/dev/null; rm -f "$DATA/run.pid"; exit 0' TERM INT
while true; do
  # stamp the commit (and -dirty for uncommitted changes) so /version says what runs
  REV="$(git -C "$REPO" rev-parse --short HEAD 2>/dev/null || echo dev)"
  git -C "$REPO" diff --quiet HEAD -- . 2>/dev/null || REV="$REV-dirty"
  (cd "$REPO" && go build -ldflags "-X main.version=$REV" -o "$DATA/pamon" ./cmd/pamon) >> "$DATA/build.log" 2>&1 || true
  if [ -f "$DATA/pamon.log" ] && [ "$(stat -c %s "$DATA/pamon.log")" -gt 50000000 ]; then
    mv -f "$DATA/pamon.log" "$DATA/pamon.log.1"
  fi
  set -a; [ -f "$REPO/.env" ] && . "$REPO/.env"; set +a
  "$DATA/pamon" -config "$REPO/targets.yaml" -db "$DATA/pamon.db" \
    -addr "${PAMON_ADDR:-127.0.0.1:8080}" -location "${PAMON_LOCATION:-home-wsl}" >> "$DATA/pamon.log" 2>&1 &
  CHILD=$!
  wait "$CHILD"
  echo "$(date -Is) pamon exited ($?), restarting in 10 s" >> "$DATA/pamon.log"
  sleep 10
done
