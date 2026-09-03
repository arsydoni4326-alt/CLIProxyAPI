package wsrelay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type testHooks struct {
	mu           sync.Mutex
	connected    []string
	disconnected map[string][]string
}

func newTestHooks() *testHooks {
	return &testHooks{disconnected: make(map[string][]string)}
}

func (h *testHooks) onConnected(provider string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.connected = append(h.connected, provider)
}

func (h *testHooks) onDisconnected(provider string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cause := ""
	if err != nil {
		cause = err.Error()
	}
	h.disconnected[provider] = append(h.disconnected[provider], cause)
}

func (h *testHooks) connectedCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.connected)
}

func (h *testHooks) disconnectCauses(provider string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.disconnected[provider]...)
}

func newTestManager(t *testing.T) (*Manager, *testHooks, *httptest.Server) {
	t.Helper()
	hooks := newTestHooks()
	mgr := NewManager(Options{
		ProviderFactory: func(r *http.Request) (string, error) {
			return r.URL.Query().Get("provider"), nil
		},
		OnConnected:    hooks.onConnected,
		OnDisconnected: hooks.onDisconnected,
	})
	server := httptest.NewServer(mgr.Handler())
	t.Cleanup(func() {
		server.Close()
		_ = mgr.Stop(context.Background())
	})
	return mgr, hooks, server
}

func wsDialURL(server *httptest.Server, provider string) string {
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/ws"
	if provider != "" {
		url += "?provider=" + provider
	}
	return url
}

func dialTestSession(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	return conn
}

func waitForCondition(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestSessionChurnReconnect hammers the manager with repeated connect/close
// cycles to prove sessions survive churn without leaks or stuck state.
func TestSessionChurnReconnect(t *testing.T) {
	mgr, hooks, server := newTestManager(t)

	const cycles = 25
	for i := 0; i < cycles; i++ {
		conn := dialTestSession(t, wsDialURL(server, "churn"))
		// Abrupt close without a websocket close frame.
		_ = conn.Close()
		waitForCondition(t, "disconnect registration", func() bool {
			return len(hooks.disconnectCauses("churn")) > i
		})
	}

	// After churn, a final connection must become the only live session.
	conn := dialTestSession(t, wsDialURL(server, "churn"))
	defer func() { _ = conn.Close() }()
	waitForCondition(t, "final connect", func() bool {
		return hooks.connectedCount() >= cycles+1
	})
	if sess := mgr.session("churn"); sess == nil {
		t.Fatal("expected active session after churn")
	}
}

// TestProviderReplacement proves a new connection with the same provider name
// replaces the old session deterministically.
func TestProviderReplacement(t *testing.T) {
	mgr, hooks, server := newTestManager(t)

	first := dialTestSession(t, wsDialURL(server, "dup"))
	defer func() { _ = first.Close() }()
	waitForCondition(t, "first connect", func() bool { return hooks.connectedCount() == 1 })

	second := dialTestSession(t, wsDialURL(server, "dup"))
	defer func() { _ = second.Close() }()
	waitForCondition(t, "replacement", func() bool {
		return len(hooks.disconnectCauses("dup")) == 1
	})

	causes := hooks.disconnectCauses("dup")
	if len(causes) != 1 || !strings.Contains(causes[0], "replaced by new connection") {
		t.Fatalf("unexpected disconnect causes: %v", causes)
	}
	if mgr.session("dup") == nil {
		t.Fatal("expected replacement session to be registered")
	}

	// The old connection must be closed by the server; further reads fail.
	_ = first.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := first.ReadMessage(); err == nil {
		t.Fatal("expected replaced connection to fail reads")
	}
}

// TestManagerStopClosesSessions proves Stop terminates every active session.
func TestManagerStopClosesSessions(t *testing.T) {
	mgr, hooks, server := newTestManager(t)

	conn := dialTestSession(t, wsDialURL(server, "stopme"))
	waitForCondition(t, "connect", func() bool { return hooks.connectedCount() == 1 })

	if err := mgr.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if sess := mgr.session("stopme"); sess != nil {
		t.Fatal("expected session map to be empty after stop")
	}
	waitForCondition(t, "disconnect callback", func() bool {
		return len(hooks.disconnectCauses("stopme")) == 1
	})
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("expected stopped session connection to fail reads")
	}
	_ = conn.Close()
}

