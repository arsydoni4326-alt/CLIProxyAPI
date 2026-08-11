package api

// Contract-drift guard for the management surface that cpa-usage-keeper
// depends on (workspace ROADMAP Phase 2.7, step 3). The routes and semantics
// pinned here mirror cpa-usage-keeper/internal/cpa/endpoints.go and
// client.FetchUsageQueue; if the backend breaks one of them, these tests fail
// instead of Keeper silently dropping usage data.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
)

const keeperContractManagementKey = "keeper-contract-management-key"

func newKeeperContractServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("MANAGEMENT_PASSWORD", keeperContractManagementKey)
	return newTestServer(t)
}

func keeperContractRequest(t *testing.T, server *Server, method, target string, withAuth bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if withAuth {
		req.Header.Set("Authorization", "Bearer "+keeperContractManagementKey)
	}
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	return rr
}

// TestKeeperContractUsageQueue pins the usage-queue pull contract:
// Bearer auth required, bare JSON array, FIFO drain, and `count` semantics.
func TestKeeperContractUsageQueue(t *testing.T) {
	server := newKeeperContractServer(t)

	prevQueueEnabled := redisqueue.Enabled()
	redisqueue.SetEnabled(true)
	t.Cleanup(func() {
		redisqueue.SetEnabled(false)
		redisqueue.SetEnabled(prevQueueEnabled)
	})

	t.Run("unauthenticated requests are rejected with 401", func(t *testing.T) {
		if rr := keeperContractRequest(t, server, http.MethodGet, "/v0/management/usage-queue?count=1", false); rr.Code != http.StatusUnauthorized {
			t.Fatalf("missing key status = %d, want %d body=%s", rr.Code, http.StatusUnauthorized, rr.Body.String())
		}
	})

	t.Run("count must be a positive integer", func(t *testing.T) {
		for _, target := range []string{
			"/v0/management/usage-queue?count=0",
			"/v0/management/usage-queue?count=-3",
			"/v0/management/usage-queue?count=abc",
		} {
			if rr := keeperContractRequest(t, server, http.MethodGet, target, true); rr.Code != http.StatusBadRequest {
				t.Fatalf("GET %s status = %d, want %d body=%s", target, rr.Code, http.StatusBadRequest, rr.Body.String())
			}
		}
	})

	t.Run("pull drains records FIFO as a bare JSON array", func(t *testing.T) {
		// Earlier subtests (and test-server startup) may have enqueued records;
		// reset to a known-empty queue before seeding.
		for len(redisqueue.PopOldest(100)) > 0 {
		}
		redisqueue.Enqueue([]byte(`{"id":1}`))
		redisqueue.Enqueue([]byte(`{"id":2}`))
		redisqueue.Enqueue([]byte(`{"id":3}`))

		// No count => exactly one record (the oldest).
		rr := keeperContractRequest(t, server, http.MethodGet, "/v0/management/usage-queue", true)
		if rr.Code != http.StatusOK {
			t.Fatalf("default count status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
		var single []json.RawMessage
		if err := json.Unmarshal(rr.Body.Bytes(), &single); err != nil {
			t.Fatalf("default count response is not a JSON array: %v body=%s", err, rr.Body.String())
		}
		if len(single) != 1 || string(single[0]) != `{"id":1}` {
			t.Fatalf("default count payload = %s, want exactly one record {\"id\":1}", rr.Body.String())
		}

		// count=2 => the two remaining records, oldest first.
		rr = keeperContractRequest(t, server, http.MethodGet, "/v0/management/usage-queue?count=2", true)
		if rr.Code != http.StatusOK {
			t.Fatalf("count=2 status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
		var batch []json.RawMessage
		if err := json.Unmarshal(rr.Body.Bytes(), &batch); err != nil {
			t.Fatalf("count=2 response is not a JSON array: %v body=%s", err, rr.Body.String())
		}
		if len(batch) != 2 || string(batch[0]) != `{"id":2}` || string(batch[1]) != `{"id":3}` {
			t.Fatalf("count=2 payload = %s, want FIFO [{\"id\":2},{\"id\":3}]", rr.Body.String())
		}

		// Drained queue => 200 with an empty JSON array.
		rr = keeperContractRequest(t, server, http.MethodGet, "/v0/management/usage-queue?count=1", true)
		if rr.Code != http.StatusOK {
			t.Fatalf("drained status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
		var empty []json.RawMessage
		if err := json.Unmarshal(rr.Body.Bytes(), &empty); err != nil {
			t.Fatalf("drained response is not a JSON array: %v body=%s", err, rr.Body.String())
		}
		if len(empty) != 0 {
			t.Fatalf("drained payload = %s, want []", rr.Body.String())
		}
	})
}

// TestKeeperContractManagementRouteTable pins every management endpoint that
// cpa-usage-keeper calls (mirrors cpa-usage-keeper/internal/cpa/endpoints.go).
// Each route must (a) stay registered — a valid management key must not get a
// 404 — and (b) stay behind management auth — no key must get 401. A renamed
// or removed route fails this test loudly.
func TestKeeperContractManagementRouteTable(t *testing.T) {
	server := newKeeperContractServer(t)

	routes := []struct {
		name   string
		method string
		path   string
	}{
		{"usage queue pull", http.MethodGet, "/v0/management/usage-queue?count=1"},
		{"config", http.MethodGet, "/v0/management/config"},
		{"auth files", http.MethodGet, "/v0/management/auth-files"},
		{"api keys", http.MethodGet, "/v0/management/api-keys"},
		{"vertex api key", http.MethodGet, "/v0/management/vertex-api-key"},
		{"gemini api key", http.MethodGet, "/v0/management/gemini-api-key"},
		{"codex api key", http.MethodGet, "/v0/management/codex-api-key"},
		{"claude api key", http.MethodGet, "/v0/management/claude-api-key"},
		// Note: cpa-usage-keeper/internal/cpa/endpoints.go declares an ampcode
		// endpoint constant, but it is never called by the client and no such
		// backend route exists; it is intentionally not pinned here.
		{"openai compatibility", http.MethodGet, "/v0/management/openai-compatibility"},
		{"interactions api key", http.MethodGet, "/v0/management/interactions-api-key"},
		{"xai api key", http.MethodGet, "/v0/management/xai-api-key"},
		{"request log by id", http.MethodGet, "/v0/management/request-log-by-id/contract-test-id"},
		{"api call", http.MethodPost, "/v0/management/api-call"},
		{"auth files status", http.MethodPatch, "/v0/management/auth-files/status"},
	}

	for _, route := range routes {
		t.Run(route.name+" requires management auth", func(t *testing.T) {
			rr := keeperContractRequest(t, server, route.method, route.path, false)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s without key status = %d, want %d body=%s", route.method, route.path, rr.Code, http.StatusUnauthorized, rr.Body.String())
			}
		})

		t.Run(route.name+" stays registered", func(t *testing.T) {
			// A successful auth resets the failed-attempt counter for the client
			// IP, so the unauthenticated probes above cannot trigger the ban
			// threshold (5 consecutive failures) for the same IP.
			rr := keeperContractRequest(t, server, route.method, route.path, true)
			assertRouteRegistered(t, route, rr)
		})
	}
}

// assertRouteRegistered fails when a management route was dropped or renamed.
// Gin's unmatched-route 404 carries an empty body, while a registered handler
// (e.g. GetRequestLogByID with no matching log file) returns a domain 404 with
// a JSON error body — so only an empty-body 404 signals contract drift.
func assertRouteRegistered(t *testing.T, route struct {
	name   string
	method string
	path   string
}, rr *httptest.ResponseRecorder) {
	t.Helper()
	if rr.Code == http.StatusNotFound && rr.Body.Len() == 0 {
		t.Fatalf("%s %s returned an empty 404 with a valid management key; route renamed or removed (keeper contract drift)", route.method, route.path)
	}
}
