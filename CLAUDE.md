# CLAUDE.md — Payment Gateway (Monolith)

## Project Overview

A global payment gateway. Supports both **blockchain payments** (MetaMask → smart contract) and **traditional payments** (cards, wallets, bank transfers, BNPL via Stripe). Monolithic Go application with a single binary.

### What This System Does

1. Merchants integrate via a unified REST API (single API key + HMAC signature)
2. Users pay through Stripe Checkout / Payment Element (cards, Apple Pay, Google Pay, Link, ACH, SEPA, Klarna, Afterpay) or via MetaMask (on-chain crypto)
3. System validates, deduplicates, fraud-checks, then queues transactions via Kafka
4. Async consumers batch-insert into MySQL, settle with providers, notify merchants via webhook

### Architecture Decision: Monolith

Single Go binary with internal packages. NOT microservices. All communication is in-process function calls. Kafka is used for async processing (not for inter-service communication). Deploy as one Deployment on Kubernetes.

---

## Tech Stack

| Layer | Technology | Purpose |
|-------|----------|---------|
| Language | Go 1.22+ | Primary language, all code |
| HTTP framework | Fiber | REST API, webhook handler |
| Database | MySQL 8 | Primary datastore, sharded by merchant_id |
| Cache | Redis 7 | Idempotency, Bloom filter, rate limiting, distributed locks |
| Message queue | Kafka | Async event bus (payment.events, payment.confirmed) |
| Blockchain | go-ethereum | Chain listener, event subscriber, confirmation tracker |
| Payment provider | Stripe (PaymentIntents + Checkout) | Cards, wallets, bank debits, BNPL |
| Monitoring | Prometheus + Grafana | Metrics, alerting |
| Container | Docker + Kubernetes | Deployment, scaling |
| Testing | testcontainers-go | Integration tests with real MySQL, Redis, Kafka |

---

## Project Structure

```
payment-gateway/
├── CLAUDE.md                    # This file
├── cmd/
│   └── server/
│       └── main.go              # Entry point, wire dependencies, start server
├── internal/
│   ├── config/
│   │   └── config.go            # Load config from env vars
│   ├── api/
│   │   ├── router.go            # Fiber router setup, middleware registration
│   │   ├── middleware/
│   │   │   ├── auth.go          # HMAC-SHA256 signature verification
│   │   │   ├── ratelimit.go     # Per-merchant rate limiting (Redis)
│   │   │   └── requestid.go     # X-Request-ID injection for tracing
│   │   └── handler/
│   │       ├── payment.go       # POST /api/payments — create payment
│   │       ├── payment_status.go # GET /api/payments/:id — poll status
│   │       ├── webhook.go       # POST /webhook/stripe — Stripe event callback
│   │       └── health.go        # GET /healthz, GET /readyz
│   ├── domain/
│   │   ├── order.go             # Order entity + status enum
│   │   ├── transaction.go       # PendingTx entity (blockchain)
│   │   └── merchant.go          # Merchant entity + API key
│   ├── service/
│   │   ├── payment_service.go   # Core payment orchestration logic
│   │   ├── webhook_service.go   # Webhook verification + processing
│   │   ├── fraud_service.go     # Velocity check, risk scoring
│   │   └── reconciliation.go    # Cron: poll pending orders, fix stuck ones
│   ├── provider/
│   │   ├── provider.go          # Provider interface
│   │   ├── stripe/
│   │   │   ├── client.go        # Stripe SDK client (PaymentIntent, Checkout, Refund)
│   │   │   ├── signature.go     # Stripe-Signature header verification (HMAC-SHA256)
│   │   │   └── types.go         # Request/response types
│   │   └── blockchain/
│   │       ├── listener.go      # Orchestrator: subscriber + tracker + scanner
│   │       ├── subscriber.go    # WebSocket event subscription (go-ethereum)
│   │       ├── confirmation.go  # Block confirmation tracker
│   │       ├── backfill.go      # Backfill scanner (eth_getLogs)
│   │       ├── reconciliation.go # On-chain state reconciliation
│   │       └── cursor.go        # Block cursor persistence
│   ├── consumer/
│   │   ├── payment_consumer.go  # Kafka consumer: batch insert MySQL
│   │   └── settlement_consumer.go # Kafka consumer: notify merchant callback
│   ├── repository/
│   │   ├── order_repo.go        # MySQL CRUD for orders (with sharding)
│   │   ├── pending_tx_repo.go   # MySQL CRUD for blockchain pending_txs
│   │   └── merchant_repo.go     # MySQL CRUD for merchants
│   ├── cache/
│   │   ├── idempotency.go       # Redis: SETNX + Bloom filter for dedup
│   │   ├── ratelimiter.go       # Redis: sliding window rate limit
│   │   └── lock.go              # Redis: distributed lock for double-spending
│   └── kafka/
│       ├── producer.go          # Kafka producer with retry
│       └── consumer.go          # Kafka consumer group base
├── pkg/
│   ├── hmac/
│   │   └── hmac.go              # HMAC-SHA256 utilities
│   ├── response/
│   │   └── response.go          # Standardized JSON response helpers
│   └── logger/
│       └── logger.go            # Structured logging (slog)
├── migrations/
│   ├── 001_create_orders.sql
│   ├── 002_create_pending_txs.sql
│   ├── 003_create_merchants.sql
│   └── 004_create_block_cursors.sql
├── integration_test/
│   ├── testenv.go               # Spin up MySQL + Redis + Kafka containers
│   ├── payment_test.go          # Payment flow integration tests
│   ├── webhook_test.go          # Webhook handling tests
│   ├── blockchain_test.go       # Chain listener tests
│   └── external_mock_test.go    # Stripe mock HTTP server tests
├── docker-compose.yml           # Local dev: MySQL + Redis + Kafka (KRaft, no Zookeeper)
├── Dockerfile
├── Makefile
└── go.mod
```

