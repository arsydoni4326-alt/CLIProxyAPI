# Contributing

Thank you for considering a contribution to CLIProxyAPI.

## Development Setup

- Go 1.26+
- `git clone` this repository; submodules live under `frontend/` and `cpauk/` (`git submodule update --init --recursive`).
- Default config: `config.yaml` (template: `config.example.yaml`); auth material defaults under `auths/`.

## Common Commands

```bash
gofmt -w .                                   # Format (required after Go changes)
go build -o cli-proxy-api ./cmd/server       # Build
go run ./cmd/server                          # Run dev server
go test ./...                                # Run all tests
go test -v -run TestName ./path/to/pkg       # Run a single test
```

## Coding Conventions

- Keep changes small and simple (KISS); follow the existing architecture and patterns.
- Comments in English only; use logrus structured logging; never leak secrets in logs.
- Do not use `log.Fatal`/`log.Fatalf` in request paths; return errors instead.
- Avoid panics in HTTP handlers; prefer logged errors and meaningful status codes.
- Wrap deferred errors: `defer func() { if err := f.Close(); err != nil { log.Errorf(...) } }()`.
- `internal/runtime/executor/` holds executors and their unit tests only; helper files go under `internal/runtime/executor/helps/`.

## Testing

- Add or update tests for every feature, bug fix, or behavior change where practical.
- Run relevant tests before submitting; never claim a test was run if it was not.
- Prefer controllable clocks over `time.Sleep` in TTL/expiration tests.

## Pull Requests

- Update any documentation affected by your change (`README*`, `docs/`, `config.example.yaml`, `CHANGELOG.md`) in the same PR and note the docs touched in the PR description.
- Keep the merge-preservation rules in `docs/MERGE-PRESERVATION-fork-fixes.md` intact when resolving upstream merges.
