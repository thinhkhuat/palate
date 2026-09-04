package mcp

import (
	"testing"

	"github.com/thinhkhuat/palate/internal/config"
)

func TestCheckAuthGuard(t *testing.T) {
	cfg := config.Default()
	t.Setenv(cfg.MCP.TokenEnv, "")
	if err := checkAuthGuard(cfg, ":8080"); err == nil {
		t.Fatal(":8080 without token must refuse (binds all interfaces)")
	}
	if err := checkAuthGuard(cfg, "0.0.0.0:8080"); err == nil {
		t.Fatal("0.0.0.0 without token must refuse")
	}
	if err := checkAuthGuard(cfg, "127.0.0.1:8080"); err != nil {
		t.Fatalf("explicit loopback without token must pass: %v", err)
	}
	t.Setenv(cfg.MCP.TokenEnv, "test-token")
	if err := checkAuthGuard(cfg, ":8080"); err != nil {
		t.Fatalf(":8080 with token must pass: %v", err)
	}
	if err := checkAuthGuard(cfg, "0.0.0.0:8080"); err != nil {
		t.Fatalf("0.0.0.0 with token must pass: %v", err)
	}
}

func TestCheckToken(t *testing.T) {
	if !CheckToken("abc", "abc") {
		t.Fatal("equal tokens must match")
	}
	if CheckToken("abc", "abd") {
		t.Fatal("different tokens must not match")
	}
	if CheckToken("", "") {
		t.Fatal("empty expected must never match")
	}
}