---

## Core Flows

### Flow 1: Traditional Payment (Stripe) — 95% of traffic

```
POST /api/payments
│
├── middleware: verify HMAC-SHA256(payload, merchant.secret_key)
├── middleware: rate limit check (Redis INCR rate:{merchant_id})
│
├── handler/payment.go:
│   ├── 1. BF.EXISTS bloom:tx:{transaction_id}
│   │      ├── "definitely not" → continue
│   │      └── "maybe" → Redis GET idem:{transaction_id}
│   │            ├── exists → return cached response
│   │            └── not exists → false positive, continue
│   │
│   ├── 2. Fraud check: velocity (Redis ZRANGEBYSCORE)
│   │
│   ├── 3. BF.ADD bloom:tx:{transaction_id}
│   │      Redis SET idem:{transaction_id} = {status: accepted} EX 86400
│   │
│   ├── 4. Call Stripe: POST /v1/checkout/sessions (or /v1/payment_intents)
│   │      with Idempotency-Key: {merchant_id}:{transaction_id}
│   │      → get session_id + checkout_url (or payment_intent_id + client_secret)
│   │
│   ├── 5. Kafka produce → topic: payment.events, key: merchant_id
│   │
│   └── 6. Return 202 {transaction_id, checkout_url|client_secret, status: "accepted"}
│
│   ═══════════════ ASYNC ═══════════════
│
├── consumer/payment_consumer.go:
│   ├── 7. Poll 500 messages
│   ├── 8. Batch INSERT INTO orders
│   └── 9. On failure: insert one-by-one, errors → dead letter topic
│
├── POST /webhook/stripe (Stripe calls us):
│   ├── 10. Verify Stripe-Signature header: HMAC-SHA256(timestamp + "." + payload, webhook_secret)
│   │       Reject if timestamp drift > 5 min (replay protection)
│   ├── 11. Double check: GET /v1/payment_intents/{id} (or /v1/checkout/sessions/{id})
│   ├── 12. Idempotent check: Redis SETNX webhook:{event_id}
│   ├── 13. Switch on event.type:
│   │        - payment_intent.succeeded / checkout.session.completed → status='paid'
│   │        - payment_intent.payment_failed → status='failed'
│   │        - charge.refunded → status='refunded'
│   ├── 14. Update orders SET status=...
│   ├── 15. Kafka produce → topic: payment.confirmed
│   └── 16. Return HTTP 200 (MUST be < 5 seconds; Stripe retries on non-2xx)
│
├── consumer/settlement_consumer.go:
│   ├── 17. Webhook to merchant callback_url (with HMAC-SHA256 signed body)
│   └── 18. Log to analytics
│
└── service/reconciliation.go (cron every 5 min):
    ├── SELECT orders WHERE status='pending' AND created_at < NOW() - 10min
    ├── GET /v1/payment_intents/{id} from Stripe
    └── If status='succeeded' → force confirm
```

