# Session Context

## Current Objective

Create dedicated documentation (`docs/MERGE-2026-08-16-main-merge-auth-translator-perf.md`) for merge commit `8a71d358` (origin/main merged into dev; parents 8b8aaea8 / 7efe0a7c), then update this file. Documentation-only task; no source code modifications. The merge covers: session-affinity and rebound-session safety (new SDK `sdk/cliproxy/auth/session_cache.go`), request-scoped 401 fault cooldown exclusions, Anthropic unified rate-limit parsing (`internal/runtime/executor/helps/claude_ratelimit.go`), translator correctness plus batched raw-array allocation performance, OpenAI Responses websocket transcript handling, model registry (max_completion_tokens), and skipping inactive request interceptors.

### Latest Merge Reviewed

The most recently documented merge is `8a71d358` — see `docs/MERGE-2026-08-16-main-merge-auth-translator-perf.md` for the full write-up (session-affinity and rebound-session safety, request-scoped fault cooldown exclusions, Anthropic unified rate limits, translator dedup and allocation performance, OpenAI Responses websockets, model registry, interceptor skipping; 27 commits / 105 files, 8 future-work recommendations).
Previous: `ea8c8b02` — see `docs/MERGE-2026-08-15-auth-metadata-mcp-alias.md` (auth metadata merge, BIP-39 MCP aliases, model registry, test coverage, 10 future-work recommendations).

The previously documented staged merge (still summarized in the sections below) spans **69 files, ~5,060 insertions, ~707 deletions** across all major subsystems.

---

## Merge Impact by Domain

### 1. Config System (`internal/config/`)

**Per-credential `RequestRetry *int`** added to every provider key struct:

| Struct | File |
|--------|------|
| `ClaudeKey.RequestRetry` | `config_types.go` |
| `CodexKey.RequestRetry` | `config_types.go` |
| `GeminiKey.RequestRetry` | `config_types.go` |
| `OpenAICompatibility.RequestRetry` | `config_types.go` |
| `VertexCompatKey.RequestRetry` | `vertex_compat.go` |

Semantics:
- `nil` or negative → use global `request-retry`
- `0` → disable retries for this credential
- positive → override global retry count

New test files: `request_retry_test.go` (73 lines), `xai_api_key_test.go` (4-line extension).

`config.example.yaml` updated (+9 lines) with documentation comments.

### 2. Hot-Reload / Watcher / Synthesizer (`internal/watcher/`, `internal/watcher/diff/`, `internal/watcher/synthesizer/`)

- **`config_diff.go`** (+31 lines): `getOpenAICompatRequestRetry()` and `getOpenAICompatDisabled()` — new diff keys for openai-compat request-retry and disabled status.
- **`openai_compat.go`** (+3 lines): Map `openai_compat_disabled` and `openai_compat_request_retry` to DiffAction entries.
- **`config_diff_test.go`** (+5 lines): Extended to cover new diff keys.
- **`synthesizer/config.go`** (+11 lines): New logic to skip disabled openai-compat providers and emit warnings for unsupported provider-type request-retry mapping.
- **`synthesizer/config_test.go`** (+84 lines): Tests for disabled-provider exclusion and request-retry mapping.
- **`synthesizer/helpers.go`** (+9 lines): `getOpenAICompatRequestRetry()` helper function.
- **`synthesizer/helpers_test.go`** (+32 lines): Tests for the new helper.

### 3. Management API (`internal/api/handlers/management/`)

- **`api_tools.go`** (+43 lines): New endpoint `/v0/management/config/api-tools/config-file` for reading the raw config file. New tool definitions for auth index.
- **`api_tools_test.go`** (+99 lines): Tests for the new config-file endpoint and tool definitions.
- **`config_auth_index.go`** (+2 lines): Extended auth index with new tool references.
- **`config_lists.go`** (+28 lines): Expanded OpenConfig provider lists.
- **`config_openai_compat_test.go`** (+6 lines): Tests for new openai-compat config fields.
- **`config_xai_key_test.go`** (+6 lines): Extended XAI key config tests.
- **`server_test.go`** (+2 lines): Server test scaffolding updates.

### 4. Runtime Executors (`internal/runtime/executor/`)

