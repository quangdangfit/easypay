CREATE TABLE IF NOT EXISTS pending_txs (
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
    INDEX idx_chain_status (chain_id, status),
    INDEX idx_order_id     (order_id)
) ENGINE=InnoDB;