### Flow 2: Blockchain Payment (MetaMask)

```
POST /api/payments?method=crypto
│
├── Same validation as Flow 1 (steps 1-3)
├── Generate payment data: contract address + order_id
├── Return checkout page info (FE handles MetaMask connection)
│
│   ═══════════════ ON-CHAIN ═══════════════
│   User calls smart contract pay(order_id) directly via MetaMask
│   ═══════════════════════════════════════════
│
├── provider/blockchain/subscriber.go:
│   ├── WebSocket: SubscribeFilterLogs for PaymentReceived event
│   ├── Parse log: orderId (topic[1]), payer (topic[2]), token+amount (data)
│   ├── Dedup: BF.EXISTS + DB check on tx_hash
│   ├── Save to pending_txs table, status='pending'
│   └── Advance block cursor
│
├── provider/blockchain/confirmation.go (runs every block_time):
│   ├── Get latest block number
│   ├── For each pending tx: confirmations = latest - tx_block
│   ├── Detect reorg: TransactionReceipt == null → mark reorged
│   ├── Enough confirmations → validate off-chain:
│   │     amount match, token match, chain_id match, order not expired
│   ├── Update status → confirmed
│   └── Kafka produce → payment.confirmed
│
├── 4-layer defense against missed events:
│   ├── Layer 1: Block cursor (resume from last processed block on restart)
│   ├── Layer 2: Backfill scanner every 5 min (eth_getLogs HTTP)
│   ├── Layer 3: Reconciliation every 1 hour (query smart contract state)
│   └── Layer 4: Alert if order stuck > 30 min
```

---

## Database Schema

### MySQL (sharded by merchant_id % 8)

