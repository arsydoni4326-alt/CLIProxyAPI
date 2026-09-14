# Specification

CLIProxyAPI is a proxy server that provides OpenAI (including Responses), Gemini (including Interactions), Claude, and Codex compatible API interfaces over local HTTP, backed by CLI OAuth accounts and round-robin load balancing.

## Requirements

- Expose OpenAI/Gemini/Claude/Codex-compatible endpoints regardless of the upstream provider selected for a request.
- Authenticate upstream providers via stored OAuth credentials (`auths/` by default) or API keys.
- Load-balance across multiple accounts with retry, cooldown, and quota-aware failover.
- Provide a management API and UI for configuration, auth files, OAuth logins, and monitoring.
- Support hot-reload of configuration (`internal/watcher/`) and remote model registry updates (`internal/registry/`).

## Constraints

- Fork-specific fixes are protected across upstream merges; see `docs/MERGE-PRESERVATION-fork-fixes.md` before resolving any merge conflict.
- Timeouts are only allowed during credential acquisition (see `AGENTS.md` for the exhaustive exception list).
- Never log or persist secrets/tokens in plaintext logs.

## Acceptance

- `go build -o test-output ./cmd/server` must succeed after every change.
- Relevant `go test ./...` packages must pass; failures are investigated, not ignored.
- Documentation affected by a change must be updated in the same change.
