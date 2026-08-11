# Session Context

## Current Objective

Create comprehensive developer/operator documentation following the merge from `origin/main`, consolidating architecture, SDK extension points, configuration surface, and operational guidance. Documentation-only task; no source code modifications.

## Completed Work

### 1. Created `docs/internals-overview.md`

A new comprehensive architecture and operations guide (~200 lines) covering:

- **Section 1 — Project Overview:** Module path (`github.com/router-for-me/CLIProxyAPI/v7`), Go 1.26+ requirement, unified API surface (OpenAI/Gemini/Claude/Codex/Grok/Kimi), OAuth + API-key auth, multi-account pooling, hot-reload capability.
- **Section 2 — Architecture & Key Directories:** Full map of `cmd/`, `internal/api/`, `internal/thinking/` (canonical reasoning pipeline, ProviderApplier), `internal/runtime/executor/` (+ `helps/` rule), `internal/translator/` (edit restrictions), `internal/registry/`, `internal/store/`, `internal/auth/`, `internal/watcher/`, `sdk/cliproxy/`, `sdk/access/`, `sdk/api/`. Code conventions imported verbatim from `AGENTS.md` (KISS, gofmt, compile-check command, defer-close pattern, network-timeout exceptions list).
- **Section 3 — Configuration & Operation:** Full `config.yaml` surface table (host/port/TLS, remote-management, auth-dir, api-keys, plugins, routing strategies incl. weighted-round-robin max weight 1,000,000, Claude cloaking, Codex live-media-relay, payload rewriting with gjson/sjson). Storage backends (file/PGSTORE_*/GITSTORE_*/OBJECTSTORE_*).
- **Section 4 — Extension, SDK, and Hot Reload:** Builder pattern usage (`NewBuilder`, `WithServerOptions`, `WithCoreAuthManager`), `sdk/access` provider chain (RegisterProvider, config-api-key built-in, custom provider pattern, hot-reload via `configaccess.Register` + `SetProviders`), and the watch/diff contract (shadow snapshot, `AuthUpdate{add|modify|delete}`, buffered channel capacity 256, `consumeAuthUpdates` goroutine, coalescing per credential ID, back-pressure absorption).
- **Section 5 — Credentials, Security, & Best Practices:** Secret auto-hashing, auth-dir permissions, network binding, cloaking bypass implications, plugin trust (in-process `.so`), SDK auth chain configuration guidance.
- **Section 6 — Advanced Features:** Model pooling/aliasing YAML example (`openai-compatibility` with alias, fork, display-name, force-mapping, is-compat, thinking levels). Routing strategy definitions. Payload rewriting via gjson/sjson. WebSocket auth and flow visualization endpoints.
- **Section 7 — Ecosystem:** help.router-for.me, MANAGEMENT_API.md, external usage tracking (CPA Usage Keeper, CPA-Manager-Plus), third-party client list pointer.
- **Section 8 — Testing, Deployment, & Contributing:** `go test ./...`, compile verification command, Docker/compose quickstart, contributing workflow with AGENTS.md compliance steps.
- **Section 9 — Recommendations for Future Work:** 10 prioritized items (credential/event auditing, fine-grained RBAC, config validation/linting, live provider/model inspection, SSO/enterprise identity examples, credential caching with hooks, enhanced admin API/SDK surface, hot-reload error surface in panel/TUI, expanded integration test docs, i18n infrastructure).
- **See Also:** Cross-links to `sdk-usage.md`, `sdk-advanced.md`, `sdk-access.md`, `sdk-watcher.md`, and root `README.md`.

### 2. Updated `README.md`

Added a link to the new internals overview in the **SDK Docs** section:

```markdown
- Internals Overview: [docs/internals-overview.md](docs/internals-overview.md)
```

### 3. Created `session.md` (this file)

Established project-root session context per `.clinerules/project-guidelines.md` requirements.

## Implementation Decisions

- **File naming:** Used `internals-overview.md` instead of `ARCHITECTURE.md` to avoid confusion with `AGENTS.md` and to indicate this is a high-level operational/architecture map rather than an authoritative design doc.
- **Content consolidation:** Imported conventions directly from `AGENTS.md` to keep contributor rules visible in the docs hierarchy without duplicating the file.
- **Cross-linking:** Ensured bidirectional linkage between `README.md` → `internals-overview.md` → `sdk-*.md` files so readers can navigate from quick start → internals → specific SDK topics.
- **Future work section:** Derived from observed capabilities in `config.example.yaml` (e.g., flow-visualization, cloaking, routing), the SDK extension points (custom providers, watcher queue), and gaps in current observability/enterprise features.

## Unresolved / Pending

- None. All documentation work is complete and consistent with the current codebase state.

## Important Discoveries

- The `internal/thinking/` pipeline uses a canonical `ThinkingConfig` representation translated per-provider; this architecture must be preserved in any future changes (noted in docs).
- The `sdk/access` hot-reload contract requires both `configaccess.Register(&newCfg.SDKConfig)` and `accessManager.SetProviders(sdkaccess.RegisteredProviders())` to propagate config changes.
- The watcher uses a 256-capacity buffered channel with coalescing per credential ID to prevent hot-reload back-pressure from blocking producers.
- `internal/translator/` files require explicit repository write permission to edit (enforced by convention; check via `gh repo view --json viewerPermission -q .viewerPermission`).

## Known Limitations

- No `MANAGEMENT_API.md` exists in the repository root; it is hosted externally at help.router-for.me. The new doc references this correctly.
- `test/README.md` does not exist; the note in `internals-overview.md` says "where present" to remain accurate if integration tests are added later.

## Relevant Test Results

- No tests were run (documentation-only change). Compile verification (`go build -o test-output ./cmd/server && rm test-output`) is documented but not executed in this session.
