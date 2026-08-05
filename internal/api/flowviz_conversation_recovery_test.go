package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// flowTestContext builds a minimal Gin context carrying the request headers and
// body of a proxied upstream call. FullPath is intentionally not configured:
// registry-fallback paths are stubbed in these tests.
func flowTestContext(method, target string, headers map[string]string, body string) *gin.Context {
	gin.SetMode(gin.TestMode)

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	return c
}

// authContext is a convenience for salt tests: exactly one of the two auth
// surfaces is populated.
func authContext(authorization, apiKey string) *gin.Context {
	headers := map[string]string{}
	if authorization != "" {
		headers["Authorization"] = authorization
	}
	if apiKey != "" {
		headers["x-api-key"] = apiKey
	}
	return flowTestContext(http.MethodPost, "/v1/chat/completions", headers, "")
}

func TestFlowThreadKeyExtractsAdditionalKeys(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"flat conversation_id", `{"conversation_id":"conv-1","model":"gpt-5"}`, "conv-1"},
		{"camelCase conversationId", `{"conversationId":"conv-2","model":"gpt-5"}`, "conv-2"},
		{"nested conversation.id", `{"conversation":{"id":"conv-3"},"model":"claude-sonnet"}`, "conv-3"},
		{"session_id", `{"session_id":"ses-1","model":"gpt-5"}`, "ses-1"},
		{"camelCase sessionId", `{"sessionId":"ses-2","model":"gpt-5"}`, "ses-2"},
		{"chat_id", `{"chat_id":"chat-1","model":"gpt-5"}`, "chat-1"},
		{"thread_id", `{"thread_id":"thr-1","model":"gpt-5"}`, "thr-1"},
		{"parentMessageId", `{"parentMessageId":"par-1","model":"gpt-5"}`, "par-1"},
		{"metadata.conversation_id", `{"metadata":{"conversation_id":"meta-conv-1"},"model":"gpt-5"}`, "meta-conv-1"},
		{"metadata.thread_id", `{"metadata":{"thread_id":"meta-thr-1"},"model":"gpt-5"}`, "meta-thr-1"},
		{"previous_response_id", `{"previous_response_id":"pr-1","model":"gpt-5"}`, "pr-1"},
		{"previous_interaction_id", `{"previous_interaction_id":"pi-1","model":"gpt-5"}`, "pi-1"},
		{"no thread key", `{"model":"gpt-5","stream":true}`, ""},
		{"empty string key ignored", `{"conversation_id":"  ","model":"gpt-5"}`, ""},
		{"non-string key ignored", `{"conversation_id":42,"model":"gpt-5"}`, ""},
		{"conversation as string", `{"conversation":"conv-9","model":"gpt-5"}`, "conv-9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := flowThreadKey([]byte(tc.body))
			if got != tc.want {
				t.Errorf("flowThreadKey(%q) = %q; want %q", tc.body, got, tc.want)
			}
		})
	}
}

func TestFlowThreadKeyPrecedence(t *testing.T) {
	// Specific continuation/thread keys win over the nested conversation bag.
	body := []byte(`{"previous_response_id":"pr-1","conversation":{"id":"conv-1"},"model":"gpt-5"}`)
	if got := flowThreadKey(body); got != "pr-1" {
		t.Errorf("flowThreadKey precedence = %q; want %q", got, "pr-1")
	}
}

func TestFlowClientSaltIsolation(t *testing.T) {
	// Same credential across auth surfaces + scheme normalization -> same salt.
	keyA := flowClientSalt(authContext("Bearer sk-a", ""))
	keyB := flowClientSalt(authContext("sk-a", ""))
	if keyA == "" || keyA != keyB {
		t.Errorf("normalized salt mismatch: Bearer=%q bare=%q (want equal non-empty)", keyA, keyB)
	}

	// Different credentials -> different salts.
	keyC := flowClientSalt(authContext("Bearer sk-b", ""))
	if keyA == keyC {
		t.Errorf("different credentials produced same salt %q", keyA)
	}
	// The same credential expressed on the other auth surface must map to the
	// same salt (surface normalization, not isolation).
	keyD := flowClientSalt(authContext("", "sk-a"))
	if keyA != keyD {
		t.Errorf("x-api-key and Authorization salt mismatch: %q vs %q (want equal)", keyA, keyD)
	}

	// Anonymous -> no salt.
	if got := flowClientSalt(authContext("", "")); got != "" {
		t.Errorf("anonymous request produced salt %q; want empty", got)
	}

	// Degenerate header values -> no salt.
	if got := flowClientSalt(authContext("Bearer ", "")); got != "" {
		t.Errorf("empty Bearer produced salt %q; want empty", got)
	}
	if got := flowClientSalt(nil); got != "" {
		t.Errorf("nil context produced salt %q; want empty", got)
	}
}