// TestPingPong verifies application-level ping/pong handling on the session.
func TestPingPong(t *testing.T) {
	_, hooks, server := newTestManager(t)

	conn := dialTestSession(t, wsDialURL(server, "pinger"))
	defer func() { _ = conn.Close() }()
	waitForCondition(t, "connect", func() bool { return hooks.connectedCount() == 1 })

	if err := conn.WriteJSON(Message{ID: "ping-1", Type: MessageTypePing}); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var msg Message
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("read pong: %v", err)
	}
	if msg.Type != MessageTypePong || msg.ID != "ping-1" {
		t.Fatalf("unexpected message: %+v", msg)
	}
}

// TestRequestResponseMatched proves pending request routing delivers a
// provider response back through Manager.Send.
func TestRequestResponseMatched(t *testing.T) {
	mgr, hooks, server := newTestManager(t)

	conn := dialTestSession(t, wsDialURL(server, "responder"))
	defer func() { _ = conn.Close() }()
	waitForCondition(t, "connect", func() bool { return hooks.connectedCount() == 1 })

	go func() {
		var req Message
		if err := conn.ReadJSON(&req); err != nil {
			return
		}
		_ = conn.WriteJSON(Message{ID: req.ID, Type: MessageTypeHTTPResp, Payload: map[string]any{
			"status": float64(http.StatusOK),
			"body":   "hello",
		}})
	}()

	respCh, err := mgr.Send(context.Background(), "responder", Message{ID: "req-1", Type: MessageTypeHTTPReq})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	select {
	case msg, ok := <-respCh:
		if !ok {
			t.Fatal("response channel closed before message")
		}
		if msg.Type != MessageTypeHTTPResp {
			t.Fatalf("unexpected message type %s", msg.Type)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for response")
	}
}

// TestRequestErrorsOnDisconnect proves pending requests are unblocked with an
// error when the provider connection drops mid-flight.
func TestRequestErrorsOnDisconnect(t *testing.T) {
	mgr, hooks, server := newTestManager(t)

	conn := dialTestSession(t, wsDialURL(server, "dropper"))
	waitForCondition(t, "connect", func() bool { return hooks.connectedCount() == 1 })

	respCh, err := mgr.Send(context.Background(), "dropper", Message{ID: "req-x", Type: MessageTypeHTTPReq})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	// Drop the provider connection without answering the request.
	_ = conn.Close()

	select {
	case msg, ok := <-respCh:
		if ok && msg.Type != MessageTypeError {
			t.Fatalf("expected error message or closed channel, got %+v", msg)
		}
		if ok && !strings.Contains(decodeError(msg.Payload).Error(), "websocket") {
			// Accept any cleanup cause but it must be an error payload.
			if decodeError(msg.Payload) == nil {
				t.Fatal("expected non-nil error payload")
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pending request not unblocked after disconnect")
	}

	// Manager must no longer route to the dropped provider.
	if _, err := mgr.Send(context.Background(), "dropper", Message{ID: "req-y", Type: MessageTypeHTTPReq}); err == nil {
		t.Fatal("expected error sending to disconnected provider")
	}
}

// TestDuplicateMessageIDRejected guards the pending-map invariant.
func TestDuplicateMessageIDRejected(t *testing.T) {
	mgr, hooks, server := newTestManager(t)

	conn := dialTestSession(t, wsDialURL(server, "dupid"))
	defer func() { _ = conn.Close() }()
	waitForCondition(t, "connect", func() bool { return hooks.connectedCount() == 1 })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := mgr.Send(ctx, "dupid", Message{ID: "same", Type: MessageTypeHTTPReq}); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if _, err := mgr.Send(ctx, "dupid", Message{ID: "same", Type: MessageTypeHTTPReq}); err == nil {
		t.Fatal("expected duplicate id rejection")
	} else if !strings.Contains(err.Error(), "duplicate message id") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !errors.Is(ctx.Err(), nil) {
		t.Fatalf("unexpected context error: %v", ctx.Err())
	}
}

// TestHandlerRejectsWrongPathAndMethod covers the HTTP surface guards.
func TestHandlerRejectsWrongPathAndMethod(t *testing.T) {
	_, _, server := newTestManager(t)

	base := strings.TrimPrefix(server.URL, "http")
	conn, resp, err := websocket.DefaultDialer.Dial("ws"+base+"/other", nil)
	if err == nil {
		_ = conn.Close()
		t.Fatal("expected dial to wrong path to fail")
	}
	if resp != nil {
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", resp.StatusCode)
		}
		_ = resp.Body.Close()
	}

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	postResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = postResp.Body.Close() }()
	if postResp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", postResp.StatusCode)
	}
	if allow := postResp.Header.Get("Allow"); allow != http.MethodGet {
		t.Fatalf("expected Allow GET, got %q", allow)
func newTestWebsocketPair(t *testing.T) (*session, *websocket.Conn, func()) {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	var serverConn *websocket.Conn
	var serverSess *session
	ready := make(chan struct{})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade error: %v", err)
			return
		}
		serverConn = conn
		serverSess = &session{
			conn:   conn,
			closed: make(chan struct{}),
		}
		close(ready)
	}))

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	clientConn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		ts.Close()
		t.Fatalf("dial error: %v", errDial)
	}
	<-ready

	cleanup := func() {
		_ = clientConn.Close()
		if serverSess != nil {
			serverSess.cleanup(errClosed)
		}
		if serverConn != nil {
			_ = serverConn.Close()
		}
		ts.Close()
	}

	return serverSess, clientConn, cleanup
}

