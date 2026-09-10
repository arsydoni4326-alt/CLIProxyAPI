package auth

import (
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestDebugLogAuthSelection_IncludesProviderNameForAPIKey(t *testing.T) {
	prevLevel := log.GetLevel()
	log.SetLevel(log.DebugLevel)
	defer log.SetLevel(prevLevel)

	logger, hook := logtest.NewNullLogger()
	logger.SetLevel(log.DebugLevel)

	auth := &Auth{
		Provider: "openai-compatibility",
		Label:    "glm",
		Attributes: map[string]string{
			"auth_kind": "api_key",
			"api_key":   "sk-abcdefghijklmnopqrstuvwxyz012345",
		},
	}

	debugLogAuthSelection(logger.WithField("test", "api_key"), auth, "openai-compatibility", "glm-5.3-flash")

	if len(hook.Entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(hook.Entries))
	}
	msg := hook.Entries[0].Message
	if !strings.Contains(msg, "(provider=glm)") {
		t.Fatalf("expected log to contain specific provider name \"(provider=glm)\", got: %s", msg)
	}
	if !strings.Contains(msg, "Use API key") {
		t.Fatalf("expected log to contain \"Use API key\", got: %s", msg)
	}
	if !strings.Contains(msg, "for model glm-5.3-flash") {
		t.Fatalf("expected log to contain model name, got: %s", msg)
	}
	if strings.Contains(msg, "sk-abcdefghijklmnopqrstuvwxyz012345") {
		t.Fatalf("expected API key to be hidden in log, got: %s", msg)
	}
}

func TestDebugLogAuthSelection_IncludesProviderForBuiltinAPIKey(t *testing.T) {
	prevLevel := log.GetLevel()
	log.SetLevel(log.DebugLevel)
	defer log.SetLevel(prevLevel)

	logger, hook := logtest.NewNullLogger()
	logger.SetLevel(log.DebugLevel)

	auth := &Auth{
		Provider: "gemini",
		Attributes: map[string]string{
			"auth_kind": "api_key",
			"api_key":   "AIzaSyTest1234567890",
		},
	}

	debugLogAuthSelection(logger.WithField("test", "builtin_api_key"), auth, "gemini", "gemini-3.5-flash")

	if len(hook.Entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(hook.Entries))
	}
	msg := hook.Entries[0].Message
	if !strings.Contains(msg, "(provider=gemini)") {
		t.Fatalf("expected log to contain \"(provider=gemini)\", got: %s", msg)
	}
}

func TestDebugLogAuthSelection_OAuthIncludesAccountName(t *testing.T) {
	prevLevel := log.GetLevel()
	log.SetLevel(log.DebugLevel)
	defer log.SetLevel(prevLevel)

	logger, hook := logtest.NewNullLogger()
	logger.SetLevel(log.DebugLevel)

	auth := &Auth{
		Provider: "codex",
		FileName: "auths/codex-user@example.com.json",
		Metadata: map[string]any{
			"auth_kind":    "oauth",
			"email":        "codex-user@example.com",
			"access_token": "token",
		},
	}

	debugLogAuthSelection(logger.WithField("test", "oauth"), auth, "codex", "gpt-5.6")

	if len(hook.Entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(hook.Entries))
	}
	msg := hook.Entries[0].Message
	for _, want := range []string{
		"Use OAuth",
		"provider=codex",
		"account=codex-user@example.com",
		"auth_file=codex-user@example.com.json",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("expected log to contain %q, got: %s", want, msg)
		}
	}
}

func TestDebugLogAuthSelection_OAuthWithoutEmailStillLogsProvider(t *testing.T) {
	prevLevel := log.GetLevel()
	log.SetLevel(log.DebugLevel)
	defer log.SetLevel(prevLevel)

	logger, hook := logtest.NewNullLogger()
	logger.SetLevel(log.DebugLevel)

	auth := &Auth{
		Provider: "claude",
		FileName: "auths/claude.json",
		Metadata: map[string]any{
			"auth_kind":    "oauth",
			"access_token": "token",
		},
	}

	debugLogAuthSelection(logger.WithField("test", "oauth_no_email"), auth, "claude", "claude-fable-5")

	if len(hook.Entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(hook.Entries))
	}
	msg := hook.Entries[0].Message
	if !strings.Contains(msg, "provider=claude") {
		t.Fatalf("expected log to contain \"provider=claude\", got: %s", msg)
	}
	if strings.Contains(msg, "account=") {
		t.Fatalf("expected no account= segment when email is missing, got: %s", msg)
	}
}

func TestProviderDisplayName(t *testing.T) {
	cases := []struct {
		name     string
		auth     *Auth
		provider string
		want     string
	}{
		{
			name:     "nil auth falls back to provider arg",
			auth:     nil,
			provider: "gemini",
			want:     "gemini",
		},
		{
			name: "compat_name preferred for openai compatibility",
			auth: &Auth{
				Provider:   "openai-compatibility",
				Attributes: map[string]string{"compat_name": "glm"},
			},
			provider: "openai-compatibility",
			want:     "glm",
		},
		{
			name: "label used when compat_name missing",
			auth: &Auth{
				Provider: "openai-compatibility",
				Label:    "custom-upstream",
			},
			provider: "openai-compatibility",
			want:     "custom-upstream",
		},
		{
			name: "builtin provider name returned as-is",
			auth: &Auth{
				Provider: "claude",
			},
			provider: "claude",
			want:     "claude",
		},
		{
			name:     "auth provider overrides provider arg",
			auth:     &Auth{Provider: "codex"},
			provider: "ignored",
			want:     "codex",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := providerDisplayName(tc.auth, tc.provider)
			if got != tc.want {
				t.Fatalf("providerDisplayName() = %q, want %q", got, tc.want)
			}
		})
	}
}
