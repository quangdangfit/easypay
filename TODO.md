# TODO — Payment Gateway Implementation Plan

A checklist for building the global payment gateway monolith (Stripe + blockchain) per `CLAUDE.md`. Work top-to-bottom; each phase is independently testable.

---

## Phase 0 — Project Bootstrap

- [ ] Initialize Go module: `go mod init github.com/quangdangfit/easypay`
- [ ] Set Go version to 1.22+ in `go.mod`
- [ ] Create top-level directory skeleton: `cmd/`, `internal/`, `pkg/`, `migrations/`, `integration_test/`
- [ ] Add `Makefile` with targets: `run`, `build`, `test`, `test-integration`, `migrate`, `lint`
- [ ] Add `Dockerfile` (multi-stage Go build, distroless final image)
- [ ] Add `.gitignore` entries for `bin/`, `.env`, `coverage.out`, IDE folders
- [ ] Add `.env.example` listing every required env var from CLAUDE.md
- [ ] Pin core deps: `gofiber/fiber/v2`, `go-sql-driver/mysql`, `redis/go-redis/v9`, `segmentio/kafka-go` (or `confluent-kafka-go`), `ethereum/go-ethereum`, `stripe/stripe-go/v76`, `prometheus/client_golang`, `testcontainers/testcontainers-go`
- [ ] Install Stripe CLI locally for webhook forwarding (`stripe listen --forward-to localhost:8080/webhook/stripe`)

---

## Phase 1 — Foundation

- [ ] `internal/config/config.go` — load all env vars into a typed `Config` struct; fail fast on missing required keys (incl. `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET`)
- [ ] `pkg/logger/logger.go` — `slog` setup with JSON handler; helper to inject `request_id`, `merchant_id`, `order_id`
- [ ] `pkg/response/response.go` — JSON envelope helpers (`OK`, `Accepted`, `Error`)
- [ ] `internal/domain/order.go` — `Order` struct + `OrderStatus` enum (`created/pending/paid/failed/expired/refunded`); fields for `stripe_session_id`, `stripe_payment_intent_id`, `stripe_charge_id`
- [ ] `internal/domain/transaction.go` — `PendingTx` struct + status enum (`pending/confirmed/failed/reorged`)
- [ ] `internal/domain/merchant.go` — `Merchant` struct + status enum (`active/suspended`)
- [ ] `internal/api/middleware/requestid.go` — inject `X-Request-ID` (generate if missing)
- [ ] `internal/api/handler/health.go` — `GET /healthz` (liveness), `GET /readyz` (DB+Redis+Kafka ping)
- [ ] `internal/api/router.go` — Fiber app, register middleware + health routes
- [ ] `cmd/server/main.go` — wire config → deps → router; signal-based graceful shutdown (SIGTERM, drain in-flight)
- [ ] `docker-compose.yml` — MySQL 8, Redis 7, Kafka + Zookeeper (with healthchecks)
- [ ] `migrations/001_create_orders.sql` — per CLAUDE.md schema (Stripe ID columns + `idx_stripe_pi` index)
- [ ] `migrations/002_create_pending_txs.sql`
- [ ] `migrations/003_create_merchants.sql`
- [ ] `migrations/004_create_block_cursors.sql`
- [ ] Migration runner (e.g. `golang-migrate` via `make migrate`)
- [ ] **Smoke test:** `make run` boots, `/healthz` returns 200, `/readyz` returns 200 against compose stack

---

## Phase 2 — Core Payment Flow (Sync Path)

### Repositories
- [ ] Define `OrderRepository` interface (per CLAUDE.md "Key Interfaces")
- [ ] `internal/repository/order_repo.go` — MySQL impl: `Create`, `GetByOrderID`, `UpdateStatus(orderID, status, stripePaymentIntentID)`, `BatchCreate`, `GetPendingBefore`
- [ ] Implement merchant_id sharding helper (`shard = merchant_id % 8`)
- [ ] Connection pool: `MaxOpenConns=100`, `MaxIdleConns=25`
- [ ] `internal/repository/merchant_repo.go` — lookup by `api_key`; cache hot merchants in memory (LRU)

