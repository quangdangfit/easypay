# Plan: sync-write payment gateway, sharded by merchant_id

## 1. Mục tiêu

Loại bỏ async batch insert (Kafka → consumer → MySQL) khỏi write path. Lý
do: gap giữa "trả 202 cho merchant" và "row có trong MySQL" tạo ra một
loạt edge case phải vá bằng Redis pending, DLQ replay, reconciliation
self-heal — và bất kỳ band-aid nào fail đều dẫn tới silent loss khi Stripe
charge sau đó.

Sau plan này:
- POST /api/payments **INSERT thẳng vào MySQL trong handler**, return chỉ
  sau khi commit thành công.
- **MySQL UNIQUE là idempotency layer duy nhất** — không Redis idem cache,
  không Bloom filter, không cold-path lookup.
- Sharded by `merchant_id` ngay từ đầu.
- Schema gọn lại đáng kể để insert latency ~1ms p99 trên một shard.

## 2. Kiến trúc mới

```
POST /api/payments {merchant_order_id, amount, currency, ...}
│
├── HMAC verify (middleware) — không đổi
├── Compute transaction_id = HMAC(secret, merchant_id || ":" || merchant_order_id)
├── Compute order_id      = first 12 bytes of transaction_id (deterministic)
├── shard = hash(merchant_id) mod N
├── INSERT INTO orders_<shard> (...) VALUES (...)
│   ├── 1062 (UNIQUE on transaction_id) → SELECT existing → return cached response
│   └── OK → continue
├── (Eager) Stripe.CreateCheckoutSession(idempotency_key=transaction_id_hex)
│   → UPDATE orders SET stripe_*, checkout_url WHERE transaction_id=...
├── (Lazy)  build /pay/:order_id token URL — không UPDATE row
├── (Optional) Kafka payment.events fan-out fire-and-forget cho analytics
└── Return 201 + {order_id, checkout_url, status}
```

**Không còn `payment_consumer`. Không còn `pending order Redis snapshot`.
Không còn `Bloom + idem cache`. Không còn `cold-path GetByMerchantTransaction`
ở service layer.**

## 3. Schema mới — minimal row

Mục tiêu: page packing cao, index nhỏ, insert nhanh. Các quyết định có
note tradeoff để confirm.

```sql
CREATE TABLE orders_<shard> (
  -- Identity (totally fixed-width, packs nicely)
  merchant_id      INT UNSIGNED       NOT NULL,  -- 4B. Migrate from VARCHAR.
  transaction_id   BINARY(16)         NOT NULL,  -- 16B. HMAC truncated 128b.
  order_id         BINARY(12)         NOT NULL,  -- 12B. transaction_id[:12].
  -- Money
  amount           BIGINT UNSIGNED    NOT NULL,  -- 8B. Smallest currency unit.
  currency_code    SMALLINT UNSIGNED  NOT NULL,  -- 2B. ISO 4217 numeric (840=USD).
  -- State (enums encoded as TINYINT)
  status           TINYINT UNSIGNED   NOT NULL,  -- 1B. 0=created 1=pending 2=paid 3=failed 4=expired 5=refunded
  payment_method   TINYINT UNSIGNED   NOT NULL,  -- 1B. 0=card 1=crypto_eth 2=wallet ...
  -- Stripe artefacts (nullable, set after Stripe call)
  stripe_pi_id     VARBINARY(32),                -- Stripe PI IDs ~27 chars.
  stripe_session   VARBINARY(80),                -- Stripe Checkout session IDs ~66 chars.
  -- Hosted-checkout URL. Considering: drop and reconstruct from stripe_session
  -- + a known prefix to save ~200B/row. CONFIRM whether merchant ever sees a
  -- non-Stripe URL (lazy mode does — keep this field).
  checkout_url     VARCHAR(255),
  -- Time
  created_at       INT UNSIGNED       NOT NULL,  -- 4B. Unix seconds (valid through 2106).
  updated_at       INT UNSIGNED       NOT NULL,  -- 4B.
  PRIMARY KEY (merchant_id, transaction_id),     -- Clustered, writes & reads stay co-merchant.
  UNIQUE KEY uniq_order (order_id),              -- Resolver lookup by order_id.
  KEY idx_status_created (status, created_at)    -- Reconciliation cron.
) ENGINE=InnoDB ROW_FORMAT=DYNAMIC;
```

