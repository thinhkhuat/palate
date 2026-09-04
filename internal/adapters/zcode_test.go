package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateZCodeHooksContract(t *testing.T) {
	events := GenerateZCodeHooks("")
	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d: %v", len(events), events)
	}
	if _, ok := events["PreCompact"]; ok {
		t.Fatal("ZCode has no PreCompact event — it must not be emitted")
	}
	for _, name := range []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "Stop"} {
		if _, ok := events[name]; !ok {
			t.Fatalf("missing event %s", name)
		}
	}
	if m := events["PostToolUse"][0].Matcher; m != "Edit|Write" {
		t.Fatalf("PostToolUse matcher = %q, want Edit|Write (regex, case-sensitive; covers ApplyPatch alias)", m)
	}
	for name, entries := range events {
		for _, e := range entries {
			for _, h := range e.Hooks {
				if h.Type != "command" {
					t.Fatalf("%s: hook type = %q, want command", name, h.Type)
				}
				if !strings.Contains(h.Command, "palate") || !strings.Contains(h.Command, "hook ") {
					t.Fatalf("%s: unexpected command %q", name, h.Command)
				}
				if h.Timeout <= 0 {
					t.Fatalf("%s: timeout must be positive (command hooks: seconds)", name)
				}
			}
		}
	}
}

func TestWriteZCodeHooksMergesAndPreserves(t *testing.T) {
	dir := t.TempDir()
	path := ZCodeWorkspaceConfigPath(dir)
	preexisting := `{
  "plugins": {"enabledPlugins": {"demo@x": true}},
  "mcp": {"servers": {"exa": {"type": "remote"}}},
  "hooks": {
    "timeoutMs": 30000,
    "events": {
      "SessionStart": [{"hooks": [{"type": "command", "command": "echo foreign"}]}],
      "Stop": [{"hooks": [{"type": "command", "command": "/old/path/palate hook stop", "timeout": 10}]}]
    }
  }
}`
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(preexisting), 0600); err != nil {
		t.Fatal(err)
	}

	if err := WriteZCodeHooks(dir, "/usr/local/bin/palate"); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("config is not valid JSON after merge: %v", err)
	}

	// Foreign top-level keys preserved.
	if _, ok := cfg["plugins"]; !ok {
		t.Fatal("plugins key clobbered by merge")
	}
	if _, ok := cfg["mcp"]; !ok {
		t.Fatal("mcp key clobbered by merge")
	}

	hooks, _ := cfg["hooks"].(map[string]interface{})
	if enabled, _ := hooks["enabled"].(bool); !enabled {
		t.Fatal("hooks.enabled must be true — ZCode config-file hooks are disabled by default")
	}
	if _, ok := hooks["timeoutMs"]; !ok {
		t.Fatal("pre-existing hooks.timeoutMs clobbered by merge")
	}

	events, _ := hooks["events"].(map[string]interface{})
	// Foreign hook entry preserved alongside the palate entry.
	ss, _ := events["SessionStart"].([]interface{})
	if len(ss) != 2 {
		t.Fatalf("SessionStart should hold foreign + palate entries, got %d", len(ss))
	}
	// Stale palate entry (old binary path) replaced, not duplicated.
	stop, _ := events["Stop"].([]interface{})
	if len(stop) != 1 {
		t.Fatalf("Stop should hold exactly the fresh palate entry, got %d", len(stop))
	}
	stopEntry := stop[0].(map[string]interface{})
	stopHooks := stopEntry["hooks"].([]interface{})
	cmd := stopHooks[0].(map[string]interface{})["command"].(string)
	if cmd != "/usr/local/bin/palate hook stop" {
		t.Fatalf("stale palate command not replaced: %q", cmd)
	}

	// Idempotent: rewriting with the same binary changes nothing.
	before := string(raw)
	if err := WriteZCodeHooks(dir, "/usr/local/bin/palate"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if before != string(after) {
		t.Fatal("WriteZCodeHooks is not idempotent")
	}
}

func TestRemoveZCodeHooks(t *testing.T) {
	dir := t.TempDir()
	if err := WriteZCodeHooks(dir, "palate"); err != nil {
		t.Fatal(err)
	}
	// Add a foreign entry so we can prove it survives removal.
	path := ZCodeWorkspaceConfigPath(dir)
	raw, _ := os.ReadFile(path)
	var cfg map[string]interface{}
	_ = json.Unmarshal(raw, &cfg)
	events := cfg["hooks"].(map[string]interface{})["events"].(map[string]interface{})
	events["Notification"] = []interface{}{map[string]interface{}{
		"hooks": []interface{}{map[string]interface{}{"type": "command", "command": "echo keep-me"}},
	}}
	out, _ := json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(path, out, 0600)

	n, err := RemoveZCodeHooksAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("expected 4 palate entries removed, got %d", n)
	}

	raw, _ = os.ReadFile(path)
	_ = json.Unmarshal(raw, &cfg)
	hooks, hasHooks := cfg["hooks"].(map[string]interface{})
	if !hasHooks {
		t.Fatal("hooks block must survive while foreign events remain")
	}
	events = hooks["events"].(map[string]interface{})
	if len(events) != 1 {
		t.Fatalf("foreign event must survive, got %d events: %v", len(events), events)
	}
	if _, ok := events["Notification"]; !ok {
		t.Fatal("foreign event Notification was removed")
	}
	if CountPalateHooksZCode(path) != 0 {
		t.Fatal("palate entries remain after removal")
	}

	// Removing again is a no-op.
	if n, _ := RemoveZCodeHooksAt(path); n != 0 {
		t.Fatalf("second removal should find nothing, got %d", n)
	}
}

func TestRemoveZCodeHooksCleansEmptyBlock(t *testing.T) {
	dir := t.TempDir()
	if err := WriteZCodeHooks(dir, "palate"); err != nil {
		t.Fatal(err)
	}
	path := ZCodeWorkspaceConfigPath(dir)
	if _, err := RemoveZCodeHooksAt(path); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var cfg map[string]interface{}
	_ = json.Unmarshal(raw, &cfg)
	if _, ok := cfg["hooks"]; ok {
		t.Fatal("empty hooks block should be removed entirely (restores disabled-by-default)")
	}
}

func TestWriteZCodeHooksGlobalPath(t *testing.T) {
	// Only assert the path shape; writing would touch the real home dir.
	p, err := ZCodeGlobalConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(filepath.ToSlash(p), ".zcode/cli/config.json") {
		t.Fatalf("global path = %q, want ~/.zcode/cli/config.json", p)
	}
}

func TestCountPalateHooksTopLevel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.local.json")
	if err := WriteClaudeCodeHooks(dir, "palate"); err != nil {
		t.Fatal(err)
	}
	if n := CountPalateHooksTopLevel(path); n != 5 {
		t.Fatalf("claude hooks count = %d, want 5", n)
	}
	if n, err := RemoveClaudeCodeHooks(dir); err != nil || n != 5 {
		t.Fatalf("remove = (%d, %v), want (5, nil)", n, err)
	}
	if n := CountPalateHooksTopLevel(path); n != 0 {
		t.Fatalf("after removal count = %d, want 0", n)
	}
}
