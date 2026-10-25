#!/usr/bin/env bash
# Where the monitor's SQLite database lives between GitHub Actions runs.
#
#   db.sh restore   newest snapshot that decrypts and checks clean -> pamon.db
#   db.sh save      consistent snapshot of pamon.db -> one of three rotating KV slots
#   db.sh daily     once a day, a dated snapshot on the db-seed release (7 kept)
#
# Every run saves, so losing anything costs at most one run (5 minutes). The slots sit
# in Cloudflare KV behind the access gateway; an upload only ever replaces one slot, so
# the other two survive a failed or corrupt write. The release copies are the long-term
# fallback (and the seed for a store that is still empty).
#
# Needs: ./pamon built, BACKUP_TOKEN, SEED_KEY (gpg passphrase), GH_TOKEN for the release.
set -euo pipefail

STORE="${PAMON_BACKUP_URL:-https://pamon.hyunsuhahaha.workers.dev/__backup}"
SLOTS=3
KEEP_DAILY=7

auth=(-H "Authorization: Bearer ${BACKUP_TOKEN:?}")
decrypt() { gpg --batch --quiet --pinentry-mode loopback --passphrase "${SEED_KEY:?}" -d "$1"; }
encrypt() { gpg --batch --yes --quiet --pinentry-mode loopback --passphrase "${SEED_KEY:?}" -c -o "$2" "$1"; }

# usable FILE.gpg: decrypts, unzips and passes pamon's corruption check -> pamon.db
usable() {
  rm -f restore.db verify.db
  decrypt "$1" | gunzip > restore.db || return 1
  ./pamon -db restore.db -snapshot verify.db > /dev/null || return 1
  mv restore.db pamon.db
  rm -f pamon.db-wal pamon.db-shm verify.db
}

from_release() {
  local asset
  asset=$(gh release view db-seed --json assets --jq \
    '[.assets[] | select(.name | test("^pamon-[0-9]{8}\\.db\\.gz\\.gpg$"))] | sort_by(.name) | last | .name // "pamon.db.gz.gpg"')
  gh release download db-seed -p "$asset" --clobber
  usable "$asset" || { echo "::error::release backup $asset is not usable"; exit 1; }
  echo "restored from release asset $asset"
}

restore() {
  local list
  # An unreachable store must stop the run: carrying on from an older copy would
  # overwrite newer history at the next save.
  if ! list=$(curl -fsS --retry 3 --retry-all-errors "${auth[@]}" "$STORE/list"); then
    echo "::error::backup store unreachable; not running on an older database"
    exit 1
  fi
  for s in $(jq -r '.slots[].slot' <<<"$list"); do
    at=$(jq -r --arg s "$s" '.slots[] | select(.slot==$s) | .at' <<<"$list")
    if curl -fsS --retry 3 --retry-all-errors "${auth[@]}" "$STORE/slot/$s" -o slot.gpg && usable slot.gpg; then
      echo "restored slot $s (saved $at)"
      return
    fi
    echo "::warning::slot $s (saved $at) is not usable, trying an older one"
  done
  if [ "$(jq '.slots | length' <<<"$list")" = 0 ]; then
    # Empty store: the one-time move from the Actions cache, when the workflow restored it.
    if [ -f pamon.db ] && ./pamon -db pamon.db -snapshot verify.db > /dev/null; then
      rm -f verify.db
      echo "store is empty: starting from the Actions cache copy"
      return
    fi
  else
    echo "::warning::no usable slot; falling back to the daily release backup"
  fi
  from_release
}

save() {
  ./pamon -db pamon.db -snapshot snap.db
  gzip -9c snap.db > snap.db.gz
  encrypt snap.db.gz snap.db.gz.gpg
  local slot=$(( ${GITHUB_RUN_NUMBER:-0} % SLOTS ))
  curl -fsS --retry 3 --retry-all-errors -X PUT "${auth[@]}" -H "x-pamon-run: ${GITHUB_RUN_ID:-local}" \
    --data-binary @snap.db.gz.gpg "$STORE/slot/$slot"
  echo
  echo "saved slot $slot ($(stat -c %s snap.db.gz.gpg) bytes)"
  # The old Actions cache copies are stale from here on; none may ever be restored again.
  gh cache list --key pamon-db- --limit 100 --json key --jq '.[].key' | xargs -r -n1 gh cache delete || true
}

daily() {
  local name="pamon-$(date -u +%Y%m%d).db.gz.gpg"
  if gh release view db-seed --json assets --jq '.assets[].name' | grep -qx "$name"; then
    echo "today's backup exists ($name)"
    return
  fi
  [ -f snap.db.gz.gpg ] || save
  cp snap.db.gz.gpg "$name"
  gh release upload db-seed "$name"
  # Older dated copies go only after today's is up, so there is never a gap.
  gh release view db-seed --json assets --jq '.assets[].name' | grep -E '^pamon-[0-9]{8}\.db\.gz\.gpg$' |
    sort -r | tail -n +$((KEEP_DAILY + 1)) | xargs -r -n1 gh release delete-asset db-seed -y
  echo "uploaded $name"
}

"${1:?restore|save|daily}"
