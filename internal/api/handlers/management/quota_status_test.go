package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestGetQuotaStatus_Empty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	h := &Handler{
		authManager: coreauth.NewManager(nil, nil, nil),
	}
	engine.GET("/v0/management/quota-status", h.GetQuotaStatus)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/quota-status", nil)
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp struct {
		QuotaStatus []quotaStatusEntry `json:"quota_status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.QuotaStatus) != 0 {
		t.Fatalf("quota_status len = %d, want 0", len(resp.QuotaStatus))
	}
}

func TestGetQuotaStatus_NilManager(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	h := &Handler{}
	engine.GET("/v0/management/quota-status", h.GetQuotaStatus)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/quota-status", nil)
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp struct {
		QuotaStatus []quotaStatusEntry `json:"quota_status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.QuotaStatus) != 0 {
		t.Fatalf("quota_status len = %d, want 0", len(resp.QuotaStatus))
	}
}

func TestGetQuotaStatus_WithSignals(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	manager := coreauth.NewManager(nil, nil, nil)
	observedAt := time.Now().Truncate(time.Second)
	auth := &coreauth.Auth{
		ID:       "auth-1",
		Provider: "claude",
		Quota: coreauth.QuotaState{
			ObservedAt: observedAt,
			Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-5h-Status": "allowed",
			},
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	h := &Handler{authManager: manager}
	engine.GET("/v0/management/quota-status", h.GetQuotaStatus)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/quota-status", nil)
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp struct {
		QuotaStatus []quotaStatusEntry `json:"quota_status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.QuotaStatus) != 1 {
		t.Fatalf("quota_status len = %d, want 1", len(resp.QuotaStatus))
	}
	entry := resp.QuotaStatus[0]
	if entry.AuthID != "auth-1" {
		t.Fatalf("auth_id = %q, want %q", entry.AuthID, "auth-1")
	}
	if entry.Provider != "claude" {
		t.Fatalf("provider = %q, want %q", entry.Provider, "claude")
	}
	if entry.Model != "" {
		t.Fatalf("model = %q, want empty", entry.Model)
	}
	if entry.ObservedAt == "" {
		t.Fatalf("observed_at is empty")
	}
	if _, err := time.Parse(time.RFC3339, entry.ObservedAt); err != nil {
		t.Fatalf("observed_at not RFC3339: %q (%v)", entry.ObservedAt, err)
	}
	if entry.Signals["Anthropic-Ratelimit-Unified-5h-Status"] != "allowed" {
		t.Fatalf("signals = %#v", entry.Signals)
	}
}

func TestGetQuotaStatus_SkipsUnsupportedProviders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "auth-kimi",
		Provider: "kimi",
		Quota: coreauth.QuotaState{
			ObservedAt: time.Now(),
			Signals:    map[string]string{"X-Test": "value"},
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	h := &Handler{authManager: manager}
	engine.GET("/v0/management/quota-status", h.GetQuotaStatus)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/quota-status", nil)
	engine.ServeHTTP(rec, req)

	var resp struct {
		QuotaStatus []quotaStatusEntry `json:"quota_status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.QuotaStatus) != 0 {
		t.Fatalf("unsupported provider quota leaked: %+v", resp.QuotaStatus)
	}
}

func TestGetQuotaStatus_ModelLevelQuota(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	manager := coreauth.NewManager(nil, nil, nil)
	observedAt := time.Now().Truncate(time.Second)
	auth := &coreauth.Auth{
		ID:       "auth-2",
		Provider: "codex",
		ModelStates: map[string]*coreauth.ModelState{
			"codex-model": {
				Quota: coreauth.QuotaState{
					ObservedAt: observedAt,
					Signals:    map[string]string{"X-Codex-Primary-Used-Percent": "42"},
					Exceeded:   true,
					Reason:     "quota",
				},
				UpdatedAt: observedAt,
			},
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	h := &Handler{authManager: manager}
	engine.GET("/v0/management/quota-status", h.GetQuotaStatus)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/quota-status", nil)
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp struct {
		QuotaStatus []quotaStatusEntry `json:"quota_status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.QuotaStatus) != 1 {
		t.Fatalf("quota_status len = %d, want 1", len(resp.QuotaStatus))
	}
	entry := resp.QuotaStatus[0]
	if entry.AuthID != "auth-2" {
		t.Fatalf("auth_id = %q, want %q", entry.AuthID, "auth-2")
	}
	if entry.Provider != "codex" {
		t.Fatalf("provider = %q, want %q", entry.Provider, "codex")
	}
	if entry.Model != "codex-model" {
		t.Fatalf("model = %q, want %q", entry.Model, "codex-model")
	}
	if !entry.Exceeded {
		t.Fatalf("exceeded = false, want true")
	}
	if entry.Reason != "quota" {
		t.Fatalf("reason = %q, want %q", entry.Reason, "quota")
	}
	if entry.Signals["X-Codex-Primary-Used-Percent"] != "42" {
		t.Fatalf("signals = %#v", entry.Signals)
	}
}

func TestGetQuotaStatus_Sorting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	manager := coreauth.NewManager(nil, nil, nil)
	observedAt := time.Now().Truncate(time.Second)
	auth := &coreauth.Auth{
		ID:       "auth-3",
		Provider: "claude",
		Label:    "test",
		Quota: coreauth.QuotaState{
			ObservedAt: observedAt,
			Signals:    map[string]string{"Anthropic-Ratelimit-Unified-5h-Status": "allowed"},
		},
		ModelStates: map[string]*coreauth.ModelState{
			"model-b": {
				Quota: coreauth.QuotaState{
					ObservedAt: observedAt,
					Signals:    map[string]string{"X-Model": "b"},
				},
				UpdatedAt: observedAt,
			},
			"model-a": {
				Quota: coreauth.QuotaState{
					ObservedAt: observedAt,
					Signals:    map[string]string{"X-Model": "a"},
				},
				UpdatedAt: observedAt,
			},
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	h := &Handler{authManager: manager}
	engine.GET("/v0/management/quota-status", h.GetQuotaStatus)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/quota-status", nil)
	engine.ServeHTTP(rec, req)

	var resp struct {
		QuotaStatus []quotaStatusEntry `json:"quota_status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.QuotaStatus) != 3 {
		t.Fatalf("quota_status len = %d, want 3", len(resp.QuotaStatus))
	}
	if resp.QuotaStatus[0].Model != "" {
		t.Fatalf("first entry model = %q, want empty", resp.QuotaStatus[0].Model)
	}
	if resp.QuotaStatus[1].Model != "model-a" {
		t.Fatalf("second entry model = %q, want model-a", resp.QuotaStatus[1].Model)
	}
	if resp.QuotaStatus[2].Model != "model-b" {
		t.Fatalf("third entry model = %q, want model-b", resp.QuotaStatus[2].Model)
	}
}
