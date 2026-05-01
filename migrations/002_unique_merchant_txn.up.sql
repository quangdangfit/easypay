-- Tighten the orders idempotency contract: transaction_id is unique
-- per merchant, not globally. Two merchants may legitimately reuse the
-- same internal transaction_id; the enforcement and the long-tail
-- idempotency lookup both key on (merchant_id, transaction_id).
ALTER TABLE orders DROP INDEX transaction_id;
ALTER TABLE orders ADD UNIQUE INDEX uniq_merchant_txn (merchant_id, transaction_id);