**OpenAI Compat Executor** (`openai_compat_executor.go`, +173 lines):
- **Major rewrite** of SSE frame parsing in `ExecuteStream()`. Introduced `frameData [][]byte` + `upstreamEvent string` accumulation.
- New `processFrame()` closure validates accumulated frame before translation:
  - `openAICompatErrorEvent(eventName)` — detects upstream error event names (`"error"`, `"response.error"`, `"response.failed"`) → publishes `statusErr{code: 502}`.
  - `openAICompatStreamDataError(dataPayload, eventName)` — inspects JSON payload for error fields and extracts numeric status code.
- New `compactResp` and `CompactResponsesResponse` methods for response compaction.
- New test file `openai_compat_executor_compact_test.go` (+239 lines).

**Kimi Executor** (`kimi_executor.go`, +8 lines):
- Added `TranslateStreamWithClaudeInputTokens()` — enables unified frame processing for Kimi streams.

**Codex WebSockets** (`codex_websockets_session.go`, `codex_websockets_stream.go`, `codex_websockets_execute.go`):
- New `SessionResponse{HasMore, SessionID}` struct and `SessionEventType` enum for session-state tracking.
- `sseDone` channel introduced to handle orphan downstream connections cleanly.
- `codex_websockets_spawn_agent_test.go` (+143 lines): Comprehensive spawn_agent simulation tests.
- `codex_multi_agent_v2.go` (+6 lines): Extended multi-agent v2 logic.

**Claude Executor** (`claude_executor_request.go`, +7 lines):
- New `anthropic_claude_code_config` handling for Claude Code configuration forwarding.

### 5. Protocol Translators (`internal/translator/`)

**New shared helper** (`internal/translator/common/gemini.go`, 8 lines):
- `IsGeminiThoughtPart(part gjson.Result) bool` — checks whether a Gemini part has `"thought": true`.

**All three "inbound" Gemini translators now strip hidden Gemini thought parts:**

| Translator | Lines Added | Behavior |
|-----------|-------------|----------|
| `claude→gemini` | +7 | `IsGeminiThoughtPart` in system instruction + per-content translation |
| `codex→gemini` | +7 | Same thought-part stripping |
| `openai→gemini` | +14 | Same thought-part stripping |

All three have corresponding test additions (+30 lines each) with `TestConvertGeminiRequestTo*_DropsHiddenThoughtParts`.

**OpenAI Responses → Responses** (`openai_openai-responses_response.go`, +3 lines):
- New `injectThinkingMetadata()` function for thinking metadata injection in response translation.

### 6. SDK Stream/Image/Responses/WebSocket Handlers (`sdk/api/handlers/`)

**Stream Forwarder** (`stream_forwarder.go`, +63 lines; `stream_forwarder_test.go` new, 84 lines):
- `PendingStreamError(errs <-chan *interfaces.ErrorMessage) (*interfaces.ErrorMessage, bool)` — non-blocking peek into error channel.
- `StreamForwardOptions{ChunkError func() *interfaces.ErrorMessage, NormalizeTerminalError func(*interfaces.ErrorMessage) *interfaces.ErrorMessage}` — new callbacks for mid-stream error detection and error normalization.

**Results Handlers** (`handlers_stream.go`, +179 lines; `handlers_stream_bootstrap_test.go`, +29 lines):
- `responsesSSEFramer` — new framer that injects errors mid-stream (e.g., `response.failed` payloads detected during `WriteChunk`).
- Bootstrap error detection improvements.

**OpenAI Responses Handlers** (`openai_responses_handlers.go`, +463 lines):
- New event types: `response.code_thinking.delta`, `response.file_search_call.completed`, `response.computer_use_call.*`, `response.action.*`.
- `localStorage` history tracking for conversation continuity.
- Stall timer (12s) for detecting hung responses.
- `handleForwardedData` and `handleStreamEvent` restructuring.

**OpenAI Responses Stream Tests** (`openai_responses_handlers_stream_test.go`, +99 lines):
- New test cases covering code_thinking.delta, file_search_call, computer_use_call events.

**OpenAI Responses Stream Error Tests** (`openai_responses_handlers_stream_error_test.go`, +786 lines):
- Comprehensive error-handling tests: upstream errors, client disconnects, mid-stream failures.

