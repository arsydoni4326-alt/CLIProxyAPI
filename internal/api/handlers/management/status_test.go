package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestGetStatus_Defaults(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	h := &Handler{}
	engine.GET("/v0/management/status", h.GetStatus)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/status", nil)
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("status field = %q, want %q", resp.Status, "ok")
	}
	if resp.Version == "" {
		t.Fatalf("version field is empty")
	}
	if resp.WatcherState {
		t.Fatalf("watcher_state = true, want false when no watcher callback installed")
	}
	if resp.Uptime != "" || resp.StartedAt != "" {
		t.Fatalf("uptime/started_at set when no startedAt: uptime=%q started_at=%q", resp.Uptime, resp.StartedAt)
	}
}

func TestGetStatus_WithStartedAtAndWatcher(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	h := &Handler{}
	started := time.Now().Add(-90 * time.Second)
	h.SetStartedAt(started)
	h.SetWatcherState(func() bool { return true })
	engine.GET("/v0/management/status", h.GetStatus)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/status", nil)
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !resp.WatcherState {
		t.Fatalf("watcher_state = false, want true when watcher callback reports running")
	}
	if resp.StartedAt == "" {
		t.Fatalf("started_at is empty, want RFC3339 timestamp")
	}
	if _, err := time.Parse(time.RFC3339, resp.StartedAt); err != nil {
		t.Fatalf("started_at not RFC3339: %q (%v)", resp.StartedAt, err)
	}
	if resp.Uptime == "" {
		t.Fatalf("uptime is empty when startedAt is set")
	}
}

func TestSetWatcherState_ReportsFalse(t *testing.T) {
	h := &Handler{}
	h.SetStartedAt(time.Now())
	h.SetWatcherState(func() bool { return false })

	// Verify the installed callback reflects the latest state instead of a stale default.
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/v0/management/status", h.GetStatus)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/status", nil)
	engine.ServeHTTP(rec, req)

	var resp statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.WatcherState {
		t.Fatalf("watcher_state = true, want false from installed callback")
	}
}