### Cache layer
- [ ] Define `IdempotencyChecker` interface
- [ ] `internal/cache/idempotency.go` — Redis Bloom filter (`BF.EXISTS`/`BF.ADD` per shard) + `SETNX` on `idem:{merchant_id}:{transaction_id}` (TTL 24h)
- [ ] `internal/cache/ratelimiter.go` — sliding window via Redis sorted set (`rate:{merchant_id}`, TTL 60s)
- [ ] `internal/cache/lock.go` — `SET NX PX 30000` distributed lock for `lock:{user_id}:{method}`

### Middleware
- [ ] `pkg/hmac/hmac.go` — HMAC-SHA256 sign + constant-time verify
- [ ] `internal/api/middleware/auth.go` — verify `X-API-Key`, `X-Timestamp` (reject if drift > 5min), `X-Signature`
- [ ] `internal/api/middleware/ratelimit.go` — 429 if merchant exceeds `rate_limit/min`

### Kafka producer
- [ ] Define `EventPublisher` interface
- [ ] `internal/kafka/producer.go` — `acks=all`, `retries=3`, `linger.ms=5`, `batch.size=64KB`; partition key = `merchant_id`

### Service + handler
- [ ] `internal/service/payment_service.go` — orchestrate steps 1–6 of Flow 1 (bloom → fraud → bloom add + idem set → Stripe call → Kafka produce → response)
- [ ] `internal/api/handler/payment.go` — `POST /api/payments`; returns 202 with `{order_id, transaction_id, stripe_session_id, checkout_url, stripe_payment_intent_id, client_secret, status}`
- [ ] **Unit tests:** mock all interfaces; cover idempotency hit/miss, bloom false positive, rate limit, signature failure

---

## Phase 3 — Async Processing

- [ ] `internal/kafka/consumer.go` — base consumer group with manual offset commit
- [ ] `internal/consumer/payment_consumer.go` — poll 500 msgs → `BatchCreate` → on failure fall back to per-row insert → DLQ topic `payment.events.dlq`
- [ ] `internal/api/handler/payment_status.go` — `GET /api/payments/:id` (DB lookup, no provider call)
- [ ] Wire consumer lifecycle into `cmd/server/main.go` (start/stop with server)
- [ ] **Unit tests:** batch insert path, fallback path, DLQ routing

---

## Phase 4 — Stripe Integration

- [ ] Define `StripeClient` interface (CreateCheckoutSession, CreatePaymentIntent, GetPaymentIntent, GetCheckoutSession, CreateRefund, VerifyWebhookSignature)
- [ ] `internal/provider/stripe/types.go` — request/response structs (CreateCheckoutRequest, CreatePaymentIntentRequest, PaymentIntent, CheckoutSession, Refund, Event)
- [ ] `internal/provider/stripe/client.go` — wrap `stripe-go/v76`; configure `stripe.Key` + API version; pass `Idempotency-Key: {merchant_id}:{transaction_id}` on every mutating call; embed `order_id` + `merchant_id` in `metadata`
- [ ] Map Stripe error types → HTTP codes: `card_error/card_declined` → 402, `rate_limit_error` → 429, `api_error/idempotency_error` → 502, network/timeout → 504
- [ ] `internal/provider/stripe/signature.go` — verify `Stripe-Signature` header: parse `t=...,v1=...`; HMAC-SHA256(`{t}.{payload}`, webhook_secret); reject if drift > 5 min; constant-time compare
- [ ] `internal/service/webhook_service.go` — verify signature → `GET /v1/payment_intents/{id}` cross-check → idempotency (`SETNX webhook:{event.id}`) → switch on `event.type` (`payment_intent.succeeded`, `checkout.session.completed`, `payment_intent.payment_failed`, `charge.refunded`, `charge.dispute.created`) → update DB → produce `payment.confirmed`
- [ ] `internal/api/handler/webhook.go` — `POST /webhook/stripe`; **MUST return 2xx in < 5s** (offload heavy work to Kafka); read raw body BEFORE any JSON parsing for signature verification
- [ ] `internal/consumer/settlement_consumer.go` — consume `payment.confirmed` → POST merchant `callback_url` (HMAC-SHA256 signed body, retries with backoff) → analytics log
- [ ] `internal/api/handler/payment.go` — `POST /api/payments/:id/refund` → `stripe.Refund.Create` with idempotency key `refund:{order_id}:{request_id}`
- [ ] **Unit tests:** signature pass/fail, tampered payload, replay outside 5 min window, malformed body, 402/429/5xx/timeout from Stripe, idempotency-key reuse returns same response, event routing for each event type
- [ ] **Manual:** `stripe trigger payment_intent.succeeded` against local server end-to-end

