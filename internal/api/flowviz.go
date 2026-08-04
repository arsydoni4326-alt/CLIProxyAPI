package api

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
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

// flowVizMiddleware mirrors per-request metadata into the hub after the handler
// completes. It does not read the request body, so model is empty unless a
// handler sets the "flow_model" context key. Latency is derived from the trace
// start time recorded by the trace middleware.
func flowVizMiddleware(hub *flowHub) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
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
