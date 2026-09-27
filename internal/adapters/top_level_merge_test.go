package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// managedPalateEvents counts distinct event keys that hold at least one
// palate entry — the harness's actual managed event surface.
func managedPalateEvents(settings map[string]interface{}) int {
	hooks, _ := settings["hooks"].(map[string]interface{})
	n := 0
	for _, entries := range hooks {
		if countEntries(entries) != len(filterPalateEntries(entries)) {
			n++
		}
	}
	return n
}

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
	if got := managedPalateEvents(cfg); got != eventCount {
		t.Fatalf("managed palate events = %d, want %d", got, eventCount)
	}
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
	checkTopLevelMergePreserves(t, dir, filepath.Join(dir, ".commandcode", "settings.local.json"), WriteCommandCodeHooks, 3)
}

// Command Code accepts exactly PreToolUse/PostToolUse/Stop/SessionStart;
// anything else is reported as `unknown hook event "X" — skipped`. Claude
// Code legitimately has five, so the two sets must not drift back together.
func TestCommandCodeHooksExcludeUnknownEvents(t *testing.T) {
	cc := GenerateCommandCodeHooks("/bin/palate")
	for _, ev := range []string{"PreCompact", "UserPromptSubmit"} {
		if _, ok := cc.Hooks[ev]; ok {
			t.Fatalf("Command Code must not emit %q — it is not a Command Code event", ev)
		}
	}
	if len(cc.Hooks) != 3 {
		t.Fatalf("command code managed events = %d, want 3", len(cc.Hooks))
	}
	claude := GenerateClaudeCodeHooks("/bin/palate")
	for _, ev := range []string{"PreCompact", "UserPromptSubmit"} {
		if _, ok := claude.Hooks[ev]; !ok {
			t.Fatalf("Claude Code must still emit %q", ev)
		}
	}
	if len(claude.Hooks) != 5 {
		t.Fatalf("claude code managed events = %d, want 5", len(claude.Hooks))
	}
}

// A live ~/.commandcode/settings.json may already carry a PreCompact entry
// written by an older install. The next install must sweep it while leaving
// every foreign hook and key alone.
func TestCommandCodeGlobalSweepsStalePreCompact(t *testing.T) {
	home := fakeHome(t)
	dir := filepath.Join(home, ".commandcode")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(dir, "settings.json")
	seed := `{"skills":["/opt/skills"],"hooks":{` +
		`"PreCompact":[{"hooks":[{"type":"command","command":"/opt/palate hook pre-compact","timeout":10}]}],` +
		`"Notification":[{"hooks":[{"type":"command","command":"echo foreign"}]}]}}`
	if err := os.WriteFile(settingsPath, []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WriteCommandCodeHooksGlobal("/bin/palate"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("settings not valid JSON: %v", err)
	}
	hooks, _ := root["hooks"].(map[string]interface{})
	if _, ok := hooks["PreCompact"]; ok {
		t.Fatalf("stale PreCompact entry survived the sweep:\n%s", data)
	}
	if got := string(data); !strings.Contains(got, "echo foreign") {
		t.Fatalf("foreign Notification hook was dropped:\n%s", got)
	}
	if _, ok := root["skills"]; !ok {
		t.Fatalf("foreign top-level key was dropped:\n%s", data)
	}
	if n := CountPalateHooksTopLevel(settingsPath); n != 3 {
		t.Fatalf("palate entries after install = %d, want 3", n)
	}

	// A foreign (non-palate) entry under an unsupported event is not ours to
	// delete — the sweep only removes palate commands, then drops the key
	// only once nothing survives.
	seedForeign := `{"hooks":{"PreCompact":[{"hooks":[{"type":"command","command":"echo someone-elses"}]}]}}`
	if err := os.WriteFile(settingsPath, []byte(seedForeign), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WriteCommandCodeHooksGlobal("/bin/palate"); err != nil {
		t.Fatal(err)
	}
	foreign, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(foreign), "someone-elses") {
		t.Fatalf("foreign PreCompact entry was deleted:\n%s", foreign)
	}
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
