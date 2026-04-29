# Dependency Injection

## The rule

**Cross-package consumers depend on interfaces, never on concrete structs.
Constructors return interface types. Concrete implementations are
unexported.**

## Why

- Mocking in unit tests is trivial — define a fake type that implements the
  interface in the test file. No `mockgen`, no reflection.
- Wiring lives in exactly one place (`cmd/server/main.go`); every other file
  reads the dependency graph by reading interface declarations.
- Refactoring an implementation (e.g. swap MySQL for Postgres) doesn't ripple
  through callers — they only know the interface.
- Reviewers can answer "what does this depend on?" by reading struct fields,
  without chasing through types.

## How — checklist

When adding a new component (service, repository, cache helper, provider,
consumer, …):

1. Decide its **port** (the methods consumers will call). Define an
   exported interface in the package. Place small interfaces in a
   `types.go` file alongside other interfaces in the same package, one
   per port.

2. Declare the implementation as an **unexported** struct
   (`paymentService`, `redisLocker`, `mysqlOrderRepository`).

3. Constructor returns the **interface**, not the concrete type:

   ```go
   func NewPaymentService(deps...) Payments { return &paymentService{...} }
   ```

4. Consumers store the **interface** as a struct field:

   ```go
   type PaymentHandler struct {
       svc service.Payments  // ✅ interface
       // svc *service.PaymentService  ❌ concrete
   }
   ```

5. Wire only in `cmd/server/main.go`. The graph is explicit: each
   `NewX(...)` call composes the dependencies of the next layer.

## Allowed exceptions

- **Value/data types** (DTOs, request/response structs, domain entities,
  options structs): keep them exported and concrete. Interfaces for `Order`
  or `CreatePaymentInput` would be over-engineering.
- **Terminal "runner" types** that are only invoked from `main.go` (e.g.
  `kafka.BatchConsumer`, blockchain `Listener`) may stay public structs
  if they have no other consumers and no test seam is needed beyond the
  handler interfaces they themselves consume.
- **Stdlib/third-party types** that already provide an interface
  (`http.Handler`, `io.Reader`) — use them directly.
- **Logger / metric helpers** (`pkg/logger`, `pkg/response`) — these are
  stateless utility functions, not dependencies.

## Common smells (reject in review)

| Smell | Fix |
|---|---|
| `func New...() *Thing` (returns concrete) | Return interface type |
| `*service.PaymentService` in a handler field | Replace with `service.Payments` |
| Test file imports concrete struct from another package to mock | Define a fake satisfying the interface in the test file |
| Constructor takes `*redis.Client` directly inside service | Wrap in a typed port (`cache.Locker`, `cache.IdempotencyChecker`) |
| Two-tier interface chain: handler → public struct → unexported impl | Collapse to handler → interface → unexported impl |

## Naming

- Interface name describes **behavior** in domain terms: `OrderRepository`,
  `Payments`, `Locker`, `IdempotencyChecker`.
- Concrete name names the **implementation**: `mysqlOrderRepository`,
  `paymentService`, `redisLocker`, `redisIdempotency`.
- Avoid "Service"/"Manager" suffix on interfaces unless the package is full
  of them — prefer plurals or verb-y names: `Payments`, `Webhooks`.

## Reference layout

```go
// internal/cache/lock.go
type Locker interface {
    Acquire(ctx context.Context, key string, ttl time.Duration) (Lock, error)
}
type Lock interface {
    Release(ctx context.Context) error
}

type redisLocker struct{ rc *redis.Client }
type redisLock   struct{ rc *redis.Client; key, token string }

func NewLocker(rc *redis.Client) Locker { return &redisLocker{rc: rc} }

func (l *redisLocker) Acquire(...) (Lock, error) { ... }
func (lk *redisLock)  Release(...) error         { ... }
```

```go
// internal/api/handler/checkout.go
type CheckoutHandler struct {
    resolver service.Checkouts   // not *service.checkoutResolver
}

func NewCheckoutHandler(r service.Checkouts, ...) *CheckoutHandler { ... }
```

```go
// cmd/server/main.go — the only place that wires concretes
locker := cache.NewLocker(rc)
resolver := service.NewCheckoutResolver(service.CheckoutResolverOptions{
    Locker: locker,
    ...
})
checkoutH := handler.NewCheckoutHandler(resolver, secret)
```
