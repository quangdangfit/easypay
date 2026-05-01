ALTER TABLE orders DROP INDEX uniq_merchant_txn;
ALTER TABLE orders ADD UNIQUE INDEX transaction_id (transaction_id);
