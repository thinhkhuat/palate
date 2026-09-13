package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLLMConfigured(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		model    string
		want     bool
	}{
		{"both empty", "", "", false},
		{"endpoint only", "https://api.example.com/v1/chat/completions", "", false},
		{"model only", "", "gpt", false},
		{"both set", "https://api.example.com/v1/chat/completions", "gpt", true},
	}
	for _, tc := range cases {
		cfg := Default()
		cfg.LLM.Endpoint = tc.endpoint
		cfg.LLM.Model = tc.model
		if got := cfg.LLMConfigured(); got != tc.want {
			t.Errorf("%s: LLMConfigured() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestDistillModeDefault(t *testing.T) {
	if got := Default().Distill.Mode; got != "auto" {
		t.Fatalf("Default().Distill.Mode = %q, want auto", got)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("db_path: .palate/palate.db\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Distill.Mode != "auto" {
		t.Fatalf("Load with no distill key: Mode = %q, want auto", cfg.Distill.Mode)
	}
}
