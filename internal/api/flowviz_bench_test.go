package api

// Benchmarks for the live-flow websocket turn observer (flowviz.go). These are
// guardrails for the lightweight constraint: the observer rides on the hottest
// proxy paths (every Responses-over-WebSocket frame), so it must stay
// effectively free when nobody is watching and bounded when someone is.
//
// They live in _test.go only: zero impact on the production binary.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
)

// benchPayload is a realistic Codex/Responses-over-WebSocket frame. It carries
// both a leading "model" field and a continuation key so benchmarks exercise
// the real sniffing path.
var benchPayload = []byte(`{"model":"gpt-5-codex","previous_response_id":"resp_bench","input":[{"type":"text","text":"hi"}]}`)

// newWSBenchContext mirrors the unit-test helper newWSContext but for
// benchmarks: a Gin context bound to a registered route (so c.FullPath()
// resolves) plus the execution context the sdk handler would pass to the
// observer (gin context under the "gin" key).
func newWSBenchContext(b *testing.B) (*gin.Context, context.Context) {
	b.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Handle(http.MethodPost, "/v1/responses", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	engine.HandleContext(c)
	if got := c.FullPath(); got != "/v1/responses" {
		b.Fatalf("route binding failed: FullPath() = %q", got)
	}
	logging.SetGinRequestID(c, "req-bench")
	exec := context.WithValue(context.Background(), "gin", c)
	return c, exec
}

// BenchmarkWSMessageStartIdleHub is the headliner for the lightweight
// constraint: a WS session with no live-flow viewer must cost almost nothing.
// WSMessageStart short-circuits on hasSubscribers() before touching the gin
// context or payload; expect a single RLock + branch (~few ns, 0 allocs).
func BenchmarkWSMessageStartIdleHub(b *testing.B) {
	_, exec := newWSBenchContext(b)
	hub := newFlowHub() // no subscribers
	obs := newFlowWSObserver(hub)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		obs.WSMessageStart(exec, "ws-turn", benchPayload)
	}
}

// BenchmarkWSMessageCompleteIdleHub measures the completion path with no live
// viewers. Like WSMessageStart, it short-circuits on hasSubscribers() before
// touching the gin context or turn table — an idle hub can hold no recorded
// turns, so completion on one is definitionally a no-op. Expect a single
// RLock + branch (~few ns, 0 allocs), symmetric with the start path.
func BenchmarkWSMessageCompleteIdleHub(b *testing.B) {
	_, exec := newWSBenchContext(b)
	hub := newFlowHub() // no subscribers
	obs := newFlowWSObserver(hub)
	// Warm up once so any one-time branch/predictor effects are amortized out
	// of the steady-state metric.
	obs.WSMessageComplete(exec, "never-started", 404)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		obs.WSMessageComplete(exec, "never-started", 404)
	}
}

// BenchmarkWSMessageStartActiveHub measures one turn-start while a live-flow
// viewer is connected: the full sniff, thread-key parse, conversation-model
// recovery, and state insert.
func BenchmarkWSMessageStartActiveHub(b *testing.B) {
	_, exec := newWSBenchContext(b)
	hub := newFlowHub()
	ch := hub.subscribe() // one viewer
	defer hub.unsubscribe(ch)
	obs := newFlowWSObserver(hub)
	// Warm up so the one-time lazy state creation is amortized out of the
	// steady-state metric.
	obs.WSMessageStart(exec, "ws-turn", benchPayload)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		obs.WSMessageStart(exec, "ws-turn", benchPayload)
	}
}

// BenchmarkWSMessageTurnActiveHub measures one complete conversation turn with
// a live viewer: start (sniff + record) then complete (lookup + publish). A
// draining goroutine models an actively-reading Live Flow page; the publish
// send is non-blocking regardless.
func BenchmarkWSMessageTurnActiveHub(b *testing.B) {
	_, exec := newWSBenchContext(b)
	hub := newFlowHub()
	ch := hub.subscribe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range ch {
		}
	}()
	obs := newFlowWSObserver(hub)
	obs.WSMessageStart(exec, "ws-turn", benchPayload) // warm up state

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		obs.WSMessageStart(exec, "ws-turn", benchPayload)
		obs.WSMessageComplete(exec, "ws-turn", 200)
	}
	hub.unsubscribe(ch)
	<-done
}

// BenchmarkHasSubscribers measures the gate all turn bookkeeping rides on.
func BenchmarkHasSubscribers(b *testing.B) {
	hub := newFlowHub()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = hub.hasSubscribers()
	}
}

// BenchmarkFlowThreadKey measures the pure continuation-key parse on a
// realistic Codex/Responses frame (matches the first key).
func BenchmarkFlowThreadKey(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = flowThreadKey(benchPayload)
	}
}