func TestFlowConvKeySalting(t *testing.T) {
	if got := flowConvKey("", "conv-1"); got != "conv-1" {
		t.Errorf("anonymous conv key = %q; want bare %q", got, "conv-1")
	}
	if got := flowConvKey("a1b2c3d4", "conv-1"); got != "a1b2c3d4|conv-1" {
		t.Errorf("salted conv key = %q; want %q", got, "a1b2c3d4|conv-1")
	}
	if got := flowConvKey("a1b2c3d4", ""); got != "" {
		t.Errorf("empty thread key produced %q; want empty", got)
	}
}

func TestNormalizeFlowModel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"gpt-5-codex(16384)", "gpt-5-codex"},
		{"claude-sonnet-4-5(high)", "claude-sonnet-4-5"},
		{"gpt-5.2", "gpt-5.2"},
		{"gemini-2.5-pro", "gemini-2.5-pro"},
		{"  gpt-4o  ", "gpt-4o"},
		{"gpt-5 (budget)", "gpt-5"}, // whitespace before the suffix is trimmed with the name
		{"gpt-5(abc", "gpt-5(abc"},  // malformed: no closing paren -> unchanged
		{"", ""},
	}
	for _, tc := range cases {
		if got := normalizeFlowModel(tc.in); got != tc.want {
			t.Errorf("normalizeFlowModel(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// stubRegistryFallback swaps the registry fallback for the duration of a test.
// The global registry must never be consulted by unit tests: it is long-lived,
// mutable, and not deterministic in isolation.
func stubRegistryFallback(t *testing.T, model string) {
	t.Helper()
	orig := flowRegistryFallbackModel
	flowRegistryFallbackModel = func(*gin.Context) string { return model }
	t.Cleanup(func() { flowRegistryFallbackModel = orig })
}

func TestResolveFlowModelResolutionOrder(t *testing.T) {
	hub := newFlowHub()
	c := authContext("Bearer sk-order", "")
	threadKey := "conv-order"

	// Payload model always wins, is normalized, and is remembered per client.
	got := resolveFlowModel(c, hub, []byte(`{"conversation_id":"`+threadKey+`","model":"gpt-5-codex(16384)"}`), "stale-conn")
	if got != "gpt-5-codex" {
		t.Errorf("payload model resolution = %q; want %q", got, "gpt-5-codex")
	}
	if cached := hub.convModelFor(flowConvKey(flowClientSalt(c), threadKey)); cached != "gpt-5-codex" {
		t.Errorf("payload model not remembered: %q", cached)
	}

	// Model-less follow-up recovers the conversation model, not the conn hint.
	got = resolveFlowModel(c, hub, []byte(`{"conversation_id":"`+threadKey+`","stream":true}`), "stale-conn")
	if got != "gpt-5-codex" {
		t.Errorf("thread-cache recovery = %q; want %q", got, "gpt-5-codex")
	}

	// Unknown continuation id on the same connection -> per-connection model.
	got = resolveFlowModel(c, hub, []byte(`{"previous_response_id":"fresh-chain-id"}`), "gpt-5-codex")
	if got != "gpt-5-codex" {
		t.Errorf("connection recovery = %q; want %q", got, "gpt-5-codex")
	}
	// The one-shot continuation id must not pollute the thread cache.
	cachedKey := flowConvKey(flowClientSalt(c), "fresh-chain-id")
	if cached := hub.convModelFor(cachedKey); cached != "" {
		t.Errorf("continuation id polluted thread cache: %q", cached)
	}
}

func TestResolveFlowModelMultiClientIsolation(t *testing.T) {
	// Two customers reuse the same short conversation id. Each client's model
	// must resolve from their own salted cache entry, never the other's.
	hub := newFlowHub()
	clientA := authContext("Bearer sk-client-a", "")
	clientB := authContext("Bearer sk-client-b", "")

	shared := `{"conversation_id":"conv-cfe9","model":"%s"}`
	resolveFlowModel(clientA, hub, []byte(strings.ReplaceAll(shared, "%s", "gpt-a")), "")
	resolveFlowModel(clientB, hub, []byte(strings.ReplaceAll(shared, "%s", "gpt-b")), "")

	gotA := resolveFlowModel(clientA, hub, []byte(`{"conversation_id":"conv-cfe9","stream":true}`), "")
	gotB := resolveFlowModel(clientB, hub, []byte(`{"conversation_id":"conv-cfe9","stream":true}`), "")
	if gotA != "gpt-a" {
		t.Errorf("client A model = %q; want %q (cross-client leak?)", gotA, "gpt-a")
	}
	if gotB != "gpt-b" {
		t.Errorf("client B model = %q; want %q (cross-client leak?)", gotB, "gpt-b")
	}

	// Anonymous clients share the bare key, but that is a deliberate tradeoff:
	// without a credential there is no client boundary to isolate on.
	anon := authContext("", "")
	resolveFlowModel(anon, hub, []byte(`{"conversation_id":"anon-shared","model":"gpt-anon"}`), "")
	if got := resolveFlowModel(anon, hub, []byte(`{"conversation_id":"anon-shared"}`), ""); got != "gpt-anon" {
		t.Errorf("anonymous recovery = %q; want %q", got, "gpt-anon")
	}
}

func TestResolveFlowModelRegistryFallback(t *testing.T) {
	stubRegistryFallback(t, "fallback-gemini")
	hub := newFlowHub()

	// Model-less, thread-less request on a gemini route -> registry fallback.
	got := resolveFlowModel(authContext("Bearer sk-fallback", ""), hub, []byte(`{"stream":true}`), "")
	if got != "fallback-gemini" {
		t.Errorf("registry fallback = %q; want %q", got, "fallback-gemini")
	}

	// Connection hint wins over the registry default on a chained turn.
	got = resolveFlowModel(authContext("Bearer sk-fallback", ""), hub, []byte(`{"previous_response_id":"chain-1"}`), "gpt-4o")
	if got != "gpt-4o" {
		t.Errorf("connection hint precedence = %q; want %q", got, "gpt-4o")
	}

	// Everything exhausted -> empty (renders under the catch-all node).
	stubRegistryFallback(t, "")
	got = resolveFlowModel(authContext("Bearer sk-fallback", ""), hub, []byte(`{"stream":true}`), "")
	if got != "" {
		t.Errorf("fully-failed resolution = %q; want empty", got)
	}
}

// sniffRequest prepares a fresh JSON request on ctx, re-applying the credential
// (a replacement httptest.Request drops all previously set headers).
func sniffRequest(ctx *gin.Context, authorization, body string) {
	ctx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(body),
	)
	if authorization != "" {
		ctx.Request.Header.Set("Authorization", authorization)
	}
	ctx.Request.Header.Set("Content-Type", "application/json")
}

func TestSniffFlowModelThreadRecoveryIntegration(t *testing.T) {
	stubRegistryFallback(t, "") // resolution must never depend on the registry here
	hub := newFlowHub()
	sub := hub.subscribe()
	defer hub.unsubscribe(sub)

	client := authContext("Bearer sk-sniff", "")

	// First turn names its model.
	sniffRequest(client, "Bearer sk-sniff", `{"conversation_id":"conv-live","model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)
	sniffFlowModel(client, hub)
	if got, _ := client.Get("flow_model"); got != "gpt-4o-mini" {
		t.Fatalf("first turn flow_model = %v; want gpt-4o-mini", got)
	}

	// The body must be restored verbatim for the downstream handler.
	restored, err := io.ReadAll(client.Request.Body)
	if err != nil {
		t.Fatalf("read restored body: %v", err)
	}
	if !strings.Contains(string(restored), `"model":"gpt-4o-mini"`) {
		t.Errorf("restored body corrupted: %s", restored)
	}

	// Follow-up turn omits the model entirely; the salted thread cache recovers
	// it without any payload change.
	sniffRequest(client, "Bearer sk-sniff", `{"conversation_id":"conv-live","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	sniffFlowModel(client, hub)
	if got, _ := client.Get("flow_model"); got != "gpt-4o-mini" {
		t.Errorf("follow-up flow_model = %v; want recovered gpt-4o-mini", got)
	}

	// The cache entry is salted: the same conversation id under a different
	// credential must not resolve.
	other := authContext("Bearer sk-other", "")
	sniffRequest(other, "Bearer sk-other", `{"conversation_id":"conv-live","stream":true}`)
	sniffFlowModel(other, hub)
	if got, exists := other.Get("flow_model"); exists && got != "" {
		t.Errorf("other client resolved model %v; want empty (cross-client leak)", got)
	}

	// Idle hub short-circuits: without subscribers nothing is sniffed.
	idle := authContext("Bearer sk-idle", "")
	idleHub := newFlowHub()
	sniffRequest(idle, "Bearer sk-idle", `{"model":"gpt-5"}`)
	sniffFlowModel(idle, idleHub)
	if got, exists := idle.Get("flow_model"); exists && got != "" {
		t.Errorf("idle hub set flow_model %v; want empty", got)
	}

	// Non-JSON content type is skipped.
	nonJSON := authContext("Bearer sk-text", "")
	nonJSON.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`model=gpt-5`),
	)
	nonJSON.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	sniffFlowModel(nonJSON, hub)
	if got, exists := nonJSON.Get("flow_model"); exists && got != "" {
		t.Errorf("non-JSON request set flow_model %v; want empty", got)
	}
}

// wsObserverCtx builds a Gin context whose derived context carries it under the
// "gin" key, the idiom used by the executor code paths when invoking the
// FlowObserver hooks.
func wsObserverCtx(t *testing.T, headers map[string]string) (*gin.Context, context.Context) {
	t.Helper()
	c := flowTestContext(http.MethodPost, "/v1/responses", headers, "")
	ctx := context.WithValue(context.Background(), "gin", c)
	return c, ctx
}

// TestFlowWebsocketTurnModelResolutionEndToEnd drives the observer across a
// multi-turn WS conversation (as the executor adapter does): start each turn,
// then complete it, verifying the recovered model flows into each published
// event without cross-client leakage.
func TestFlowWebsocketTurnModelResolutionEndToEnd(t *testing.T) {
	stubRegistryFallback(t, "") // resolution must never depend on the registry here
	hub := newFlowHub()
	sub := hub.subscribe()
	defer hub.unsubscribe(sub)

	obs := newFlowWSObserver(hub)
	if obs == nil {
		t.Fatal("newFlowWSObserver returned nil")
	}

	connA, ctxA := wsObserverCtx(t, map[string]string{"Authorization": "Bearer sk-ws-a"})
	connB, ctxB := wsObserverCtx(t, map[string]string{"Authorization": "Bearer sk-ws-b"})

	// Turn 1 on A names its model. Thread keys are extracted from top-level
	// fields (flowThreadKey uses flat gjson paths), so conversation_id sits at
	// the frame root.
	obs.WSMessageStart(ctxA, "turn-a1", []byte(`{"type":"response.create","conversation_id":"conv-ws","model":"gpt-a"}`))
	obs.WSMessageComplete(ctxA, "turn-a1", 200)

	// Turn 2 on A omits the model: recovered from the salted thread cache.
	obs.WSMessageStart(ctxA, "turn-a2", []byte(`{"type":"response.create","conversation_id":"conv-ws","input":"hi"}`))
	obs.WSMessageComplete(ctxA, "turn-a2", 200)

	// Client B reuses the same conversation id but a different model: must
	// resolve from B's own salted entry, never A's.
	obs.WSMessageStart(ctxB, "turn-b1", []byte(`{"type":"response.create","conversation_id":"conv-ws","model":"gpt-b"}`))
	obs.WSMessageComplete(ctxB, "turn-b1", 200)
	obs.WSMessageStart(ctxB, "turn-b2", []byte(`{"type":"response.create","conversation_id":"conv-ws","input":"hi"}`))
	obs.WSMessageComplete(ctxB, "turn-b2", 200)

	want := []string{"gpt-a", "gpt-a", "gpt-b", "gpt-b"}
	for i := range want {
		select {
		case raw := <-sub:
			var ev flowEvent
			if err := json.Unmarshal(raw, &ev); err != nil {
				t.Fatalf("event %d unmarshal: %v", i, err)
			}
			if ev.Method != "WS" {
				t.Errorf("event %d method = %q; want WS", i, ev.Method)
			}
			if ev.Model != want[i] {
				t.Errorf("event %d model = %q; want %q", i, ev.Model, want[i])
			}
			if ev.Timestamp == 0 {
				t.Errorf("event %d missing timestamp", i)
			}
		default:
			t.Fatalf("event %d not published", i)
		}
	}

	// A chained Responses continuation on A (fresh previous_response_id, no
	// thread key, no model) recovers the connection's last model.
	obs.WSMessageStart(ctxA, "turn-a3", []byte(`{"type":"response.append","previous_response_id":"fresh-chain-xyz"}`))
	obs.WSMessageComplete(ctxA, "turn-a3", 200)
	select {
	case raw := <-sub:
		var ev flowEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			t.Fatalf("chain event unmarshal: %v", err)
		}
		if ev.Model != "gpt-a" {
			t.Errorf("chain turn model = %q; want connection recovery %q", ev.Model, "gpt-a")
		}
	default:
		t.Fatal("chain turn event not published")
	}

	// The hub must not receive this request itself.
	if got, exists := connA.Get("flow_model"); exists {
		t.Errorf("hub-hacked flow_model %v; want unset", got)
	}
	if got, exists := connB.Get("flow_model"); exists {
		t.Errorf("hub-hacked flow_model %v; want unset", got)
	}

}

// TestFlowWebsocketObserverSkipsIdleHub guards the per-frame cost: with no
// live-flow consumer, the observer must do nothing at all.
func TestFlowWebsocketObserverSkipsIdleHub(t *testing.T) {
	hub := newFlowHub()
	obs := newFlowWSObserver(hub)
	_, ctx := wsObserverCtx(t, map[string]string{"Authorization": "Bearer sk-ws-idle"})

	// No panic, no state mutation, no event published.
	obs.WSMessageStart(ctx, "turn-1", []byte(`{"model":"gpt-5"}`))
	obs.WSMessageComplete(ctx, "turn-1", 200)

	select {
	case raw := <-hub.subscribe():
		t.Fatalf("idle hub published %s", raw)
	default:
	}
}

// TestFlowWebsocketObserverNoContext guards hook invocations whose context
// cannot be traced back to a Gin request: the event is skipped, not guessed.
func TestFlowWebsocketObserverNoContext(t *testing.T) {
	hub := newFlowHub()
	sub := hub.subscribe()
	defer hub.unsubscribe(sub)

	obs := newFlowWSObserver(hub)
	orphan := context.Background()
	obs.WSMessageStart(orphan, "turn-orphan", []byte(`{"model":"gpt-5"}`))
	obs.WSMessageComplete(orphan, "turn-orphan", 200)

	select {
	case raw := <-sub:
		t.Fatalf("orphan context published %s", raw)
	default:
	}
}

// TestFlowWebsocketObserverEvictionAtCapacity verifies the pending-turn table
// stays bounded: once flowWSStatuses turns are in flight, the next start evicts
// the oldest; its completion then renders without a start timestamp instead of
// growing the table.
func TestFlowWebsocketObserverEvictionAtCapacity(t *testing.T) {
	hub := newFlowHub()
	sub := hub.subscribe()
	defer hub.unsubscribe(sub)

	conn, ctx := wsObserverCtx(t, map[string]string{"Authorization": "Bearer sk-ws-evict"})
	obs := newFlowWSObserver(hub)

	const starts = flowWSStatuses + 1
	keys := make([]string, 0, starts)
	for i := 0; i < starts; i++ {
		key := fmt.Sprintf("evict-%d", i)
		keys = append(keys, key)
		obs.WSMessageStart(ctx, key, []byte(`{"model":"gpt-5"}`))
	}
	state := getFlowWSState(conn)
	if len(state.turns) != flowWSStatuses {
		t.Fatalf("turn table size = %d; want %d (must stay bounded at the cap)", len(state.turns), flowWSStatuses)
	}

	// Exactly one of the started keys was evicted (map iteration order is
	// random, so the victim is unknown). Its completion must publish nothing.
	evicted := ""
	for _, k := range keys {
		state.mu.Lock()
		_, ok := state.turns[k]
		state.mu.Unlock()
		if !ok {
			evicted = k
			break
		}
	}
	if evicted == "" {
		t.Fatal("no evicted key found; capacity eviction not exercised")
	}
	obs.WSMessageComplete(ctx, evicted, 200)
	select {
	case raw := <-sub:
		t.Fatalf("evicted turn published %s", raw)
	default:
	}
}
