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
}

func newFlowHub() *flowHub {
	return &flowHub{subs: make(map[chan []byte]struct{})}
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

// sniffFlowModel extracts the "model" field (metadata only) from a JSON request
// body so the live-flow view can route the pulse to the right model node. It is
// gated on having at least one subscriber so an idle hub costs nothing. Bodies
// are restored verbatim afterwards, so handlers are unaffected.
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
	if model := gjson.GetBytes(body, "model"); model.Exists() {
		if s := strings.TrimSpace(model.String()); s != "" {
			c.Set("flow_model", s)
		}
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
