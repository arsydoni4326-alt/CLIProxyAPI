package api

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	configaccess "github.com/router-for-me/CLIProxyAPI/v7/internal/access/config_access"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

// attachTestWSRoute registers a synthetic WebSocket route guarded by the
// conditional ws-auth middleware and reports whether the handler was reached.
func attachTestWSRoute(t *testing.T, server *Server, path string) *atomic.Int32 {
	t.Helper()
	var reached atomic.Int32
	server.AttachWebsocketRoute(path, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ws-handler"))
	}))
	return &reached
}

func serveWSRequest(server *Server, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	return rr
}

func TestWebsocketAuthDisabledAllowsUnauthenticated(t *testing.T) {
	server := newTestServer(t)
	server.wsAuthEnabled.Store(false)
	reached := attachTestWSRoute(t, server, "/test-ws-disabled")

	rr := serveWSRequest(server, "/test-ws-disabled", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if got := reached.Load(); got != 1 {
		t.Fatalf("handler reached = %d, want 1", got)
	}
}

func TestWebsocketAuthEnabledRejectsMissingOrInvalidKey(t *testing.T) {
	server := newTestServer(t)
	server.wsAuthEnabled.Store(true)
	reached := attachTestWSRoute(t, server, "/test-ws-guard")

	cases := []struct {
		name    string
		path    string
		headers map[string]string
	}{
		{"no credentials", "/test-ws-guard", nil},
		{"wrong bearer", "/test-ws-guard", map[string]string{"Authorization": "Bearer wrong-key"}},
		{"wrong x-api-key", "/test-ws-guard", map[string]string{"x-api-key": "wrong-key"}},
		{"wrong query key", "/test-ws-guard?key=wrong-key", nil},
		{"empty bearer", "/test-ws-guard", map[string]string{"Authorization": "Bearer "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := reached.Load()
			rr := serveWSRequest(server, tc.path, tc.headers)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusUnauthorized, rr.Body.String())
			}
			if got := reached.Load(); got != before {
				t.Fatalf("handler invoked on rejected request (reached %d -> %d)", before, got)
			}
		})
	}
}

func TestWebsocketAuthEnabledAcceptsValidKey(t *testing.T) {
	server := newTestServer(t)
	server.wsAuthEnabled.Store(true)
	reached := attachTestWSRoute(t, server, "/test-ws-valid")

	cases := []struct {
		name    string
		path    string
		headers map[string]string
	}{
		{"bearer header", "/test-ws-valid", map[string]string{"Authorization": "Bearer test-key"}},
		{"x-api-key header", "/test-ws-valid", map[string]string{"x-api-key": "test-key"}},
		{"query key", "/test-ws-valid?key=test-key", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := reached.Load()
			rr := serveWSRequest(server, tc.path, tc.headers)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
			}
			if got := reached.Load(); got != before+1 {
				t.Fatalf("handler reached delta = %d, want 1", got-before)
			}
		})
	}
}

func TestWebsocketAuthHotToggle(t *testing.T) {
	server := newTestServer(t)
	server.wsAuthEnabled.Store(false)
	reached := attachTestWSRoute(t, server, "/test-ws-toggle")

	// Disabled: unauthenticated request passes through.
	rr := serveWSRequest(server, "/test-ws-toggle", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("disabled status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Simulate a hot reload flipping ws-auth on (server_reload.go stores cfg.WebsocketAuth).
	server.wsAuthEnabled.Store(true)
	before := reached.Load()
	rr = serveWSRequest(server, "/test-ws-toggle", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("enabled status = %d, want %d; body=%s", rr.Code, http.StatusUnauthorized, rr.Body.String())
	}
	if got := reached.Load(); got != before {
		t.Fatalf("handler invoked while ws-auth enabled (reached %d -> %d)", before, got)
	}

	// Authenticated request passes again, simulating key rotation: the old
	// key fails after the access manager is reconfigured, a new one works.
	rr = serveWSRequest(server, "/test-ws-toggle", map[string]string{"Authorization": "Bearer test-key"})
	if rr.Code != http.StatusOK {
		t.Fatalf("enabled+key status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Flip back off: unauthenticated access is restored without re-registering the route.
	server.wsAuthEnabled.Store(false)
	rr = serveWSRequest(server, "/test-ws-toggle", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("re-disabled status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
}

// seedConfigAccessProvider mirrors the production builder seeding: it registers
// the inline config-api-key provider and loads it into the server's access
// manager so authentication gates on the configured keys rather than falling
// through the legacy empty-provider allow-all path. Returns a cleanup that
// restores the manager to empty.
func seedConfigAccessProvider(t *testing.T, server *Server, keys []string) {
	t.Helper()
	configaccess.Register(&sdkconfig.SDKConfig{APIKeys: keys})
	server.accessManager.SetProviders(sdkaccess.RegisteredProviders())
	t.Cleanup(func() {
		// Detach from this server's manager so a later test cannot authenticate
		// against keys it never configured.
		server.accessManager.SetProviders(nil)
	})
}

func TestWebsocketAuthKeyRotationViaUpdateClients(t *testing.T) {
	server := newTestServer(t)
	server.wsAuthEnabled.Store(true)
	reached := attachTestWSRoute(t, server, "/test-ws-rotation")

	// Seed the inline provider against the initial key set, as the builder does.
	// Use rotation-scoped keys so no other test's keys satisfy this manager.
	seedConfigAccessProvider(t, server, []string{"rotation-key-a"})

	// Initially configured key works.
	rr := serveWSRequest(server, "/test-ws-rotation", map[string]string{"Authorization": "Bearer rotation-key-a"})
	if rr.Code != http.StatusOK {
		t.Fatalf("initial key status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Rotate keys through the same reconfiguration path a config reload uses.
	// ApplyAccessProviders re-registers the inline provider from the new config
	// and calls SetProviders on the manager, hot-swapping the accepted keys.
	// WebsocketAuth must stay true in the copied config: UpdateClientsContext
	// re-stores wsAuthEnabled from cfg.WebsocketAuth, so leaving it false would
	// disable auth on the route and mask the rotation assertions.
	updatedCfg := *server.cfg
	updatedCfg.APIKeys = []string{"rotation-key-b"}
	updatedCfg.WebsocketAuth = true
	server.UpdateClients(&updatedCfg)

	// Revoked key is rejected without invoking the handler.
	before := reached.Load()
	rr = serveWSRequest(server, "/test-ws-rotation", map[string]string{"Authorization": "Bearer rotation-key-a"})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("revoked key status = %d, want %d; body=%s", rr.Code, http.StatusUnauthorized, rr.Body.String())
	}
	if got := reached.Load(); got != before {
		t.Fatalf("handler invoked with revoked key (reached %d -> %d)", before, got)
	}

	// Newly rotated key authenticates without restarting or re-registering the route.
	rr = serveWSRequest(server, "/test-ws-rotation", map[string]string{"Authorization": "Bearer rotation-key-b"})
	if rr.Code != http.StatusOK {
		t.Fatalf("rotated key status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
}
