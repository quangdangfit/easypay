DROP TABLE IF EXISTS orders_00;
DROP TABLE IF EXISTS orders_01;
DROP TABLE IF EXISTS orders_02;
DROP TABLE IF EXISTS orders_03;
DROP TABLE IF EXISTS orders_04;
DROP TABLE IF EXISTS orders_05;
DROP TABLE IF EXISTS orders_06;
DROP TABLE IF EXISTS orders_07;
DROP TABLE IF EXISTS orders_08;
DROP TABLE IF EXISTS orders_09;
DROP TABLE IF EXISTS orders_0a;
DROP TABLE IF EXISTS orders_0b;
DROP TABLE IF EXISTS orders_0c;
DROP TABLE IF EXISTS orders_0d;
DROP TABLE IF EXISTS orders_0e;
DROP TABLE IF EXISTS orders_0f;

CREATE TABLE IF NOT EXISTS orders (
    id                       BIGINT AUTO_INCREMENT PRIMARY KEY,
    order_id                 VARCHAR(64) UNIQUE NOT NULL,
    merchant_id              VARCHAR(64) NOT NULL,
    transaction_id           VARCHAR(64) NOT NULL,
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
    UNIQUE KEY uniq_merchant_txn (merchant_id, transaction_id),
    INDEX idx_merchant_status (merchant_id, status),
    INDEX idx_status_created  (status, created_at),
    INDEX idx_stripe_pi       (stripe_payment_intent_id)
) ENGINE=InnoDB;
