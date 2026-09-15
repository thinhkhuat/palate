package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func CommandCodeInstallHint() string {
	return "command-code: hook-based (mirrors claude_code) — palate hook install"
}

func GenerateCommandCodeHooks(binaryPath string) ClaudeCodeHooks {
	if binaryPath == "" {
		binaryPath = "palate"
	}
	cc := GenerateClaudeCodeHooks(binaryPath)
	out := ClaudeCodeHooks{Hooks: map[string][]HookEntry{}}
	for k, v := range cc.Hooks {
		out.Hooks[k] = v
	}
	return out
}

func WriteCommandCodeHooks(dir, binaryPath string) error {
	hooks := GenerateCommandCodeHooks(binaryPath)
	b, err := json.MarshalIndent(hooks, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, ".commandcode", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	existing := map[string]interface{}{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &existing)
	}
	var parsed map[string]interface{}
	_ = json.Unmarshal(b, &parsed)
	mergeTopLevelHooks(existing, parsed)
	out, _ := json.MarshalIndent(existing, "", "  ")
	return os.WriteFile(path, append(out, '\n'), 0644)
}

// RemoveCommandCodeHooks removes palate entries from <dir>/.commandcode/settings.local.json.
func RemoveCommandCodeHooks(dir string) (int, error) {
	return removePalateHooksTopLevel(filepath.Join(dir, ".commandcode", "settings.local.json"))
}

// CommandCodeGlobalSettingsPath returns the user-global Command Code settings
// path (~/.commandcode/settings.json).
func CommandCodeGlobalSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".commandcode", "settings.json"), nil
}

// WriteCommandCodeHooksGlobal merges palate hooks into ~/.commandcode/settings.json
// (top-level "hooks", Claude-shaped), preserving every foreign settings key
// and foreign hook entry. The pre-write state is backed up first
// (.bak-palate-<timestamp>, concierge convention) and the file's permission
// bits are preserved instead of being reset.
func WriteCommandCodeHooksGlobal(binaryPath string) error {
	path, err := CommandCodeGlobalSettingsPath()
	if err != nil {
		return err
	}
	hooks := GenerateCommandCodeHooks(binaryPath)
	b, err := json.MarshalIndent(hooks, "", "  ")
	if err != nil {
		return err
	}
	if err := backupBeforeWrite(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	mode := os.FileMode(0644)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	existing := map[string]interface{}{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &existing)
	}
	var parsed map[string]interface{}
	_ = json.Unmarshal(b, &parsed)
	mergeTopLevelHooks(existing, parsed)
	out, _ := json.MarshalIndent(existing, "", "  ")
	return os.WriteFile(path, append(out, '\n'), mode)
}

// RemoveCommandCodeHooksGlobal removes palate entries from
// ~/.commandcode/settings.json, preserving foreign hooks and keys. Returns
// the number of entries removed.
func RemoveCommandCodeHooksGlobal() (int, error) {
	path, err := CommandCodeGlobalSettingsPath()
	if err != nil {
		return 0, err
	}
	return removePalateHooksTopLevel(path)
}

