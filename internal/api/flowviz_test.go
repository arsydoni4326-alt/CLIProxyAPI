package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
)

// subscribedHub returns a hub with an attached subscriber so sniffFlowModel's
// idle-hub short-circuit does not skip body sniffing. The returned cancel must
// be called to release the subscription.
func subscribedHub(t *testing.T) (*flowHub, func()) {
	t.Helper()
	hub := newFlowHub()
	ch := hub.subscribe()
	cancel := func() { hub.unsubscribe(ch) }
	return hub, cancel
}

func newSniffContext(t *testing.T, body string) *gin.Context {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	return c
}

func sniffModel(c *gin.Context) string {
	model, _ := c.Get("flow_model")
	s, _ := model.(string)
	return s
}

func TestFlowThreadKeyExtractsKnownKeys(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"previous_response_id", `{"previous_response_id":"resp_123"}`, "resp_123"},
		{"previous_interaction_id", `{"previous_interaction_id":"int_7"}`, "int_7"},
		{"conversation.id", `{"conversation":{"id":"conv_9"}}`, "conv_9"},
		{"conversation_id", `{"conversation_id":"c-1"}`, "c-1"},
		{"conversation string", `{"conversation":"conv-x"}`, "conv-x"},
		{"session_id", `{"session_id":"s-1"}`, "s-1"},
		{"sessionId", `{"sessionId":"s-2"}`, "s-2"},
		{"chat_id", `{"chat_id":"chat-1"}`, "chat-1"},
		{"thread_id", `{"thread_id":"thr-1"}`, "thr-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := flowThreadKey([]byte(tc.body)); got != tc.want {
				t.Fatalf("flowThreadKey(%s) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

func TestFlowThreadKeyPrefersFirstKey(t *testing.T) {
	body := `{"previous_response_id":"resp_first","conversation_id":"conv_second","thread_id":"thr_third"}`
	if got := flowThreadKey([]byte(body)); got != "resp_first" {
		t.Fatalf("flowThreadKey precedence = %q, want %q", got, "resp_first")
	}
}

func TestFlowThreadKeyEmptyWhenNoKey(t *testing.T) {
	for _, body := range []string{
		`{"model":"gpt-5"}`,
		`{}`,
		`{"conversation_id":123}`, // non-string ignored
		`{"session_id":"   "}`,    // whitespace-only ignored
		`{"unknown_key":"x"}`,
	} {
		if got := flowThreadKey([]byte(body)); got != "" {
			t.Fatalf("flowThreadKey(%s) = %q, want empty", body, got)
		}
	}
}

func TestRememberAndConvModelRoundTrip(t *testing.T) {
	hub := newFlowHub()
	if got := hub.convModelFor("missing"); got != "" {
		t.Fatalf("unexpected model for unknown key: %q", got)
	}
	hub.rememberConvModel("conv-1", "gpt-5")
	hub.rememberConvModel("conv-2", "claude-sonnet-4-5")
	if got := hub.convModelFor("conv-1"); got != "gpt-5" {
		t.Fatalf("convModelFor(conv-1) = %q, want gpt-5", got)
	}
	if got := hub.convModelFor("conv-2"); got != "claude-sonnet-4-5" {
		t.Fatalf("convModelFor(conv-2) = %q, want claude-sonnet-4-5", got)
	}

	// Re-recording an existing key updates the model without duplicating order.
	hub.rememberConvModel("conv-1", "gpt-5-codex")
	if got := hub.convModelFor("conv-1"); got != "gpt-5-codex" {
		t.Fatalf("convModelFor(conv-1) after update = %q, want gpt-5-codex", got)
	}
}

func TestSniffFlowModelRecoversModelOnFollowUp(t *testing.T) {
	hub, cancel := subscribedHub(t)
	defer cancel()

	// First request in the conversation names its model and a thread key.
	first := newSniffContext(t, `{"model":"gpt-5","conversation_id":"conv-42","messages":[]}`)
	sniffFlowModel(first, hub)
	if got := sniffModel(first); got != "gpt-5" {
		t.Fatalf("first request model = %q, want gpt-5", got)
	}

	// Follow-up omits "model" but carries the same conversation key; it must be
	// recovered from the hub mapping (this is the regression the user reported).
	second := newSniffContext(t, `{"conversation_id":"conv-42","messages":[{"role":"user","content":"next"}]}`)
	sniffFlowModel(second, hub)
	if got := sniffModel(second); got != "gpt-5" {
		t.Fatalf("follow-up model = %q, want recovered gpt-5", got)
	}
}

func TestSniffFlowModelWithoutModelOrMappingLeavesModelEmpty(t *testing.T) {
	hub, cancel := subscribedHub(t)
	defer cancel()

	followUp := newSniffContext(t, `{"conversation_id":"never-seen","messages":[]}`)
	sniffFlowModel(followUp, hub)
	if got := sniffModel(followUp); got != "" {
		t.Fatalf("unmapped follow-up model = %q, want empty", got)
	}
}

// --- Websocket turn observer ------------------------------------------------

// newWSContext returns a Gin context whose route metadata matches a real
// registered route (so c.FullPath() resolves to the route template), plus the
// execution context the sdk handler would build for it (carrying the gin
// context under the "gin" key). HandleContext binds the test context to the
// engine's mux without running the handler body.
func newWSContext(t *testing.T, method, route, requestURI string) (*gin.Context, context.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Handle(method, route, func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, requestURI, nil)
	engine.HandleContext(c)
	if got := c.FullPath(); got != route {
		t.Fatalf("route binding failed: FullPath() = %q, want %q", got, route)
	}
	exec := context.WithValue(context.Background(), "gin", c)
	return c, exec
}

// readFlowEvent waits up to the timeout for one event from the subscription.
func readFlowEvent(t *testing.T, ch <-chan []byte) flowEvent {
	t.Helper()
	select {
	case data := <-ch:
		var ev flowEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			t.Fatalf("unmarshal flow event: %v", err)
		}
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for flow event")
		return flowEvent{}
	}
}

func readFlowEventOptional(ch <-chan []byte, timeout time.Duration) *flowEvent {
	select {
	case data := <-ch:
		var ev flowEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return nil
		}
		return &ev
	case <-time.After(timeout):
		return nil
	}
}

