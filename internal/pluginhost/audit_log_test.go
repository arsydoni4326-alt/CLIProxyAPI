package pluginhost

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

// auditSpyHook captures log entries so tests can assert on the plugin_ws_event fields.
type auditSpyHook struct {
	mu      sync.Mutex
	entries []*log.Entry
}

func (s *auditSpyHook) Levels() []log.Level { return log.AllLevels }

func (s *auditSpyHook) Fire(entry *log.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
	return nil
}

func (s *auditSpyHook) pluginWSEventEntries() []*log.Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*log.Entry
	for _, e := range s.entries {
		if e.Message == "plugin_ws_event" {
			out = append(out, e)
		}
	}
	return out
}

func setupAuditSpy(t *testing.T) *auditSpyHook {
	t.Helper()
	hook := &auditSpyHook{}
	log.AddHook(hook)
	t.Cleanup(func() {
		log.StandardLogger().ReplaceHooks(log.LevelHooks{})
	})
	return hook
}

func TestPluginAuditLog_EmitsWhenEnabled(t *testing.T) {
	hook := setupAuditSpy(t)
	host := newHostWithRecords(capabilityRecord{
		id: "test-plugin",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				WebSocketResponseObserver: testWebSocketObserverFunc(func(_ context.Context, _ pluginapi.WebSocketResponseEvent) error {
					return nil
				}),
			},
		},
	})
	host.runtimeConfig = &config.Config{
		Plugins: config.PluginsConfig{AuditLogEnabled: true},
	}

	host.ObserveWebSocketResponseEvent(context.Background(), pluginapi.WebSocketResponseEvent{
		RequestID:    "req-100",
		TraceID:      "trace-200",
		SourceFormat: "openai",
		Model:        "gpt-4o",
		Provider:     "openai",
		AuthID:       "auth-abc",
		AuthLabel:    "my-key",
		EventType:    "test.event",
		Payload:      []byte(`{"sensitive":"data"}`),
		Metadata:     map[string]any{"secret": "value"},
	})

	entries := hook.pluginWSEventEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 plugin_ws_event entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Data["event"] != "plugin_ws_event" {
		t.Errorf("event = %v, want plugin_ws_event", e.Data["event"])
	}
	if e.Data["plugin_id"] != "test-plugin" {
		t.Errorf("plugin_id = %v, want test-plugin", e.Data["plugin_id"])
	}
	if e.Data["event_type"] != "test.event" {
		t.Errorf("event_type = %v, want test.event", e.Data["event_type"])
	}
	if e.Data["source_format"] != "openai" {
		t.Errorf("source_format = %v, want openai", e.Data["source_format"])
	}
	if e.Data["model"] != "gpt-4o" {
		t.Errorf("model = %v, want gpt-4o", e.Data["model"])
	}
	if e.Data["provider"] != "openai" {
		t.Errorf("provider = %v, want openai", e.Data["provider"])
	}
	if e.Data["auth_id"] != "auth-abc" {
		t.Errorf("auth_id = %v, want auth-abc", e.Data["auth_id"])
	}
	if e.Data["auth_label"] != "my-key" {
		t.Errorf("auth_label = %v, want my-key", e.Data["auth_label"])
	}
	if e.Data["request_id"] != "req-100" {
		t.Errorf("request_id = %v, want req-100", e.Data["request_id"])
	}
	if e.Data["trace_id"] != "trace-200" {
		t.Errorf("trace_id = %v, want trace-200", e.Data["trace_id"])
	}
}

func TestPluginAuditLog_NoLogWhenDisabled(t *testing.T) {
	hook := setupAuditSpy(t)
	host := newHostWithRecords(capabilityRecord{
		id: "test-plugin",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				WebSocketResponseObserver: testWebSocketObserverFunc(func(_ context.Context, _ pluginapi.WebSocketResponseEvent) error {
					return nil
				}),
			},
		},
	})
	host.runtimeConfig = &config.Config{
		Plugins: config.PluginsConfig{AuditLogEnabled: false},
	}

	host.ObserveWebSocketResponseEvent(context.Background(), pluginapi.WebSocketResponseEvent{
		RequestID:    "req-100",
		EventType:    "test.event",
		SourceFormat: "openai",
		Model:        "gpt-4o",
		Provider:     "openai",
	})

	entries := hook.pluginWSEventEntries()
	if len(entries) != 0 {
		t.Fatalf("expected 0 plugin_ws_event entries when disabled, got %d", len(entries))
	}
}