---

## Phase 5 — Blockchain Path

- [ ] `internal/repository/pending_tx_repo.go` — CRUD + `UNIQUE(tx_hash)` enforcement; status transitions
- [ ] `internal/provider/blockchain/cursor.go` — read/write `block_cursors`, `cursor:{chain_id}` Redis mirror
- [ ] `internal/provider/blockchain/subscriber.go` — go-ethereum `SubscribeFilterLogs` for `PaymentReceived`; parse `orderId`, `payer`, `token`, `amount`; dedup via Bloom + DB; persist `pending_tx`; advance cursor **after** save
- [ ] `internal/provider/blockchain/confirmation.go` — every block_time: compute `confirmations = latest - tx_block`; reorg detection (`TransactionReceipt == nil`); validate amount/token/chain_id/expiry on threshold; produce `payment.confirmed`
- [ ] `internal/provider/blockchain/backfill.go` — every 5 min: `eth_getLogs` from cursor to head over HTTP RPC
- [ ] `internal/provider/blockchain/reconciliation.go` — every 1 hour: query smart contract state for stuck orders
- [ ] `internal/provider/blockchain/listener.go` — orchestrator that wires subscriber + confirmation tracker + backfill + reconciliation; restart on WS disconnect
- [ ] Update `POST /api/payments` to handle `?method=crypto` (returns checkout payload, no Stripe call)
- [ ] Alert hook (Layer 4): emit metric / log if pending order > 30 min
- [ ] **Unit tests:** reorg handling, amount mismatch rejection, cursor resume after restart, dedup on tx_hash

---

## Phase 6 — Safety Nets & Ops

- [ ] `internal/service/reconciliation.go` — cron every 5 min: pending orders > 10 min → `GET /v1/payment_intents/{id}` → force-confirm if `status='succeeded'`
- [ ] `internal/service/fraud_service.go` — velocity check (`ZRANGEBYSCORE` over rolling window) + risk score; `FraudChecker` interface (consider also reading Stripe Radar `outcome.risk_score` from PaymentIntent)
- [ ] Prometheus metrics: request count/latency, Kafka lag, DB pool stats, Stripe error rate (by `error.type`), Stripe webhook lag, on-chain confirmation lag, stuck-order gauge
- [ ] `GET /metrics` endpoint
- [ ] Structured log fields enforced everywhere (`request_id`, `merchant_id`, `order_id`, `stripe_payment_intent_id` when present)
- [ ] Graceful shutdown: stop accepting HTTP, drain Kafka consumers, close blockchain WS, flush logs

---

## Phase 7 — Integration Tests

