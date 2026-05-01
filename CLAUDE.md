# CLAUDE.md — Payment Gateway (Monolith)

Global payment gateway. Supports **traditional payments** (cards, wallets,
bank transfers, BNPL via Stripe) and **blockchain payments** (MetaMask →
smart contract). Single Go binary with internal packages — NOT
microservices. All cross-package calls are in-process. Kafka is for async
fan-out only, never inter-service RPC.

For setup, env vars, and full DB schema see `README.md`, `.env.example`, and
`migrations/`. This file is the architectural source of truth — keep it
under ~300 lines.

## Tech Stack

| Layer | Tech |
|---|---|
| Language / framework | Go 1.25+, Fiber, `database/sql`, `slog` |
| Datastore | MySQL 8 (sharded by `merchant_id`), Redis 7 (RedisStack — Bloom) |
| Async bus | Kafka (KRaft) — fan-out only, not source of truth |
| Payments | Stripe (PaymentIntents + Checkout Sessions), go-ethereum |
| Test | testcontainers-go (real MySQL + Redis + Kafka) |

---

## Core Flow — Stripe payment (95% of traffic)

```
POST /api/payments
│
├── middleware: HMAC-SHA256(payload, merchant.secret_key) + rate limit
│
├── service.payment_service.Create:
│   ├── 1. Idempotency: BF.EXISTS bloom:tx → Redis GET idem:* → return cached
│   ├── 2. Fraud check: velocity (Redis ZRANGEBYSCORE)
│   ├── 3. (Eager mode) Stripe CreateCheckoutSession with Idempotency-Key
│   │       — Lazy mode skips this; resolver creates the session on first
│   │         hit of /pay/:id and UPDATEs the row.
│   ├── 4. INSERT INTO orders SYNC (status='pending', stripe artefacts if eager)
│   │       — MySQL is source of truth; no async batch insert.
│   ├── 5. BF.ADD + Redis SET idem:* (cached response)
│   ├── 6. Kafka produce → payment.events (FAN-OUT only, not write path)
│   └── 7. Return 202 + checkout_url
│
├── POST /webhook/stripe (Stripe → us):
│   ├── Verify Stripe-Signature (HMAC-SHA256, t.payload), reject drift > 5min
│   ├── Double-check: GET /v1/payment_intents/{id}
│   ├── Dedupe on Stripe event.id (Redis SETNX)
│   ├── Map event.type → status: succeeded→paid, failed→failed, refunded→refunded
│   ├── UPDATE orders, Kafka produce → payment.confirmed
│   └── Return 200 within 5s (Stripe retries 3 days otherwise)
│
├── consumer/settlement_consumer.go:
│   └── payment.confirmed → POST merchant callback_url (HMAC-signed)
│
└── service/reconciliation.go (cron 5m):
    └── SELECT orders WHERE status='pending' AND created_at < NOW() - 10min
        → GET /v1/payment_intents/{id} → force-confirm if Stripe says succeeded
```

**Why sync write (not async batch insert)?** Earlier design queued the row
via Kafka and a `payment_consumer` did the insert. Lazy resolver could win
the race vs the consumer, create a Stripe session, no-op its UpdateCheckout
against a missing row, then on next click create a duplicate session. Sync
write closes that hole; the few-ms cost is dwarfed by the Stripe call.

## Core Flow — Blockchain payment

```
POST /api/payments?method=crypto → service returns {contract, order_id, chain}
                                   row inserted sync, status='pending'
User invokes contract.pay(order_id) via MetaMask

provider/blockchain/subscriber.go (WebSocket):
  PaymentReceived event → BF.EXISTS + DB tx_hash check → save pending_txs

provider/blockchain/confirmation.go (per block):
  confirmations = latest - tx_block; reorg detect via TransactionReceipt
  enough confirmations + off-chain validation → status='confirmed'
  Kafka produce → payment.confirmed

4-layer defense (all idempotent):
  block cursor → backfill scanner (5m) → reconciliation (1h) → alert (>30m stuck)
```

---

## Implementation Rules

