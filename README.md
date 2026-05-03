# easypay

[![CI](https://github.com/quangdangfit/easypay/actions/workflows/ci.yml/badge.svg)](https://github.com/quangdangfit/easypay/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/quangdangfit/easypay/graph/badge.svg)](https://codecov.io/gh/quangdangfit/easypay)
[![Go Report Card](https://goreportcard.com/badge/github.com/quangdangfit/easypay)](https://goreportcard.com/report/github.com/quangdangfit/easypay)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A Go monolith payment gateway with **Stripe** for traditional payments and an
**on-chain (MetaMask + smart contract)** path. The merchant write path is a
**synchronous MySQL INSERT** keyed on `(merchant_id, transaction_id)` — the
single dedupe layer. An optional "lazy checkout" mode defers Stripe Session
creation to user-click time, decoupling API throughput from Stripe's per-
account rate limits.

> See `CLAUDE.md` for the architectural source of truth.

---

## Architecture at a glance

```
                  ┌─── ACCEPT LAYER ──────────────────────────────────────┐
   POST /api/     │                                                        │
   payments  ──▶  │ HMAC verify → derive transaction_id from              │
                  │ (merchant_id, order_id) → INSERT into transactions    │
                  │ on the merchant's physical shard → 202 OK             │
                  │                                                        │
                  │ Eager mode: also create Stripe Session, persist        │
                  │ session_id; Lazy mode: return signed self-hosted URL.  │
                  └────────────────────────────────────────────────────────┘

   GET /pay/:merchant_id/:order_id?t=<sig>          (user click in browser)
        │
        ▼ CheckoutResolver — 5-tier:
        ├ ① in-process URLCache         (sub-ms)
        ├ ② MySQL row → reconstruct URL (1-3ms; deterministic from session_id)
        ├ ③ Distributed lock (Redis)    (coalesce concurrent first-clicks)
        ├ ④ Token bucket (Redis Lua)    (caps Stripe call rate fleet-wide)
        └ ⑤ gobreaker → Stripe          (circuit breaker; trips on failure rate)
            └─ on success: persist + warm cache + 302 → checkout.stripe.com

   POST /webhook/stripe → verify Stripe-Signature → cross-check via API
                       → SETNX dedupe on event.id → resolve merchant shard
                       → UPDATE transactions → produce payment.confirmed.

   Kafka payment.confirmed → settlement_consumer → POST merchant callback
                       (HMAC-signed; URL+secret looked up per delivery so
                        merchants can rotate without re-publishing).

   Cron 5m → service.OrderReconciliation: scatter-gather every shard for
             status IN ('created','pending') older than 10m, ask Stripe
             for ground truth, force-confirm or fail.
```

**Two payment paths:** Stripe (`stripe.mode: live` or `fake` for benchmarks)
and crypto (`POST /api/payments?method=crypto` → 4-layer chain listener:
WebSocket subscriber → backfill scanner → confirmation tracker → reconciler).

---

## Tech stack

| Layer | Tech |
|---|---|
| Language | Go 1.25+ |
| HTTP | Fiber |
| DB | MySQL 8 — `transactions` sharded via `merchants.shard_index` (logical) → physical pools by `ShardRouter`; control-plane pool 0 owns `merchants`, `onchain_transactions`, `block_cursors` |
| Cache / lock / rate-limit | Redis 7 |
| Async log | Kafka (KRaft mode, no Zookeeper) — only `payment.confirmed` is published |
| Provider | `stripe-go/v76` (live or fake, behind a circuit breaker), `go-ethereum` |
| Metrics | Prometheus + Grafana |
| Container | Docker / Kubernetes |
| Tests | `gomock` for unit, `testcontainers-go` for integration |

---

## Quick start

```bash
# 0. boot deps (MySQL x2, Redis, Kafka)
docker compose up -d
make migrate

# 1. configure
cp config.yaml.example config.yaml
# edit at minimum:
#   stripe.mode: fake                 # for local dev, no real Stripe call
#   app.lazy_checkout: true           # decouple API throughput from Stripe
#   security.hmac_secret: <16+ chars>
#   app.checkout_token_secret: <random>
#   security.admin_api_key: <random>  # required to use POST /admin/merchants

# 2. seed merchant + smoke test
./scripts/curl-test.sh           # creates BENCH_M1 merchant + a payment

# 3. run server
make run                         # passes --config config.yaml
# or after: make build → ./bin/easypay --config config.yaml
```

Open `http://localhost:8080/healthz` → `{"status":"alive"}`.
Open `http://localhost:8080/readyz` → checks every shard pool + Redis + Kafka.

---

## Configuration (YAML)

Server reads a YAML file passed via `--config` (default `./config.yaml`).
See `config.yaml.example` for the full schema and inline docs. The most
consequential keys:

| Key | Effect |
|---|---|
| `stripe.mode` | `live` (default), `fake` (no network — for bench/dev) |
| `app.lazy_checkout` | `true` defers Stripe Session creation until user click |
| `app.public_base_url` | Origin used in self-hosted checkout URLs |
| `app.checkout_token_secret` | Signs `/pay/:merchant_id/:order_id?t=<token>`; empty = dev mode |
| `app.checkout_default_success_url` / `..._cancel_url` | Fallback when merchant doesn't pass per-payment URLs |
| `app.stripe_rate_limit` | Token-bucket cap (rps) for Stripe SDK calls (default 80) |
| `app.logical_shard_count` | Number of logical merchant shards (default 16) |
| `db.dsn` | Single-pool DSN (legacy/dev). Ignored when `db.shards` is non-empty. |
| `db.shards[].dsn` | Per-physical-shard pools. `len(db.shards)` must divide `app.logical_shard_count`. |
| `security.hmac_secret` | Merchant request signing key (≥16 chars) |
| `security.admin_api_key` | When set, mounts `/admin/*` (gated by `X-Admin-Key`) |
| `kafka.brokers` | YAML list of `host:port` |

---

## API

### Merchant — HMAC-authenticated

| Method | Path | Description |
|---|---|---|
| POST | `/api/payments` | Create payment; returns checkout URL or crypto payload |
| GET | `/api/payments/:id` | Poll order status (id = merchant's `order_id`) |
| POST | `/api/payments/:id/refund` | Issue Stripe refund |

HMAC headers required:
- `X-API-Key` — merchant API key
- `X-Timestamp` — unix seconds (drift > `security.hmac_timestamp_skew` rejected; default 5m)
- `X-Signature` — `hex(HMAC-SHA256(secret_key, "<X-Timestamp>.<raw_body>"))`

### Hosted checkout (public)

| Method | Path | Description |
|---|---|---|
| GET | `/pay/:merchant_id/:order_id?t=<token>` | Resolve order → 302 to Stripe |
| GET | `/checkout/success?order_id=...` | Branded post-payment landing |
| GET | `/checkout/cancel?order_id=...` | Branded cancel landing |

### Webhooks (Stripe → us)

| Method | Path | Verifier |
|---|---|---|
| POST | `/webhook/stripe` | `Stripe-Signature` HMAC-SHA256, cross-checked against `GET /v1/payment_intents/{id}` |

### Admin (operator-facing)

Mounted only when `security.admin_api_key` is set; gated by `X-Admin-Key`.

| Method | Path | Description |
|---|---|---|
| POST | `/admin/merchants` | Create a merchant; returns api_key/secret_key once + assigned `shard_index` |

### Internal

| Method | Path |
|---|---|
| GET | `/healthz` (liveness) |
| GET | `/readyz` (every shard pool + Redis + Kafka) |
| GET | `/metrics` (Prometheus) |

### Example: create a payment

The merchant supplies their own `order_id` (their idempotency key); the
gateway derives `transaction_id = sha256(merchant_id || ":" || order_id)[:16]`
internally. Two retries with the same `order_id` always collapse onto the
same row via the MySQL UNIQUE.

```bash
TS=$(date +%s)
BODY='{"order_id":"ORD-1","amount":1500,"currency":"USD"}'
SIG=$(printf '%s.%s' "$TS" "$BODY" | openssl dgst -sha256 -hmac "<merchant_secret>" -hex | awk '{print $2}')

curl -X POST http://localhost:8080/api/payments \
  -H "Content-Type: application/json" \
  -H "X-API-Key: <merchant_api_key>" \
  -H "X-Timestamp: $TS" \
  -H "X-Signature: $SIG" \
  -d "$BODY"
```

Response:

```json
{
  "data": {
    "order_id": "ORD-1",
    "transaction_id": "5a3f...",
    "checkout_url": "http://localhost:8080/pay/<merchant_id>/ORD-1?t=1716000000.5a3f...",
    "status": "accepted"
  }
}
```

---

## Project layout

```
cmd/
├─ server/        # main: wires DI graph, starts HTTP + consumers
└─ bench/         # load generator with HMAC-signed requests
internal/
├─ api/
│  ├─ router.go             # Fiber routes
│  ├─ middleware/           # request id, HMAC auth, rate limit, admin auth, prom
│  └─ handler/              # per-route handlers (payment, webhook, checkout,
│                           #   refund, merchant, payment_status, health, metrics)
├─ service/                 # Payments, Webhooks, Checkouts, Merchants, Reconciler
├─ provider/
│  ├─ stripe/               # Client interface + live SDK / fake / circuit breaker
│  └─ blockchain/           # subscriber, backfill, confirmation, reconciler, cursor
├─ consumer/                # settlement_consumer (the only one)
├─ repository/              # OrderRepository, MerchantRepository, OnchainTxRepository,
│                           #   ShardRouter (logical → physical pool)
├─ cache/                   # Redis-backed Locker, RateLimiter, TokenBucket; in-proc URLCache
├─ kafka/                   # Producer (PaymentConfirmedEvent) + BatchConsumer + DLQ
├─ domain/                  # Order, Merchant, OnchainTransaction + status enums
├─ config/                  # YAML loader, validation, defaults
└─ mocks/<pkg>/             # gomock-generated mocks (regen: make mocks)
pkg/
├─ hmac/                    # HMAC-SHA256 utils
├─ checkouttoken/           # signs /pay/:merchant_id/:order_id?t=<token>
├─ logger/                  # slog wrapper with request_id ctx
└─ response/                # JSON envelopes
migrations/                 # 001_init_schema.{up,down}.sql
integration_test/           # testcontainers-based end-to-end tests
deploy/k8s/                 # Deployment, ConfigMap, Secret example, alerts
deploy/grafana/             # dashboard.json
```

### DI / interface boundaries

Every cross-package consumer depends on an **interface**, never a concrete
struct. Constructors return interface types. Concrete impls are unexported.
The full rule is at `.claude/rules/dependency-injection.md`.

```go
// internal/service/types.go
type Payments interface {
    Create(ctx context.Context, in CreatePaymentInput) (*CreatePaymentResult, error)
}

// payment_service.go
type paymentService struct{ ... }   // unexported
func NewPaymentService(...) Payments { return &paymentService{...} }

// handler/payment.go
type PaymentHandler struct {
    svc service.Payments             // depends on interface, not struct
}
```

This makes mocking trivial in unit tests and keeps wiring explicit in
`cmd/server/main.go`.

---

## Sync write & idempotency

The merchant write path is a single MySQL INSERT routed to the merchant's
physical shard:

1. `transaction_id = sha256(merchant_id || ":" || order_id)[:16]` (32 hex).
2. `INSERT INTO transactions (...)` on `ShardRouter.For(merchant.shard_index)`.
3. On `ER_DUP_ENTRY (1062)` for `(merchant_id, transaction_id)` → fetch the
   existing row → return the same response (idempotent retry). A material
   mismatch (different amount/currency/method bucket) returns 409.

No Bloom filter, no Redis idempotency cache, no pending-order snapshot — the
MySQL UNIQUE is the single dedupe layer. Trade-off: the write blocks on
MySQL (15–25 ms) instead of Kafka (5 ms), but state becomes consistent the
moment the API returns 202.

---

## Lazy checkout

When `app.lazy_checkout: true`, `POST /api/payments` skips the Stripe call
and returns a self-hosted URL `${app.public_base_url}/pay/<merchant_id>/<order_id>?t=<token>`.
The Stripe Session is created on the first GET hit on that URL.

**Why:**
- API throughput decouples from Stripe rate limits (default ~100 rps).
- Stripe call only happens at conversion-rate × creation-rate (often ~30%).
- Provider outages don't block payment creation.

**5 reliability tiers** (see `internal/service/checkout_resolver.go`):

| Tier | Mechanism | Latency |
|---|---|---|
| 1 | In-process URLCache | < 1 ms |
| 2 | MySQL row → reconstruct from `stripe_session_id` | 1-3 ms |
| 3 | Distributed lock (Redis SETNX) | 1-2 ms |
| 4 | Token-bucket rate limit (Redis Lua) | 1 ms |
| 5 | Circuit breaker → Stripe | 100-300 ms |

Inspect `Resolve()` outcomes via Prometheus:

```
checkout_resolve_result_total{result="cached_local"}  ↑ ideal
checkout_resolve_result_total{result="cached_db"}     warm-from-row
checkout_resolve_result_total{result="created"}       (real Stripe call)
checkout_resolve_result_total{result="rate_limited"}  bucket cap hit
checkout_resolve_result_total{result="breaker_open"}  Stripe degraded
checkout_resolve_result_total{result="not_found"}     unknown order
```

---

## Sharding

`merchants.shard_index TINYINT` carries each merchant's logical shard in
`[0, app.logical_shard_count)`. Picked at create time by least-loaded
selection.

`ShardRouter` range-partitions logical → physical pool. With 16 logical
shards and 2 physical pools, logical 0..7 → pool 0, 8..15 → pool 1. The
control-plane pool (index 0) owns `merchants`, `onchain_transactions`, and
`block_cursors`; their schema is created on every shard but the rows live
only on pool 0.

Global lookups (`GetByOrderIDAny`, `GetByPaymentIntentID`,
`GetPendingBefore`) scatter-gather every physical pool in parallel and stamp
the matching pool's logical-shard index on the returned row so follow-up
writes route deterministically.

`docker-compose.yml` ships two MySQL containers (`mysql` on 3306, `mysql1`
on 3307) so the multi-shard config can be exercised locally.

---

## Testing

```bash
# unit tests (offline, fast)
make test

# unit tests with coverage profile (CI target)
make unittest

# integration (testcontainers — needs Docker)
make test-integration

# regenerate gomock mocks after editing an interface
make mocks

# load generator
make bench                                  # 50 workers, 1k requests
go run ./cmd/bench --concurrency 200 --total 5000
```

---

## Operational

| Concern | Where |
|---|---|
| Liveness | `GET /healthz` |
| Readiness | `GET /readyz` (every shard pool + Redis + Kafka) |
| Metrics | `GET /metrics` (Prometheus textfile) |
| K8s manifests | `deploy/k8s/{deployment,configmap,secret.example,alerts}.yaml` |
| Grafana | `deploy/grafana/dashboard.json` |
| Alerts | stuck-orders, kafka lag, webhook 5xx, breaker open, RPC lag |

### Production checklist

- [ ] Set strong `security.hmac_secret` and `app.checkout_token_secret` (≥32 random bytes each)
- [ ] `stripe.mode: live` with real keys via Secret manager (never in the YAML file)
- [ ] `app.stripe_rate_limit` set to ~80% of Stripe quota
- [ ] `app.public_base_url` matches the public DNS name (TLS terminated upstream)
- [ ] `security.admin_api_key` set to a long random value (rotated on key compromise); leave empty in environments where `/admin/*` should not be exposed
- [ ] Configure Stripe webhook endpoint in dashboard → `/webhook/stripe`,
      subscribe to `payment_intent.succeeded`, `payment_intent.payment_failed`,
      `checkout.session.completed`, `charge.refunded`, `charge.dispute.created`
- [ ] Replicate Kafka (RF=3), MySQL (replicas + binlog backup), Redis (Sentinel/Cluster)
- [ ] Apply `deploy/k8s/alerts.yaml` to your Prometheus rules
- [ ] Run `make test-integration` against staging before each release

---

## Commit style

```
feat(payment): ...     # new feature
fix(webhook): ...      # bug fix
refactor(...): ...     # internal cleanup
perf(redis): ...       # performance work
test(e2e): ...
chore(docker): ...
docs(claude): ...
```

See `git log --oneline` for the actual cadence.

---

## License

MIT — see `LICENSE`.