func TestPluginAuditLog_NoLogWhenNilConfig(t *testing.T) {
	hook := setupAuditSpy(t)
	host := newHostWithRecords(capabilityRecord{
		id: "test-plugin",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				WebSocketResponseObserver: testWebSocketObserverFunc(func(_ context.Context, _ pluginapi.WebSocketResponseEvent) error {
					return nil
				}),
			},
		},
	})
	// runtimeConfig is nil (default from New()).

	host.ObserveWebSocketResponseEvent(context.Background(), pluginapi.WebSocketResponseEvent{
		RequestID:    "req-100",
		EventType:    "test.event",
		SourceFormat: "openai",
	})

	entries := hook.pluginWSEventEntries()
	if len(entries) != 0 {
		t.Fatalf("expected 0 plugin_ws_event entries when runtimeConfig is nil, got %d", len(entries))
	}
}

func TestPluginAuditLog_PayloadAndMetadataNeverLogged(t *testing.T) {
	hook := setupAuditSpy(t)
	host := newHostWithRecords(capabilityRecord{
		id: "test-plugin",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				WebSocketResponseObserver: testWebSocketObserverFunc(func(_ context.Context, _ pluginapi.WebSocketResponseEvent) error {
					return nil
				}),
			},
		},
	})
	host.runtimeConfig = &config.Config{
		Plugins: config.PluginsConfig{AuditLogEnabled: true},
	}

	host.ObserveWebSocketResponseEvent(context.Background(), pluginapi.WebSocketResponseEvent{
		RequestID:    "req-300",
		EventType:    "sensitive.event",
		Payload:      []byte(`{"password":"hunter2","api_key":"sk-abc123"}`),
		Metadata:     map[string]any{"token": "secret-token", "nested": map[string]any{"deep": "value"}},
		SourceFormat: "openai",
	})

	entries := hook.pluginWSEventEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	entryStr := entries[0].Message
	if strings.Contains(entryStr, "hunter2") || strings.Contains(entryStr, "sk-abc123") || strings.Contains(entryStr, "secret-token") {
		t.Errorf("audit entry must not contain payload or metadata content")
	}
	for _, e := range entries {
		for _, v := range e.Data {
			if vs, ok := v.(string); ok {
				if strings.Contains(vs, "hunter2") || strings.Contains(vs, "sk-abc123") || strings.Contains(vs, "secret-token") {
					t.Errorf("audit field must not contain payload or metadata content")
				}
			}
		}
	}
}

func TestPluginAuditLog_PerPluginFanOut(t *testing.T) {
	hook := setupAuditSpy(t)
	host := newHostWithRecords(
		capabilityRecord{
			id: "plugin-a",
			plugin: pluginapi.Plugin{
				Capabilities: pluginapi.Capabilities{
					WebSocketResponseObserver: testWebSocketObserverFunc(func(_ context.Context, _ pluginapi.WebSocketResponseEvent) error {
						return nil
					}),
				},
			},
		},
		capabilityRecord{
			id: "plugin-b",
			plugin: pluginapi.Plugin{
				Capabilities: pluginapi.Capabilities{
					WebSocketResponseObserver: testWebSocketObserverFunc(func(_ context.Context, _ pluginapi.WebSocketResponseEvent) error {
						return nil
					}),
				},
			},
		},
	)
	host.runtimeConfig = &config.Config{
		Plugins: config.PluginsConfig{AuditLogEnabled: true},
	}

	host.ObserveWebSocketResponseEvent(context.Background(), pluginapi.WebSocketResponseEvent{
		RequestID:    "req-500",
		EventType:    "shared.event",
		SourceFormat: "openai",
		Model:        "claude-4",
		Provider:     "anthropic",
	})

	entries := hook.pluginWSEventEntries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 plugin_ws_event entries (one per plugin), got %d", len(entries))
	}
	seen := map[string]bool{}
	for _, e := range entries {
		id, _ := e.Data["plugin_id"].(string)
		seen[id] = true
	}
	if !seen["plugin-a"] {
		t.Errorf("expected plugin_id=plugin-a in entries")
	}
	if !seen["plugin-b"] {
		t.Errorf("expected plugin_id=plugin-b in entries")
	}
}
