Add a new API endpoint: $ARGUMENTS

Follow the established project patterns:

1. **Handler** (`internal/api/handler/`):
    - Parse + validate request
    - Call service layer (never access DB/Redis directly)
    - Return standardized JSON response via `pkg/response`
    - Include request_id in all error responses

2. **Service** (`internal/service/`):
    - Business logic only
    - Accept and return domain types
    - All dependencies via interfaces (injected in constructor)

3. **Repository** (if new DB operation needed):
    - Add method to existing interface + implementation
    - Use parameterized queries
    - Wrap errors with context

4. **Route** (`internal/api/router.go`):
    - Register endpoint with appropriate middleware (auth, rate limit)
    - Group under correct prefix (/api for merchant, /webhook for providers)

5. **Test**:
    - Unit test for service layer logic
    - Handler test with mock service
    - Integration test if involves DB/Redis/Kafka

6. Build and verify: `go build ./... && go vet ./...`