**Removed from current schema:**
- `id BIGINT AUTO_INCREMENT` — composite PK on `(merchant_id, transaction_id)` is the cluster key, no need for surrogate.
- `callback_url` — moves to `merchants` table (per-merchant, rarely per-order).
- `stripe_charge_id` — derivable from `stripe_pi_id` via Stripe API; drop.
- Wide VARCHAR fields trimmed to actual upper bounds.

**Estimated row size:** ~300 bytes (vs ~1100-2500 bytes today depending on
URL lengths). ~3-7× more rows per InnoDB page → fewer page reads, better
buffer pool hit.

**Decisions to confirm:**
- (a) `merchant_id INT` requires assigning numeric IDs to merchants. Current
  schema uses `VARCHAR(64)`. Migration cost is real. Alternative: keep
  `VARBINARY(16)` for merchant_id (room for ULID/short string), still 16B
  fixed. Decide which.
- (b) `currency_code SMALLINT` (numeric) vs `CHAR(3)` (text). Numeric is 2B
  vs 3B and indexes faster, but every report needs a lookup table.
- (c) `transaction_id BINARY(16)` requires hex/base32 encoding for logs +
  API responses. App-side change but well-defined.
- (d) Drop `checkout_url` and reconstruct from `stripe_session`? Saves
  ~200B/row. Lazy-mode URL is `${PUBLIC_BASE}/pay/{order_id_hex}?t={token}`
  — also reconstructable. Lean toward dropping.

## 4. Sharding

- **Shard key**: `merchant_id`. Hash function: keep current
  `repository.ShardOf` (FNV-32a mod N).
- **N**: start with 16 shards (vs current `ShardCount=8`). Provisioning:
  one MySQL primary per shard + 2 replicas. Use ProxySQL or
  application-level routing in `repository`.
- **Cross-shard queries**: only reconciliation cron and analytics — both
  fan out per-shard. Hot path is always single-shard.
- **Resharding**: pick N as a power of 2 to make doubling cheap (split
  shard k into k and k+N when needed). Document the procedure.
- **Routing**: `repository.OrderRepository` becomes a thin façade that
  takes `merchant_id` and dispatches to the right `*sql.DB`. Each handler
  call must carry merchant_id (already does).

## 5. Service Create flow

Pseudo-code (sync, single function):

```go
func (s *paymentService) Create(ctx, in) (*Result, error) {
    if err := validate(in); err != nil { return nil, err }

    txnID  := hmac(s.txnSecret, in.MerchantID + ":" + in.MerchantOrderID)  // 16B
    orderID := txnID[:12]                                                   // 12B

    row := buildOrderRow(txnID, orderID, in, status=created)
    err := s.repo.Insert(ctx, row)
    switch {
    case err == nil:
        // new row inserted
    case isDuplicate(err):
        existing, gErr := s.repo.GetByTransactionID(ctx, in.MerchantID, txnID)
        if gErr != nil { return nil, gErr }
        if !sameMaterial(existing, in) {
            return nil, ErrTransactionConflict
        }
        return reconstructResult(existing), nil
    default:
        return nil, err
    }

    // Stripe / lazy checkout — same logic as today, but UPDATE is also sync.
    if eager {
        sess, _ := s.stripe.CreateCheckoutSession(ctx, req, idempotencyKey=txnID.Hex())
        s.repo.UpdateCheckout(ctx, in.MerchantID, txnID, sess)
        return resultFromStripe(row, sess), nil
    }
    return resultFromLazy(row), nil
}
```

Key invariants:
- Trước khi return cho merchant, row đã ở MySQL, durable, replicated theo
  policy của shard.
- UNIQUE(transaction_id) là điểm chốt duy nhất cho idempotency. Không có
  race nào có thể tạo orphan: hai concurrent INSERT → một thắng UNIQUE,
  một fail → SELECT trả về row của winner → cùng response.
- Stripe Idempotency-Key = `transaction_id` hex. Stripe dedupe phía họ.

## 6. Những gì bỏ

| Component | Lý do bỏ |
|---|---|
| `internal/consumer/payment_consumer.go` (+ test) | Không còn async insert |
| Topic `payment.events` (write path) | Có thể giữ nhưng fire-and-forget cho analytics, không phải write path |
| `internal/cache/idempotency.go` (Bloom + Redis idem) | UNIQUE thay thế |
| `internal/cache/order_pending.go` | Row đã ở DB khi return |
| `service.payment_service.repo.GetByMerchantTransaction` cold-path | Không cần fallback — DB là duy nhất |
| `checkout_resolver` Tier 3 (Redis pending fallback) | Row luôn có ở DB |
| `payment.events.dlq` consumer (đã propose nhưng chưa làm) | Không có async path để fail |
| Order reconciliation cron "stuck-pending sweep" | Đơn giản hóa: chỉ còn check Stripe-side status drift cho orders pending > N phút (vẫn cần) |

