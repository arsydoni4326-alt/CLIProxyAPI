# Session

## Current objective and progress

- Resolved the in-progress merge of `upstream/main` commit `2a6b87ac` into
  `feature/send-periodic-ping-control`.
- Retained the current branch's v7 implementations for all add/add conflicts,
  then reapplied the incoming orphan-delegation compatibility and Responses
  WebSocket periodic Ping features.

## Important decisions and discoveries

- The merge histories have no merge base. Existing-path conflicts were therefore
  add/add conflicts between the current v7 branch and divergent upstream paths.
- Current-branch files were kept to preserve the project's custom SDK, auth,
  session, management, watcher, TUI, and v7 module-path behavior.
- Incoming files that did not conflict remain included. The two incoming feature
  integrations are applied deliberately on top of the current branch.
- `streaming.keepalive-seconds` now covers SSE blank-line keep-alives and
  OpenAI Responses WebSocket Ping control frames. It remains disabled at zero.

## Verification

- Conflict index entries: none.
- Conflict-marker scan: clean.
- `gofmt` applied to all merge-modified Go files.
- Focused orphan-delegation, Responses WebSocket Ping, config propagation,
  watcher-diff, SDK auth, Home, and wsrelay tests passed.
- Session-affinity benchmark smoke run passed with `-benchtime=1x`.
- `go test -count=1 ./...` passed.
- `go build -o test-output ./cmd/server && rm test-output` passed.
- Restored two missing closing braces in the existing wsrelay test function,
  which had prevented the full test suite from compiling that package.

## Update (2026-09-06): restored metadata keys and SessionInfo.IsFork

- Symptom: `go build ./cmd/server` failed with
  `undefined: cliproxyexecutor.ParentSessionIDMetadataKey` in
  `sdk/cliproxy/session/identity.go` (and, once unblocked, further undefined
  references in `sdk/cliproxy/auth/selector.go` and `info.go`).
- Root cause: commit `101ca51f` (Merkle LCP session affinity / session tree)
  rewrote `sdk/cliproxy/executor/types.go` and accidentally dropped three
  constants that were still referenced:
  `ParentSessionIDMetadataKey` ("parent_session_id"),
  `IsForkMetadataKey` ("is_fork"), and
  `LCPAccessGenerationMetadataKey` ("lcp_access_generation"). It also dropped
  the `IsFork bool` field from `sdk/cliproxy/session/info.go`
  `SessionInfo` while `info.go` still assigned `info.IsFork = true`.
- Fix: restored the three constants (with their original doc comments) to the
  const block in `sdk/cliproxy/executor/types.go`, and re-added `IsFork bool`
  to `SessionInfo`. `IsSubagent` was not restored because no current code or
  test references it.
- Decision: restoring rather than renaming — the string values
  ("parent_session_id", "is_fork", "lcp_access_generation") are part of the
  metadata wire surface used across session identity, auth selection, and
  usage reporting.
- Verification: `go build ./...` exit 0; `go test ./sdk/cliproxy/...` all
  packages pass; `gofmt -l` clean on both edited files.

## Update (2026-09-06): proxy-url: "direct" diagnosis — no code bug found

- Symptom: OpenAI-compat auth entries with `proxy-url: "direct"` reportedly
  still route through the global SOCKS5 proxy (`socks5h://127.0.0.1:20170`).
- Investigation: traced full code path from YAML config parsing through
  `synthesizeOpenAICompat` → `auth.ProxyURL` → `defaultRoundTripperProvider.RoundTripperFor`
  → `proxyutil.BuildHTTPTransport("direct")` → `NewDirectTransport()` (Proxy=nil)
  → `NewProxyAwareHTTPClient` → transport assignment.
- Finding: the code path for `proxy-url: "direct"` is **correct in both source
  and the Aug 5 binary** (d9a76882). `NewDirectTransport()` clones
  `http.DefaultTransport` and sets `Proxy = nil`, which disables all proxy
  lookup including `http.ProxyFromEnvironment`. `http.DefaultTransport` is never
  mutated globally. No proxy env vars are set. No code path bypasses the
  auth-level ProxyURL for OpenAI-compat entries.
- The only change since the binary was built is commit a822b36c, which replaced
  `proxyURL` with `prefix` in `idGen.Next` calls — a stability improvement for
  ID generation, not a proxy-routing fix.
- Likely root cause: (1) the actual running config differs from what was
  analyzed (the on-disk `config.yaml` has `proxy-url: ""` globally), (2) entries
  without per-entry `proxy-url` inherit the global proxy by design, or (3) a
  network-level transparent proxy (iptables) is intercepting traffic.
- Next step: rebuild binary (`go build -o cli-proxy-api ./cmd/server`), verify
  the running config actually has `proxy-url: direct` on the target entries, and
  test with a single direct-entry provider to confirm.

## Update (2026-09-07): management provider-key tests honor credential proxies

- User isolated the symptom to the management-panel provider-key test; normal
  requests correctly honor `proxy-url: direct`.
- Root causes: the generic provider-key test sends its API key in the outgoing
  header without an `auth_index`, so `/v0/management/api-call` fell back to the
  global proxy. Separately, `config_auth_index.go` still included `proxy-url`
  when recreating synthesized IDs after commit `a822b36c` removed it, causing
  empty/stale auth indexes for all relevant config credentials.
- Fix: APICall now falls back to a unique API-key credential matched by its
  outgoing Bearer/X-Api-Key/X-Goog-Api-Key header and target base URL, then
  applies that credential's proxy (including `direct`). Management auth-index
  derivation now matches the synthesizer for Gemini, Interactions, Claude,
  Codex, xAI, OpenAI-compatible, and Vertex-compatible API keys.
- Added management regression tests for a no-auth-index direct provider test
  with an unusable global proxy, and for OpenAI-compatible auth-index mapping
  after a proxy setting change. Documentation updated in `README.md`.
- Verification: focused tests and `go test -count=1
  ./internal/api/handlers/management` pass; `go build -o test-output
  ./cmd/server && rm test-output` passes; `gofmt` and `git diff --check` pass.
  `go test ./...` is blocked only by the unrelated, pre-existing flaky
  `internal/home.TestEnsureClientsWaitsForPreviousTargetClose` (failed 2 of 5
  isolated runs); all management tests passed in that full-suite attempt.