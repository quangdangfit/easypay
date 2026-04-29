Review the code in $ARGUMENTS for payment-system-specific issues.

Check for these critical problems:

**Security**
- [ ] HMAC verification present on all merchant-facing endpoints?
- [ ] Stripe webhook: signature verify + GET Status double-check?
- [ ] No hardcoded secrets or API keys?
- [ ] Parameterized SQL queries (no string concatenation)?
- [ ] Sensitive data not logged (card numbers, keys)?

**Correctness**
- [ ] Idempotency: check before write (Bloom → Redis → DB)?
- [ ] Double-spending: SELECT FOR UPDATE on balance/status changes?
- [ ] Atomic state transitions (within DB transaction)?
- [ ] Error handling: no swallowed errors, proper wrapping?
- [ ] Context propagation: context.Context passed through?

**Concurrency**
- [ ] Goroutine leaks: every goroutine has exit path via context?
- [ ] Shared state protected by mutex or channel?
- [ ] Loop variable capture fixed (Go 1.22+ or explicit copy)?

**Performance**
- [ ] Redis before DB (short-circuit expensive operations)?
- [ ] Batch operations where possible?
- [ ] Connection pooling configured?
- [ ] No N+1 query patterns?

Report findings grouped by severity: CRITICAL → HIGH → MEDIUM → LOW.
