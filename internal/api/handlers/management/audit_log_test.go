package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
)

// spyHook captures log entries so tests can assert on the structured audit fields.
type spyHook struct {
	mu      sync.Mutex
	entries []*log.Entry
}

func (s *spyHook) Levels() []log.Level { return log.AllLevels }

func (s *spyHook) Fire(entry *log.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
	return nil
}

func (s *spyHook) mutationEntries() []*log.Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*log.Entry
	for _, e := range s.entries {
		if e.Message == "management_mutation" {
			out = append(out, e)
		}
	}
	return out
}

// auditTestServer mounts the management middleware over an any-method stub route
// and returns the capturing hook plus the engine.
func auditTestServer(t *testing.T, h *Handler) (*spyHook, *gin.Engine) {
	t.Helper()
	hook := &spyHook{}
	log.AddHook(hook)
	t.Cleanup(func() {
		log.StandardLogger().ReplaceHooks(log.LevelHooks{})
	})
	engine := gin.New()
	engine.Any("/v0/management/config.yaml", h.Middleware(), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	return hook, engine
}

func TestMiddlewareAuditLog_ConfigKeyMutationLogged(t *testing.T) {
	// Real bcrypt hash so the config-key auth path (and its pseudonymous actor)
	// is exercised end-to-end; the raw key is never stored on the handler.
	hash, errGenerate := bcrypt.GenerateFromPassword([]byte("test-secret"), bcrypt.MinCost)
	if errGenerate != nil {
		t.Fatalf("generate bcrypt hash: %v", errGenerate)
	}

	h := &Handler{
		cfg: &config.Config{
			RemoteManagement: config.RemoteManagement{
				SecretKey:       string(hash),
				AuditLogEnabled: true,
			},
		},
		failedAttempts: make(map[string]*attemptInfo),
	}
	hook, engine := auditTestServer(t, h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v0/management/config.yaml", strings.NewReader("remote-management:\n  secret-key: changed\n"))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Management-Key", "test-secret")
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	mutations := hook.mutationEntries()
	if len(mutations) != 1 {
		t.Fatalf("expected 1 management_mutation entry, got %d", len(mutations))
	}
	e := mutations[0]
	if e.Data["event"] != "admin_action" {
		t.Errorf("event = %v, want admin_action", e.Data["event"])
	}
	if e.Data["method"] != http.MethodPut {
		t.Errorf("method = %v, want %s", e.Data["method"], http.MethodPut)
	}
	if e.Data["path"] != "/v0/management/config.yaml" {
		t.Errorf("path = %v, want /v0/management/config.yaml", e.Data["path"])
	}
	if e.Data["status"] != http.StatusOK {
		t.Errorf("status = %v, want %d", e.Data["status"], http.StatusOK)
	}
	if e.Data["local"] != true {
		t.Errorf("local = %v, want true", e.Data["local"])
	}
	// Pseudonymous actor: first 8 chars of the bcrypt hash, never the key itself.
	if e.Data["actor"] != string(hash[:8]) {
		t.Errorf("actor = %v, want %q", e.Data["actor"], string(hash[:8]))
	}
	if strings.Contains(e.Message, "test-secret") {
		t.Errorf("audit entry must never contain the raw key")
	}
}

func TestMiddlewareAuditLog_EnvKeyActorLogged(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{
			RemoteManagement: config.RemoteManagement{AuditLogEnabled: true},
		},
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "test-secret",
	}
	hook, engine := auditTestServer(t, h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/config.yaml", strings.NewReader("{}"))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Management-Key", "test-secret")
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	mutations := hook.mutationEntries()
	if len(mutations) != 1 {
		t.Fatalf("expected 1 management_mutation entry, got %d", len(mutations))
	}
	if e := mutations[0]; e.Data["actor"] != "env" {
		t.Errorf("actor = %v, want env", e.Data["actor"])
	}
}

func TestMiddlewareAuditLog_GetNotLogged(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{
			RemoteManagement: config.RemoteManagement{AuditLogEnabled: true},
		},
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "test-secret",
	}
	hook, engine := auditTestServer(t, h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/config.yaml", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Management-Key", "test-secret")
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := hook.mutationEntries(); len(got) != 0 {
		t.Fatalf("expected no audit entry for GET, got %d", len(got))
	}
}

func TestMiddlewareAuditLog_DisabledNoLog(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{
			RemoteManagement: config.RemoteManagement{},
		},
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "test-secret",
	}
	hook, engine := auditTestServer(t, h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v0/management/config.yaml", strings.NewReader("{}"))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Management-Key", "test-secret")
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := hook.mutationEntries(); len(got) != 0 {
		t.Fatalf("expected no audit entry when disabled, got %d", len(got))
	}
}
