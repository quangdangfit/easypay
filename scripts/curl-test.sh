#!/usr/bin/env bash
# Smoke test the easypay API with HMAC-signed curl requests.
#
# Prereqs:
#   - Stack up:        docker compose up -d
#   - Migrations done: make migrate
#   - Server running:  make run  (or `LAZY_CHECKOUT=true make run` for fast path)
#   - Merchant seeded: this script seeds one if missing
#
# Usage:
#   ./scripts/curl-test.sh                        # create payment + get status
#   ./scripts/curl-test.sh create                 # only create
#   ./scripts/curl-test.sh status ORD-xxx         # only status of an order
#   ./scripts/curl-test.sh refund ORD-xxx 500     # partial refund of 500 cents
#   ./scripts/curl-test.sh seed                   # only seed merchant

set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-bench-api-key}"
SECRET_KEY="${SECRET_KEY:-bench-secret-key-32-bytes-minimum-len}"
MERCHANT_ID="${MERCHANT_ID:-BENCH_M1}"
DB_DSN_RAW="${DB_DSN:-root:root@tcp(localhost:3306)/payments?parseTime=true&loc=UTC}"

# Translate Go DSN → mysql client args. Expects user:pass@tcp(host:port)/db?...
parse_dsn() {
  local dsn="$1"
  USER_PASS="${dsn%%@*}"           # user:pass
  REST="${dsn#*@tcp(}"             # host:port)/db?...
  HOST_PORT="${REST%%)*}"
  REST2="${REST#*)/}"
  DB="${REST2%%\?*}"
  USER="${USER_PASS%%:*}"
  PASS="${USER_PASS#*:}"
  HOST="${HOST_PORT%%:*}"
  PORT="${HOST_PORT##*:}"
}

seed_merchant() {
  parse_dsn "$DB_DSN_RAW"
  echo "[seed] inserting merchant '${MERCHANT_ID}' into ${HOST}:${PORT}/${DB}"
  MYSQL_PWD="$PASS" mysql -h"$HOST" -P"$PORT" -u"$USER" "$DB" <<SQL
INSERT INTO merchants (merchant_id, name, api_key, secret_key, rate_limit, status)
VALUES ('${MERCHANT_ID}', 'Bench Merchant', '${API_KEY}', '${SECRET_KEY}', 1000000, 'active')
ON DUPLICATE KEY UPDATE
  secret_key = VALUES(secret_key),
  rate_limit = VALUES(rate_limit),
  status     = 'active';
SQL
  echo "[seed] done"
}

# sign <method> <body>
# Outputs: TS\nSIG on stdout (consumed via while read)
sign() {
  local body="$1"
  local ts
  ts=$(date +%s)
  local sig
  sig=$(printf '%s.%s' "$ts" "$body" | openssl dgst -sha256 -hmac "$SECRET_KEY" -hex | awk '{print $2}')
  printf '%s\n%s\n' "$ts" "$sig"
}

create_payment() {
  local txn_id="TXN-$(date +%s)-$RANDOM"
  local body
  body=$(cat <<JSON
{"transaction_id":"$txn_id","amount":1500,"currency":"USD","payment_method_types":["card"],"customer_email":"buyer@example.com","success_url":"https://merchant.local/success","cancel_url":"https://merchant.local/cancel"}
JSON
)
  body="${body%$'\n'}"

  local ts sig
  { read -r ts; read -r sig; } < <(sign "$body")

  echo "[create] POST ${BASE_URL}/api/payments  txn=${txn_id}"
  curl -sS -X POST "${BASE_URL}/api/payments" \
    -H "Content-Type: application/json" \
    -H "X-API-Key: ${API_KEY}" \
    -H "X-Timestamp: ${ts}" \
    -H "X-Signature: ${sig}" \
    --data "$body" | tee /tmp/easypay-create.json
  echo
}

get_status() {
  local order_id="$1"
  local body=""
  local ts sig
  { read -r ts; read -r sig; } < <(sign "$body")

  echo "[status] GET ${BASE_URL}/api/payments/${order_id}"
  curl -sS "${BASE_URL}/api/payments/${order_id}" \
    -H "X-API-Key: ${API_KEY}" \
    -H "X-Timestamp: ${ts}" \
    -H "X-Signature: ${sig}"
  echo
}

refund() {
  local order_id="$1"
  local amount="${2:-0}"
  local body
  body=$(printf '{"amount":%s,"reason":"requested_by_customer"}' "$amount")
  local ts sig
  { read -r ts; read -r sig; } < <(sign "$body")

  echo "[refund] POST ${BASE_URL}/api/payments/${order_id}/refund  amount=${amount}"
  curl -sS -X POST "${BASE_URL}/api/payments/${order_id}/refund" \
    -H "Content-Type: application/json" \
    -H "X-API-Key: ${API_KEY}" \
    -H "X-Timestamp: ${ts}" \
    -H "X-Signature: ${sig}" \
    --data "$body"
  echo
}

case "${1:-default}" in
  seed)    seed_merchant ;;
  create)  seed_merchant; create_payment ;;
  status)  get_status "${2:?usage: status <order_id>}" ;;
  refund)  refund "${2:?usage: refund <order_id> [amount_cents]}" "${3:-0}" ;;
  default)
    seed_merchant
    create_payment
    order_id=$(awk -F'"' '/"order_id"/ {print $4; exit}' /tmp/easypay-create.json || true)
    if [[ -n "$order_id" ]]; then
      sleep 1
      get_status "$order_id"
    fi
    ;;
  *) echo "unknown command: $1" >&2; exit 2 ;;
esac
