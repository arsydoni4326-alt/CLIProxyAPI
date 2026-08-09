package wsrelay

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Timeout exception (see docs/ARCHITECTURE.md §2.5): long-lived websocket
// sessions may set read/write deadlines; these are not request-bound
// credential-acquisition timeouts. heartbeats keep the session alive and the
// read deadline is reset on every pong.
const (
	readTimeout          = 60 * time.Second // max silence between pongs before the session is dropped
	writeTimeout         = 10 * time.Second // applies to every write, including heartbeat pings
	maxInboundMessageLen = 64 << 20         // 64 MiB
	heartbeatInterval    = 30 * time.Second // ping cadence; well under readTimeout so pongs arrive in time
)

var errClosed = errors.New("websocket session closed")

// pendingRequest serializes terminal delivery and closure of its channel.
//
// The channel ch has a single owner lifecycle: it is closed exactly once, and
// no message is ever sent after that close. Both the session teardown path
// (cleanup) and the request context-cancellation path can race to terminate a
// pending request, so close and "send terminal message then close" are made
// mutually exclusive via mu. Without this, a concurrent cleanup() sending a
// terminal error would race request()'s close(ch) (send-on-closed panic and a
// data race on the channel header).
type pendingRequest struct {
	ch     chan Message
	mu     sync.Mutex
	closed bool
}

// final delivers msg (best-effort) and then closes the channel. It is a no-op
// if the channel is already closed.
func (pr *pendingRequest) final(msg Message) {
	if pr == nil {
		return
	}
	pr.mu.Lock()
	defer pr.mu.Unlock()
	if pr.closed {
		return
	}
	pr.closed = true
	select {
	case pr.ch <- msg:
	default:
	}
	close(pr.ch)
}

// close shuts the channel without delivering a terminal message. It is a no-op
// if the channel is already closed (e.g. a terminal message already delivered).
func (pr *pendingRequest) close() {
	if pr == nil {
		return
	}
	pr.mu.Lock()
	if !pr.closed {
		pr.closed = true
		close(pr.ch)
	}
	pr.mu.Unlock()
}

type session struct {
	conn       *websocket.Conn
	manager    *Manager
	provider   string
	id         string
	closed     chan struct{}
	closeOnce  sync.Once
	writeMutex sync.Mutex
	pending    sync.Map // map[string]*pendingRequest
}

func newSession(conn *websocket.Conn, mgr *Manager, id string) *session {
	s := &session{
		conn:     conn,
		manager:  mgr,
		provider: "",
		id:       id,
		closed:   make(chan struct{}),
	}
	conn.SetReadLimit(maxInboundMessageLen)
	conn.SetReadDeadline(time.Now().Add(readTimeout))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(readTimeout))
		return nil
	})
	s.startHeartbeat()
	return s
}

func (s *session) startHeartbeat() {
	if s == nil || s.conn == nil {
		return
	}
	ticker := time.NewTicker(heartbeatInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-s.closed:
				return
			case <-ticker.C:
				s.writeMutex.Lock()
				err := s.conn.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(writeTimeout))
				s.writeMutex.Unlock()
				if err != nil {
					s.cleanup(err)
					return
				}
			}
		}
	}()
}

func (s *session) run(ctx context.Context) {
	defer s.cleanup(errClosed)
	for {
		var msg Message
		if err := s.conn.ReadJSON(&msg); err != nil {
			s.cleanup(err)
			return
		}
		s.dispatch(msg)
	}
}

func (s *session) dispatch(msg Message) {
	if msg.Type == MessageTypePing {
		_ = s.send(context.Background(), Message{ID: msg.ID, Type: MessageTypePong})
		return
	}
	if value, ok := s.pending.Load(msg.ID); ok {
		req := value.(*pendingRequest)
		if msg.Type == MessageTypeHTTPResp || msg.Type == MessageTypeError || msg.Type == MessageTypeStreamEnd {
			// Terminal message: remove it from the map, then deliver-and-close
			// atomically via final so the send can never race a concurrent close
			// from cleanup() or request() cancellation.
			if actual, loaded := s.pending.LoadAndDelete(msg.ID); loaded {
				actual.(*pendingRequest).final(msg)
			}
			return
		}
		// Non-terminal (stream chunk): plain buffered send. This is safe because
		// the channel is only ever closed on a terminal message, ctx cancel, or
		// teardown — none of which deliver here.
		select {
		case req.ch <- msg:
		default:
		}
		return
	}
	if msg.Type == MessageTypeHTTPResp || msg.Type == MessageTypeError || msg.Type == MessageTypeStreamEnd {
		s.manager.logDebugf("wsrelay: received terminal message for unknown id %s (provider=%s)", msg.ID, s.provider)
	}
}

func (s *session) send(ctx context.Context, msg Message) error {
	select {
	case <-s.closed:
		return errClosed
	default:
	}
	s.writeMutex.Lock()
	defer s.writeMutex.Unlock()
	if err := s.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	if err := s.conn.WriteJSON(msg); err != nil {
		return fmt.Errorf("write json: %w", err)
	}
	return nil
}

func (s *session) request(ctx context.Context, msg Message) (<-chan Message, error) {
	if msg.ID == "" {
		return nil, fmt.Errorf("wsrelay: message id is required")
	}
	if _, loaded := s.pending.LoadOrStore(msg.ID, &pendingRequest{ch: make(chan Message, 8)}); loaded {
		return nil, fmt.Errorf("wsrelay: duplicate message id %s", msg.ID)
	}
	value, _ := s.pending.Load(msg.ID)
	req := value.(*pendingRequest)
	if err := s.send(ctx, msg); err != nil {
		if actual, loaded := s.pending.LoadAndDelete(msg.ID); loaded {
			req := actual.(*pendingRequest)
			req.close()
		}
		return nil, err
	}
	go func() {
		select {
		case <-ctx.Done():
			if actual, loaded := s.pending.LoadAndDelete(msg.ID); loaded {
				actual.(*pendingRequest).close()
			}
		case <-s.closed:
		}
	}()
	return req.ch, nil
}

func (s *session) cleanup(cause error) {
	s.closeOnce.Do(func() {
		close(s.closed)
		// Drain and close every pending request. The map is never reset
		// (no `s.pending = sync.Map{}`): reassigning the field would be a
		// data race against concurrent request() goroutines that read
		// s.pending via LoadAndDelete when their context is cancelled.
		s.pending.Range(func(key, value any) bool {
			req := value.(*pendingRequest)
			msg := Message{ID: key.(string), Type: MessageTypeError, Payload: map[string]any{"error": cause.Error()}}
			req.final(msg)
			return true
		})
		_ = s.conn.Close()
		if s.manager != nil {
			s.manager.handleSessionClosed(s, cause)
		}
	})
}
