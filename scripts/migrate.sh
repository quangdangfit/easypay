#!/usr/bin/env bash
set -euo pipefail

# Wrapper around golang-migrate. Reads DB_DSN from env (or .env if present).
# Usage: scripts/migrate.sh [up|down|version|force <v>]

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

if [[ -f "$ROOT/.env" ]]; then
  set -a
  # shellcheck source=/dev/null
  source "$ROOT/.env"
  set +a
fi

: "${DB_DSN:?DB_DSN is required}"

# golang-migrate expects mysql://user:pass@tcp(host:port)/db
MIGRATE_DSN="mysql://${DB_DSN}"

CMD="${1:-up}"
shift || true

if ! command -v migrate >/dev/null 2>&1; then
  echo "migrate CLI not found. Install: go install -tags 'mysql' github.com/golang-migrate/migrate/v4/cmd/migrate@latest" >&2
  exit 1
fi

migrate -path "$ROOT/migrations" -database "$MIGRATE_DSN" "$CMD" "$@"