- [ ] `integration_test/testenv.go` — testcontainers spin-up of MySQL + Redis + Kafka; helper to run migrations
- [ ] `integration_test/external_mock_test.go` — Stripe mock HTTP server (configurable responses, capture `Idempotency-Key` header, replay same response on retry)
- [ ] `TestCreateOrder_HappyPath` — full sync + async flow
- [ ] `TestIdempotency_DuplicateOrder` — same `transaction_id` twice → single order; Stripe also receives same idempotency key
- [ ] `TestDoubleSpending_ConcurrentConfirm` — two confirms race against `SELECT ... FOR UPDATE`
- [ ] `TestStripeWebhookSignatureVerification` — valid + tampered + replay-outside-tolerance payloads
- [ ] `TestStripeWebhookIdempotency` — 3× identical event (same `event.id`) → 1 state change
- [ ] `TestStripeEventRouting` — `payment_intent.succeeded`, `payment_intent.payment_failed`, `charge.refunded` → correct status
- [ ] `TestReconciliationCron` — drop a webhook → cron recovers via `GET /v1/payment_intents/{id}`
- [ ] `TestBlockchainTxHashDedup` — replayed event ignored
- [ ] `TestStripeClient_*` — 400 / 402 card_declined / 429 / 5xx / timeout / malformed JSON
- [ ] `TestStripeIdempotencyKey` — same key → mock returns identical response, no duplicate side-effects
- [ ] `TestFullPaymentFlow_E2E` — real DB + Redis + Kafka + mock Stripe, end-to-end
- [ ] `make test-integration` runs `go test ./integration_test/... -v -race -count=1 -timeout 120s`

---

## Phase 8 — Deployment

- [ ] Multi-stage Dockerfile produces ~20MB image
- [ ] Kubernetes Deployment manifest (single Deployment, monolith)
- [ ] HPA on CPU + custom metric (Kafka lag)
- [ ] PodDisruptionBudget for graceful rollouts
- [ ] ConfigMap for non-secret env, Secret for `STRIPE_SECRET_KEY` / `STRIPE_WEBHOOK_SECRET` / `HMAC_SECRET` / DB creds
- [ ] Liveness probe → `/healthz`, readiness → `/readyz`
- [ ] Configure Stripe webhook endpoint in dashboard (production URL → `/webhook/stripe`); subscribe to `payment_intent.*`, `checkout.session.completed`, `charge.refunded`, `charge.dispute.created`
- [ ] Grafana dashboard JSON: latency p50/p95/p99, Kafka lag, error rate, Stripe webhook delivery lag, on-chain confirmation depth
- [ ] Alerts: stuck-order > 30 min, Kafka consumer lag > threshold, Stripe webhook 5xx rate, Stripe API error rate spike, RPC disconnect

---

## Cross-Cutting Rules (apply throughout)

- [ ] `context.Context` flows handler → service → repo on every call
- [ ] All errors wrapped: `fmt.Errorf("op: %w", err)` — never swallow
- [ ] All money is `int64` smallest-unit (cents for USD/EUR; no subunit for JPY/IDR)
- [ ] All timestamps UTC
- [ ] Every external dep behind an interface (DB, Redis, Kafka, Stripe, ETH client)
- [ ] Secrets only from env / secret manager; merchant `secret_key` stored bcrypt-hashed
- [ ] Every mutating Stripe call sends `Idempotency-Key: {merchant_id}:{transaction_id}` (or `refund:{order_id}:{request_id}`)
- [ ] Every Stripe object created carries `metadata.order_id` + `metadata.merchant_id`
- [ ] Bloom + Redis SET happen **before** Kafka produce (closes async-gap dup window)
- [ ] Block cursor persisted **after** event saved (at-least-once)
- [ ] Webhook handler returns 2xx in < 5s; heavy work → Kafka; raw body read **before** parsing for signature verify

---

## Done Criteria

- [ ] All Phase 1–7 boxes checked
- [ ] `go test ./internal/... -race` green
- [ ] `make test-integration` green
- [ ] `/readyz` green against full compose stack
- [ ] One end-to-end manual run: create payment → Stripe test card → webhook → merchant callback fires
- [ ] One end-to-end manual run: create crypto payment → MetaMask testnet tx → confirmation → merchant callback fires
- [ ] Refund flow verified: `POST /api/payments/:id/refund` → `charge.refunded` webhook → status='refunded'