```sql
CREATE TABLE orders (
    id              BIGINT AUTO_INCREMENT PRIMARY KEY,
    order_id        VARCHAR(64) UNIQUE NOT NULL,
    merchant_id     VARCHAR(64) NOT NULL,
    transaction_id  VARCHAR(64) UNIQUE NOT NULL,  -- merchant's transaction ID
    amount          BIGINT NOT NULL,               -- in smallest currency unit (cents for USD, etc.)
    currency        VARCHAR(3) DEFAULT 'USD',      -- ISO 4217
    status          ENUM('created','pending','paid','failed','expired','refunded') NOT NULL,
    payment_method  VARCHAR(30),                   -- card, link, us_bank_account, sepa_debit, klarna, afterpay, crypto_eth
    stripe_session_id        VARCHAR(255),         -- Checkout Session ID (cs_...)
    stripe_payment_intent_id VARCHAR(255),         -- PaymentIntent ID (pi_...)
    stripe_charge_id         VARCHAR(255),         -- Charge ID (ch_...)
    checkout_url    VARCHAR(1024),                 -- Stripe-hosted checkout URL
    callback_url    VARCHAR(512),                  -- merchant webhook URL
    created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_merchant_status (merchant_id, status),
    INDEX idx_status_created (status, created_at),
    INDEX idx_stripe_pi (stripe_payment_intent_id)
) ENGINE=InnoDB;

CREATE TABLE pending_txs (
    id               BIGINT AUTO_INCREMENT PRIMARY KEY,
    tx_hash          VARCHAR(66) UNIQUE NOT NULL,
    block_number     BIGINT NOT NULL,
    order_id         VARCHAR(64) NOT NULL,
    payer            VARCHAR(42),
    token            VARCHAR(42),
    amount           DECIMAL(78,0),
    chain_id         BIGINT NOT NULL,
    confirmations    BIGINT DEFAULT 0,
    required_confirm BIGINT NOT NULL,
    status           ENUM('pending','confirmed','failed','reorged') DEFAULT 'pending',
    created_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_chain_status (chain_id, status)
) ENGINE=InnoDB;

CREATE TABLE merchants (
    id          BIGINT AUTO_INCREMENT PRIMARY KEY,
    merchant_id VARCHAR(64) UNIQUE NOT NULL,
    name        VARCHAR(255) NOT NULL,
    api_key     VARCHAR(128) UNIQUE NOT NULL,
    secret_key  VARCHAR(128) NOT NULL,
    callback_url VARCHAR(512),
    rate_limit  INT DEFAULT 1000,              -- requests per minute
    status      ENUM('active','suspended') DEFAULT 'active',
    created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB;

CREATE TABLE block_cursors (
    chain_id    BIGINT PRIMARY KEY,
    last_block  BIGINT NOT NULL,
    updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB;
```

### Redis Key Patterns

```
idem:{merchant_id}:{transaction_id}   → cached response JSON     (TTL 24h)
bloom:tx:{shard}                       → Bloom filter per shard   (no TTL)
rate:{merchant_id}                     → sorted set, timestamps   (TTL 60s)
lock:{user_id}:{method}               → distributed lock         (TTL 30s)
webhook:{stripe_event_id}             → "1"                      (TTL 24h)
cursor:{chain_id}                      → last_block number        (no TTL)
```

---

## API Endpoints

### Public (merchant-facing)

```
POST   /api/payments              # Create payment (returns checkout_url or crypto checkout)
GET    /api/payments/:id          # Poll payment status
POST   /api/payments/:id/refund   # Request refund (Stripe Refunds API)
```

### Webhook (provider callbacks)

```
POST   /webhook/stripe            # Stripe event callback
```

### Internal

```
GET    /healthz                   # Liveness probe (always 200 if process alive)
GET    /readyz                    # Readiness probe (200 if DB + Redis + Kafka connected)
GET    /metrics                   # Prometheus metrics
```

### Request/Response Examples

**POST /api/payments**
```json
// Request
{
  "transaction_id": "TXN-20260429-001",
  "amount": 1500,
  "currency": "USD",
  "payment_method_types": ["card", "link", "us_bank_account"],
  "customer_email": "user@example.com",
  "success_url": "https://merchant.com/success",
  "cancel_url": "https://merchant.com/cancel",
  "callback_url": "https://merchant.com/webhook"
}
// Headers: X-API-Key, X-Timestamp, X-Signature (HMAC-SHA256)

// Response 202
{
  "order_id": "ORD-abc123",
  "transaction_id": "TXN-20260429-001",
  "stripe_session_id": "cs_test_a1b2c3...",
  "checkout_url": "https://checkout.stripe.com/c/pay/cs_test_a1b2c3...",
  "stripe_payment_intent_id": "pi_3OxYz...",
  "client_secret": "pi_3OxYz..._secret_..."
  ,"status": "accepted"
}
```

**POST /webhook/stripe**
```
// Headers from Stripe:
//   Stripe-Signature: t=1714377600,v1=5257a869e7ec...,v0=...
//
// Body (example: payment_intent.succeeded):
{
  "id": "evt_1Oxyz...",
  "object": "event",
  "type": "payment_intent.succeeded",
  "data": {
    "object": {
      "id": "pi_3OxYz...",
      "amount": 1500,
      "currency": "usd",
      "status": "succeeded",
      "metadata": { "order_id": "ORD-abc123" }
    }
  },
  "created": 1714377600
}
// We MUST return HTTP 2xx within 5 seconds, else Stripe retries up to 3 days.
```