func TestWSObserverPublishesTurnEvent(t *testing.T) {
	hub := newFlowHub()
	ch := hub.subscribe()
	defer hub.unsubscribe(ch)

	c, exec := newWSContext(t, http.MethodGet, "/v1/responses", "/v1/responses")
	logging.SetGinRequestID(c, "req-ws-1")
	obs := newFlowWSObserver(hub)

	payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
	obs.WSMessageStart(exec, "turn-1", payload)
	time.Sleep(5 * time.Millisecond) // ensure measurable latency
	obs.WSMessageComplete(exec, "turn-1", http.StatusOK)

	ev := readFlowEvent(t, ch)
	if ev.Method != "WS" {
		t.Fatalf("Method = %q, want WS", ev.Method)
	}
	if ev.Model != "gpt-5" {
		t.Fatalf("Model = %q, want gpt-5", ev.Model)
	}
	if ev.Status != http.StatusOK {
		t.Fatalf("Status = %d, want 200", ev.Status)
	}
	if ev.ID != "req-ws-1" {
		t.Fatalf("ID = %q, want req-ws-1", ev.ID)
	}
	if ev.LatencyMs < 0 {
		t.Fatalf("LatencyMs = %d, want >= 0", ev.LatencyMs)
	}
	if ev.Timestamp <= 0 {
		t.Fatalf("Timestamp = %d, want > 0", ev.Timestamp)
	}
}

func TestWSObserverRecoversModelViaThreadKey(t *testing.T) {
	hub := newFlowHub()
	ch := hub.subscribe()
	defer hub.unsubscribe(ch)

	_, exec := newWSContext(t, http.MethodGet, "/v1/responses", "/v1/responses")
	obs := newFlowWSObserver(hub)

	// First turn names its model along with the continuation key.
	obs.WSMessageStart(exec, "turn-1", []byte(`{"type":"response.create","model":"gpt-5-codex","previous_response_id":"resp_9"}`))
	obs.WSMessageComplete(exec, "turn-1", http.StatusOK)
	_ = readFlowEvent(t, ch)

	// Follow-up turn omits the model but carries the continuation key; the
	// model must be recovered from the hub's conversation mapping.
	obs.WSMessageStart(exec, "turn-2", []byte(`{"type":"response.append","previous_response_id":"resp_9","input":"next"}`))
	obs.WSMessageComplete(exec, "turn-2", http.StatusOK)

	ev := readFlowEvent(t, ch)
	if ev.Model != "gpt-5-codex" {
		t.Fatalf("follow-up Model = %q, want recovered gpt-5-codex", ev.Model)
	}
}

func TestWSObserverCompleteWithoutStartSkips(t *testing.T) {
	hub := newFlowHub()
	ch := hub.subscribe()
	defer hub.unsubscribe(ch)

	_, exec := newWSContext(t, http.MethodGet, "/v1/responses", "/v1/responses")
	obs := newFlowWSObserver(hub)

	// Completion without a recorded start (observer enabled mid-stream) must
	// publish nothing rather than fabricating metadata.
	obs.WSMessageComplete(exec, "ghost-turn", http.StatusOK)
	if ev := readFlowEventOptional(ch, 150*time.Millisecond); ev != nil {
		t.Fatalf("unexpected event for unknown turn: %+v", *ev)
	}
}

func TestWSObserverIdleHubCostsNothing(t *testing.T) {
	hub := newFlowHub() // no subscribers

	_, exec := newWSContext(t, http.MethodGet, "/v1/responses", "/v1/responses")
	obs := newFlowWSObserver(hub)

	obs.WSMessageStart(exec, "turn-1", []byte(`{"type":"response.create","model":"gpt-5"}`))
	// A turn start on an idle hub must not be recorded (no subscribers to serve),
	// so the matching completion publishes nothing even after subscribing.
	ch := hub.subscribe()
	defer hub.unsubscribe(ch)
	obs.WSMessageComplete(exec, "turn-1", http.StatusOK)
	if ev := readFlowEventOptional(ch, 150*time.Millisecond); ev != nil {
		t.Fatalf("unexpected event from idle-hub start: %+v", *ev)
	}
}

func TestWSObserverMapsErrorStatuses(t *testing.T) {
	hub := newFlowHub()
	ch := hub.subscribe()
	defer hub.unsubscribe(ch)

	_, exec := newWSContext(t, http.MethodGet, "/v1/responses", "/v1/responses")
	obs := newFlowWSObserver(hub)
	payload := []byte(`{"type":"response.create","model":"gpt-5"}`)

	cases := []struct {
		name   string
		turn   string
		status int
	}{
		{"bad gateway", "t1", http.StatusBadGateway},
		{"conflict replay", "t2", http.StatusConflict},
		{"upstream error passthrough", "t3", http.StatusTooManyRequests},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs.WSMessageStart(exec, tc.turn, payload)
			obs.WSMessageComplete(exec, tc.turn, tc.status)
			ev := readFlowEvent(t, ch)
			if ev.Status != tc.status {
				t.Fatalf("Status = %d, want %d", ev.Status, tc.status)
			}
			if ev.Method != "WS" {
				t.Fatalf("Method = %q, want WS", ev.Method)
			}
		})
	}
}
