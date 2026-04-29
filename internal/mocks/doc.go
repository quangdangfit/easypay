// Package mocks centralizes generated gomock mocks for our internal
// interfaces. Each subdirectory mirrors the source package and carries a
// short `<pkg>mock` package name (e.g. `repomock`, `cachemock`).
//
// Regenerate with `make mocks` (or `go generate ./internal/mocks/`).
package mocks

//go:generate mockgen -source=../repository/order_repo.go -destination=repo/mock_order_repo.go -package=repomock
//go:generate mockgen -source=../repository/merchant_repo.go -destination=repo/mock_merchant_repo.go -package=repomock
//go:generate mockgen -source=../repository/pending_tx_repo.go -destination=repo/mock_pending_tx_repo.go -package=repomock

//go:generate mockgen -source=../cache/idempotency.go -destination=cache/mock_idempotency.go -package=cachemock
//go:generate mockgen -source=../cache/lock.go -destination=cache/mock_lock.go -package=cachemock
//go:generate mockgen -source=../cache/order_pending.go -destination=cache/mock_pending_order.go -package=cachemock
//go:generate mockgen -source=../cache/ratelimiter.go -destination=cache/mock_ratelimiter.go -package=cachemock
//go:generate mockgen -source=../cache/token_bucket.go -destination=cache/mock_token_bucket.go -package=cachemock
//go:generate mockgen -source=../cache/url_cache.go -destination=cache/mock_url_cache.go -package=cachemock

//go:generate mockgen -source=../provider/stripe/client.go -destination=stripe/mock_client.go -package=stripemock

//go:generate mockgen -source=../kafka/producer.go -destination=kafka/mock_event_publisher.go -package=kafkamock
//go:generate mockgen -source=../kafka/consumer.go -destination=kafka/mock_batch_handler.go -package=kafkamock

//go:generate mockgen -source=../provider/blockchain/client.go -destination=blockchain/mock_chain_client.go -package=bcmock
//go:generate mockgen -source=../provider/blockchain/cursor.go -destination=blockchain/mock_cursor_store.go -package=bcmock

//go:generate mockgen -source=../service/types.go -destination=service/mock_types.go -package=svcmock
//go:generate mockgen -source=../service/fraud_service.go -destination=service/mock_fraud.go -package=svcmock

//go:generate mockgen -source=../api/handler/health.go -destination=handler/mock_pinger.go -package=handlermock
