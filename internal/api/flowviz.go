package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// flowEvent is the bounded, metadata-only record mirrored for each request.
// It intentionally excludes prompts, bodies, headers, and credentials.
type flowEvent struct {
	ID        string `json:"id"`
	Timestamp int64  `json:"ts"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Model     string `json:"model,omitempty"`
	Status    int    `json:"status"`
	LatencyMs int64  `json:"latency_ms"`
}

// flowHub is an in-process pub-sub fan-out for flow events. It is shared-nothing:
// publishing is a non-blocking send; slow or absent subscribers cause drops, never
// backpressure on the request path. No persistence or queueing exists.
type flowHub struct {
	mu   sync.RWMutex
	subs map[chan []byte]struct{}

	// convModel remembers which model a conversation/thread uses so follow-up
	// requests that omit the "model" body field can still be routed to the right
	// node in the live-flow view. It is metadata-only, in-memory, and bounded
	// (see flowConvModelCapacity). Keys are salted per client (a short digest of
	// the auth credential — never the credential itself) so two customers that
	// reuse the same thread id never cross-resolve each other's model. Entries
	// are added opportunistically whenever a request carries both a thread key
	// and a model, and read back when a request carries only a thread key.
	convMu    sync.Mutex
	convModel map[string]string
	convOrder []string // FIFO insertion order for eviction (oldest first)
}

// flowConvModelCapacity bounds the conversation->model cache. Conversations are
// visualization-only context, so evicting the oldest beyond this cap is safe: a
// dropped entry just means one follow-up request renders against the catch-all
// "others" node instead of its model node.
const flowConvModelCapacity = 1024

func newFlowHub() *flowHub {
	return &flowHub{
		subs:      make(map[chan []byte]struct{}),
		convModel: make(map[string]string),
		convOrder: make([]string, 0, flowConvModelCapacity),
	}
}

// rememberConvModel records threadKey -> model, evicting the oldest entry once
// the capacity is exceeded. Empty inputs are ignored.
func (h *flowHub) rememberConvModel(threadKey, model string) {
	if threadKey == "" || model == "" {
		return
	}
	h.convMu.Lock()
	if _, exists := h.convModel[threadKey]; !exists {
		h.convOrder = append(h.convOrder, threadKey)
	}
	h.convModel[threadKey] = model
	for len(h.convOrder) > flowConvModelCapacity {
		oldest := h.convOrder[0]
		h.convOrder = h.convOrder[1:]
		delete(h.convModel, oldest)
	}
	h.convMu.Unlock()
}

// convModelFor returns the model previously recorded for threadKey, if any.
func (h *flowHub) convModelFor(threadKey string) string {
	if threadKey == "" {
		return ""
	}
	h.convMu.Lock()
	m := h.convModel[threadKey]
	h.convMu.Unlock()
	return m
}

// flowClientSalt derives a stable, credential-free salt from the request's auth
// headers so conversation->model mappings are isolated per client. Two customers
// that happen to reuse the same conversation/thread id (common with short ids
// like "conv-1") must not leak model routing across each other. Only a short
// one-way digest of the credential is retained — never the credential itself.
func flowClientSalt(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	secret := strings.TrimSpace(c.Request.Header.Get("Authorization"))
	if secret != "" {
		// Normalize the scheme so "Bearer sk-x" and "sk-x" resolve to the same
		// salt for the same credential. TrimPrefix runs before TrimSpace so a
		// degenerate "Bearer " (no key) collapses to empty and yields no salt.
		secret = strings.TrimSpace(strings.TrimPrefix(secret, "Bearer"))
		if secret == "" {
			return ""
		}
	} else {
		secret = strings.TrimSpace(c.Request.Header.Get("x-api-key"))
	}
	if secret == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:8])
}

// flowConvKey namespaces a thread key by the originating client's salt so the
// hub's conversation->model cache can never cross-resolve two clients that
// happen to share a thread id. Anonymous requests (no auth header) keep the
// bare thread key.
func flowConvKey(salt, threadKey string) string {
	if threadKey == "" {
		return ""
	}
	if salt == "" {
		return threadKey
	}
	return salt + "|" + threadKey
}

// normalizeFlowModel strips a thinking budget/level suffix (e.g.
// "gpt-5-codex(16384)" -> "gpt-5-codex") so budget variants of the same model
// route to one stable topology node instead of fragmenting the ring by budget.
func normalizeFlowModel(model string) string {
	model = strings.TrimSpace(model)
	if !strings.Contains(model, "(") {
		return model
	}
	if res := thinking.ParseSuffix(model); res.HasSuffix && strings.TrimSpace(res.ModelName) != "" {
		return strings.TrimSpace(res.ModelName)
	}
	return model
}

// flowHandlerTypeForPath maps a request route to the model-registry handler
// family used by the recovery fallback. Best-effort: unknown routes return "".
func flowHandlerTypeForPath(path string) string {
	switch {
	case strings.Contains(path, "/chat/completions"),
		strings.Contains(path, "/completions"),
		strings.Contains(path, "/responses"),
		strings.Contains(path, "/codex"):
		return "openai"
	case strings.Contains(path, "/messages"),
		strings.Contains(path, "/claude"):
		return "claude"
	case strings.Contains(path, "/gemini"),
		strings.Contains(path, "/v1beta"):
		return "gemini"
	}
	return ""
}

// flowRegistryFallbackModel resolves the gateway's default routed model for the
// request's API family, so a follow-up that names neither a model nor a known
// thread still renders against a meaningful node instead of the catch-all. It
// is a variable so tests can stub it without touching the global registry.
var flowRegistryFallbackModel = func(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	handlerType := flowHandlerTypeForPath(c.FullPath())
	if handlerType == "" {
		return ""
	}
	model, err := registry.GetGlobalRegistry().GetFirstAvailableModel(handlerType)
	if err != nil || model == "" {
		return ""
	}
	return model
}

// resolveFlowModel is the single model-recovery path shared by HTTP bodies and
// websocket frames. Resolution order:
//  1. a "model" field in the payload (normalized; remembered for the thread);
//  2. the hub's per-client conversation->model mapping for the thread key;
//  3. connModel: the per-connection last observed model, used for chained
//     websocket streams whose follow-up frames carry only a fresh
//     previous_response_id (never a thread key or model). It wins over the
//     registry default because it is the actual conversation model;
//  4. the model registry's first available model for the request's API family.
//
// connModel is empty for plain HTTP requests (no persistent connection, so no
// cross-request hint exists). When a thread key is present but nothing
// resolves, a debug log records the miss (warn-level would spam on legitimate
// model-less turn streams).
func resolveFlowModel(c *gin.Context, hub *flowHub, sniff []byte, connModel string) string {
	threadKey := flowThreadKey(sniff)
	convKey := flowConvKey(flowClientSalt(c), threadKey)

	var modelStr string
	if model := gjson.GetBytes(sniff, "model"); model.Exists() {
		modelStr = normalizeFlowModel(model.String())
	}
	if modelStr != "" {
		// First request (or any request that names its model): record + use it
		// so subsequent follow-ups in the same thread can be routed too.
		if convKey != "" {
			hub.rememberConvModel(convKey, modelStr)
		}
		return modelStr
	}

	// Follow-up with no "model" body field: recover the conversation's model.
	if modelStr = hub.convModelFor(convKey); modelStr != "" {
		return modelStr
	}

	// Chained websocket turn: prefer the connection's last model over the global
	// default. The fresh continuation id can never match the thread cache, so the
	// closest signal to the real conversation model is the one already seen on
	// this connection. Not remembered under the thread key — continuation ids are
	// one-shot and must not pollute the cache.
	if connModel != "" {
		return connModel
	}

	// Last resort: the gateway's default routed model for this API family so
	// the pulse still lands on a meaningful node rather than the catch-all.
	if modelStr = flowRegistryFallbackModel(c); modelStr != "" {
		if convKey != "" {
			hub.rememberConvModel(convKey, modelStr)
		}
		return modelStr
	}

	if threadKey != "" {
		path, reqID := "", ""
		if c != nil {
			path = c.FullPath()
			reqID = logging.GetGinRequestID(c)
		}
		log.Debugf("live-flow: model recovery failed (renders under catch-all node); path=%s thread=%s request_id=%s", path, threadKey, reqID)
	}
	return ""
}

// publish encodes the event and broadcasts to subscribers, dropping when any
// subscriber channel is full. Encoding only happens when there is at least one
// subscriber, so an idle hub costs a single RLock check per request.
func (h *flowHub) publish(ev flowEvent) {
	h.mu.RLock()
	if len(h.subs) == 0 {
		h.mu.RUnlock()
		return
	}
	data, err := json.Marshal(ev)
	if err != nil {
		h.mu.RUnlock()
		return
	}
	for ch := range h.subs {
		select {
		case ch <- data:
		default:
		}
	}
	h.mu.RUnlock()
}

func (h *flowHub) subscribe() chan []byte {
	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *flowHub) unsubscribe(ch chan []byte) {
	h.mu.Lock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

// flowModelSniffCap bounds how many bytes of a JSON request body are scanned
// for the leading "model" field. Model names live within the first few hundred
// bytes in practice; anything beyond the cap is ignored so uploads/streaming
// bodies never blow up memory here. The body is always restored for the
// downstream handler regardless of whether a model was found.
const flowModelSniffCap = 64 * 1024

// flowThreadKey extracts a stable conversation/thread identifier from a JSON
// request body across the API families this gateway proxies. Follow-up requests
// in a conversation frequently omit "model", but they do carry one of these
// continuation/thread keys, which lets us recover the conversation's model from
// the hub cache. Returns "" when no known thread key is present.
func flowThreadKey(body []byte) string {
	// Ordered roughly by how specific/unique each key is. The first non-empty
	// string wins.
	keys := []string{
		"previous_response_id",    // OpenAI Responses continuation
		"previous_interaction_id", // Interactions continuation
		"conversation.id",         // Anthropic-style nested conversation object
		"conversation_id",         // flat conversation id (various chat clients)
		"conversationId",          // camelCase variant (utility clients)
		"conversation",            // flat conversation (string form)
		"session_id",              // session-scoped clients
		"sessionId",
		"chat_id", // chat-threaded clients
		"thread_id",
		"parentMessageId",          // chat-clone/cli utility clients
		"metadata.conversation_id", // OpenAI Responses metadata bag
		"metadata.thread_id",
	}
	for _, k := range keys {
		if v := gjson.GetBytes(body, k); v.Exists() && v.Type == gjson.String {
			if s := strings.TrimSpace(v.String()); s != "" {
				return s
			}
		}
	}
	return ""
}

// sniffFlowModel extracts the "model" field (metadata only) from a JSON request
// body so the live-flow view can route the pulse to the right model node. It is
// gated on having at least one subscriber so an idle hub costs nothing. Bodies
// are restored verbatim afterwards, so handlers are unaffected.
//
// Follow-up requests in a conversation often omit "model". Resolution is
// delegated to resolveFlowModel: the resolved model is remembered per client
// (salted by the request's auth credential, so shared thread ids never leak
// across customers) whenever a thread key is present, recovered on follow-ups,
// and finally falls back to the gateway's default model for the API family so
// the pulse still routes to a meaningful node on every request.
func sniffFlowModel(c *gin.Context, hub *flowHub) {
	hub.mu.RLock()
	idle := len(hub.subs) == 0
	hub.mu.RUnlock()
	if idle || c == nil || c.Request == nil || c.Request.Body == nil {
		return
	}
	ct := strings.ToLower(c.Request.Header.Get("Content-Type"))
	// Accept JSON content types: application/json, application/json; charset=utf-8,
	// text/json, or any type ending with +json (e.g. application/vnd.api+json).
	// Empty content-type is also accepted for requests that omit the header
	// entirely (some CLI tools and non-standard clients do this).
	isJSON := strings.Contains(ct, "json") ||
		strings.Contains(ct, "text/json") ||
		strings.HasSuffix(ct, "+json") ||
		ct == ""
	if !isJSON {
		return
	}
	// For chunked or unknown-length bodies, attempt to read a small prefix
	// instead of rejecting outright. This captures models from streaming/
	// chunked requests where Content-Length is -1 (common for large prompts
	// or multipart-like payloads). The prefix is small enough to avoid
	// memory pressure and is always restored.
	if c.Request.ContentLength > flowModelSniffCap {
		return
	}
	limit := int64(flowModelSniffCap)
	if c.Request.ContentLength > 0 && c.Request.ContentLength < limit {
		limit = c.Request.ContentLength
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, limit))
	_ = c.Request.Body.Close()
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	c.Request.ContentLength = int64(len(body))
	c.Request.Header.Del("Content-Length") // let Go recompute on proxy
	if err != nil || len(body) == 0 {
		return
	}

	if modelStr := resolveFlowModel(c, hub, body, ""); modelStr != "" {
		c.Set("flow_model", modelStr)
	}
}

// flowVizMiddleware mirrors per-request metadata into the hub after the handler
// completes. It does not read the request body, so model is empty unless a
// handler sets the "flow_model" context key. When the model is still empty after
// the handler runs, the middleware consults the model registry's default for the
// request's API family so the pulse still routes to a meaningful node instead
// of showing no animation. Latency is derived from the trace start time
// recorded by the trace middleware.
func flowVizMiddleware(hub *flowHub) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		sniffFlowModel(c, hub)
		c.Next()

		status := c.Writer.Status()
		model, _ := c.Get("flow_model")
		modelStr, _ := model.(string)

		// Last-resort: when the body sniff + conversation cache both missed,
		// consult the registry default so the pulse lands on a real model node
		// rather than the catch-all or nowhere. This is safe here (after c.Next)
		// because it only reads the path and registry state, not the body.
		if modelStr == "" {
			modelStr = flowRegistryFallbackModel(c)
		}

		hub.publish(flowEvent{
			ID:        logging.GetGinRequestID(c),
			Timestamp: start.UnixMilli(),
			Method:    c.Request.Method,
			Path:      c.FullPath(),
			Model:     modelStr,
			Status:    status,
			LatencyMs: time.Since(start).Milliseconds(),
		})
	}
}

var flowVizUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin:     func(*http.Request) bool { return true },
}

// ServeHTTP upgrades the connection and streams flow events until the client
// disconnects or falls behind. A slow reader is dropped after one missed buffer
// rather than blocking other subscribers.
func (h *flowHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := flowVizUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()

	ch := h.subscribe()
	defer h.unsubscribe(ch)

	for data := range ch {
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if errWrite := conn.WriteMessage(websocket.TextMessage, data); errWrite != nil {
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Websocket conversation turns
// ---------------------------------------------------------------------------
//
// Websocket conversation routes (e.g. Responses over WS at /v1/responses,
// /backend-api/codex/responses) hold a single HTTP request open for the whole
// session, so flowVizMiddleware emits exactly one event — only after c.Next()
// returns on disconnect — and none while the conversation is live. To keep the
// live-flow stream animating, every request frame (response.create /
// response.append) inside an open WS session is observed as one virtual
// request: one flowEvent per turn, with Method "WS", the route template of the
// upgrade request, turn latency, and the conversation model recovered from the
// frame payload (payloads are in-memory frame bytes, never re-read from the
// wire, so capturing them is free). The sdk handler packages call the observer
// through the FlowObserver interface (declared in sdk/api/handlers, installed
// from server.go) because an sdk package cannot import internal/api.

// flowWSStatuses bounds the pending-turn table for WS requests. Pathological
// clients could complete more turns than this without surfacing an event; the
// cap keeps one connection's bookkeeping bounded (eviction simply means a turn
// renders without its start timestamp, never a leak).
const flowWSStatuses = 256

type flowWSTurn struct {
	model string
	start time.Time
}

// flowWSRequestState tracks the pending turns of one websocket-based HTTP
// request: resolved model (from payload or thread cache) + turn start, keyed by
// the turn's request id. Entries are removed on completion, so steady-state
// memory holds only in-flight turns.
//
// lastModel is the most recent model resolved on this connection, used to route
// chained Responses streams whose follow-up frames carry only a fresh
// previous_response_id (never a thread key or model). It is scoped to the
// request, so two clients on different connections can never observe each
// other's model resolution.
type flowWSRequestState struct {
	mu        sync.Mutex
	turns     map[string]flowWSTurn
	lastModel string
}

func newFlowWSRequestState() *flowWSRequestState {
	return &flowWSRequestState{turns: make(map[string]flowWSTurn, 8)}
}

// flowWSStateKey is the Gin context key under which *flowWSRequestState is
// stored. Raw strings matching this exact text convert to the key cleanly.
const flowWSStateKey = "cpa_flow_ws_state"

// wsFlowObserver implements handlers.FlowObserver against a flowHub.
type wsFlowObserver struct {
	hub *flowHub
}

// ginContextFrom resolves the originating request's Gin context of a websocket
// turn. The execution context passed to the hooks is derived from
// c.Request.Context() with the Gin context stored under the "gin" key (the
// established idiom across executor-level code paths). Returns false when the
// hook was called with a context that cannot be traced back — the event is
// simply skipped rather than guessed at.
func ginContextFrom(ctx context.Context) (*gin.Context, bool) {
	if ctx == nil {
		return nil, false
	}
	v := ctx.Value("gin")
	c, ok := v.(*gin.Context)
	return c, ok && c != nil
}

// hasSubscribers reports whether the hub currently has at least one live-flow
// consumer. All sniffing/bookkeeping short-circuits on this so a WS session
// without viewers costs effectively nothing.
func (h *flowHub) hasSubscribers() bool {
	h.mu.RLock()
	ok := len(h.subs) > 0
	h.mu.RUnlock()
	return ok
}

// WSMessageStart implements handlers.FlowObserver. It resolves the turn's model
// via resolveFlowModel (the same salted thread-key recovery used for HTTP
// bodies, plus a per-connection fallback for chained Responses streams), then
// records it under the request id so the matching completion can publish
// precise metadata.
func (o *wsFlowObserver) WSMessageStart(ctx context.Context, requestID string, payload []byte) {
	hub := o.hub
	if hub == nil || !hub.hasSubscribers() {
		return
	}
	c, ok := ginContextFrom(ctx)
	if !ok || c.Request == nil {
		return
	}

	// Cap oversized frames defensively: model/thread keys live in the opening
	// bytes of realistic frames; scanning remains bounded without touching
	// memory ownership of the caller's payload.
	sniff := payload
	if len(sniff) > flowModelSniffCap {
		sniff = sniff[:flowModelSniffCap]
	}

	state := getFlowWSState(c)

	// Resolve against the payload, per-client thread cache, then the
	// connection's last observed model (chained streams carry only fresh
	// continuation ids that never match the thread cache); the registry default
	// is only consulted when the connection has no model yet.
	modelStr := resolveFlowModel(c, hub, sniff, state.lastModel)
	if modelStr == "" {
		log.Debugf("live-flow: ws turn model unresolved; request_id=%s turn=%s", logging.GetGinRequestID(c), requestID)
	}

	state.mu.Lock()
	if modelStr != "" {
		state.lastModel = modelStr
	}
	// Evict one entry at capacity (any key — eviction merely means that turn's
	// completion renders without a start timestamp; never a leak).
	if len(state.turns) >= flowWSStatuses {
		for k := range state.turns {
			delete(state.turns, k)
			break
		}
	}
	state.turns[requestID] = flowWSTurn{model: modelStr, start: time.Now()}
	state.mu.Unlock()
}

// WSMessageComplete implements handlers.FlowObserver: it publishes one event
// per websocket turn, reusing the route's request id so a turn correlates with
// its parent connection in the UI. Status comes straight from the turn outcome
// (200 ok; 4xx/5xx on error). Latency is measured from WSMessageStart.
func (o *wsFlowObserver) WSMessageComplete(ctx context.Context, requestID string, status int) {
	hub := o.hub
	// Short-circuit on an idle hub, mirroring WSMessageStart: turns can only
	// have been recorded while a subscriber was attached, so with no
	// subscribers there is nothing to complete (and no gin context to touch).
	if hub == nil || !hub.hasSubscribers() {
		return
	}
	c, ok := ginContextFrom(ctx)
	if !ok || c.Request == nil {
		return
	}
	state := getFlowWSState(c)
	state.mu.Lock()
	turn, exists := state.turns[requestID]
	if exists {
		delete(state.turns, requestID)
	}
	state.mu.Unlock()
	// Completions without a recorded start (before the feature engaged, or
	// evicted) skip publishing rather than fabricating metadata.
	if !exists {
		return
	}

	hub.publish(flowEvent{
		ID:        logging.GetGinRequestID(c),
		Timestamp: turn.start.UnixMilli(),
		Method:    "WS",
		Path:      c.FullPath(),
		Model:     turn.model,
		Status:    status,
		LatencyMs: time.Since(turn.start).Milliseconds(),
	})
}

// getFlowWSState fetches (lazily creating) the per-request WS turn table.
// Writing on first use is safe: the Responses WS handler is the only goroutine
// that calls the observer for a single request (the frame read loop is
// single-threaded per connection).
func getFlowWSState(c *gin.Context) *flowWSRequestState {
	if v, exists := c.Get(flowWSStateKey); exists {
		if state, ok := v.(*flowWSRequestState); ok && state != nil {
			return state
		}
	}
	state := newFlowWSRequestState()
	c.Set(flowWSStateKey, state)
	return state
}

// newFlowWSObserver builds the observer that server.go installs into
// sdk/api/handlers when flow visualization is enabled.
func newFlowWSObserver(hub *flowHub) handlers.FlowObserver {
	if hub == nil {
		return nil
	}
	return &wsFlowObserver{hub: hub}
}