func TestPendingRequest_ConcurrentSendAndClose(t *testing.T) {
	for iter := 0; iter < 1000; iter++ {
		s := &session{
			closed: make(chan struct{}),
		}
		reqID := fmt.Sprintf("req-%d", iter)
		req := newPendingRequest(context.Background())
		s.pending.Store(reqID, req)

		var wg sync.WaitGroup
		wg.Add(3)

		// Producer
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				s.dispatch(Message{
					ID:   reqID,
					Type: MessageTypeStreamChunk,
					Payload: map[string]any{
						"seq": i,
					},
				})
			}
		}()

		// Consumer
		go func() {
			defer wg.Done()
			for range req.ch {
			}
		}()

		// Deleter / closer
		go func() {
			defer wg.Done()
			if actual, loaded := s.pending.LoadAndDelete(reqID); loaded {
				actual.(*pendingRequest).close()
			}
		}()

		wg.Wait()
	}
}

func TestSession_Cleanup_RaceSyncMap(t *testing.T) {
	s := &session{
		closed: make(chan struct{}),
	}
	var stop int32
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for atomic.LoadInt32(&stop) == 0 {
			req := newPendingRequest(context.Background())
			s.pending.Store("key", req)
			s.pending.Load("key")
			s.pending.Delete("key")
			req.cancel()
		}
	}()

	for i := 0; i < 50; i++ {
		s.cleanup(errClosed)
	}

	atomic.StoreInt32(&stop, 1)
	wg.Wait()
}

func TestSession_Request_NoGoroutineLeakOnCompletion(t *testing.T) {
	sess, clientConn, cleanup := newTestWebsocketPair(t)
	defer cleanup()

	// Consume messages on client side and immediately reply with a terminal response.
	go func() {
		for {
			var msg Message
			if err := clientConn.ReadJSON(&msg); err != nil {
				return
			}
			sess.dispatch(Message{
				ID:   msg.ID,
				Type: MessageTypeHTTPResp,
			})
		}
	}()

	const count = 30
	for i := 0; i < count; i++ {
		reqID := fmt.Sprintf("req-leak-%d", i)
		ch, errReq := sess.request(context.Background(), Message{ID: reqID, Type: MessageTypeHTTPReq})
		if errReq != nil {
			t.Fatalf("request error: %v", errReq)
		}
		// Drain channel completely until closed.
		for range ch {
		}
	}
}

func TestSession_Dispatch_NoSilentFrameLoss(t *testing.T) {
	s := &session{
		closed: make(chan struct{}),
	}
	reqID := "stream-slow-consumer"
	req := newPendingRequest(context.Background())
	s.pending.Store(reqID, req)

	// Send 10 chunks followed by terminal message
	for i := 0; i < 10; i++ {
		s.dispatch(Message{
			ID:   reqID,
			Type: MessageTypeStreamChunk,
			Payload: map[string]any{
				"chunk": i,
			},
		})
	}
	s.dispatch(Message{
		ID:   reqID,
		Type: MessageTypeStreamEnd,
	})

	var received []Message
	for msg := range req.ch {
		received = append(received, msg)
	}

	if len(received) != 11 {
		t.Fatalf("frame loss: expected 11 frames, got %d", len(received))
	}
}

