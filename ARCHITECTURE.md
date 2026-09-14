# Architecture

Go 1.26+ proxy server providing OpenAI/Gemini/Claude/Codex compatible APIs with OAuth and round-robin load balancing. This document summarizes the component layout; authoritative details live in `AGENTS.md` and `docs/`.

## Components

- `cmd/server/` — server entrypoint (`--config`, `--tui`, `--standalone`, `--local-model`, `--no-browser`, `--oauth-callback-port`).
- `internal/api/` — Gin HTTP API: routes, middleware, modules; `internal/api/modules/amp/` hosts the Amp integration; `internal/api/handlers/management/` implements the management API (including provider OAuth and Devin OAuth).
- `internal/thinking/` — thinking/reasoning pipeline: `ApplyThinking()` parses suffixes, normalizes to a canonical `ThinkingConfig`, then applies provider-specific output via `ProviderApplier`. The "canonical representation → per-provider translation" flow must not be broken.
- `internal/runtime/executor/` — per-provider executors (including the Codex WebSocket executor); helpers live in `helps/`.
- `internal/translator/` — provider protocol translators plus shared `common` utilities.
- `internal/registry/` — model registry and remote updater (`StartModelsUpdater`; `--local-model` disables remote updates).
- `internal/store/` — storage backends (file default; optional Postgres/git/object store) and secret resolution.
- `internal/cache/`, `internal/watcher/`, `internal/wsrelay/`, `internal/tui/`, `internal/managementasset/` — request signature caching, config hot-reload, WebSocket relay sessions, Bubbletea TUI, and management assets respectively.
- `sdk/cliproxy/` — embeddable SDK entry (service/builder/watchers/pipeline); `sdk/auth/` holds per-provider refresh logic.
- `internal/pluginhost/` + `sdk/pluginapi`/`sdk/pluginabi`/`sdk/pluginhost` — plugin host RPC and quota/auth provider plugins.

## Data Flow

1. Client request → Gin route (`internal/api/`) → handler (`sdk/api/handlers/`).
2. Handler resolves a model and selects a credential via the auth conductor (`sdk/cliproxy/auth/`) with cooldown, pinning, and quota signals.
3. The request is translated (`internal/translator/`) and executed by the provider executor (`internal/runtime/executor/`); thinking config is applied via `internal/thinking/`.
4. Responses stream back to the client; usage/quota signals feed cooldowns and the registry.

## Trade-offs

- Storage is file-based by default for simplicity; remote backends are opt-in via environment configuration.
- Credential rotation happens in place for HTTP modes, but WebSocket passthrough turns require client replay on credential failure (connection-scoped continuations cannot rotate credentials).
