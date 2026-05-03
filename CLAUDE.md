# CLAUDE.md — Payment Gateway

Global payment gateway. Supports **traditional payments** (cards, wallets,
bank transfers, BNPL via Stripe) and **blockchain payments** (MetaMask →
smart contract). Single Go binary with internal packages — NOT
microservices. All cross-package calls are in-process. Kafka carries
terminal-state notifications only (`payment.confirmed`); the write path
is sync MySQL.

For setup, configuration, and full DB schema see `README.md`,
`config.yaml.example`, and `migrations/`. This file is the architectural
source of truth — keep it under ~300 lines.

## Tech Stack

| Layer | Tech |
|---|---|
| Language / framework | Go 1.25+, Fiber, `database/sql`, `slog` |
| Datastore | MySQL 8 (sharded `transactions`; control-plane pool for `merchants`, `onchain_transactions`, `block_cursors`), Redis 7 |
| Async bus | Kafka (KRaft) — settlement callback notifications only |
| Payments | Stripe (Checkout Sessions) with `live`/`fake` modes + circuit breaker; go-ethereum |
| Test | testcontainers-go (real MySQL + Redis + Kafka); `gomock` for unit tests |

---

## Core Flow — Stripe payment (95% of traffic)

```
POST /api/payments {order_id, amount, currency, ...}
│
├── middleware: HMAC-SHA256(timestamp + "." + body, merchant.secret_key)
│              + Redis fixed-window rate limit
│
├── service.payment_service.Create:
│   ├── 1. transaction_id = sha256(merchant_id || ":" || order_id)[:16]   (32 hex chars)
│   ├── 2. INSERT INTO transactions (...) VALUES (...)  — sync, routed via merchant.shard_index
│   │   ├── 1062 (UNIQUE on (merchant_id, transaction_id)) → GetByTransactionID →
│   │   │       reconstruct response. Conflict (different amount/currency/method bucket) → 409.
│   │   └── OK → continue
│   ├── 3. (Eager — default) Stripe.CreateCheckoutSession(idempotency_key=transaction_id)
│   │           → UPDATE row SET stripe_session, stripe_pi_id
│   │   (Lazy)  → return /pay/:merchant_id/:order_id?t=<signed_token> URL, no Stripe call
│   └── 4. Return 202 + {order_id, transaction_id, checkout_url, status:"accepted"}
│
├── POST /webhook/stripe (Stripe → us):
│   ├── Verify Stripe-Signature (HMAC-SHA256, t=<ts>), reject drift > 5min
│   ├── Dedupe on Stripe event.id (Redis SETNX, 24h TTL)
│   ├── Cross-check: GET /v1/payment_intents/{id} — must be `succeeded`
│   ├── Resolve merchant.shard_index via cached merchant lookup
│   ├── UPDATE transactions WHERE merchant_id=? AND order_id=?  (metadata carries both)
│   │   — UPDATE 0 rows is a hard 5xx (Stripe retries; ops alerts)
│   └── Kafka produce → payment.confirmed (succeeded + refunded; failed updates row only)
│
├── consumer/settlement_consumer.go:
│   └── payment.confirmed → look up merchants.callback_url + secret →
│       POST merchant callback (HMAC-signed). Event itself doesn't carry the URL —
│       merchants can rotate either field without re-publishing.
│
└── service/reconciliation.go (cron 5m):
    └── scatter-gather every physical shard for status IN ('created','pending')
        AND created_at < now-10min → GET /v1/payment_intents/{id} →
        force-confirm `succeeded`, fail `canceled`/`requires_payment_method`.
```

**Why sync write?** The MySQL UNIQUE on `(merchant_id, transaction_id)`
is the single dedupe layer. Two concurrent Create calls with the same
`order_id` derive the same `transaction_id`; one wins INSERT, the other
gets 1062, falls back to GetByTransactionID, returns the winner's
response. No Bloom filter, no Redis idempotency cache, no pending-order
snapshot, no separate payment consumer — strictly fewer moving parts,
strictly stronger consistency. Trade-off: write latency ≈ 15–25ms
(absorbed by the Stripe call latency in eager mode; in lazy mode the
Stripe call moves to `/pay/:id` first-click).

## Core Flow — Blockchain payment

```
POST /api/payments?method=crypto → service returns {contract, order_id, chain}
                                   row inserted sync, payment_method='crypto_eth'
User invokes contract.pay(order_id) via MetaMask

provider/blockchain/subscriber.go (WebSocket):
  PaymentReceived event → onchain_transactions.tx_hash UNIQUE dedup →
                          INSERT pending_tx (status='pending')

provider/blockchain/confirmation.go (per block):
  confirmations = latest - tx_block; reorg detect via TransactionReceipt
  enough confirmations + amount cross-check → onchain_tx status='confirmed' AND
  orders.status='paid' → Kafka produce → payment.confirmed

4-layer defense (all idempotent):
  WebSocket subscriber (cursor) → backfill scanner (eth_getLogs) →
  confirmation tracker → reconciler (stuck-tx alert + receipt re-check)
```