---

## Implementation Rules

### General

- Use `context.Context` everywhere. Pass it from handler → service → repository.
- Use `slog` for structured logging. Every log line includes: `request_id`, `merchant_id`, `order_id`.
- All monetary amounts are `int64` in smallest currency unit (cents for USD/EUR, no subunit for JPY/IDR).
- All timestamps are UTC.
- Error handling: wrap errors with `fmt.Errorf("operation: %w", err)`. Never swallow errors silently.
- Use interfaces for all external dependencies (DB, Redis, Kafka, Stripe). Enables testing.

### Security

- HMAC-SHA256 verification on every merchant request. Reject if timestamp > 5 minutes old.
- Stripe webhook: verify `Stripe-Signature` header (timestamp + HMAC-SHA256 of `t.payload`), THEN call `GET /v1/payment_intents/{id}` to double-check. NEVER trust webhook body alone.
- Reject Stripe webhooks where `timestamp` drift > 5 min (replay protection — Stripe's recommended tolerance).
- Use Stripe's `Idempotency-Key` header on every state-mutating Stripe API call (key = `{merchant_id}:{transaction_id}`).
- Secrets loaded from environment variables, never hardcoded.
- Merchant `secret_key` stored hashed (bcrypt) in DB, plaintext only in env/secrets manager.

### Idempotency

- Every write endpoint is idempotent.
- Key = `{merchant_id}:{transaction_id}` — used for both our Redis cache AND Stripe's `Idempotency-Key` header.
- Check: Bloom filter first → Redis GET → DB query (in that order, short-circuit on hit).
- BF.ADD + Redis SET happen BEFORE Kafka produce. This prevents duplicates during the async gap.
- Webhook idempotency: dedupe on Stripe `event.id` (guaranteed unique by Stripe).

### Kafka

- Topic `payment.events`: partitioned by `merchant_id` (ordering per merchant).
- Topic `payment.confirmed`: partitioned by `order_id`.
- Producer: `acks=all`, `retries=3`, `linger.ms=5`, `batch.size=64KB`.
- Consumer: manual offset commit AFTER successful batch insert.
- Dead letter topic: `payment.events.dlq` for messages that fail after 3 retries.

### MySQL

- Use `database/sql` with connection pooling: `MaxOpenConns=100`, `MaxIdleConns=25`.
- All payment state changes use transactions with `SELECT ... FOR UPDATE` to prevent double-spending.
- Batch insert: `INSERT INTO orders (...) VALUES (...), (...), ...` — 500 rows per batch.
- On batch failure: fallback to individual inserts, failed rows go to dead letter.

### Stripe

- Use the official Go SDK: `github.com/stripe/stripe-go/v76`.
- Pass `order_id` and `merchant_id` in PaymentIntent / Checkout `metadata` so webhook handlers can correlate without a DB lookup.
- For card payments default to PaymentIntents + Payment Element. For redirect-heavy methods (Klarna, Afterpay) use Checkout Sessions.
- Refunds: `POST /v1/refunds` with `payment_intent` + `amount` + idempotency key `refund:{order_id}:{request_id}`.
- Map Stripe errors: `card_declined` → 402, `rate_limit` → 429, `api_error` → 502, network/timeout → 504 (retryable).

### Blockchain

- Use go-ethereum `ethclient` for RPC calls.
- WebSocket for real-time event subscription, HTTP for backfill queries.
- Never hardcode chain-specific values. Use `ChainConfig` struct per chain.
- Block cursor: persist AFTER event is saved to DB (at-least-once delivery).
- 4-layer defense: subscriber → backfill scanner → reconciliation → alerts. All layers are idempotent.

---

## Running Locally

```bash
# Start dependencies
docker-compose up -d   # MySQL, Redis, Kafka (KRaft mode)

# Run migrations
make migrate

# Forward Stripe webhooks to local server (requires `stripe` CLI)
stripe listen --forward-to localhost:8080/webhook/stripe

# Start server
make run
# or: go run cmd/server/main.go

# Run tests
make test               # unit tests
make test-integration   # integration tests (needs Docker)
```

### Environment Variables

```env
# App
APP_ENV=development
APP_PORT=8080
LOG_LEVEL=debug

# MySQL
DB_DSN=root:password@tcp(localhost:3306)/payments?parseTime=true
DB_MAX_OPEN_CONNS=100

# Redis
REDIS_ADDR=localhost:6379
REDIS_PASSWORD=
REDIS_DB=0

# Kafka
KAFKA_BROKERS=localhost:9092
KAFKA_TOPIC_EVENTS=payment.events
KAFKA_TOPIC_CONFIRMED=payment.confirmed
KAFKA_CONSUMER_GROUP=payment-engine

# Stripe
STRIPE_SECRET_KEY=sk_test_xxx
STRIPE_PUBLISHABLE_KEY=pk_test_xxx
STRIPE_WEBHOOK_SECRET=whsec_xxx
STRIPE_API_VERSION=2024-06-20
STRIPE_DEFAULT_CURRENCY=USD

# Blockchain
ETH_RPC_WS=wss://eth-sepolia.g.alchemy.com/v2/YOUR_KEY
ETH_RPC_HTTP=https://eth-sepolia.g.alchemy.com/v2/YOUR_KEY
ETH_CONTRACT_ADDRESS=0x1234...
ETH_REQUIRED_CONFIRMATIONS=12
ETH_START_BLOCK=19000000

# Security
HMAC_SECRET=your-hmac-secret
```

---

## Testing Strategy

### Unit Tests

- Every service and repository has unit tests.
- External dependencies mocked via interfaces.
- Run: `go test ./internal/... -v -race`

### Integration Tests (testcontainers)

Use real MySQL + Redis + Kafka in Docker containers.

| Test | What it validates |
|------|-------------------|
| TestCreateOrder_HappyPath | Full Phase 1 + 3 flow |
| TestIdempotency_DuplicateOrder | Redis Bloom + SETNX dedup |
| TestDoubleSpending_ConcurrentConfirm | SELECT FOR UPDATE under concurrency |
| TestStripeWebhookSignatureVerification | Stripe-Signature verify + tampered data rejection + replay window |
| TestStripeWebhookIdempotency | 3 identical events (same event.id) → process only 1 |
| TestStripeEventRouting | succeeded / failed / refunded → correct status transitions |
| TestReconciliationCron | Missed webhook → cron recovery via PaymentIntent retrieval |
| TestBlockchainTxHashDedup | UNIQUE constraint on tx_hash |
| TestStripeClient_* (mock server) | Error handling: 400, 402 card_declined, 429, 5xx, timeout, malformed JSON |
| TestStripeIdempotencyKey | Same key → Stripe returns identical response |
| TestFullPaymentFlow_E2E | Entire flow with real DB + Redis + mock Stripe |

Run: `go test ./integration_test/... -v -race -count=1 -timeout 120s`

---

## Implementation Order

Build in this order. Each phase is independently testable.

### Phase 1: Foundation
1. `cmd/server/main.go` — Fiber server, graceful shutdown
2. `internal/config/` — env var loading
3. `internal/domain/` — all entities
4. `internal/api/router.go` — route registration
5. `internal/api/handler/health.go` — health endpoints
6. `docker-compose.yml` — MySQL + Redis + Kafka
7. `migrations/` — all SQL files

### Phase 2: Core Payment Flow
8. `internal/repository/order_repo.go` — MySQL CRUD
9. `internal/repository/merchant_repo.go` — merchant lookup
10. `internal/cache/idempotency.go` — Bloom filter + Redis SETNX
11. `internal/cache/ratelimiter.go` — sliding window
12. `internal/api/middleware/auth.go` — HMAC verification
13. `internal/api/middleware/ratelimit.go`
14. `internal/kafka/producer.go`
15. `internal/service/payment_service.go` — orchestration
16. `internal/api/handler/payment.go` — POST /api/payments

### Phase 3: Async Processing
17. `internal/kafka/consumer.go` — base consumer
18. `internal/consumer/payment_consumer.go` — batch insert
19. `internal/api/handler/payment_status.go` — GET status

### Phase 4: Stripe Integration
20. `internal/provider/stripe/client.go` — PaymentIntent + Checkout Session + Refund + GET status
21. `internal/provider/stripe/signature.go` — Stripe-Signature (HMAC-SHA256) verify
22. `internal/service/webhook_service.go`
23. `internal/api/handler/webhook.go` — POST /webhook/stripe
24. `internal/consumer/settlement_consumer.go` — merchant webhook

### Phase 5: Blockchain
25. `internal/repository/pending_tx_repo.go`
26. `internal/provider/blockchain/subscriber.go`
27. `internal/provider/blockchain/confirmation.go`
28. `internal/provider/blockchain/cursor.go`
29. `internal/provider/blockchain/backfill.go`
30. `internal/provider/blockchain/reconciliation.go`
31. `internal/provider/blockchain/listener.go` — wire all together

### Phase 6: Safety Nets
32. `internal/service/reconciliation.go` — cron poll pending orders
33. `internal/service/fraud_service.go` — velocity + risk score

### Phase 7: Integration Tests
34. `integration_test/testenv.go`
35. `All test files`

---

## Key Interfaces

These MUST be defined as interfaces for testability:

```go
type OrderRepository interface {
    Create(ctx context.Context, order *Order) error
    GetByOrderID(ctx context.Context, orderID string) (*Order, error)
    UpdateStatus(ctx context.Context, orderID string, status OrderStatus, stripePaymentIntentID string) error
    BatchCreate(ctx context.Context, orders []*Order) error
    GetPendingBefore(ctx context.Context, before time.Time) ([]*Order, error)
}

type IdempotencyChecker interface {
    Check(ctx context.Context, key string) (exists bool, cachedResponse []byte, err error)
    Set(ctx context.Context, key string, response []byte, ttl time.Duration) error
}

type StripeClient interface {
    CreateCheckoutSession(ctx context.Context, req CreateCheckoutRequest, idempotencyKey string) (*CheckoutSession, error)
    CreatePaymentIntent(ctx context.Context, req CreatePaymentIntentRequest, idempotencyKey string) (*PaymentIntent, error)
    GetPaymentIntent(ctx context.Context, id string) (*PaymentIntent, error)
    GetCheckoutSession(ctx context.Context, id string) (*CheckoutSession, error)
    CreateRefund(ctx context.Context, req CreateRefundRequest, idempotencyKey string) (*Refund, error)
    VerifyWebhookSignature(payload []byte, sigHeader, secret string) (*Event, error)
}

type EventPublisher interface {
    PublishPaymentEvent(ctx context.Context, event PaymentEvent) error
    PublishPaymentConfirmed(ctx context.Context, event PaymentConfirmedEvent) error
}

type FraudChecker interface {
    Check(ctx context.Context, merchantID string, userID string, amount int64) (score int, err error)
}
```

---

## Commit Convention

```
feat(payment): add Stripe Checkout Session creation endpoint
fix(webhook): handle out-of-order payment_intent.succeeded after charge.refunded
perf(redis): batch idempotency checks to reduce round-trips
test(e2e): add full payment flow integration test
chore(docker): add Kafka to docker-compose
```

Types: feat, fix, refactor, perf, test, docs, chore, ci  
Scope: payment, webhook, blockchain, kafka, redis, mysql, api, config  
Always commit changes using git with descriptive conventional commit messages.
