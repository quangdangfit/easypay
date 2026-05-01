Implement Phase $ARGUMENTS from the CLAUDE.md implementation order.

Steps:
1. Read CLAUDE.md to find the phase and its numbered steps.
2. For each step in the phase, create the file at the specified path.
3. Follow the interfaces defined in CLAUDE.md "Key Interfaces" section.
4. Follow rules in .claude/rules/ (code-style, security, testing).
5. After implementing each file, run `go build ./...` to verify it compiles.
6. When the phase is complete, run `go vet ./...` and fix any issues.
7. Write unit tests for each new service/repository file.
8. Commit with conventional commit format: `feat(scope): description`

Important:
- Check existing files first. Don't overwrite already-implemented code.
- Import paths use the module name from go.mod.
- Every new exported function needs a doc comment.
