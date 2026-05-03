# Pre-commit checks

Before creating any git commit on behalf of the user, Claude **must** run
both gates below from the repo root and confirm they pass.

## The gates

### 1. Lint must be clean

```bash
golangci-lint run ./...
```

- Required output: `0 issues.` (or no output at all on older versions).
- If the binary is not installed, fall back to `go vet ./...` and call out
  in the response that the full linter wasn't available.

### 2. Unit tests must pass

```bash
go test -race -count=1 -timeout 120s ./internal/... ./pkg/...
```

- Integration tests (`./integration_test/...`) are **not** part of the gate
  — they need Docker and run on CI.
- `-race` is mandatory; data races silently corrupt MySQL/Redis state.

## Workflow

1. Stage the change (Edit / Write).
2. Run the two commands in parallel (independent, no shared state).
3. **Pass:** proceed with the commit. Mention "lint + tests green" in the
   end-of-turn summary.
4. **Fail:** do **not** commit. Fix the underlying issue, re-run the gate,
   and only commit once both are green.
5. Never bypass with `--no-verify`, `-tags=skip-something`, or skipping the
   step "to save time." If the user explicitly opts out (e.g. "just
   commit, lint is broken in another file"), respect that but say so in
   the response.

## When to skip

- **Docs-only changes** (`*.md`, comments, `config.yaml.example`): both gates
  can be skipped. State this explicitly in the response.
- **Migration files** (`migrations/*.sql`): skip lint, but still run unit
  tests if any Go code touches them (e.g. a repo refactor).
- **Generated files** (vendored deps, mocks): the gate may already be
  passing on these — let the linter's own ignore rules handle it.

## Common failure modes and quick fixes

| Symptom | Fix |
|---|---|
| `gofmt` issues | `gofmt -w <files>` |
| `errcheck` on `defer x.Close()` | `defer func() { _ = x.Close() }()` |
| `gosec G115` int → uint64 | wrap with `nonNegUint64()` (see `internal/config`) |
| `staticcheck ST1020` exported func comment | rewrite to `// FuncName ...` form |
| Tests fail with `clientFoundRows`-related "not found" | check the DSN includes `clientFoundRows=true` |
| Tests fail with Kafka "Unknown Topic" | rare locally; CI uses topic pre-creation in `NewPublisher` |

## Why we run both, not just lint

Lint catches static issues; unit tests catch behavioral regressions
(idempotency, signature math, lazy-checkout fall-through). Either one alone
has let real bugs through this repo. Both together remain fast (<30s
typically), well below the threshold where a developer would skip them.

## Relationship to CI

CI (`.github/workflows/ci.yml`) runs the same two gates plus a build step
and integration tests. The local pre-commit gate is a fast-fail mirror, not
a replacement — broken commits still get caught upstream, but at the cost
of a wasted CI run and a follow-up "fix lint" commit polluting history.
