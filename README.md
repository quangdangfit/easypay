# easypay

[![CI](https://github.com/quangdangfit/easypay/actions/workflows/ci.yml/badge.svg)](https://github.com/quangdangfit/easypay/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/quangdangfit/easypay/graph/badge.svg)](https://codecov.io/gh/quangdangfit/easypay)
[![Go Report Card](https://goreportcard.com/badge/github.com/quangdangfit/easypay)](https://goreportcard.com/report/github.com/quangdangfit/easypay)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A Go monolith payment gateway with **Stripe** for traditional payments and an
**on-chain (MetaMask + smart contract)** path. Designed for high accept-layer
throughput by deferring Stripe Session creation to user-click time
("lazy checkout"), so the merchant API isn't bound by Stripe's per-account
rate limits.

> Status: working end-to-end. See `TODO.md` for roadmap and `CLAUDE.md` for
> the design contract this repo is built against.

---

## Architecture at a glance

```
                  ┌─── ACCEPT LAYER (50k+ TPS target) ───────────────────┐
   POST /api/     │                                                       │
   payments  ──▶  │  HMAC verify → Bloom+Idem → publish Kafka → 202 OK   │
                  │  (NO Stripe call when LAZY_CHECKOUT=true)            │
                  └───────────────────────┬───────────────────────────────┘
                                          │
                                  Kafka (durable WAL)
                                          │
                  ┌───────────────────────▼───────────────────────────────┐
                  │  payment.events  consumer → batch INSERT MySQL       │
                  │  payment.confirmed consumer → POST merchant callback │
                  └───────────────────────────────────────────────────────┘

   GET /pay/:id?t=<sig>                       (user click in browser)
        │
        ▼ CheckoutResolver — 6-tier:
        ├ ① in-process LRU             (sub-ms)
        ├ ② MySQL row                  (1-3ms, hot orders cached)
        ├ ③ Redis pending snapshot     (covers consumer lag right after POST)
        ├ ④ Distributed lock (Redis)   (coalesce concurrent first-clicks)
        ├ ⑤ Token bucket (Redis Lua)   (caps Stripe call rate fleet-wide)
        └ ⑥ gobreaker → Stripe         (circuit breaker; 50% failure rate trips)
            └─ on success: persist + warm LRU + 302 → checkout.stripe.com

   POST /webhook/stripe → verify Stripe-Signature → cross-check via API
                       → SETNX dedupe on event.id → update DB → produce
                         payment.confirmed → settlement consumer notifies
                         merchant via HMAC-signed POST.
```

**Two payment paths:** Stripe (live or `STRIPE_MODE=fake` for benchmarks) and
crypto (`POST /api/payments?method=crypto` → 4-layer chain listener
subscribes to `PaymentReceived` events).

---

## Tech stack

| Layer | Tech |
|---|---|
| Language | Go 1.22+ |
| HTTP | Fiber |
| DB | MySQL 8 (sharded by `merchant_id`) |
| Cache / lock / rate-limit | Redis 7 (RedisStack for Bloom) |
| Async log | Kafka (KRaft mode, no Zookeeper) |
| Provider | `stripe-go/v76`, `go-ethereum` |
| Metrics | Prometheus + Grafana |
| Container | Docker / Kubernetes |
| Tests | testcontainers-go |

---

## Quick start

```bash
# 0. boot deps
docker compose up -d
make migrate

# 1. configure
cp .env.example .env
# edit at minimum:
#   STRIPE_MODE=fake             # for local dev, no real Stripe call
#   LAZY_CHECKOUT=true           # decouple API throughput from Stripe
#   HMAC_SECRET=<32+ random>
#   CHECKOUT_TOKEN_SECRET=<32+ random>

# 2. seed merchant + smoke test
./scripts/curl-test.sh           # creates BENCH_M1 merchant + a payment

# 3. run server
make run
# (server reloads .env automatically via godotenv)
```

Open `http://localhost:8080/healthz` → `{"status":"alive"}`.
Open `http://localhost:8080/readyz` → checks DB + Redis + Kafka.

---

## Configuration (env)

See `.env.example` for the full list. The most consequential ones:

| Var | Effect |
|---|---|
| `STRIPE_MODE` | `live` (default), `fake` (no network — for bench/dev) |
| `LAZY_CHECKOUT` | `true` defers Stripe Session creation until user click |
| `PUBLIC_BASE_URL` | Origin used in self-hosted checkout URLs |
| `CHECKOUT_TOKEN_SECRET` | Signs `/pay/:id?t=<token>`; empty = dev mode |
| `CHECKOUT_DEFAULT_SUCCESS_URL` | Fallback when merchant doesn't pass it |
| `STRIPE_RATE_LIMIT` | Token-bucket cap (rps) for Stripe SDK calls |
| `HMAC_SECRET` | Merchant request signing key (≥16 chars) |
| `KAFKA_BROKERS` | `host:port,host:port` |
| `DB_DSN` | `user:pass@tcp(host:port)/db?parseTime=true&loc=UTC` |

---

## API

### Public — merchant-facing

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/api/payments` | HMAC | Create payment; returns checkout URL |
| GET | `/api/payments/:id` | HMAC | Poll order status |
| POST | `/api/payments/:id/refund` | HMAC | Issue Stripe refund |

HMAC headers required:
- `X-API-Key` — merchant API key
- `X-Timestamp` — unix seconds (drift > 5 min rejected)
- `X-Signature` — `hex(HMAC-SHA256(secret, "<X-Timestamp>.<raw_body>"))`

### Public — hosted checkout

| Method | Path | Description |
|---|---|---|
| GET | `/pay/:id?t=<token>` | Lazy create + 302 to Stripe |
| GET | `/checkout/success?order_id=...` | Branded post-payment page |
| GET | `/checkout/cancel?order_id=...` | Branded cancel page |

### Webhooks (provider → us)

| Method | Path | Verifier |
|---|---|---|
| POST | `/webhook/stripe` | `Stripe-Signature` HMAC-SHA256 |

### Internal

| Method | Path |
|---|---|
| GET | `/healthz` (liveness) |
| GET | `/readyz` (DB + Redis + Kafka ping) |
| GET | `/metrics` (Prometheus) |

### Example: create a payment

```bash
TS=$(date +%s)
BODY='{"transaction_id":"TXN-1","amount":1500,"currency":"USD"}'
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
    "order_id": "ORD-abc...",
    "transaction_id": "TXN-1",
    "checkout_url": "http://localhost:8080/pay/ORD-abc...?t=1716000000.5a3f...",
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
│  ├─ middleware/           # request id, HMAC auth, rate limit, prom
│  └─ handler/              # per-route handlers, depend on service interfaces
├─ service/                 # orchestration (Payments, Webhooks, Checkouts, Reconciler)
├─ provider/
│  ├─ stripe/               # Client interface + SDK / fake / breaker impls
│  └─ blockchain/           # subscriber, confirmation, backfill, reconciler
├─ consumer/                # Kafka consumers: payment_consumer, settlement_consumer
├─ repository/              # MySQL impls of OrderRepository, MerchantRepository, ...
├─ cache/                   # Redis-backed Locker, URLCache, TokenBucket, Idempotency, ...
├─ kafka/                   # Producer + base consumer
├─ domain/                  # Order, Merchant, PendingTx + status enums
└─ config/                  # env-driven typed Config
pkg/
├─ hmac/                    # HMAC-SHA256 utils
├─ checkouttoken/           # signs /pay/:id?t=<token>
├─ logger/                  # slog wrapper with request_id ctx
└─ response/                # JSON envelopes
migrations/                 # 001_init_schema.{up,down}.sql
integration_test/           # testcontainers-based end-to-end tests
deploy/k8s/                 # Deployment, ConfigMap, HPA, PDB, alerts
```

### DI / interface boundaries

Every cross-package consumer depends on an **interface**, never a concrete
struct. Constructors return interface types. Concrete impls are unexported.
The full rule is at `.claude/rules/dependency-injection.md`.

Example:

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

## Lazy checkout

When `LAZY_CHECKOUT=true`, the merchant API skips Stripe entirely and
returns a self-hosted URL `${PUBLIC_BASE_URL}/pay/<order>?t=<token>`. The
Stripe Session is created on the first GET hit on that URL.

**Why:**
- API throughput decouples from Stripe rate limits (default ~100 rps)
- Stripe call only happens at conversion-rate × creation-rate (often ~30%)
- Provider outages don't block payment creation

**6 reliability tiers** (see `internal/service/checkout_resolver.go`):

| Tier | Mechanism | Latency |
|---|---|---|
| 1 | In-process LRU | < 1ms |
| 2 | MySQL row | 1-3ms |
| 3 | Redis snapshot fallback | < 1ms |
| 4 | Distributed lock | 1-2ms |
| 5 | Token-bucket rate limit | 1ms |
| 6 | Circuit breaker → Stripe | 100-300ms |

Check `Resolve()` results via Prometheus:

```
checkout_resolve_result_total{result="cached_local"}  ↑ ideal
checkout_resolve_result_total{result="created"}       (real Stripe call)
checkout_resolve_result_total{result="rate_limited"}  bucket cap hit
checkout_resolve_result_total{result="breaker_open"}  Stripe degraded
```

---

## Testing

```bash
# unit tests (offline, fast)
make test

# integration (testcontainers — needs Docker)
make test-integration   # gated on EASYPAY_INTEGRATION=1

# load generator
make bench                                  # 50 workers, 1k requests
go run ./cmd/bench --concurrency 200 --duration 60s --poll-status
```

---

## Operational

| Concern | Where |
|---|---|
| Liveness | `GET /healthz` |
| Readiness | `GET /readyz` (DB+Redis+Kafka) |
| Metrics | `GET /metrics` (Prometheus textfile) |
| K8s manifests | `deploy/k8s/{deployment,configmap,alerts}.yaml` |
| Grafana | `deploy/grafana/dashboard.json` |
| Alerts | stuck-orders, kafka lag, webhook 5xx, breaker open, RPC lag |

### Production checklist

- [ ] Set strong `HMAC_SECRET` and `CHECKOUT_TOKEN_SECRET` (≥32 random bytes each)
- [ ] `STRIPE_MODE=live` with real keys via Secret manager (never env file)
- [ ] `STRIPE_RATE_LIMIT` set to ~80% of Stripe quota
- [ ] `PUBLIC_BASE_URL` matches the public DNS name (TLS terminated upstream)
- [ ] Configure Stripe webhook endpoint in dashboard → `/webhook/stripe`,
      subscribe to `payment_intent.*`, `checkout.session.completed`,
      `charge.refunded`, `charge.dispute.created`
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
```

See `git log --oneline` for the actual cadence.

---

## License

MIT — see `LICENSE`.
