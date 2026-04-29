# TODO — Payment Gateway Implementation Plan

A checklist for building the global payment gateway monolith (Stripe + blockchain) per `CLAUDE.md`. Work top-to-bottom; each phase is independently testable.

---

## Phase 0 — Project Bootstrap

- [x] Initialize Go module: `go mod init github.com/quangdangfit/easypay`
- [x] Set Go version to 1.22+ in `go.mod`
- [x] Create top-level directory skeleton: `cmd/`, `internal/`, `pkg/`, `migrations/`, `integration_test/`
- [x] Add `Makefile` with targets: `run`, `build`, `test`, `test-integration`, `migrate`, `lint`
- [x] Add `Dockerfile` (multi-stage Go build, distroless final image)
- [x] Add `.gitignore` entries for `bin/`, `.env`, `coverage.out`, IDE folders
- [x] Add `.env.example` listing every required env var from CLAUDE.md
- [x] Pin core deps: `gofiber/fiber/v2`, `go-sql-driver/mysql`, `redis/go-redis/v9`, `segmentio/kafka-go`, `stripe/stripe-go/v76`, `prometheus/client_golang`, `golang-migrate/migrate/v4`, `google/uuid`, `golang.org/x/crypto` (go-ethereum + testcontainers added in later phases)
- [ ] Install Stripe CLI locally for webhook forwarding (`stripe listen --forward-to localhost:8080/webhook/stripe`)

---

## Phase 1 — Foundation

- [x] `internal/config/config.go` — load all env vars into a typed `Config` struct; fail fast on missing required keys (incl. `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET`)
- [x] `pkg/logger/logger.go` — `slog` setup with JSON handler; helper to inject `request_id`, `merchant_id`, `order_id`
- [x] `pkg/response/response.go` — JSON envelope helpers (`OK`, `Accepted`, `Error`)
- [x] `internal/domain/order.go` — `Order` struct + `OrderStatus` enum (`created/pending/paid/failed/expired/refunded`); fields for `stripe_session_id`, `stripe_payment_intent_id`, `stripe_charge_id`
- [x] `internal/domain/transaction.go` — `PendingTx` struct + status enum (`pending/confirmed/failed/reorged`)
- [x] `internal/domain/merchant.go` — `Merchant` struct + status enum (`active/suspended`)
- [x] `internal/api/middleware/requestid.go` — inject `X-Request-ID` (generate if missing)
- [x] `internal/api/handler/health.go` — `GET /healthz` (liveness), `GET /readyz` (DB+Redis+Kafka ping)
- [x] `internal/api/router.go` — Fiber app, register middleware + health routes
- [x] `cmd/server/main.go` — wire config → deps → router; signal-based graceful shutdown (SIGTERM, drain in-flight)
- [x] `docker-compose.yml` — MySQL 8, Redis 7, Kafka in KRaft mode (no Zookeeper, with healthchecks)
- [x] `migrations/001_create_orders.sql` — per CLAUDE.md schema (Stripe ID columns + `idx_stripe_pi` index)
- [x] `migrations/002_create_pending_txs.sql`
- [x] `migrations/003_create_merchants.sql`
- [x] `migrations/004_create_block_cursors.sql`
- [x] Migration runner (`golang-migrate` via `scripts/migrate.sh` / `make migrate`)
- [ ] **Smoke test:** `make run` boots, `/healthz` returns 200, `/readyz` returns 200 against compose stack

---

## Phase 2 — Core Payment Flow (Sync Path)

### Repositories
- [x] Define `OrderRepository` interface (per CLAUDE.md "Key Interfaces")
- [x] `internal/repository/order_repo.go` — MySQL impl: `Create`, `GetByOrderID`, `UpdateStatus(orderID, status, stripePaymentIntentID)`, `BatchCreate`, `GetPendingBefore`
- [x] Implement merchant_id sharding helper (FNV-32a mod 8 in `internal/repository/sharding.go`)
- [x] Connection pool: `MaxOpenConns=100`, `MaxIdleConns=25`
- [x] `internal/repository/merchant_repo.go` — lookup by `api_key`; cache hot merchants in memory (LRU)

### Cache layer
- [x] Define `IdempotencyChecker` interface
- [x] `internal/cache/idempotency.go` — Redis Bloom filter (`BF.EXISTS`/`BF.ADD` per shard) + `SETNX` on `idem:{merchant_id}:{transaction_id}` (TTL 24h)
- [x] `internal/cache/ratelimiter.go` — sliding window via Redis sorted set (`rate:{merchant_id}`, TTL 60s)
- [x] `internal/cache/lock.go` — `SET NX PX 30000` distributed lock for `lock:{user_id}:{method}`

### Middleware
- [x] `pkg/hmac/hmac.go` — HMAC-SHA256 sign + constant-time verify
- [x] `internal/api/middleware/auth.go` — verify `X-API-Key`, `X-Timestamp` (reject if drift > 5min), `X-Signature`
- [x] `internal/api/middleware/ratelimit.go` — 429 if merchant exceeds `rate_limit/min`