---

## Implementation Rules

### General
- `context.Context` first arg on every I/O function. Pass handler → service → repo.
- `slog` structured logging. Every line has `request_id`, `merchant_id`, `order_id` where available.
- Money is `int64` smallest unit (cents for USD/EUR, no subunit for JPY/IDR).
- Timestamps UTC, `time.Time` (never strings). On disk we store `INT UNSIGNED` seconds.
- Wrap errors with context: `fmt.Errorf("operation: %w", err)`. Never swallow.
- Cross-package consumers depend on **interfaces**, never concrete structs. See `.claude/rules/dependency-injection.md`.

### Security
- HMAC-SHA256 on every merchant request. Signed material is
  `<X-Timestamp>.<raw_body>`; reject drift > `security.hmac_timestamp_skew` (default 5min).
- Stripe webhook: signature verify THEN `GET /v1/payment_intents/{id}` double-check. Never trust the body alone.
- Stripe `Idempotency-Key` on every state-mutating call:
  - Create payment: `transaction_id` (32 hex chars).
  - Lazy-checkout resolver: `checkout:{merchant_id}:{order_id}`.
  - Refund: caller-supplied or `refund:{order_id}`.
- Secrets from env/config only. Merchant `secret_key` is stored **plaintext**
  in the `merchants` table because the gateway needs to recompute HMACs on
  every request; protect it via DB-level encryption / minimal access.
  Generated as 32 random bytes hex at create time.
- Hosted-checkout URLs are signed with `app.checkout_token_secret`
  (`pkg/checkouttoken`); empty secret disables verification (dev only).

### Idempotency
- Every write endpoint idempotent. **Single layer: MySQL UNIQUE.**
- Merchant supplies `order_id` (VARCHAR(64), `[A-Za-z0-9._:-]`); service
  derives `transaction_id = sha256(merchant_id || ":" || order_id)[:16]`
  (32 hex chars). Two retries with the same input collapse onto the same
  row via the UNIQUE on `(merchant_id, transaction_id)`.
- A retry that materially differs (different amount, currency, or
  crypto-vs-Stripe bucket) returns 409 `transaction_conflict`.
- Webhook dedupe on Stripe `event.id` (Redis SETNX, 24h TTL).

### Kafka
- `payment.confirmed`: partition by `order_id` (only topic this app publishes).
- Producer (`segmentio/kafka-go`): `RequireAll`, `MaxAttempts=3`, batch 64KB.
- Consumer: manual offset commit AFTER successful handle.
- Batch handler with per-message fallback; failed messages go to
  `payment.confirmed.dlq` after fallback exhausts.

### MySQL — sharded `transactions`, control-plane for everything else
- `transactions` schema: `merchant_id VARBINARY(16)`, `transaction_id BINARY(16)`,
  `order_id VARCHAR(64)`, `amount BIGINT UNSIGNED`,
  `currency_code SMALLINT UNSIGNED` (ISO 4217 numeric),
  `status TINYINT UNSIGNED`, `payment_method TINYINT UNSIGNED`,
  Stripe artefacts as `VARBINARY`, timestamps `INT UNSIGNED` seconds.
  PK `(merchant_id, transaction_id)`, UNIQUE `(merchant_id, order_id)`,
  index `(status, created_at)` and `stripe_pi_id`. **No `checkout_url` column** —
  reconstructed at read time from `stripe_session_id`
  (`https://checkout.stripe.com/c/pay/{id}`) or signed lazy-token URL.
- `merchants.shard_index TINYINT` carries the merchant's logical shard
  (`[0, app.logical_shard_count)`, default 16). Picked at create time by
  least-loaded selection. The `ShardRouter` range-partitions logical →
  physical pool. `len(db.shards)` must divide `logical_shard_count`.
- **Control-plane pool** = physical pool 0. Owns `merchants`,
  `onchain_transactions`, and `block_cursors`. Schema for these tables is
  applied to every shard (branch-free migrations) but rows only exist on
  pool 0.
- `MerchantRepository` runs an in-process LRU+TTL cache (cap 1024, 5min)
  on api-key and merchant-id lookups, since they fire on every request.
- Pool defaults: `MaxOpenConns=100`, `MaxIdleConns=25` (per physical pool).
- All status transitions are single-statement UPDATEs keyed on
  `(merchant_id, order_id)`. Parameterised queries always.
- Global lookups (`GetByOrderIDAny`, `GetByPaymentIntentID`,
  `GetPendingBefore`) scatter-gather across every physical pool and stamp
  the matching pool's logical-shard index on the returned row so
  follow-up writes route deterministically.

