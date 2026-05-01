-- Replace the legacy single `orders` table with 16 merchant-sharded tables
-- carrying a minimal, fixed-width row layout. Designed for sync write on the
-- payment hot path: the row commits to MySQL inside the POST /api/payments
-- handler, before we return 201 to the merchant.
--
-- Identity:
--   merchant_id    VARBINARY(16)  — stored verbatim; supports ULIDs / short
--                                   ASCII strings up to 16 bytes.
--   transaction_id BINARY(16)     — HMAC-SHA256(secret, merchant_id || ':' ||
--                                   merchant_order_id) truncated to 128 bits.
--   order_id       BINARY(12)     — first byte = shard index, remaining 11
--                                   bytes = HMAC slice. Lets order_id-only
--                                   lookups (webhook, hosted checkout)
--                                   route to a single shard with no extra
--                                   index or cache.
--
-- Encoded enums (saves bytes vs the previous ENUM/VARCHAR columns):
--   status         TINYINT UNSIGNED  0=created 1=pending 2=paid 3=failed
--                                    4=expired 5=refunded
--   payment_method TINYINT UNSIGNED  0=card 1=crypto_eth 2=wallet
--                                    3=bank_transfer 4=bnpl_klarna
--                                    5=bnpl_afterpay  9=unknown
--   currency_code  SMALLINT UNSIGNED ISO 4217 numeric (840=USD, 978=EUR, …)
--
-- Stripe artefacts kept as VARBINARY (case-sensitive, no collation tax).
--
-- Removed vs the v1 schema:
--   - id BIGINT AUTO_INCREMENT     — composite PK (merchant_id,
--                                    transaction_id) is the cluster key.
--   - callback_url VARCHAR(512)    — moved to `merchants.callback_url`
--                                    (per-merchant, rarely per-order).
--   - checkout_url VARCHAR(1024)   — reconstructed from stripe_session_id
--                                    or order_id at read time.
--   - stripe_charge_id             — derivable from stripe_pi_id via Stripe
--                                    GET /v1/payment_intents/{id}.
--
-- Estimated row size ~290 B (vs ~1100-2500 B before). Higher InnoDB page
-- packing → better buffer-pool hit rate on hot merchants.

DROP TABLE IF EXISTS orders;

CREATE TABLE IF NOT EXISTS orders_00 (
    merchant_id      VARBINARY(16)     NOT NULL,
    transaction_id   BINARY(16)        NOT NULL,
    order_id         BINARY(12)        NOT NULL,
    amount           BIGINT UNSIGNED   NOT NULL,
    currency_code    SMALLINT UNSIGNED NOT NULL,
    status           TINYINT UNSIGNED  NOT NULL,
    payment_method   TINYINT UNSIGNED  NOT NULL,
    stripe_pi_id     VARBINARY(64),
    stripe_session   VARBINARY(96),
    created_at       INT UNSIGNED      NOT NULL,
    updated_at       INT UNSIGNED      NOT NULL,
    PRIMARY KEY (merchant_id, transaction_id),
    UNIQUE KEY uniq_order (order_id),
    KEY idx_status_created (status, created_at)
) ENGINE=InnoDB ROW_FORMAT=DYNAMIC;

CREATE TABLE IF NOT EXISTS orders_01 LIKE orders_00;
CREATE TABLE IF NOT EXISTS orders_02 LIKE orders_00;
CREATE TABLE IF NOT EXISTS orders_03 LIKE orders_00;
CREATE TABLE IF NOT EXISTS orders_04 LIKE orders_00;
CREATE TABLE IF NOT EXISTS orders_05 LIKE orders_00;
CREATE TABLE IF NOT EXISTS orders_06 LIKE orders_00;
CREATE TABLE IF NOT EXISTS orders_07 LIKE orders_00;
CREATE TABLE IF NOT EXISTS orders_08 LIKE orders_00;
CREATE TABLE IF NOT EXISTS orders_09 LIKE orders_00;
CREATE TABLE IF NOT EXISTS orders_0a LIKE orders_00;
CREATE TABLE IF NOT EXISTS orders_0b LIKE orders_00;
CREATE TABLE IF NOT EXISTS orders_0c LIKE orders_00;
CREATE TABLE IF NOT EXISTS orders_0d LIKE orders_00;
CREATE TABLE IF NOT EXISTS orders_0e LIKE orders_00;
CREATE TABLE IF NOT EXISTS orders_0f LIKE orders_00;
