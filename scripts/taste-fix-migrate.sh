#!/bin/sh
# One-time data migration for the 2026-09-15 taste fix. Idempotent.
#
# 1. Any live user_diff row left over from the retired verbatim file-diff path
#    is re-tiered STATED -> OBSERVED. A file diff is an observed hypothesis
#    about taste, never a stated requirement; tiering it STATED made 54
#    verbatim code dumps indistinguishable from explicit requirements.
# 2. The store is marked dirty and TASTE.md re-rendered, so the file drops the
#    (source, TIER) suffix in favour of command-code bullets.
#
# Usage: scripts/taste-fix-migrate.sh [--db PATH] [--binary PATH] [--dry-run]
set -eu

DB="${PALATE_DB:-$HOME/.claude/palate/palate.db}"
BIN="${PALATE_BIN:-}"
DRY=0

while [ $# -gt 0 ]; do
  case "$1" in
    --db) DB="$2"; shift 2 ;;
    --binary) BIN="$2"; shift 2 ;;
    --dry-run) DRY=1; shift ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

if [ ! -f "$DB" ]; then echo "migrate: no DB at $DB" >&2; exit 1; fi

COUNT=$(sqlite3 "$DB" "SELECT COUNT(*) FROM preferences WHERE source='user_diff' AND valid_until IS NULL AND trust_tier<>'OBSERVED';")
echo "migrate: $COUNT live user_diff row(s) not yet OBSERVED"

if [ "$DRY" = "1" ]; then
  echo "migrate: dry run, nothing written"
  exit 0
fi

if [ "$COUNT" != "0" ]; then
  BACKUP="$DB.pre-obs-migrate.$(date +%Y%m%d-%H%M%S)"
  cp "$DB" "$BACKUP"
  echo "migrate: backup $BACKUP"
  sqlite3 "$DB" "UPDATE preferences SET trust_tier='OBSERVED' WHERE source='user_diff' AND valid_until IS NULL AND trust_tier<>'OBSERVED';
                 INSERT INTO events(event_type, payload) VALUES('migrate_user_diff_tier', '{\"to\":\"OBSERVED\",\"reason\":\"file diffs are observed, not stated\"}');"
  echo "migrate: re-tiered $COUNT row(s) to OBSERVED"
fi

if [ -n "$BIN" ]; then
  PALATE_HOME="$(dirname "$DB")" "$BIN" taste >/dev/null
  echo "migrate: TASTE.md re-rendered by $BIN"
else
  echo "migrate: no --binary given; run \`palate taste\` to re-render TASTE.md"
fi
