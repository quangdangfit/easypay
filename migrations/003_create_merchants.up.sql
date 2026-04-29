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
