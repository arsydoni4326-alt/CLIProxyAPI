package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
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