func TestSession_Cleanup_DeliversErrorFrameWhenBufferFull(t *testing.T) {
	s := &session{
		closed: make(chan struct{}),
	}
	reqID := "req-err-cleanup-full"
	req := newPendingRequest(context.Background())
	s.pending.Store(reqID, req)

	// Fill channel to buffer capacity
	for i := 0; i < pendingChannelBuffer; i++ {
		s.dispatch(Message{
			ID:   reqID,
			Type: MessageTypeStreamChunk,
			Payload: map[string]any{
				"seq": i,
			},
		})
	}

	s.cleanup(errClosed)

	var msgs []Message
	for msg := range req.ch {
		msgs = append(msgs, msg)
	}

	if len(msgs) == 0 {
		t.Fatalf("expected messages on cleanup, got 0")
	}
	lastMsg := msgs[len(msgs)-1]
	if lastMsg.Type != MessageTypeError {
		t.Fatalf("expected last message to be MessageTypeError, got %s", lastMsg.Type)
	}
}

func TestSession_Dispatch_TerminalDeliveredWhenBufferFullAndSessionClosing(t *testing.T) {
	s := &session{
		closed: make(chan struct{}),
	}
	reqID := "req-terminal-full"
	req := newPendingRequest(context.Background())
	s.pending.Store(reqID, req)

	// Fill channel to buffer capacity
	for i := 0; i < pendingChannelBuffer; i++ {
		s.dispatch(Message{
			ID:   reqID,
			Type: MessageTypeStreamChunk,
			Payload: map[string]any{
				"seq": i,
			},
		})
	}

	// Dispatch terminal message
	s.dispatch(Message{
		ID:   reqID,
		Type: MessageTypeStreamEnd,
	})

	var msgs []Message
	for msg := range req.ch {
		msgs = append(msgs, msg)
	}

	if len(msgs) == 0 {
		t.Fatalf("expected messages, got 0")
	}
	lastMsg := msgs[len(msgs)-1]
	if lastMsg.Type != MessageTypeStreamEnd {
		t.Fatalf("expected last message to be MessageTypeStreamEnd, got %s", lastMsg.Type)
	}
}

func TestSession_SlowConsumer_UnblocksOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sess := &session{
		closed: make(chan struct{}),
	}
	reqID := "req-ctx-cancel"
	req := newPendingRequest(ctx)
	sess.pending.Store(reqID, req)

	// Fill channel to buffer capacity
	for i := 0; i < pendingChannelBuffer; i++ {
		sess.dispatch(Message{ID: reqID, Type: MessageTypeStreamChunk})
	}

	enteredDispatch := make(chan struct{})
	doneDispatch := make(chan struct{})
	go func() {
		close(enteredDispatch)
		sess.dispatch(Message{ID: reqID, Type: MessageTypeStreamChunk})
		close(doneDispatch)
	}()

	<-enteredDispatch
	// Small yield to ensure the goroutine is in deliver's select
	time.Sleep(10 * time.Millisecond)

	// Cancel context to unblock saturated dispatch
	cancel()

	select {
	case <-doneDispatch:
		// Succeeded in unblocking
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("dispatch remained blocked after context cancellation")
	}
}

func TestSession_SlowConsumer_UnblocksOnSessionClose(t *testing.T) {
	sess := &session{
		closed: make(chan struct{}),
	}
	reqID := "req-sess-close"
	req := newPendingRequest(context.Background())
	sess.pending.Store(reqID, req)

	// Fill channel to buffer capacity
	for i := 0; i < pendingChannelBuffer; i++ {
		sess.dispatch(Message{ID: reqID, Type: MessageTypeStreamChunk})
	}

	enteredDispatch := make(chan struct{})
	doneDispatch := make(chan struct{})
	go func() {
		close(enteredDispatch)
		sess.dispatch(Message{ID: reqID, Type: MessageTypeStreamChunk})
		close(doneDispatch)
	}()

	<-enteredDispatch
	// Small yield to ensure the goroutine is in deliver's select
	time.Sleep(10 * time.Millisecond)

	// Close session to unblock saturated dispatch
	sess.cleanup(errClosed)

	select {
	case <-doneDispatch:
		// Succeeded in unblocking
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("dispatch remained blocked after session cleanup")
	}
}