### Stripe
- SDK: `github.com/stripe/stripe-go/v76`. `stripe.Mode` selects:
  - `live` — real Stripe SDK; requires `secret_key` + `webhook_secret`.
  - `fake` — in-process synthetic responses for load tests / local e2e.
- Always wrapped in a circuit breaker (`stripe.NewBreakerClient`) so a
  bad Stripe day fails open with `ErrCircuitOpen` instead of dragging
  the whole gateway down.
- Pass `order_id` + `merchant_id` in Checkout `metadata` for webhook correlation.
- The default flow uses Checkout Sessions (`mode=payment`); `success_url`
  is required by Stripe and falls back to
  `app.checkout_default_success_url` (default
  `<public_base_url>/checkout/success`).
- Refunds: `POST /v1/refunds` keyed on the order's stored `stripe_pi_id`.
- Error mapping (`stripe.ProviderError`): API errors surface via the
  breaker; rate-limit / network errors trigger client retries.

### Blockchain
- `ethclient` for RPC. WebSocket for subscriptions, HTTP for backfill +
  receipts.
- `ChainConfig` per chain; never hardcode chain values.
- Block cursor persists AFTER event saved (at-least-once delivery).
- 4-layer idempotent defense: subscriber → backfill → confirmation →
  reconciler. Listener is mounted only when `blockchain.contract_address`
  AND `blockchain.rpc_websocket` are set.

---

## Project Layout (top-level)

```
cmd/server/             entry point (single composition root)
cmd/bench/              load harness for paid-flow benchmarks
internal/
  api/                  Fiber router + handlers + middleware
  service/              business logic (payment, webhook, merchant,
                        checkout_resolver, reconciliation)
  repository/           MySQL CRUD — order, merchant, onchain_tx repos +
                        ShardRouter (logical → physical pool mapping)
  cache/                Redis: ratelimiter, locker, token_bucket; in-proc URLCache
  kafka/                producer (PaymentConfirmedEvent) + BatchConsumer
  consumer/             settlement consumer (the only one)
  provider/stripe/      live SDK client, fake client, circuit breaker, signature verify
  provider/blockchain/  listener + subscriber + backfill + confirmation +
                        reconciler + cursor (block_cursors table)
  domain/               entities + status enum + order_id validator
  config/               YAML loading, validation, defaults
  mocks/<pkg>/          gomock-generated mocks (regen: make mocks)
pkg/                    shared utilities (hmac, response, logger, checkouttoken)
migrations/             SQL up/down (single `001_init_schema`)
integration_test/       testcontainers-go suites (build tag `integration`)
```

Key ports (defined in their owning packages):

| Interface | Package |
|---|---|
| `Payments`, `Webhooks`, `Checkouts`, `Merchants`, `Reconciler` | `internal/service` |
| `OrderRepository`, `MerchantRepository`, `OnchainTxRepository`, `ShardRouter` | `internal/repository` |
| `Locker`, `URLCache`, `RateLimiter`, `TokenBucket` | `internal/cache` |
| `EventPublisher`, `BatchHandler` | `internal/kafka` |
| `stripe.Client`, `ChainClient`, `CursorStore` | `internal/provider/...` |

### Admin

`POST /admin/merchants` (header `X-Admin-Key: $admin_api_key`) creates a
merchant, generates a 32-byte hex `api_key`/`secret_key` pair, and assigns
the least-loaded `shard_index`. Mounted only when `security.admin_api_key`
is set.

### Public hosted checkout

`GET /pay/:merchant_id/:order_id?t=<token>` resolves the order through a
five-tier `CheckoutResolver`:

1. In-process URL LRU (cap 10 000, 5s TTL) — covers reload/double-click.
2. MySQL row → reconstruct from `stripe_session_id` if present.
3. Distributed Redis lock (`checkout:<merchant>:<order>`, 10s TTL).
4. Redis token-bucket rate limiter (`stripe:create_session`, default 80 rps)
   to stay under Stripe's per-account quota.
5. Stripe call (already wrapped by circuit breaker), then UPDATE row +
   warm cache.

`/checkout/success` and `/checkout/cancel` are UX-only landing pages —
state changes happen via webhook, never via the redirect tail.

---

## Commit Convention

```
feat(payment): add Stripe Checkout Session creation endpoint
fix(webhook): handle out-of-order payment_intent.succeeded after charge.refunded
perf(redis): batch idempotency checks
test(e2e): add full payment flow integration test
```

Types: `feat`, `fix`, `refactor`, `perf`, `test`, `docs`, `chore`, `ci`
Scope: `payment`, `webhook`, `blockchain`, `kafka`, `redis`, `mysql`, `api`, `config`, `bench`

**Author:** commit as configured `git config user.name` / `user.email`. Do **not** append a `Co-Authored-By: Claude …` trailer — repo history has none, keep it that way.
