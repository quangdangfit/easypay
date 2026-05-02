# CLAUDE.md — Payment Gateway (Monolith)

Global payment gateway. Supports **traditional payments** (cards, wallets,
bank transfers, BNPL via Stripe) and **blockchain payments** (MetaMask →
smart contract). Single Go binary with internal packages — NOT
microservices. All cross-package calls are in-process. Kafka carries
terminal-state notifications only (`payment.confirmed`); the write path
is sync MySQL.

For setup, env vars, and full DB schema see `README.md`, `.env.example`, and
`migrations/`. This file is the architectural source of truth — keep it
under ~300 lines.

## Tech Stack

| Layer | Tech |
|---|---|
| Language / framework | Go 1.25+, Fiber, `database/sql`, `slog` |
| Datastore | MySQL 8 (single `transactions` table, logical shard via `merchants.shard_index`), Redis 7 |
| Async bus | Kafka (KRaft) — settlement callback notifications only |
| Payments | Stripe (PaymentIntents + Checkout Sessions), go-ethereum |
| Test | testcontainers-go (real MySQL + Redis + Kafka) |

---

## Core Flow — Stripe payment (95% of traffic)

```
POST /api/payments {order_id, amount, currency, ...}
│
├── middleware: HMAC-SHA256(payload, merchant.secret_key) + rate limit
│
├── service.payment_service.Create:
│   ├── 1. transaction_id = sha256(merchant_id || ":" || order_id)[:16]   (32 hex chars)
│   ├── 2. INSERT INTO transactions (...) VALUES (...)  — sync
│   │   ├── 1062 (UNIQUE on (merchant_id, transaction_id)) → SELECT existing → return cached response
│   │   └── OK → continue
│   ├── 3. (Eager) Stripe.CreateCheckoutSession(idempotency_key=transaction_id_hex)
│   │           → UPDATE row SET stripe_session, stripe_pi_id
│   │   (Lazy)  → build /pay/:merchant_id/:order_id?t=<token> URL, no Stripe call
│   └── 4. Return 201 + {order_id, transaction_id, checkout_url, status}
│
├── POST /webhook/stripe (Stripe → us):
│   ├── Verify Stripe-Signature (HMAC-SHA256, t.payload), reject drift > 5min
│   ├── Dedupe on Stripe event.id (Redis SETNX)
│   ├── Cross-check: GET /v1/payment_intents/{id}
│   ├── Map event.type → status: succeeded→paid, failed→failed, refunded→refunded
│   ├── UPDATE transactions WHERE merchant_id=? AND order_id=?  (metadata carries both)
│   │   — UPDATE 0 rows is a hard 5xx (Stripe retries; ops alerts)
│   └── Kafka produce → payment.confirmed
│
├── consumer/settlement_consumer.go:
│   └── payment.confirmed → look up merchants.callback_url + secret →
│       POST merchant callback (HMAC-signed). Event itself doesn't carry the URL —
│       merchants can rotate without re-publishing.
│
└── service/reconciliation.go (cron 5m):
    └── SELECT FROM transactions WHERE status IN ('created','pending') AND created_at < NOW()-10min
        → GET /v1/payment_intents/{id} → force-confirm if Stripe says succeeded
```

**Why sync write (no async batch insert, no Redis idempotency)?** The MySQL
UNIQUE on `(merchant_id, transaction_id)` is the single dedupe layer. Two
concurrent Create calls with the same `order_id` derive the same
`transaction_id`; one wins INSERT, the other gets 1062, falls back to
GetByTransactionID, returns the winner's row. No Bloom filter, no
`idem:*` Redis cache, no pending-order Redis snapshot, no
`payment_consumer` — strictly fewer moving parts, strictly stronger
consistency. Trade-off: write latency ≈ 15–25ms (vs 5ms Kafka publish);
absorbed by the Stripe call latency in eager mode.

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
- Every write endpoint idempotent. **Single layer: MySQL UNIQUE.**
- Merchant supplies `order_id` (VARCHAR(64), `[A-Za-z0-9._:-]`); service
  derives `transaction_id = sha256(merchant_id || ":" || order_id)[:16]`
  (32 hex chars). Two retries with the same input collapse onto the same
  row via the UNIQUE on `(merchant_id, transaction_id)`.
- Stripe `Idempotency-Key = transaction_id` hex (32 chars).
- Webhook dedupe on Stripe `event.id` (Redis SETNX, 24h TTL).

### Kafka
- `payment.confirmed`: partition by `order_id` (only topic published from this app).
- Producer: `acks=all`, `retries=3`, `linger.ms=5`, `batch.size=64KB`.
- Consumer: manual offset commit AFTER successful handle.
- DLQ: `payment.confirmed.dlq` after 3 retries.

### MySQL — single `transactions` table, logical shard
- One physical table `transactions`, PK `(merchant_id, transaction_id)`,
  unique `(merchant_id, order_id)`, indexes on `(status, created_at)`
  and `stripe_pi_id`.
- Schema mixes fixed-width binary with VARCHAR for the merchant-supplied
  `order_id`: `merchant_id VARBINARY(16)`, `transaction_id BINARY(16)`,
  `order_id VARCHAR(64)`, `currency_code SMALLINT` (ISO 4217 numeric),
  `status TINYINT`, `payment_method TINYINT`. Stripe artefacts are
  `VARBINARY`. No `checkout_url` column — reconstructed from
  `stripe_session_id` (`https://checkout.stripe.com/c/pay/{id}`) or signed
  lazy-token URL on read.
- `merchants.shard_index TINYINT` carries the merchant's logical shard
  (`[0, LOGICAL_SHARD_COUNT)`, default 16). Picked at create time by
  least-loaded selection. Today every shard lives on the same physical
  table; future deployments can use this column to partition without
  changing application code.
- Pool: `MaxOpenConns=100`, `MaxIdleConns=25`.
- All status transitions are single-statement UPDATEs keyed on
  `(merchant_id, order_id)`. Parameterised queries always.

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
  repository/           MySQL CRUD — order repo + merchant + onchain_tx + block_cursor
  cache/                Redis: ratelimit, lock, token_bucket, url_cache
  kafka/                producer (PaymentConfirmedEvent only) + base consumer
  consumer/             settlement consumer (the only one)
  provider/{stripe,blockchain}/
  domain/               entities + status enum + order_id helpers
  config/               env loading
  mocks/<pkg>/          gomock-generated mocks (regen: make mocks)
pkg/                    shared utilities (hmac, response, logger, checkouttoken)
migrations/             SQL up/down (single `001_init_schema`)
integration_test/       testcontainers-go suites
```

Key interfaces (defined as ports in their owning package, see source for exact signatures): `OrderRepository`, `MerchantRepository`, `OnchainTxRepository`, `StripeClient`, `EventPublisher`, `FraudChecker`, `Payments`, `Webhooks`, `Checkouts`, `Merchants`, `Locker`, `TokenBucket`, `URLCache`.

### Admin

`POST /admin/merchants` (header `X-Admin-Key: $ADMIN_API_KEY`) creates a
merchant, generates an api_key/secret_key pair, and assigns the
least-loaded `shard_index`. Mounted only when `ADMIN_API_KEY` is set.

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
