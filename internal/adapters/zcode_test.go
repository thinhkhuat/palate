package adapters

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// A binary under a directory with a space (or a quote) must survive the shell
// the harness runs the hook command through: the command reaches the binary
// with the hook arguments intact, for every generator that emits commands.
func TestHookCommandRunsFromPathWithSpace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell quoting; Windows uses double quotes")
	}
	dir := filepath.Join(t.TempDir(), "My Apps", "it's here")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "palate")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"REACHED $*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	commands := map[string]string{
		"claude-code":  GenerateClaudeCodeHooks(bin).Hooks["Stop"][0].Hooks[0].Command,
		"command-code": GenerateCommandCodeHooks(bin).Hooks["Stop"][0].Hooks[0].Command,
		"zcode":        GenerateZCodeHooks(bin)["Stop"][0].Hooks[0].Command,
	}
	for name, cmd := range commands {
		out, err := exec.Command("/bin/sh", "-c", cmd).CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != "REACHED hook stop" {
			t.Fatalf("%s: %q ran as %q (err %v)", name, cmd, out, err)
		}
		if !isPalateCommand(cmd) {
			t.Fatalf("%s: palate no longer recognizes its own entry %q", name, cmd)
		}
	}
	// The plugin form keeps $ live: a plugin root holding a space still expands
	// inside the quotes and reaches the binary.
	plugin := GenerateClaudeCodeHooks("${PALATE_TEST_ROOT}/palate").Hooks["Stop"][0].Hooks[0].Command
	pc := exec.Command("/bin/sh", "-c", plugin)
	pc.Env = append(os.Environ(), "PALATE_TEST_ROOT="+dir)
	if out, err := pc.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "REACHED hook stop" {
		t.Fatalf("plugin form %q ran as %q (err %v)", plugin, out, err)
	}
	// A shell-inert path stays byte-identical, so existing installs do not churn.
	if got := GenerateClaudeCodeHooks("/usr/local/bin/palate").Hooks["Stop"][0].Hooks[0].Command; got != "/usr/local/bin/palate hook stop" {
		t.Fatalf("inert path was rewritten: %q", got)
	}
}

func TestIsPalateCommandQuoted(t *testing.T) {
	for _, c := range []string{
		"/usr/local/bin/palate hook stop",
		`"/Users/a b/palate" hook stop`,
		`"/Users/it\"s/pal\` + "`" + `ate/palate" hook stop`,
		"'/Users/a b/palate' hook stop",
		`"C:/Program Files/palate/palate.exe" hook stop`,
		`C:\Users\me\bin\palate.exe hook stop`,
		"C:/Users/me/bin/palate.exe hook stop",
		"/opt/palate/bin/palate-darwin-arm64 hook session-start",
		"palate hook session-start",
		"/usr/local/bin/palate prewarm --scope session",
		// Pre-0.5.2 builds wrote a spaced path unquoted; upgrades must still
		// find (and replace) those broken entries.
		"/Users/a b/palate hook stop",
		`C:\Program Files\palate\palate.exe hook stop`,
		"palate",
	} {
		if !isPalateCommand(c) {
			t.Errorf("not recognized as palate: %q", c)
		}
	}
	// Foreign hooks that merely mention a path ending in /palate must never be
	// claimed — install and uninstall delete whatever this function claims.
	for _, c := range []string{
		"'/opt/other tool/run' hook stop",
		"echo palate-ish",
		"'/opt/foo/palate' --version",
		`cat "/etc/palate" | wc`,
		`C:\tools\palate.exe --serve`,
		`python3 "/opt/x/palate" run`,
		`D:\repos\palate other.bat`,
		"echo 'a' && '/opt/palate' run-something",
		`cd "/Users/me/src/palate" && make lint`,
		`python3 "$HOME/tools/palate" --notify`,
		"/usr/local/bin/palate serve",
		"/opt/palateer hook stop",
		"/opt/palate-wrapper.sh hook stop",
		"git -C /Users/me/src/palate hook run pre-commit",
		"echo see /opt/palate hook docs",
		"/usr/local/bin/palate hook",
		`"$HOME/x"/palate hook stop`,
	} {
		if isPalateCommand(c) {
			t.Errorf("foreign command claimed as palate: %q", c)
		}
	}
}

// Every path shellArg can emit — on either OS — must read back as palate's.
func TestShellArgRoundTrip(t *testing.T) {
	cases := []struct{ goos, path, want string }{
		{"windows", `C:\Users\me\palate.exe`, "C:/Users/me/palate.exe"},
		{"windows", `C:\Program Files\palate\palate.exe`, `"C:/Program Files/palate/palate.exe"`},
		{"windows", `C:\Users\RUNNER~1\Thịnh\palate.exe`, "C:/Users/RUNNER~1/Thịnh/palate.exe"},
		{"darwin", "/usr/local/bin/palate", "/usr/local/bin/palate"},
		{"darwin", "/Users/a b/palate", `"/Users/a b/palate"`},
		{"linux", "/opt/it's \"odd\" `x`/palate", `"/opt/it's \"odd\" \` + "`x\\`" + `/palate"`},
	}
	for _, c := range cases {
		got := shellArgFor(c.goos, c.path)
		if got != c.want {
			t.Errorf("shellArgFor(%s, %q) = %q, want %q", c.goos, c.path, got, c.want)
		}
		if !isPalateCommand(got + " hook stop") {
			t.Errorf("palate does not recognize its own command %q", got+" hook stop")
		}
	}
}

// Installing over an entry an older build wrote for a spaced path (unquoted,
// so broken) replaces it: one Stop entry afterwards, and uninstall clears it.
func TestInstallReplacesLegacyUnquotedSpacedEntry(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/Users/a b/palate hook stop","timeout":10}]}]}}`
	if err := os.WriteFile(settings, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteClaudeCodeHooks(dir, "/Users/a b/palate"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Hooks map[string][]HookEntry `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if n := len(cfg.Hooks["Stop"]); n != 1 || cfg.Hooks["Stop"][0].Hooks[0].Command != `"/Users/a b/palate" hook stop` {
		t.Fatalf("want exactly the quoted Stop entry, got %d: %s", n, raw)
	}
	if _, err := RemoveClaudeCodeHooks(dir); err != nil {
		t.Fatal(err)
	}
	if raw, _ = os.ReadFile(settings); strings.Contains(string(raw), "palate") {
		t.Fatalf("uninstall left a palate entry: %s", raw)
	}
}

// Every command any generator emits, for any path shape, must read back as
// palate's — so a new hook event cannot silently fall outside the matcher.
func TestEveryGeneratedCommandIsRecognized(t *testing.T) {
	for _, bin := range []string{"", "/usr/local/bin/palate", "/Users/a b/palate", "${CLAUDE_PLUGIN_ROOT}/bin/palate", "/opt/palate-darwin-arm64"} {
		var cmds []string
		for _, entries := range GenerateClaudeCodeHooks(bin).Hooks {
			for _, e := range entries {
				for _, h := range e.Hooks {
					cmds = append(cmds, h.Command)
				}
			}
		}
		for _, entries := range GenerateZCodeHooks(bin) {
			for _, e := range entries {
				for _, h := range e.Hooks {
					cmds = append(cmds, h.Command)
				}
			}
		}
		for _, c := range cmds {
			if !isPalateCommand(c) {
				t.Errorf("generated command not recognized: %q", c)
			}
		}
	}
}
