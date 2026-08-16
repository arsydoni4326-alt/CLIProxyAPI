package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
)

func TestAuthenticateManagementKey_LocalhostIPBan_BlocksCorrectKeyDuringBan(t *testing.T) {
	h := &Handler{
		cfg:            &config.Config{},
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "test-secret",
	}

	for i := 0; i < 5; i++ {
		allowed, statusCode, errMsg, _ := h.AuthenticateManagementKey("127.0.0.1", true, "wrong-secret")
		if allowed {
			t.Fatalf("expected auth to be denied at attempt %d", i+1)
		}
		if statusCode != http.StatusUnauthorized || errMsg != "invalid management key" {
			t.Fatalf("unexpected auth failure at attempt %d: status=%d msg=%q", i+1, statusCode, errMsg)
		}
	}

	allowed, statusCode, errMsg, _ := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret")
	if allowed {
		t.Fatalf("expected correct key to be denied while banned")
	}
	if statusCode != http.StatusForbidden {
		t.Fatalf("expected forbidden status while banned, got %d", statusCode)
	}
	if !strings.HasPrefix(errMsg, "IP banned due to too many failed attempts. Try again in") {
		t.Fatalf("unexpected banned message: %q", errMsg)
	}
}

func TestAuthenticateManagementKey_RemoteDisabled_RemoteClientNeverBans(t *testing.T) {
	h := &Handler{
		cfg:            &config.Config{},
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "test-secret",
	}

	// allow-remote is off by default: remote attempts short-circuit before fail().
	for i := 0; i < 10; i++ {
		allowed, statusCode, errMsg, _ := h.AuthenticateManagementKey("203.0.113.1", false, "wrong-secret")
		if allowed {
			t.Fatalf("expected auth to be denied at attempt %d", i+1)
		}
		if statusCode != http.StatusForbidden || errMsg != "remote management disabled" {
			t.Fatalf("unexpected failure at attempt %d: status=%d msg=%q", i+1, statusCode, errMsg)
		}
	}
	if len(h.failedAttempts) != 0 {
		t.Fatalf("remote-disabled attempts must not accumulate failure state, got %d entries", len(h.failedAttempts))
	}

	// A local client is unaffected by the remote-disabled exercise.
	allowed, _, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret")
	if !allowed {
		t.Fatalf("expected local auth to succeed after remote-disabled attempts")
	}
}

func TestAuthenticateManagementKey_RemoteEnabled_RemoteClientBansAtThreshold(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{RemoteManagement: config.RemoteManagement{
			AllowRemote: true,
		}},
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "test-secret",
	}

	const threshold = 5
	for i := 0; i < threshold; i++ {
		allowed, statusCode, errMsg, _ := h.AuthenticateManagementKey("203.0.113.2", false, "wrong-secret")
		if allowed {
			t.Fatalf("expected auth to be denied at attempt %d", i+1)
		}
		if statusCode != http.StatusUnauthorized || errMsg != "invalid management key" {
			t.Fatalf("unexpected failure at attempt %d: status=%d msg=%q", i+1, statusCode, errMsg)
		}
	}

	// The threshold-th failure fires the ban; the next attempt is forbidden.
	allowed, statusCode, errMsg, _ := h.AuthenticateManagementKey("203.0.113.2", false, "wrong-secret")
	if allowed {
		t.Fatalf("expected banned remote attempt to be denied")
	}
	if statusCode != http.StatusForbidden {
		t.Fatalf("expected forbidden status after threshold overflow, got %d", statusCode)
	}
	if !strings.HasPrefix(errMsg, "IP banned due to too many failed attempts. Try again in") {
		t.Fatalf("unexpected banned message: %q", errMsg)
	}
}

func TestAuthenticateManagementKey_CustomThreshold(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{RemoteManagement: config.RemoteManagement{
			MaxFailedAttempts: 2,
		}},
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "test-secret",
	}

	for i := 0; i < 2; i++ {
		allowed, statusCode, errMsg, _ := h.AuthenticateManagementKey("127.0.0.1", true, "wrong-secret")
		if allowed {
			t.Fatalf("expected auth to be denied at attempt %d", i+1)
		}
		if statusCode != http.StatusUnauthorized || errMsg != "invalid management key" {
			t.Fatalf("unexpected failure at attempt %d: status=%d msg=%q", i+1, statusCode, errMsg)
		}
	}

	allowed, statusCode, errMsg, _ := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret")
	if allowed {
		t.Fatalf("expected correct key to be denied while banned")
	}
	if statusCode != http.StatusForbidden {
		t.Fatalf("expected forbidden status while banned, got %d", statusCode)
	}
	if !strings.HasPrefix(errMsg, "IP banned due to too many failed attempts. Try again in") {
		t.Fatalf("unexpected banned message: %q", errMsg)
	}
}

func TestAuthenticateManagementKey_CustomBanDuration(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{RemoteManagement: config.RemoteManagement{
			MaxFailedAttempts: 1,
			BanDuration:       "5m",
		}},
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "test-secret",
	}

	allowed, statusCode, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "wrong-secret")
	if allowed || statusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 on the single allowed failure, got allowed=%v status=%d", allowed, statusCode)
	}

	blocks := h.blockedIPSnapshot()
	if len(blocks) != 1 {
		t.Fatalf("expected 1 blocked IP, got %d", len(blocks))
	}
	if blocks[0].IP != "127.0.0.1" {
		t.Fatalf("unexpected blocked IP: %q", blocks[0].IP)
	}
	d, err := time.ParseDuration(blocks[0].Remaining)
	if err != nil {
		t.Fatalf("remaining %q is not a duration: %v", blocks[0].Remaining, err)
	}
	if d > 5*time.Minute || d < 4*time.Minute {
		t.Fatalf("unexpected remaining ban time: %v", d)
	}
}

func TestMiddlewareSetsSupportPluginHeader(t *testing.T) {

	h := &Handler{
		cfg:            &config.Config{},
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "test-secret",
	}
	middleware := h.Middleware()

	t.Run("invalid key", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/config", nil)
		c.Request.RemoteAddr = "127.0.0.1:12345"
		c.Request.Header.Set("X-Management-Key", "wrong-secret")

		middleware(c)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
		if got := rec.Header().Get("X-CPA-SUPPORT-PLUGIN"); got != pluginhost.SupportPluginHeaderValue() {
			t.Fatalf("X-CPA-SUPPORT-PLUGIN = %q, want %q", got, pluginhost.SupportPluginHeaderValue())
		}
	})

	t.Run("valid key", func(t *testing.T) {
		engine := gin.New()
		engine.GET("/v0/management/config", middleware, func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v0/management/config", nil)
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("X-Management-Key", "test-secret")
		engine.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got := rec.Header().Get("X-CPA-SUPPORT-PLUGIN"); got != pluginhost.SupportPluginHeaderValue() {
			t.Fatalf("X-CPA-SUPPORT-PLUGIN = %q, want %q", got, pluginhost.SupportPluginHeaderValue())
		}
	})
}
