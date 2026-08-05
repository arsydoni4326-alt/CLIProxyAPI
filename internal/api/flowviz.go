package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
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
	// (see flowConvModelCapacity). Entries are added opportunistically whenever a
	// request carries both a thread key and a model, and read back when a request
	// carries only a thread key.
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
		"conversation",            // flat conversation (string form)
		"session_id",              // session-scoped clients
		"sessionId",
		"chat_id", // chat-threaded clients
		"thread_id",
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
// Follow-up requests in a conversation often omit "model". When the body carries
// a thread key, the mapping to the resolved model is remembered; when the body
// carries only a thread key, the model is recovered from that mapping so the
// pulse is still routed to the correct model node on every request.
func sniffFlowModel(c *gin.Context, hub *flowHub) {
	hub.mu.RLock()
	idle := len(hub.subs) == 0
	hub.mu.RUnlock()
	if idle || c == nil || c.Request == nil || c.Request.Body == nil {
		return
	}
	if ct := c.Request.Header.Get("Content-Type"); !strings.Contains(ct, "json") {
		return
	}
	// Never risk truncating an oversize or unknown-length body: LimitReader would
	// silently drop the tail before restore. Only bodies small enough to fit the
	// cap (per declared Content-Length) are sniffed.
	if c.Request.ContentLength < 0 || c.Request.ContentLength > flowModelSniffCap {
		return
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, flowModelSniffCap))
	_ = c.Request.Body.Close()
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	c.Request.ContentLength = int64(len(body))
	c.Request.Header.Del("Content-Length") // let Go recompute on proxy
	if err != nil || len(body) == 0 {
		return
	}

	threadKey := flowThreadKey(body)
	var modelStr string
	if model := gjson.GetBytes(body, "model"); model.Exists() {
		modelStr = strings.TrimSpace(model.String())
	}

	if modelStr != "" {
		// First request (or any request that names its model): record + use it so
		// subsequent follow-ups in the same thread can be routed too.
		if threadKey != "" {
			hub.rememberConvModel(threadKey, modelStr)
		}
		c.Set("flow_model", modelStr)
		return
	}

	// Follow-up request with no "model" body field: recover the conversation's
	// model so the live-flow pulse still routes to the right node.
	if modelStr = hub.convModelFor(threadKey); modelStr != "" {
		c.Set("flow_model", modelStr)
	}
}

// flowVizMiddleware mirrors per-request metadata into the hub after the handler
// completes. It does not read the request body, so model is empty unless a
// handler sets the "flow_model" context key. Latency is derived from the trace
// start time recorded by the trace middleware.
func flowVizMiddleware(hub *flowHub) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		sniffFlowModel(c, hub)
		c.Next()

		status := c.Writer.Status()
		model, _ := c.Get("flow_model")
		modelStr, _ := model.(string)
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
