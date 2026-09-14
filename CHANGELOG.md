# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v7.2.162-arsydoni4326-alt]

### Fixed

- **Backend upstream merge conflict resolved (`upstream/main` → `bugfix/upstream-use-loopback-callback`)**: The in-progress merge of `upstream/main` (Devin follow-ups, loopback OAuth callback, plugin quota providers, Codex native fidelity) had 1 unresolved conflict in `sdk/api/handlers/openai/openai_responses_websocket.go`. Resolution kept **both** sides:
  - **Imports**: the fork's `internal/logging` (used by the live-flow observer mirroring) and upstream's `internal/util` (used by `IsCodexResponsesLiteRequest` for Codex native-fidelity detection) are both present. The merged file body already contained both features, so no feature was dropped or altered.
- **Project documentation gaps**: created the missing required root documents `CONTRIBUTING.md`, `ROADMAP.md`, `SPECIFICATION.md`, and `ARCHITECTURE.md` (none existed in git history). Content reflects the current architecture and workflow from `AGENTS.md` and `docs/`.

### Verified

- `go build -o test-output ./cmd/server` succeeds; `gofmt` clean on touched files; `go vet` clean on `sdk/api/handlers/openai/`.
- `go test ./sdk/api/handlers/... -count=1` passes (includes the websocket and forward-handler tests covering both the flow-observer mirroring and the native-fidelity path).

## [v7.2.161-arsydoni4326-alt]

### Fixed

- **Frontend submodule upstream merge completed**: The in-progress merge of `upstream/main` into `frontend` (`feature/enhance-devin-quota-fetcher` branch) had 6 unresolved conflicts, all in fork-preservation-sensitive files (`docs/MERGE-PRESERVATION-fork-fixes.md` items 5 and 8). Resolution:
  - `src/services/api/oauth.ts` and `src/pages/OAuthPage.tsx`: `startAuth` now accepts **both** the fork's `{ noProxy }` option and upstream's `AbortSignal` argument (in either position), so the **"Do Not Use Proxy" toggle (fork feature, default checked) keeps working** while Devin OAuth requests remain abortable per upstream's cancellation contract.
  - `src/i18n/locales/{en,ru,zh-CN,zh-TW}.json`: kept **both** sides — the fork's `auth_login.do_not_use_proxy` / `do_not_use_proxy_hint` keys and upstream's `devin_oauth_*` / `devin_callback_*` keys.
- **Pre-existing test failure re-confirmed as environmental (not a regression)**: `tests/updateNotification.test.ts` "renders the current and available upstream versions with repository links" fails under `bun test` because bun does not provide browser globals (`localStorage`, `window`) used by `secureStorage`; verified identical failure on the pre-merge commit `87fbf06`.

### Added

- **Upstream frontend features brought in by the merge**: Devin OAuth login flow (session cancel, callback submission, request cancellation), Devin quota fetcher with session-specific request pools, auth-file cooldown section UI, and accompanying tests (`tests/devin*.test.ts`, `tests/oauthRequestCancellation.test.ts`, `tests/authFileCooldowns.test.ts`).

### Changed

- **Static management asset**: `static/management.html` rebuilt from the merged frontend (bundle date 2026-09-14) via the documented /tmp build recipe (repo-local `frontend/node_modules` is root-owned; see `session.md`). Verified the bundle contains both `do_not_use_proxy` and `devin_oauth` strings.

## [v7.2.160-arsydoni4326-alt]
## [v7.2.160-arsydoni4326-alt]

### Added

- **OAuth "Do Not Use Proxy" toggle (fork feature)**: The management UI OAuth page gains a **"Do Not Use Proxy"** checkbox, **checked by default**. When checked, OAuth/device-code logins (Codex, Anthropic/Claude, Antigravity, Kimi, xAI/Grok) connect directly, bypassing both the configured `proxy-url` and environment proxies; unchecking restores the configured proxy for the login. The UI sends `no_proxy=true` per request on the `{provider}-auth-url` endpoints, and the Go handlers (`internal/api/handlers/management/auth_files_provider_oauth.go`) build each provider's auth service with the `"direct"` proxy override, so toggling takes effect on the next login **without restarting the service**. Frontend changes live in the `frontend` submodule (`src/pages/OAuthPage.tsx`, `src/services/api/oauth.ts`, `src/pages/OAuthPage.module.scss`, locale files) and the served `static/management.html` was rebuilt. This feature is protected across upstream merges by item 8 of `docs/MERGE-PRESERVATION-fork-fixes.md`.

### Fixed

- **Compose deployments ran the upstream image**: `docker-compose.yml` and `docker-compose.cluster.yml` set `pull_policy: always` but defaulted to the upstream image `eceasy/cli-proxy-api:latest`, so `docker compose up -d` on this fork ran a CPA build without any of the fork-specific fixes (`docs/MERGE-PRESERVATION-fork-fixes.md` items 1–6). The default is now `${CLI_PROXY_IMAGE:-ghcr.io/arsydoni4326-alt/cliproxyapi:latest}`, which is built from this repository's `Dockerfile`; set `CLI_PROXY_IMAGE=eceasy/cli-proxy-api:latest` to run upstream, or run `docker compose up -d --build` to build locally. Recreate (do not just restart) the container after changing the image. This is a deployment defect, separate from the intermittent bidirectional resets diagnosed on 2026-09-13 (see §7 of the fork-fixes document).

### Changed

- **Documentation**: `docs/MERGE-PRESERVATION-fork-fixes.md` gained item 7 plus §3.7 (compose image contract), a compose check in the §4 merge checklist, and §7 with §7.1 "Reading the CPA-side log" — the image/version check, a direct RESP `PING` probe with expected replies, and the tcpdump/firewall/conntrack checks used to localize resets that both ends observe.

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
