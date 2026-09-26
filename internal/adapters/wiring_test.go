package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// fakeHome points HOME at a throwaway dir for the duration of one test;
// every detector/writer resolves paths through os.UserHomeDir, so the real
// harness configs are never touched by tests.
func fakeHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h) // os.UserHomeDir reads this on Windows
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	return home
}

func TestLiveWiringClaudeCodeAbsentThenPlugin(t *testing.T) {
	home := fakeHome(t)
	if w := wiringClaudeCode(); w.State != WiringAbsent {
		t.Fatalf("empty home: want absent, got %+v", w)
	}
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if w := wiringClaudeCode(); w.State != WiringNotWired {
		t.Fatalf("bare ~/.claude: want not wired, got %+v", w)
	}
	// plugin-vehicle hooks.json in the marketplace cache layout
	ver := filepath.Join(dir, "plugins", "cache", "palate", "palate", "0.5.0", "hooks")
	if err := os.MkdirAll(ver, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ver, "hooks.json"), []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	w := wiringClaudeCode()
	if w.State != WiringWired || w.Detail == "" {
		t.Fatalf("plugin hooks: want wired with detail, got %+v", w)
	}
}

func TestLiveWiringClaudeCodeEnabledPlugins(t *testing.T) {
	home := fakeHome(t)
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	settings := map[string]interface{}{
		"enabledPlugins": map[string]interface{}{"palate@palate": true},
	}
	b, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), b, 0644); err != nil {
		t.Fatal(err)
	}
	if w := wiringClaudeCode(); w.State != WiringWired {
		t.Fatalf("enabled plugin: want wired, got %+v", w)
	}
}

func TestLiveWiringCodexTomlTable(t *testing.T) {
	home := fakeHome(t)
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	toml := "[mcp_servers.exa]\nurl = \"https://example.invalid\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(toml), 0600); err != nil {
		t.Fatal(err)
	}
	if w := wiringCodex(); w.State != WiringNotWired {
		t.Fatalf("no palate table: want not wired, got %+v", w)
	}
	toml += "\n[mcp_servers.palate]\ncommand = \"/usr/local/bin/palate\"\nargs = [\"serve\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(toml), 0600); err != nil {
		t.Fatal(err)
	}
	if w := wiringCodex(); w.State != WiringWired {
		t.Fatalf("palate table: want wired, got %+v", w)
	}
}

func TestLiveWiringZCodeGlobalWriteAndBackup(t *testing.T) {
	home := fakeHome(t)
	cfgDir := filepath.Join(home, ".zcode", "cli")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(cfgDir, "config.json")
	foreign := `{"plugins": {"loaded": true}}`
	if err := os.WriteFile(cfgPath, []byte(foreign), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WriteZCodeHooksGlobal("/bin/palate"); err != nil {
		t.Fatal(err)
	}
	if w := wiringZCode(); w.State != WiringWired {
		t.Fatalf("wired global zcode: want wired, got %+v", w)
	}
	// the pre-write state must survive as a .bak-palate-* copy (concierge
	// convention: back up before every config mutation)
	baks, err := filepath.Glob(cfgPath + ".bak-palate-*")
	if err != nil || len(baks) == 0 {
		t.Fatalf("expected a backup of %s, got %v (err %v)", cfgPath, baks, err)
	}
	bakData, err := os.ReadFile(baks[0])
	if err != nil || string(bakData) != foreign {
		t.Fatalf("backup content mismatch: %q (err %v)", bakData, err)
	}
	// foreign keys survive the merge
	var root map[string]interface{}
	data, err := os.ReadFile(cfgPath)
	if err != nil || json.Unmarshal(data, &root) != nil {
		t.Fatalf("rewritten config unreadable: %v", err)
	}
	if _, ok := root["plugins"]; !ok {
		t.Fatal("foreign key 'plugins' lost by the merge")
	}
}

func TestLiveWiringCommandCodeGlobalRoundTrip(t *testing.T) {
	home := fakeHome(t)
	dir := filepath.Join(home, ".commandcode")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"skills": ["/opt/skills"]}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WriteCommandCodeHooksGlobal("/bin/palate"); err != nil {
		t.Fatal(err)
	}
	if w := wiringCommandCode(); w.State != WiringWired {
		t.Fatalf("wired global command code: want wired, got %+v", w)
	}
	var root map[string]interface{}
	data, err := os.ReadFile(settingsPath)
	if err != nil || json.Unmarshal(data, &root) != nil {
		t.Fatalf("rewritten settings unreadable: %v", err)
	}
	if _, ok := root["skills"]; !ok {
		t.Fatal("foreign key 'skills' lost by the merge")
	}
	// idempotent: a second write must not duplicate palate entries
	if err := WriteCommandCodeHooksGlobal("/bin/palate"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(settingsPath)
	var root2 map[string]interface{}
	if err := json.Unmarshal(data, &root2); err != nil {
		t.Fatal(err)
	}
	hooks, _ := root2["hooks"].(map[string]interface{})
	total := 0
	for _, entries := range hooks {
		if list, ok := entries.([]interface{}); ok {
			total += len(list)
		}
	}
	if total == 0 || total > 5 {
		t.Fatalf("expected 1-5 managed hook entries after double write, got %d", total)
	}
	// uninstall restores not-wired
	n, err := RemoveCommandCodeHooksGlobal()
	if err != nil || n == 0 {
		t.Fatalf("remove: n=%d err=%v", n, err)
	}
	if w := wiringCommandCode(); w.State != WiringNotWired {
		t.Fatalf("after removal: want not wired, got %+v", w)
	}
}
