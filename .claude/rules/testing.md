# Testing Rules

## Unit Tests
- Every service and repository has unit tests.
- Mock external dependencies via interfaces. Use hand-written mocks, not mockgen (simpler for this project size).
- Table-driven tests for functions with multiple input/output cases.
- Test file lives next to source file: `payment_service.go` → `payment_service_test.go`.
- Run: `go test ./internal/... -v -race`

## Integration Tests
- Located in `integration_test/` directory.
- Use `testcontainers-go` to spin up real MySQL, Redis, Kafka.
- `SetupTestEnv(t)` creates containers, `t.Cleanup()` auto-destroys them.
- `env.CleanTables(t)` between sub-tests (truncate, not recreate).
- External APIs (Stripe) mocked with `httptest.NewServer`.
- Run: `go test ./integration_test/... -v -race -count=1 -timeout 120s`

## What MUST Be Integration Tested
These behaviors cannot be verified with unit tests:
- `SELECT FOR UPDATE` under concurrent goroutines (double-spending prevention).
- Redis `SETNX` race conditions (idempotency).
- Kafka produce → consume → batch insert pipeline.
- MySQL UNIQUE constraint on `tx_hash` (blockchain dedup).

## Test Naming Convention
```
TestComponentName_Scenario_ExpectedBehavior

Examples:
TestPaymentService_DuplicateTransaction_ReturnsIdempotentResponse
TestWebhookHandler_InvalidSignature_Returns401
TestKafkaConsumer_BatchInsert_PartialFailure_SendsToDeadLetter
```