### General
- `context.Context` first arg on every I/O function. Pass handler → service → repo.
- `slog` structured logging. Every line has `request_id`, `merchant_id`, `order_id` where available.
- Money is `int64` smallest unit (cents for USD/EUR, no subunit for JPY/IDR).
- Timestamps UTC, `time.Time` (never strings).
- Wrap errors with context: `fmt.Errorf("operation: %w", err)`. Never swallow.
- Cross-package consumers depend on **interfaces**, never concrete structs. See `.claude/rules/dependency-injection.md`.

### Security
- HMAC-SHA256 on every merchant request, reject timestamp drift > 5min.
- Stripe webhook: signature verify THEN `GET /v1/payment_intents/{id}` double-check. Never trust the body alone.
- Stripe `Idempotency-Key` on every state-mutating call (key = `{merchant_id}:{transaction_id}`).
- Secrets from env only. Merchant `secret_key` bcrypt-hashed in DB; plaintext lives in env/secrets manager.

### Idempotency
- Every write endpoint idempotent.
- Single key: `{merchant_id}:{transaction_id}` — used for both Redis cache AND Stripe `Idempotency-Key`.
- Order: Bloom filter → Redis GET → DB unique constraint (safety net).
- Webhook dedupe on Stripe `event.id`.

### Kafka
- `payment.events`: partition by `merchant_id` (per-merchant ordering).
- `payment.confirmed`: partition by `order_id`.
- Producer: `acks=all`, `retries=3`, `linger.ms=5`, `batch.size=64KB`.
- Consumer: manual offset commit AFTER successful handle.
- DLQ: `payment.events.dlq` after 3 retries.

### MySQL
- Pool: `MaxOpenConns=100`, `MaxIdleConns=25`.
- All status transitions in tx with `SELECT ... FOR UPDATE`.
- Parameterized queries always. No `fmt.Sprintf` for SQL.

### Stripe
- SDK: `github.com/stripe/stripe-go/v76`.
- Pass `order_id` + `merchant_id` in PaymentIntent / Checkout `metadata` for webhook correlation.
- Cards default to PaymentIntents + Payment Element. Redirect-heavy methods (Klarna, Afterpay) use Checkout Sessions.
- Refunds: `POST /v1/refunds` with idem key `refund:{order_id}:{request_id}`.
- Error mapping: `card_declined`→402, `rate_limit`→429, `api_error`→502, network/timeout→504 (retryable).

### Blockchain
- `ethclient` for RPC. WebSocket for subs, HTTP for backfill.
- `ChainConfig` per chain; never hardcode chain values.
- Block cursor persists AFTER event saved (at-least-once).
- 4-layer idempotent defense (subscriber → backfill → reconcile → alert).

---

## Project Layout (top-level)

```
cmd/server/             entry point
internal/
  api/                  Fiber router + handlers + middleware
  service/              business logic (payment, webhook, reconciliation, checkout_resolver)
  repository/           MySQL CRUD
  cache/                Redis: idempotency, ratelimit, lock, token_bucket, url_cache
  kafka/                producer + base consumer
  consumer/             settlement consumer (only one — payment_consumer was retired)
  provider/{stripe,blockchain}/
  domain/               entities + status enum
  config/               env loading
  mocks/<pkg>/          gomock-generated mocks (regen: make mocks)
pkg/                    shared utilities (hmac, response, logger, checkouttoken)
migrations/             SQL up/down
integration_test/       testcontainers-go suites
```

Key interfaces (defined as ports in their owning package, see source for exact signatures): `OrderRepository`, `IdempotencyChecker`, `StripeClient`, `EventPublisher`, `FraudChecker`, `Checkouts`, `Locker`, `TokenBucket`, `URLCache`.

---

## Commit Convention

```
feat(payment): add Stripe Checkout Session creation endpoint
fix(webhook): handle out-of-order payment_intent.succeeded after charge.refunded
perf(redis): batch idempotency checks
test(e2e): add full payment flow integration test
```

Types: `feat`, `fix`, `refactor`, `perf`, `test`, `docs`, `chore`, `ci`
Scope: `payment`, `webhook`, `blockchain`, `kafka`, `redis`, `mysql`, `api`, `config`

**Author:** commit as configured `git config user.name` / `user.email`. Do **not** append a `Co-Authored-By: Claude …` trailer — repo history has none, keep it that way.
