package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thinhkhuat/palate/internal/config"
)

func TestStaleGatewayWarning(t *testing.T) {
	home := t.TempDir()
	cfg := config.Config{Home: home}
	cfg.LLM.Endpoint = "http://127.0.0.1:9/v1/chat/completions"
	cfg.LLM.Model = "m-new"
	if got := staleGatewayWarning(cfg); got != "" {
		t.Fatalf("no stamp yet: want no warning, got %q", got)
	}
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	stamp := `{"model":"m-old","endpoint_host":"gw.example","at":"2026-09-16T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(home, "state", "last_gateway.json"), []byte(stamp), 0o600); err != nil {
		t.Fatal(err)
	}
	got := staleGatewayWarning(cfg)
	if !strings.Contains(got, "m-old") || !strings.Contains(got, "m-new") || !strings.Contains(got, "restart") {
		t.Fatalf("a mismatching stamp must produce the restart hint: %q", got)
	}
	cfg.LLM.Model = "m-old"
	if got := staleGatewayWarning(cfg); got != "" {
		t.Fatalf("matching model: want no warning, got %q", got)
	}
	cfg.LLM.Endpoint = ""
	if got := staleGatewayWarning(cfg); got != "" {
		t.Fatalf("no gateway configured: want no warning, got %q", got)
	}
}
