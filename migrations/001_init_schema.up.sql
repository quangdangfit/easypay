-- easypay schema v1 (consolidated).
--
-- Four tables:
--   merchants             — API auth + per-merchant config + logical shard.
--   transactions          — traditional payments (Stripe). Single physical
--                           table; logical shard is merchants.shard_index
--                           which the routing layer can later use to split.
--   onchain_transactions  — blockchain payments (smart-contract events).
--   block_cursors         — last-seen block per chain for the listener.
--
-- Identity contract (transactions):
--   merchant_id    VARBINARY(16)  — merchant's external id (ASCII / ULID).
--   order_id       VARCHAR(64)    — merchant-supplied idempotency key.
--   transaction_id BINARY(16)     — gateway-derived from
--                                   sha256(merchant_id || ':' || order_id)[:16].
-- Two retries with the same (merchant_id, order_id) deterministically derive
-- the same transaction_id; the (merchant_id, transaction_id) UNIQUE on the
-- transactions table is the single dedupe layer.

CREATE TABLE IF NOT EXISTS merchants (
    id           BIGINT AUTO_INCREMENT PRIMARY KEY,
    merchant_id  VARCHAR(64) UNIQUE NOT NULL,
    name         VARCHAR(255) NOT NULL,
    api_key      VARCHAR(128) UNIQUE NOT NULL,
    secret_key   VARCHAR(128) NOT NULL,
    callback_url VARCHAR(512),
    rate_limit   INT DEFAULT 1000,
    status       ENUM('active','suspended') DEFAULT 'active',
    -- shard_index assigns the merchant to a logical bucket in
    -- [0, LOGICAL_SHARD_COUNT). Today every bucket lives on the same
    -- physical table; tomorrow we can split per-shard or per-shard-group.
    shard_index  TINYINT UNSIGNED NOT NULL DEFAULT 0,
    created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_api_key (api_key),
    INDEX idx_shard_index (shard_index)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS transactions (
    merchant_id      VARBINARY(16)     NOT NULL,
    transaction_id   BINARY(16)        NOT NULL,
    order_id         VARCHAR(64)       NOT NULL,
    amount           BIGINT UNSIGNED   NOT NULL,
    currency_code    SMALLINT UNSIGNED NOT NULL,
    status           TINYINT UNSIGNED  NOT NULL,
    payment_method   TINYINT UNSIGNED  NOT NULL,
    stripe_pi_id     VARBINARY(64),
    stripe_session   VARBINARY(96),
    created_at       INT UNSIGNED      NOT NULL,
    updated_at       INT UNSIGNED      NOT NULL,
    PRIMARY KEY (merchant_id, transaction_id),
    UNIQUE KEY uniq_merchant_order (merchant_id, order_id),
    KEY idx_status_created (status, created_at),
    KEY idx_pi_id (stripe_pi_id)
) ENGINE=InnoDB ROW_FORMAT=DYNAMIC;

CREATE TABLE IF NOT EXISTS onchain_transactions (
    id               BIGINT AUTO_INCREMENT PRIMARY KEY,
    tx_hash          VARCHAR(66) UNIQUE NOT NULL,
    block_number     BIGINT NOT NULL,
    order_id         VARCHAR(64) NOT NULL,
    payer            VARCHAR(42),
    token            VARCHAR(42),
    amount           DECIMAL(65,0),
    chain_id         BIGINT NOT NULL,
    confirmations    BIGINT DEFAULT 0,
    required_confirm BIGINT NOT NULL,
    status           ENUM('pending','confirmed','failed','reorged') DEFAULT 'pending',
    created_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_chain_status (chain_id, status),
    INDEX idx_order_id     (order_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS block_cursors (
    chain_id   BIGINT PRIMARY KEY,
    last_block BIGINT NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB;
