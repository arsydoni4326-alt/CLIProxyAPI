# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v7.2.159-arsydoni4326-alt]

> **Fork-specific changes.** The entries below are local changes on top of
> upstream `router-for-me/CLIProxyAPI`. See
> [`docs/MERGE-PRESERVATION-fork-fixes.md`](docs/MERGE-PRESERVATION-fork-fixes.md)
> for the merge-conflict-resolution guide that keeps them alive across upstream
> merges.

### Fixed

- **Usage keeper connection resets (`connection reset by peer`)**: Two changes:
  - The Redis usage-protocol handler (`internal/api/redis_queue_protocol.go`) now replies with a proper RESP error (`ERR remote management disabled`) before closing a connection when management routes are disabled, instead of closing abruptly. Clients (e.g. `cpa-usage-keeper`) previously received a TCP RST on their pending `AUTH`/`SUBSCRIBE` read and surfaced it as `read redis subscribe auth response: read: connection reset by peer`.
  - Accepted connections now enable TCP keep-alive (`internal/api/protocol_multiplexer.go`, idle 30s / interval 15s / count 6). Long-lived idle connections (Redis usage subscriptions, streaming HTTP responses) through NAT/overlay middleboxes (e.g. CGNAT/Tailscale addresses) were silently dropped because the mappings expired. No request timeouts were introduced — this only enables keep-alive probes.
  - `TestRedisProtocol_ManagementDisabled_RejectsConnection` was updated to pin the new graceful error contract.
- **Management center raw i18n keys** (`system_info.title`, `footer.version`, `system_info.version_check_error`, etc. displayed as raw keys): Fixed in the `frontend` submodule. Locale files nested `system_info`, `footer`, `notification`, `language`, `theme`, `sidebar`, `plugin_management`, `plugin_resource`, `plugin_store`, `providersPage`, and `quota_management` under `config_management.*` while components reference them at top level; `en.json` and `ru.json` additionally lacked the full `system_info` block. All four locale files were restructured to top level and missing keys were filled in; the served `static/management.html` was rebuilt from the corrected sources. **Note:** this fix lives in the submodule (`arsydoni4326-alt/Cli-Proxy-API-Management-Center`); the submodule pointer must be kept in sync. Do not "fix" this by moving component keys under `config_management.*`.
- **Version-check `context deadline exceeded`**: The management version-check HTTP client (`internal/api/handlers/management/config_basic.go`) timeout was raised from 10s to 30s. The endpoint (`https://api.github.com/repos/arsydoni4326-alt/CLIProxyAPI/releases/latest`) is correct; the timeout was simply too short for slow links.

### Changed

- **Static management asset**: `static/management.html` regenerated from the frontend build that includes the i18n fix (bundle date 2026-09-12). It is a build artifact refreshed from `frontend/dist/index.html`, not an independent source.

---

## [v7.2.158-arsydoni4326-alt]

### Added

- **Codex model-level cooling**: New configuration option `codex.model-level-cooling` that enables per-model quota cooldown instead of credential-level cooldown. When enabled, hitting a usage limit on one Codex model only cools down that specific model, allowing sibling models to continue working.
- **RapidProxy sponsor**: Added RapidProxy as a new sponsor in README files (English, Chinese, Japanese).
- **New test coverage**: Comprehensive tests for Codex quota failover behavior, Claude executor cloaking, auth conductor cooldown/selection logic, and model registry safety.

### Changed

- **Sponsor list**: Removed RunAPI from README sponsor tables in all language versions.
- **Config defaults**: Updated `config.example.yaml` with refined retry and cooldown settings including `request-retry`, `max-retry-credentials`, `max-retry-interval`, and quota backoff configuration.
- **Claude executor cloaking**: Improved cloaking logic for Claude Code requests including better fingerprint computation and billing header generation.
- **Codex executor**: Added model-level cooling support and improved terminal error handling for quota-related failures.
- **SDK auth conductor**: Enhanced auth lifecycle management, cooldown state persistence, session affinity lookups, and result policy application.
- **Model registry**: Improved model registration, capability tracking, and availability caching.
- **Client error handling**: Added better classification for upstream request faults including `IsItemNotPersisted` detection for responses with `store: false`.

### Fixed

- **Build failure in MerklePrefixMatcher**: Fixed compilation error in LCP session lookup where `m.now()` method was undefined; replaced with direct `time.Now()` call.
- **Build failure in WatcherWrapper**: Added missing `running` field to `WatcherWrapper` struct and implemented `Running()` method in internal watcher to expose running state.
- **Codex quota behavior**: Terminal quota errors now properly cool down the affected account and fail over to alternative credentials. Model-level cooling ensures sibling models are not affected by quota limits on a single model.
- **Auth refresh and persistence**: Improved handling of auth state updates, metadata merging, and refresh scheduling.

---

## [Previous Versions]

> Older changelog entries would appear here once established.