**WebSocket Responses** (`openai_responses_websocket.go`, `openai_responses_websocket_requests.go`, `openai_responses_websocket_test.go`):
- `openai_responses_websocket.go` (+20 lines): `cleanupPrewarm()` to prevent stale prewarm goroutines.
- `openai_responses_websocket_prewarm.go` (-28 lines): Removed (functionality subsumed).
- `openai_responses_websocket_requests.go` (+280 lines): Major rewrite of WS request/response handling.
- `openai_responses_websocket_test.go` (+255 lines): Extended WS test coverage.

**WebSocket Tool-Call Repair** (`openai_responses_websocket_toolcall_repair.go`, +341 lines):
- Major rewrite of tool-call repair logic with `FailedRequestRule` support.

**Image Handlers** (`openai_images_handlers.go`, +291 lines; `openai_images_handlers_test.go`, +158 lines):
- New `ImageResult{OutputFormat string, Result string}` struct.
- `forwardImagesStream` rewritten with SSE frame-aware processing.
- `emitError` now returns the error message for chainable error handling.
- `mimeTypeFromOutputFormat` simplified to use `img.OutputFormat`.
- `response.completed` event handling moved into per-image loop with `handleFrame(ctx, cancel, emitError, writeEvent, responseFormat)`.

**Routing** (`handlers_routing.go`, +2 lines): Routing logic updates for new provider types.

### 7. Model Registry (`internal/registry/`)

**Hardcoded Builtins** (`model_definitions.go`, +16 lines):
- `xaiBuiltinImage20ModelID = "grok-imagine-image-2.0"`.
- `xaiBuiltinImage20ModelInfo()` — returns `ModelInfo{ID: "grok-imagine-image-2.0", OwnedBy: "xai", Type: "xai", DisplayName: "Grok Imagine Image 2.0", Created: 1786060800}`.
- `WithXAIBuiltins()` now includes `xaiBuiltinImage20ModelInfo()`.

**models.json** (+1214 lines, major restructure):
- New model: `grok-4.6` (500K context, 65,536 max completion tokens, thinking levels [low/medium/high/xhigh], text+image input, text output).
- Massive croissant-metadata restructuring across all providers — description, branding metadata, and display attributes updated/corrected.

### 8. SDK Auth Types (`sdk/cliproxy/auth/`)

- `types.go` (+7 lines): Added `TypeOpenConfig = "openconfig"` constant and `OpenConfig` struct for OpenConfig OAuth credentials.
- `types_test.go` (+42 lines): Tests for `TypeOpenConfig` serialization and `OpenConfig` struct fields.

---

## Implementation Decisions

- **Coverage:** This update documents all 69 files changed in the merge, organized by domain.
- **Content consolidation:** New features are documented inline in the relevant sections of `internals-overview.md` rather than creating separate subsection files.
- **Cross-linking:** All new features are cross-referenced with relevant SDK docs and the example config.
- **Future work section:** 5 new recommendations added, derived from this merge (SSE frame standardization, WS session lifecycle formalization, response compaction contract, croissant metadata validation, OpenConfig provider expansion).

## Unresolved / Pending

- The prewarm removal (`openai_responses_websocket_prewarm.go`) should be verified for backward compatibility with older management panel versions.
- The croissant metadata restructuring in `models.json` changes many provider descriptions simultaneously — this may affect any tooling that reads model metadata from the file directly.

## Important Discoveries

- **Per-credential retry override:** The `RequestRetry *int` field is now uniform across all 5 provider key structs, with consistent semantics (nil/neg→global, 0→disable, positive→override). This simplifies credential-level retry policy.
- **SSE frame-aware buffering:** The openai-compat executor now properly buffers multi-line SSE frames before dispatching to the translator, fixing a class of streaming errors where partial frames were processed.
- **Thought-part detection:** `IsGeminiThoughtPart()` is now a shared utility, used by all three Gemini translators. This prevents hidden reasoning parts from leaking into non-Gemini protocols.
- **StreamForwarder callbacks:** `ChunkError` and `NormalizeTerminalError` enable mid-stream error injection without changing the forwarder loop — an important extension pattern for embedders.
- **Stall timer:** The new 12-second stall timer in responses handlers prevents hung streams from consuming resources indefinitely.
- **Config-file API endpoint:** `/v0/management/config/api-tools/config-file` enables external tooling to read the raw config programmatically.
- **OpenConfig auth type:** `TypeOpenConfig` expands the auth ecosystem for third-party OpenConfig-compatible providers.
- **grok-4.6 and grok-imagine-image-2.0:** Two new models extending the xAI provider's capability surface.
- **Disabled provider handling:** The synthesizer now excludes disabled openai-compat providers from diffs, preventing hot-reload conflicts.

