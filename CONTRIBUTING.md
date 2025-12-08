# Contributing to MarkLoud

Thanks for considering a contribution! This doc keeps new contributors productive and the project stable.

## Quick start
1. Install Go 1.24+ (Bubble Tea 1.3 requires it).
2. Clone and install tools:
   ```bash
   go install honnef.co/go/tools/cmd/staticcheck@latest
   ```
3. Run the checks:
   ```bash
   make fmt vet lint test
   ```
4. Hack on code, add tests, and ensure `make ci` passes before opening a PR.

## Development workflow
- Formatting: `make fmt` (CI runs `fmtcheck`).
- Linting: `make lint` (staticcheck).
- Tests: `make test` (add `-race` locally for concurrency changes).
- Modules: run `make tidy` when dependencies change.
- Logs & binaries: keep `logs/`, `*.aac`, built binaries out of git (see `.gitignore`).

## Code style
- Keep functions small and testable; prefer dependency injection for anything that hits the network or filesystem.
- Errors: wrap with context using `fmt.Errorf("...: %w", err)`.
- Logging: avoid logging secrets; prefer structured logs.
- UI: follow existing Bubble Tea patterns and avoid blocking calls in the update loop.

## Pull requests
- Describe the user-facing change and testing performed.
- Add/adjust tests for new behavior.
- Update docs (`README.md`) when flags/env vars change.
- One logical change per PR when possible.

## Reporting bugs
Use the “Bug report” issue template and include your OS, Go version, MarkLoud version, and logs from `logs/markloud_errors.log` if present.

## Security issues
Please do **not** open a public issue. See `SECURITY.md` for the private channel.

## Community expectations
By participating you agree to the Code of Conduct (`CODE_OF_CONDUCT.md`).
