package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	setupTempProject(t)
	out, err := runCLI(t, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(out, version) {
		t.Fatalf("expected output to contain version %q, got %q", version, out)
	}
}

func TestHookHelpWarnsOnPluginDoubleFire(t *testing.T) {
	setupTempProject(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	out, err := runCLI(t, "hook", "help")
	if err != nil {
		t.Fatalf("hook help: %v", err)
	}
	if strings.Contains(out, "warning:") {
		t.Fatalf("no warning expected with nothing installed: %s", out)
	}

	if _, err := runCLI(t, "hook", "install", "--harness", "claude_code"); err != nil {
		t.Fatalf("hook install: %v", err)
	}
	out, err = runCLI(t, "hook", "help")
	if err != nil {
		t.Fatalf("hook help: %v", err)
	}
	if strings.Contains(out, "warning:") {
		t.Fatalf("no warning expected without a plugin cache present: %s", out)
	}

	pluginHooks := filepath.Join(home, ".claude", "plugins", "cache", "palate-marketplace", "palate", "0.1.0", "hooks", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(pluginHooks), 0o755); err != nil {
		t.Fatalf("mkdir plugin cache: %v", err)
	}
	if err := os.WriteFile(pluginHooks, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write plugin hooks.json: %v", err)
	}

	out, err = runCLI(t, "hook", "help")
	if err != nil {
		t.Fatalf("hook help: %v", err)
	}
	if !strings.Contains(out, pluginDoubleFireWarning) {
		t.Fatalf("expected double-fire warning, got: %s", out)
	}

	// Remove the workspace-level hooks: the plugin cache alone must not warn.
	if _, err := runCLI(t, "hook", "uninstall", "--harness", "claude_code"); err != nil {
		t.Fatalf("hook uninstall: %v", err)
	}
	out, err = runCLI(t, "hook", "help")
	if err != nil {
		t.Fatalf("hook help: %v", err)
	}
	if strings.Contains(out, "warning:") {
		t.Fatalf("no warning expected once workspace hooks are removed: %s", out)
	}
}