## 7. Những gì giữ

| Component | Ghi chú |
|---|---|
| Lazy checkout | Vẫn rất giá trị — write path không hit Stripe (rate limit) |
| `checkout_resolver` | Bỏ Tier 3 Redis pending; còn LRU local + DB + lock + Stripe call |
| Webhook handler | Vẫn verify signature, GET /v1/payment_intents, UPDATE row. UPDATE 0 rows giờ là bug nghiêm trọng (không phải race), alert ngay. |
| Stripe `Idempotency-Key` = transaction_id hex | Dedupe ở Stripe side khi merchant retry |
| HMAC verify on every merchant request | Không đổi |
| Reconciliation cron (đơn giản hóa) | Quét `status='pending' AND created_at < NOW()-10min`, GET Stripe PI status, force-confirm. Không còn lo "row mất tích" |

## 8. Sharding implementation

Trong code:

```go
type ShardedOrderRepo struct {
    shards [ShardCount]OrderRepository  // each is a mysqlOrderRepository(*sql.DB)
}

func (r *ShardedOrderRepo) Insert(ctx, row) error {
    return r.shards[ShardOf(row.MerchantID)].Insert(ctx, row)
}
// ... GetByTransactionID, GetByOrderID (broadcast or use index), etc.
```

`GetByOrderID` không có merchant_id trong query → cần broadcast tới mọi
shard hoặc maintain một bảng routing `order_id → shard_id`. Lựa chọn:

- **Option A**: Broadcast SELECT to all shards in parallel. Acceptable với
  N=16 nếu chỉ trong checkout_resolver (rare path so với INSERT). Latency
  ~max(shard_latency).
- **Option B**: Embed `shard_id` trong order_id (vd 1 byte đầu). Decode →
  route tới shard duy nhất. Cần thiết kế lại order_id format.
- **Option C**: Routing table `order_id → (merchant_id, shard_id)` ở
  Redis hoặc 1 MySQL "router" instance riêng. Thêm 1 lookup, thêm 1
  failure point.

→ Recommend **Option B**: `order_id = shard_byte || HMAC(secret,
merchant_id || merchant_order_id)[1..12]`. Resolver decode 1 byte đầu, đi
thẳng vào shard. Zero-cost routing.

## 9. Migration plan

**Phase 0 — preparation (no behavior change):**
- Provision N=16 shard primaries + replicas trong staging.
- Add `merchant_id INT` mapping ở `merchants` table (column mới `numeric_id`).

**Phase 1 — schema migration:**
- Migration 003: tạo `orders_v2` với schema mới trên TỪNG shard.
- Backfill `orders_v2` từ `orders` cũ qua background job (resumable, chunked).
- Verify counts + checksums.

**Phase 2 — dual-write:**
- Code path: INSERT vào CẢ `orders_v2` (sync) lẫn `orders` cũ (async, vẫn
  qua Kafka). Reads vẫn từ `orders` cũ.
- Soak 1-2 tuần, alert mọi divergence.

**Phase 3 — read switch:**
- Reads chuyển sang `orders_v2`. Writes vẫn dual.
- Soak 1 tuần.

**Phase 4 — single-write cleanup:**
- Drop async path. Drop `payment_consumer`, Redis idempotency cache,
  pending store, `payment.events` topic.
- Drop bảng `orders` cũ.

**Phase 5 — code cleanup:**
- Remove dead code (consumer, mocks, tests, configs).
- Update CLAUDE.md đúng kiến trúc mới.

## 10. Open questions cần confirm trước khi code

1. **Định danh merchant**: `merchant_id` chuyển sang INT, VARBINARY(16),
   hay giữ VARCHAR? Quyết định ảnh hưởng tới mọi index + API contract.
2. **Currency**: numeric ISO 4217 hay CHAR(3)?
3. **Stripe ID storage**: VARBINARY (case-sensitive, no collation) hay
   VARCHAR ASCII?
4. **`checkout_url`**: drop và reconstruct, hay keep?
5. **`payment.events` topic**: bỏ hẳn hay giữ làm fan-out (analytics,
   audit, settlement consumer vẫn dùng)?
