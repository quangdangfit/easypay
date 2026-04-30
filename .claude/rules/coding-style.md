# Go Code Style

## General
- Go 1.25+ required. Use new loop variable scoping (no need for `v := v` in goroutines).
- Use `context.Context` as first parameter in every function that does I/O.
- Use `slog` for structured logging. Every log line includes: `request_id`, `merchant_id`, `order_id` where available.
- All monetary amounts are `int64` in smallest currency unit (IDR has no decimals, so 150000 IDR = `150000`).
- All timestamps are UTC. Use `time.Time`, never `string` for timestamps.

## Error Handling
- Wrap errors with context: `fmt.Errorf("create order: %w", err)`.
- Never swallow errors silently. If ignoring intentionally, comment why: `_ = conn.Close() // best-effort cleanup`.
- Use sentinel errors for expected cases: `var ErrOrderNotFound = errors.New("order not found")`.
- Return errors, don't panic. Only panic for programmer bugs (nil pointer that should never be nil).

## Naming
- Interfaces: describe behavior, not implementation. `OrderRepository`, not `MySQLOrderRepo`.
- Concrete types: include implementation detail. `mysqlOrderRepository`, `redisIdempotencyChecker`.
- Files: snake_case matching the primary type. `payment_service.go`, `order_repo.go`.
- Test files: same name with `_test.go` suffix. `payment_service_test.go`.

## Project Conventions
- All external dependencies accessed through interfaces. No direct `*sql.DB` in service layer.
- Constructor functions return interfaces: `func NewOrderRepository(db *sql.DB) OrderRepository`.
- Concrete implementations are unexported. See `.claude/rules/dependency-injection.md` for the full DI contract.
- Table-driven tests preferred for multiple cases.
- Integration tests in `integration_test/` directory, use testcontainers.
- Lint and unit tests must pass before every commit. See `.claude/rules/pre-commit-checks.md`.
