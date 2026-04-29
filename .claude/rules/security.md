# Security Rules

## Secrets
- NEVER hardcode secrets, API keys, passwords, or connection strings in source code.
- All secrets come from environment variables, loaded in `internal/config/config.go`.
- Use placeholder values in examples: `SB-Mid-server-xxx`, `your-secret-here`.
- If you need a test key, use a clearly fake value: `test-key-do-not-use-in-production`.

## Payment-Specific Security
- HMAC-SHA256 verification on EVERY merchant-facing endpoint. No exceptions.
- Stripe webhooks: ALWAYS verify SHA512 signature, THEN call GET /v2/{id}/status. NEVER trust webhook body alone.
- `SELECT ... FOR UPDATE` for any balance/status change to prevent double-spending.
- Idempotency key checked BEFORE any state change. Bloom filter → Redis → DB, in that order.
- All payment state transitions must be atomic (within a DB transaction).

## Input Validation
- Validate all input at handler level before passing to service.
- Amount: must be positive, within min/max range (1,000 - 999,999,999 IDR).
- Strings: max length enforced, no SQL injection (use parameterized queries always).
- Enum values: validate against known set (status, payment_method, currency).

## Do NOT
- Do not log full request bodies containing sensitive data (card numbers, passwords).
- Do not return internal error details to clients. Return generic error + request_id.
- Do not use `fmt.Sprintf` to build SQL queries. Always use parameterized queries.
- Do not store raw Stripe server_key in MySQL. It stays in env vars only.
