package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func checkTopLevelMergePreserves(t *testing.T, dir, settingsPath string, write func(dir, bin string) error, eventCount int) {
	t.Helper()
	preexisting := `{
  "other": "x",
  "hooks": {
    "SessionStart": [{"hooks": [{"type": "command", "command": "echo foreign"}]}],
    "Stop": [{"hooks": [{"type": "command", "command": "/old/path/palate hook stop", "timeout": 10}]}]
  }
}`
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(preexisting), 0600); err != nil {
		t.Fatal(err)
	}

	if err := write(dir, "/usr/local/bin/palate"); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("settings is not valid JSON after merge: %v", err)
	}
	if cfg["other"] != "x" {
		t.Fatal("foreign top-level key clobbered by merge")
	}
	hooks, _ := cfg["hooks"].(map[string]interface{})
	ss, _ := hooks["SessionStart"].([]interface{})
	if len(ss) != 2 {
		t.Fatalf("SessionStart should hold foreign + palate entries, got %d", len(ss))
	}
	found := false
	for _, e := range ss {
		if strings.Contains(jsonStr(e), "keep-me") || strings.Contains(jsonStr(e), "echo foreign") {
			found = true
		}
	}
	if !found {
		t.Fatal("foreign SessionStart entry was clobbered")
	}
	stop, _ := hooks["Stop"].([]interface{})
	if len(stop) != 1 {
		t.Fatalf("Stop should hold exactly the fresh palate entry, got %d", len(stop))
	}
	if !strings.Contains(jsonStr(stop[0]), "/usr/local/bin/palate hook stop") {
		t.Fatalf("stale palate command not replaced: %v", stop[0])
	}

	before := string(raw)
	if err := write(dir, "/usr/local/bin/palate"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(settingsPath)
	if before != string(after) {
		t.Fatal("hook install is not idempotent")
	}
	_ = eventCount
}

func jsonStr(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestWriteClaudeCodeHooksMergesAndPreserves(t *testing.T) {
	dir := t.TempDir()
	checkTopLevelMergePreserves(t, dir, filepath.Join(dir, ".claude", "settings.local.json"), WriteClaudeCodeHooks, 5)
}

func TestWriteCommandCodeHooksMergesAndPreserves(t *testing.T) {
	dir := t.TempDir()
	checkTopLevelMergePreserves(t, dir, filepath.Join(dir, ".commandcode", "settings.local.json"), WriteCommandCodeHooks, 5)
}

// Uninstall removes only palate's entries: a foreign hook on the same event,
// a foreign event, and unrelated top-level keys all survive byte-for-byte in
// value, and the file is left valid JSON.
func TestUninstallPreservesForeignClaudeCode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := `{"permissions":{"allow":["Bash(go test:*)"]},"hooks":{"PostToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"echo foreign"}]}],"Notification":[{"hooks":[{"type":"command","command":"say hi"}]}]}}`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteClaudeCodeHooks(dir, "/usr/local/bin/palate"); err != nil {
		t.Fatalf("install: %v", err)
	}
	if got := CountPalateHooksTopLevel(path); got != 5 {
		t.Fatalf("after install: palate hooks = %d, want 5", got)
	}
	removed, err := RemoveClaudeCodeHooks(dir)
	if err != nil || removed != 5 {
		t.Fatalf("uninstall: removed=%d err=%v, want 5", removed, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("settings not valid JSON after uninstall: %v", err)
	}
	if got := CountPalateHooksTopLevel(path); got != 0 {
		t.Fatalf("after uninstall: palate hooks = %d, want 0", got)
	}
	s := string(raw)
	for _, want := range []string{`"echo foreign"`, `"say hi"`, `"Bash(go test:*)"`, `"Notification"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("uninstall dropped foreign content %s:\n%s", want, s)
		}
	}
}
