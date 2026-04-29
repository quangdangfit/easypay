CREATE TABLE IF NOT EXISTS orders (
    id                       BIGINT AUTO_INCREMENT PRIMARY KEY,
    order_id                 VARCHAR(64) UNIQUE NOT NULL,
    merchant_id              VARCHAR(64) NOT NULL,
    transaction_id           VARCHAR(64) UNIQUE NOT NULL,
    amount                   BIGINT NOT NULL,
    currency                 VARCHAR(3) NOT NULL DEFAULT 'USD',
    status                   ENUM('created','pending','paid','failed','expired','refunded') NOT NULL,
    payment_method           VARCHAR(30),
    stripe_session_id        VARCHAR(255),
    stripe_payment_intent_id VARCHAR(255),
    stripe_charge_id         VARCHAR(255),
    checkout_url             VARCHAR(1024),
    callback_url             VARCHAR(512),
    created_at               TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at               TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_merchant_status (merchant_id, status),
    INDEX idx_status_created  (status, created_at),
    INDEX idx_stripe_pi       (stripe_payment_intent_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS pending_txs (
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

CREATE TABLE IF NOT EXISTS merchants (
    id           BIGINT AUTO_INCREMENT PRIMARY KEY,
    merchant_id  VARCHAR(64) UNIQUE NOT NULL,
    name         VARCHAR(255) NOT NULL,
    api_key      VARCHAR(128) UNIQUE NOT NULL,
    secret_key   VARCHAR(128) NOT NULL,
    callback_url VARCHAR(512),
    rate_limit   INT DEFAULT 1000,
    status       ENUM('active','suspended') DEFAULT 'active',
    created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_api_key (api_key)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS block_cursors (
    chain_id   BIGINT PRIMARY KEY,
    last_block BIGINT NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB;
