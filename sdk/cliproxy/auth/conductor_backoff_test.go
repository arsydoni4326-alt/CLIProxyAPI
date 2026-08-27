package auth

import (
	"math"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// --- QuotaBackoffConfig accessor tests ---

func TestQuotaBackoffConfig_Defaults(t *testing.T) {
	var nilCfg *internalconfig.QuotaBackoffConfig
	var zeroCfg internalconfig.QuotaBackoffConfig

	// Nil config: all defaults
	if !nilCfg.IsEnabled() {
		t.Fatal("nil config should default enabled=true")
	}
	if nilCfg.Base() != time.Second {
		t.Fatalf("nil config base = %v, want 1s", nilCfg.Base())
	}
	if nilCfg.MaxCap() != 30*time.Minute {
		t.Fatalf("nil config max = %v, want 30m", nilCfg.MaxCap())
	}
	if math.Abs(nilCfg.Jitter()-0.20) > 1e-9 {
		t.Fatalf("nil config jitter = %v, want 0.20", nilCfg.Jitter())
	}

	// Zero config: same defaults
	if !zeroCfg.IsEnabled() {
		t.Fatal("zero config should default enabled=true")
	}
	if zeroCfg.Base() != time.Second {
		t.Fatalf("zero config base = %v, want 1s", zeroCfg.Base())
	}
}

func TestQuotaBackoffConfig_CustomValues(t *testing.T) {
	enabled := false
	base := 5
	max := 600
	jitter := 0.10
	cfg := &internalconfig.QuotaBackoffConfig{
		Enabled:        &enabled,
		BaseSeconds:    &base,
		MaxSeconds:     &max,
		JitterFraction: &jitter,
	}

	if cfg.IsEnabled() {
		t.Fatal("expected disabled")
	}
	if cfg.Base() != 5*time.Second {
		t.Fatalf("base = %v, want 5s", cfg.Base())
	}
	if cfg.MaxCap() != 10*time.Minute {
		t.Fatalf("max = %v, want 10m", cfg.MaxCap())
	}
	if math.Abs(cfg.Jitter()-0.10) > 1e-9 {
		t.Fatalf("jitter = %v, want 0.10", cfg.Jitter())
	}
}

func TestQuotaBackoffConfig_NegativeAndOversizedValues(t *testing.T) {
	base := -1
	max := -10
	jitter := 5.0 // > 1.0
	cfg := &internalconfig.QuotaBackoffConfig{
		BaseSeconds:    &base,
		MaxSeconds:     &max,
		JitterFraction: &jitter,
	}
	if cfg.Base() != time.Second {
		t.Fatalf("negative base should default to 1s, got %v", cfg.Base())
	}
	if cfg.MaxCap() != 30*time.Minute {
		t.Fatalf("negative max should default to 30m, got %v", cfg.MaxCap())
	}
	if cfg.Jitter() != 1.0 {
		t.Fatalf("jitter > 1.0 should be capped at 1.0, got %v", cfg.Jitter())
	}
}

// --- nextQuotaCooldown tests ---

func TestNextQuotaCooldown_BasicEscalation(t *testing.T) {
	// Use default config (enabled, base=1s, max=30m, jitter=0.20)
	SetQuotaBackoffConfig(&internalconfig.QuotaBackoffConfig{})

	// Level 0 → level 1, delay should be around 1s ± jitter
	delay, level := nextQuotaCooldown(0, false)
	if level != 1 {
		t.Fatalf("level = %d, want 1", level)
	}
	if delay < time.Second || delay > 2*time.Second {
		t.Fatalf("delay = %v, want ~1s (±20%%)", delay)
	}

	// Level 1 → level 2, delay should be around 2s ± jitter
	delay, level = nextQuotaCooldown(1, false)
	if level != 2 {
		t.Fatalf("level = %d, want 2", level)
	}
	if delay < 1600*time.Millisecond || delay > 3*time.Second {
		t.Fatalf("delay = %v, want ~2s (±20%%)", delay)
	}
}

func TestNextQuotaCooldown_JitterRange(t *testing.T) {
	// Disable jitter to test deterministic base behavior
	jitter := 0.0
	SetQuotaBackoffConfig(&internalconfig.QuotaBackoffConfig{JitterFraction: &jitter})

	// Run 100 iterations at level 5 (base delay = 32s)
	var minDelay, maxDelay time.Duration
	for i := 0; i < 100; i++ {
		delay, _ := nextQuotaCooldown(5, false)
		if i == 0 || delay < minDelay {
			minDelay = delay
		}
		if i == 0 || delay > maxDelay {
			maxDelay = delay
		}
	}
	// With jitter=0, all delays should be identical (32s)
	if minDelay != 32*time.Second || maxDelay != 32*time.Second {
		t.Fatalf("jitter=0 should be deterministic: min=%v max=%v, want both 32s", minDelay, maxDelay)
	}
}

func TestNextQuotaCooldown_JitterDisabled(t *testing.T) {
	jitter := 0.0
	SetQuotaBackoffConfig(&internalconfig.QuotaBackoffConfig{JitterFraction: &jitter})

	for level := 0; level < 10; level++ {
		delay, newLevel := nextQuotaCooldown(level, false)
		expected := time.Duration(1<<level) * time.Second
		if expected > 30*time.Minute {
			expected = 30 * time.Minute
		}
		if delay != expected {
			t.Fatalf("level %d: delay = %v, want %v", level, delay, expected)
		}
		if newLevel != level+1 {
			t.Fatalf("level %d: newLevel = %d, want %d", level, newLevel, level+1)
		}
	}
}

func TestNextQuotaCooldown_DisableCooling(t *testing.T) {
	SetQuotaBackoffConfig(&internalconfig.QuotaBackoffConfig{})
	delay, level := nextQuotaCooldown(5, true)
	if delay != 0 {
		t.Fatalf("disableCooling=true should return 0 delay, got %v", delay)
	}
	if level != 5 {
		t.Fatalf("disableCooling=true should keep level, got %d", level)
	}
}

func TestNextQuotaCooldown_DisabledViaConfig(t *testing.T) {
	enabled := false
	SetQuotaBackoffConfig(&internalconfig.QuotaBackoffConfig{Enabled: &enabled})
	delay, level := nextQuotaCooldown(3, false)
	if delay != 0 {
		t.Fatalf("config enabled=false should return 0 delay, got %v", delay)
	}
	if level != 3 {
		t.Fatalf("config enabled=false should keep level, got %d", level)
	}
}

func TestNextQuotaCooldown_CapsAtMax(t *testing.T) {
	max := 10
	SetQuotaBackoffConfig(&internalconfig.QuotaBackoffConfig{MaxSeconds: &max, JitterFraction: floatPtr(0)})

	// Level 4 = 16s exceeds max of 10s → should cap and not advance level
	delay, level := nextQuotaCooldown(4, false)
	if delay != 10*time.Second {
		t.Fatalf("delay = %v, want 10s (cap)", delay)
	}
	if level != 4 {
		t.Fatalf("level = %d, want 4 (no advance at cap)", level)
	}
}

func TestNextQuotaCooldown_NegativeLevel(t *testing.T) {
	SetQuotaBackoffConfig(&internalconfig.QuotaBackoffConfig{})
	delay, level := nextQuotaCooldown(-1, false)
	// Negative level normalizes to 0 → base delay ± jitter
	if level != 1 {
		t.Fatalf("level = %d, want 1", level)
	}
	if delay < time.Second || delay > 2*time.Second {
		t.Fatalf("delay = %v, want ~1s", delay)
	}
}

func floatPtr(f float64) *float64 { return &f }