6. **Settlement consumer** (callback merchant) — vẫn cần. Trigger từ
   `payment.confirmed` (webhook publish) hay từ DB CDC (Debezium)?
   Recommend giữ Kafka cho path này, không liên quan write path.
7. **Webhook self-heal**: với sync write, UPDATE 0 rows = bug critical.
   Alert + 500 (để Stripe retry) thay vì silent 200.
8. **N shards**: 16? Plan resharding pre-doubling threshold (CPU >70%,
   IOPS >70%).
9. **Connection pooling**: per-shard pool (`MaxOpenConns=25`/shard × 16
   shards × N pods)? Cần ProxySQL nếu pod count cao.
10. **Backfill chiến lược**: chunks size, throttling, snapshot consistency.

## 11. Implementation TODO (phased)

### Phase A — schema + sharded repo (foundation)
- [ ] Decide & document open questions §10.1-§10.4.
- [ ] Migration `003_orders_v2_minimal.up.sql`: new table per shard.
- [ ] `repository/orders_v2.go`: Insert / GetByTransactionID / GetByOrderID
      / UpdateCheckout / UpdateStatus on the new schema.
- [ ] `repository/sharded.go`: ShardedOrderRepository implementing
      OrderRepository, dispatching by merchant_id.
- [ ] `cmd/server/main.go`: wire N shard pools (config-driven DSN list).
- [ ] Unit tests on routing + integration test on a 2-shard testcontainer.

### Phase B — service rewrite
- [ ] `payment_service.Create`: sync INSERT path; remove Redis idem,
      remove pending Put, remove cold-path lookup.
- [ ] `payment_service.deriveOrderID`: switch to `shard_byte || HMAC`.
- [ ] `payment_service.transactionID`: HMAC(secret, merchant_id ||
      merchant_order_id), 16B.
- [ ] Update `CreatePaymentInput` to require `MerchantOrderID` instead of
      raw `TransactionID`. Handler/API contract change.
- [ ] Update `checkout_resolver`: remove Tier 3 Redis pending.
- [ ] Update tests + add concurrency tests (1k goroutines same input →
      1 row, identical responses).

### Phase C — backfill + dual-write soak
- [ ] Backfill job: copy old `orders` → sharded `orders_v2`, chunked,
      resumable, verifiable.
- [ ] Dual-write toggle (env flag `DUAL_WRITE=true`).
- [ ] Comparator job: random-sample reads from both, alert on divergence.
- [ ] 2-week production soak.

### Phase D — read switch
- [ ] Toggle reads to v2 (env flag `READ_FROM_V2=true`).
- [ ] 1-week soak.

### Phase E — cutover & cleanup
- [ ] Remove `payment_consumer` + tests + mocks.
- [ ] Remove `cache/idempotency.go`, `cache/order_pending.go` + tests.
- [ ] Remove `payment.events` from publisher + topic creation.
- [ ] Remove `service.GetByMerchantTransaction` cold-path block.
- [ ] Remove `checkout_resolver` Tier 3.
- [ ] Drop old `orders` table after final snapshot.
- [ ] Update CLAUDE.md (architecture section), update doc/diagram.
- [ ] Remove env vars: `LAZY_CHECKOUT_*` (giữ), `ORDER_ID_SECRET` (vẫn dùng cho txn derivation), retire any redis idem env.

### Phase F — operational
- [ ] Per-shard dashboards: insert RPS, p99 latency, lock waits.
- [ ] Alert: webhook UPDATE 0 rows > 0 (hard error now).
- [ ] Alert: per-shard CPU > 70%, IOPS > 70% (resharding trigger).
- [ ] Update load-test (`cmd/bench`) tới sharded mode + assert p99 < 5ms
      single-shard insert.

## 12. Trade-offs đã chấp nhận

- **Write latency tăng** từ ~5ms (Kafka publish) lên ~15-25ms (MySQL
  insert + commit). Bù lại bằng strong consistency + bỏ toàn bộ
  reconciliation/replay machinery.
- **MySQL primary là write SPOF cho từng shard**. Mitigated bằng replicas
  + auto-failover (Aurora/Vitess/Orchestrator).
- **Sharding ops** — phải maintain N pools, plan resharding. Acceptable
  cost cho 50k+ TPS target.
- **Backfill** là rủi ro vận hành duy nhất đáng kể. Phải làm cẩn thận
  trong staging trước.