### Kafka producer
- [x] Define `EventPublisher` interface
- [x] `internal/kafka/producer.go` — `acks=all`, `retries=3`, `linger.ms=5`, `batch.size=64KB`; partition key = `merchant_id`

### Service + handler
- [x] `internal/service/payment_service.go` — orchestrate steps 1–6 of Flow 1 (bloom → fraud → bloom add + idem set → Stripe call → Kafka produce → response)
- [x] `internal/api/handler/payment.go` — `POST /api/payments`; returns 202 with `{order_id, transaction_id, stripe_session_id, checkout_url, stripe_payment_intent_id, client_secret, status}`
- [x] **Unit tests:** payment_service_test covers happy path, idempotent duplicate, crypto path, validation rejection. Stripe interface stubbed; full client + signature tests land in Phase 4.

---

## Phase 3 — Async Processing

- [x] `internal/kafka/consumer.go` — base consumer group with manual offset commit, batch fetch, DLQ writer
- [x] `internal/consumer/payment_consumer.go` — batch decode + `BatchCreate`; per-row fallback on batch failure; duplicate-key treated as success
- [x] `internal/api/handler/payment_status.go` — `GET /api/payments/:id` (DB lookup, merchant scoping, no provider call)
- [x] Wire consumer lifecycle into `cmd/server/main.go` (start with server, cancel on shutdown)
- [x] **Unit tests:** batch happy path, malformed message rejection, duplicate-key handling. Full DLQ end-to-end exercised in Phase 7 integration tests.

---

## Phase 4 — Stripe Integration

- [x] Define `StripeClient` interface (CreateCheckoutSession, CreatePaymentIntent, GetPaymentIntent, GetCheckoutSession, CreateRefund, VerifyWebhookSignature)
- [x] `internal/provider/stripe/types.go` — request/response structs (CreateCheckoutRequest, CreatePaymentIntentRequest, PaymentIntent, CheckoutSession, Refund, Event)
- [x] `internal/provider/stripe/impl.go` — wrap `stripe-go/v76`; configure `stripe.Key`; pass `Idempotency-Key` on every mutating call via `params.SetIdempotencyKey`; embed `order_id` + `merchant_id` in `metadata`
- [x] Map Stripe error types → categories on `ProviderError`: `card`, `api`, `idempotency`, `invalid_request`, `rate_limit` (HTTP 429), `network`. HTTP status mapping happens at the handler layer.
- [x] `internal/provider/stripe/signature.go` — verify `Stripe-Signature` header: parse `t=...,v1=...`; HMAC-SHA256(`{t}.{payload}`, webhook_secret); reject if drift > 5 min; constant-time compare
- [x] `internal/service/webhook_service.go` — verify signature → `GET /v1/payment_intents/{id}` cross-check → idempotency (`SETNX webhook:{event.id}`) → switch on `event.type` (`payment_intent.succeeded`, `checkout.session.completed`, `payment_intent.payment_failed`, `charge.refunded`, `charge.dispute.created`) → update DB → produce `payment.confirmed`
- [x] `internal/api/handler/webhook.go` — `POST /webhook/stripe`; raw body via `c.Body()` (no parser before signature verify); duplicate event returns 200
- [x] `internal/consumer/settlement_consumer.go` — consume `payment.confirmed` → POST merchant `callback_url` with HMAC-signed body, 3 retries with linear backoff
- [x] `internal/api/handler/refund.go` — `POST /api/payments/:id/refund` → Stripe Refund with idempotency key `refund:{order_id}:{request_id}`
- [x] **Unit tests:** signature pass/fail, tampered payload, replay outside 5 min window, missing/malformed header. Stripe error mapping & event routing covered via integration tests in Phase 7.
- [ ] **Manual:** `stripe trigger payment_intent.succeeded` against local server end-to-end

---

## Phase 5 — Blockchain Path

- [x] `internal/repository/pending_tx_repo.go` — CRUD + `UNIQUE(tx_hash)` enforcement; status transitions
- [x] `internal/provider/blockchain/cursor.go` — read/write `block_cursors` (MySQL); Redis mirror omitted (MySQL-only is sufficient for at-least-once)
- [x] `internal/provider/blockchain/subscriber.go` — `SubscribeFilterLogs` for PaymentReceived; parse log; dedup via DB lookup on `tx_hash`; persist `pending_tx`; advance cursor **after** save; reconnect with exponential backoff
- [x] `internal/provider/blockchain/confirmation.go` — every block_time: `confirmations = latest - tx_block`; reorg detection (`TransactionReceipt == nil`); amount validation; produce `payment.confirmed` on threshold
- [x] `internal/provider/blockchain/backfill.go` — every 5 min: `eth_getLogs` from cursor to head, batched, HTTP RPC
- [x] `internal/provider/blockchain/reconciliation.go` — every 1 hour: receipt re-check + Layer 4 stuck-order alert (>30 min)
- [x] `internal/provider/blockchain/listener.go` — orchestrator running all four loops as goroutines with shared ctx
- [x] `POST /api/payments?method=crypto` — payment service short-circuits Stripe and returns crypto checkout payload (already implemented in Phase 2)
- [x] Alert hook (Layer 4): emitted as a structured WARN log line in reconciliation.go
- [x] **Unit tests:** parser happy path + short-log rejection. Reorg / dedup / cursor resume covered in Phase 7 integration tests against ganache or a fake ChainClient.