## Known Limitations

- No documentation for the Management API config-file endpoint beyond what's in the test file — should be documented in the external `MANAGEMENT_API.md`.
- The stall timer default (12s) is hardcoded and not configurable — may need a config key for different latency environments.
- `IsGeminiThoughtPart` uses a simple `"thought": true` check — future Gemini model changes could introduce new thought-part structures.

## Documentation Completion — Round 2

The `docs/internals-overview.md` has been updated with **4 additional future work recommendations** (items 17–20) derived from the merge analysis:

| # | Recommendation | Motivation |
|---|---------------|-----------|
| 17 | Configurable Stall Timer | The 12-second stall timer in OpenAI Responses handlers is hardcoded — expose as config key |
| 18 | Per-Credential Request-Retry UI | All 5 provider key structs support `RequestRetry *int` but no management panel UI exists |
| 19 | Disabled-Provider Observability | Only openai-compat providers emit warnings when disabled; extend to all provider types with Management API surface |
| 20 | Gemini Thought-Part Schema Contract | `IsGeminiThoughtPart()` uses fragile `"thought": true` check; formalize part schema contract |

Total future work recommendations now: **20 items** (items 1–5 from this merge, items 6–16 from previous work, items 17–20 newly added).

The `internals-overview.md` body (sections 1–8) was already fully documented in the previous session. Only the future work section was touched in this pass.

## Relevant Test Results

- New tests added across config, watcher, executor, translator, and handler domains.
- No test failures expected from documentation-only changes.
## 2026-08-15 — SDK & Example Growth (Phase 3.4) SHIPPED

- New `examples/access-hook/` — offline self-checking `sdk/access` embedding demo (custom providers, chaining, aggregated auth errors), exits non-zero on failure.
- Docs EN+CN: `docs/EXAMPLE-{CUSTOM-PROVIDER,TRANSLATOR,ACCESS-HOOK}.md` + `_CN.md`.
- Root `Makefile` `verify-examples` gate (gofmt/vet/build all examples + runtime smoke of offline-capable access-hook & translator). `make verify-examples` green.
- No changes to `sdk/` packages; root ROADMAP §3.4 SHIPPED.

## 2026-08-16 — Rate Limiting & IP Blocking Tuning (Phase 3.7) SHIPPED

- `internal/config/config_types.go`: added `remote-management.max-failed-attempts` (default 5) + `remote-management.ban-duration` (default 30m) with `MaxFailures()`/`BanWindow()` accessors; defaults in one place.
- Blocking logic reads the accessors at auth time → hot-tunable via the existing config reload (no new machinery).
- `GET /v0/management/status` reports `blocked_ips` (`ip`, `remaining`, sorted, expired excluded) — `status_test.go` pins format + expiry exclusion.
- `handler_test.go`: localhost-IP ban blocks the correct key during ban; remote-enabled threshold ban; custom-threshold edge.
- `config.example.yaml` documents both keys.
- Root ROADMAP §3.7 ✅ SHIPPED; docs (ARCHITECTURE/SPECIFICATION) synced; validated `go build`/`go vet`/`gofmt` clean, management suite green (pre-existing `claude_executor` drift aside).

## 2026-08-16 — Dependency hygiene (Phase 3.8) — SBOM + audit gates

- CI: `pr-test-build.yml` gained a `govulncheck` step (pinned `golang.org/x/vuln/cmd/govulncheck@v1.7.0`, module cache + gobin cached), fails closed; release `release.yaml` gained an `sbom` job (`anchore/sbom-action@v0.24.0`, `dependency-snapshot: false`, `upload-release-assets: true`) emitting `cli-proxy-api-sbom.cdx.json`.
- Umbrella release (root `.github/workflows/release.yml`) also generates this SBOM + `.sha256` and records it in the release manifest.
- Root `Makefile` `check-deps` mirrors the gate locally (`go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...` + syft SBOM at `CLIProxyAPI/backend.sbom.cdx.json`).
- Policy: `docs/DEPENDENCY_POLICY.md`; root ROADMAP §3.8 🟢 in progress. No runtime code changes (net-new deps 0).
