package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestGetOpenAICompatIncludesDisableCooling(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	requestRetry := 0
	disableCooling := true
	h := NewHandlerWithoutConfigFilePath(&config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "Mimo CN",
				BaseURL: "https://token-plan-cn.xiaomimimo.com/v1",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "test-key"},
				},
				Models: []config.OpenAICompatibilityModel{
					{Name: "mimo-v2.5", Alias: ""},
				},
				SupportPromptCacheKey: true,
				DisableCooling:        &disableCooling,
				RequestRetry:          &requestRetry,
			},
		},
	}, nil)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/openai-compatibility", nil)
	h.GetOpenAICompat(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var body struct {
		OpenAICompatibility []struct {
			SupportPromptCacheKey *bool `json:"support-prompt-cache-key"`
			DisableCooling        *bool `json:"disable-cooling"`
			RequestRetry          *int  `json:"request-retry"`
		} `json:"openai-compatibility"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(body.OpenAICompatibility) != 1 {
		t.Fatalf("expected 1 openai-compatibility entry, got %d", len(body.OpenAICompatibility))
	}
	if body.OpenAICompatibility[0].SupportPromptCacheKey == nil || !*body.OpenAICompatibility[0].SupportPromptCacheKey {
		t.Fatalf("expected support-prompt-cache-key to be present and true, got %#v", body.OpenAICompatibility[0].SupportPromptCacheKey)
	}
	if body.OpenAICompatibility[0].DisableCooling == nil || !*body.OpenAICompatibility[0].DisableCooling {
		t.Fatalf("expected disable-cooling to be present and true, got %#v", body.OpenAICompatibility[0].DisableCooling)
	}
	if body.OpenAICompatibility[0].RequestRetry == nil || *body.OpenAICompatibility[0].RequestRetry != 0 {
		t.Fatalf("expected request-retry to be present and 0, got %#v", body.OpenAICompatibility[0].RequestRetry)
	}
}

func TestOpenAICompatKeyAuthIndexMatchesSynthesizedCredentialAfterProxyChange(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "Local Router",
				BaseURL: "http://127.0.0.1:20128/v1",
				Prefix:  "local",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "direct-test-key", ProxyURL: "direct"},
				},
			},
		},
	}
	synthesized, errSynthesize := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{
		Config:      cfg,
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errSynthesize != nil {
		t.Fatalf("synthesize config credentials: %v", errSynthesize)
	}
	if len(synthesized) != 1 {
		t.Fatalf("synthesized credentials = %d, want 1", len(synthesized))
	}

	manager := coreauth.NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(context.Background(), synthesized[0]); errRegister != nil {
		t.Fatalf("register synthesized credential: %v", errRegister)
	}
	h := &Handler{cfg: cfg, authManager: manager}

	entries := h.openAICompatibilityWithAuthIndex()
	if len(entries) != 1 || len(entries[0].APIKeyEntries) != 1 {
		t.Fatalf("OpenAI compatibility entries = %#v, want one credential", entries)
	}
	if got, want := entries[0].APIKeyEntries[0].AuthIndex, synthesized[0].EnsureIndex(); got != want {
		t.Fatalf("auth index = %q, want %q", got, want)
	}
}