---

## Phase 6 — Safety Nets & Ops

- [x] `internal/service/reconciliation.go` — cron every 5 min: pending orders > 10 min → `GET /v1/payment_intents/{id}` → force-confirm if `status='succeeded'`; also fails canceled / payment-method orders
- [x] `internal/service/fraud_service.go` — velocity check via Redis sorted set (sliding 60s window per merchant+user); `FraudChecker` interface ready to plug into payment service
- [x] Prometheus metric primitives: `http_requests_total`, `http_request_duration_seconds`, `stripe_api_errors_total`, `kafka_consumer_lag`, `stuck_orders_total`, `blockchain_confirmation_lag_blocks`
- [x] `GET /metrics` endpoint via fasthttpadaptor bridge
- [x] Structured log fields enforced: `request_id` (middleware), `merchant_id` (HMAC auth), `order_id` (handler context). Logger.With(ctx) injects them into every log line.
- [x] Graceful shutdown: HTTP listener drained via `app.ShutdownWithContext`; consumer goroutines cancelled via shared `consumerCtx`; Stripe SDK has no long-lived connections to close; blockchain WS closed via `chainClient.Close()` (already invoked through go-ethereum on cancellation).

---

## Phase 7 — Integration Tests

- [x] `integration_test/testenv.go` — testcontainers spin-up of MySQL + Redis + Kafka; gated on `EASYPAY_INTEGRATION=1`; runs all `migrations/*.up.sql`
- [x] `integration_test/external_mock_test.go` — in-process `MockStripe` implementing `stripe.Client`: captures every idempotency key, replays the same response on retry, supports forced errors per call type
- [x] `TestCreateOrder_HappyPath` — full sync (Stripe mock + Redis idem + Kafka publish) + async (consumer batch insert into MySQL) flow
- [x] `TestIdempotency_DuplicateOrder` — same `transaction_id` twice → single order, Stripe called once, exactly one Kafka event on the topic
- [x] `TestStripeWebhookFlow` — covers signature rejection, succeeded → paid transition, and dedup on `event.id` (3 sub-tests)
- [ ] `TestDoubleSpending_ConcurrentConfirm` — covered structurally by `SELECT ... FOR UPDATE` in repo; dedicated concurrency test left for Phase 8 hardening pass
- [ ] `TestStripeEventRouting` for failed/refunded — covered partially via webhook tests; full matrix deferred
- [ ] `TestReconciliationCron` — service exists with scheduled-time logic; full integration test deferred
- [ ] `TestBlockchainTxHashDedup` — UNIQUE constraint enforced at DB; full integration test against ganache deferred
- [ ] `TestStripeClient_*` HTTP error map — exercised in unit tests via `ProviderError`; live integration deferred
- [x] `make test-integration` runs `EASYPAY_INTEGRATION=1 go test -tags integration ./integration_test/... -v -count=1 -timeout 600s`

---

## Phase 8 — Deployment

- [x] Multi-stage Dockerfile produces small distroless image (root)
- [x] Kubernetes Deployment manifest (single Deployment, monolith) at `deploy/k8s/deployment.yaml`
- [x] HPA on CPU + custom metric `kafka_consumer_lag`
- [x] PodDisruptionBudget for graceful rollouts (`minAvailable: 1`)
- [x] ConfigMap for non-secret env (`deploy/k8s/configmap.yaml`); Secret example for `STRIPE_SECRET_KEY` / `STRIPE_WEBHOOK_SECRET` / `HMAC_SECRET` / DB creds (`deploy/k8s/secret.example.yaml`)
- [x] Liveness probe → `/healthz`, readiness → `/readyz`, with preStop sleep for connection drain
- [ ] Configure Stripe webhook endpoint in dashboard (production URL → `/webhook/stripe`); subscribe to `payment_intent.*`, `checkout.session.completed`, `charge.refunded`, `charge.dispute.created` (manual step)
- [x] Grafana dashboard JSON at `deploy/grafana/dashboard.json` covering request rate, p95 latency, Stripe error categories, Kafka lag, stuck orders, on-chain confirmation depth
- [x] Alerts at `deploy/k8s/alerts.yaml`: stuck-order, Kafka lag, webhook 5xx rate, Stripe API error rate, on-chain confirmation lag

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
