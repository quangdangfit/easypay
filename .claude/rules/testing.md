# Testing Rules

## Unit Tests
- Every service and repository has unit tests.
- Mock external dependencies via interfaces. Use **gomock** (`go.uber.org/mock`), not hand-written fakes.
  - Generated mocks live in `internal/mocks/<pkg>/` (`repomock`, `cachemock`, `kafkamock`, `stripemock`, `bcmock`, `svcmock`, `handlermock`).
  - Add a `//go:generate mockgen ...` directive to `internal/mocks/doc.go` whenever you add a new interface.
  - Run `make mocks` to regenerate (requires `go install go.uber.org/mock/mockgen@latest`).
  - For stateful behaviour (e.g. an in-memory `OrderRepository`), wire `EXPECT().DoAndReturn(...)` with a closure over a map; see `internal/service/mock_helpers_test.go` and `internal/provider/blockchain/mock_helpers_test.go` for shared patterns.
  - **Exception**: when an interface is defined in the *same* package as the test, importing its mock from `internal/mocks/<pkg>/` causes an import cycle. In that narrow case keep a small in-package fake (see `noopHandler` in `internal/kafka/consumer_test.go`, `failClient` in `internal/provider/stripe/breaker_test.go`, `fakeChain` / `memCursor` in `internal/provider/blockchain/`).
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
