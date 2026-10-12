package config

import (
	"testing"
	"time"
)

// TestLLMTimeoutKnob: the per-request gateway timeout is configurable because
// a whole-profile consolidation pass over a mature store exceeds the old fixed
// 30 s (observed 2026-09-15: context deadline exceeded, zero actions applied).
func TestLLMTimeoutKnob(t *testing.T) {
	cfg := Default()
	if got := cfg.LLMTimeout(); got != 30*time.Second {
		t.Fatalf("default timeout = %v, want 30s", got)
	}
	t.Setenv("PALATE_LLM_TIMEOUT", "180")
	cfg = Default()
	applyEnv(&cfg)
	if got := cfg.LLMTimeout(); got != 180*time.Second {
		t.Fatalf("PALATE_LLM_TIMEOUT=180 → %v, want 3m", got)
	}
	// A non-positive value falls back rather than disabling the deadline.
	t.Setenv("PALATE_LLM_TIMEOUT", "0")
	cfg = Default()
	applyEnv(&cfg)
	if got := cfg.LLMTimeout(); got != 30*time.Second {
		t.Fatalf("PALATE_LLM_TIMEOUT=0 → %v, want the 30s fallback", got)
	}
}
