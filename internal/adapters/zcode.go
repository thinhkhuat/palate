package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// ZCode hook contract (official zcode-guide plugin, diagnosing-hooks):
//   - Configuration-file hooks live under the top-level "hooks" key of
//     ~/.zcode/cli/config.json (global) or <repo>/.zcode/config.json
//     (workspace), shaped { enabled?, timeoutMs?, maxOutputBytes?,
//     events: { <Event>: [ { matcher?, hooks: [...] } ] } }.
//   - They are DISABLED by default: "enabled": true must be set.
//   - Exactly seven events exist: SessionStart, UserPromptSubmit,
//     PreToolUse, PermissionRequest, PostToolUse, PostToolUseFailure, Stop.
//     There is NO PreCompact (SessionStart fires on compact via its source).
//   - Matchers are case-sensitive regex on the tool name; "Edit|Write"
//     also covers the ApplyPatch alias.
//   - stdout is parsed as strict JSON (extra keys fail validation), so the
//     SessionStart handler must emit hookSpecificOutput JSON, not plain text.

// ZCodeWorkspaceConfigPath returns the workspace-relative config path for dir.
func ZCodeWorkspaceConfigPath(dir string) string {
	return filepath.Join(dir, ".zcode", "config.json")
}

// ZCodeGlobalConfigPath returns the user-global config path (~/.zcode/cli/config.json).
func ZCodeGlobalConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".zcode", "cli", "config.json"), nil
}

// GenerateZCodeHooks returns the palate hook set for ZCode: the four
// supported events that map to learning signals. PreCompact is omitted
// because ZCode does not have that event.
func GenerateZCodeHooks(binaryPath string) map[string][]HookEntry {
	if binaryPath == "" {
		binaryPath = "palate"
	}
	return map[string][]HookEntry{
		"SessionStart": {{
			Hooks: []Hook{{Type: "command", Command: binaryPath + " hook session-start", Timeout: 10}},
		}},
		"UserPromptSubmit": {{
			Hooks: []Hook{{Type: "command", Command: binaryPath + " hook user-prompt", Timeout: 30}},
		}},
		"PostToolUse": {{
			Matcher: "Edit|Write",
			Hooks:   []Hook{{Type: "command", Command: binaryPath + " hook post-tool-use", Timeout: 10}},
		}},
		"Stop": {{
			Hooks: []Hook{{Type: "command", Command: binaryPath + " hook stop", Timeout: 10}},
		}},
	}
}

// writeZCodeConfigAt merges palate hooks into the ZCode config at path,
// preserving every foreign key (plugins, mcp, ...) and foreign hook entries.
// Idempotent: palate-managed entries are replaced, not duplicated.
func writeZCodeConfigAt(path string, binaryPath string) error {
	events := GenerateZCodeHooks(binaryPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	root := map[string]interface{}{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &root)
	}
	hooks, _ := root["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = map[string]interface{}{}
	}
	hooks["enabled"] = true
	existingEvents, _ := hooks["events"].(map[string]interface{})
	if existingEvents == nil {
		existingEvents = map[string]interface{}{}
	}
	// Drop stale palate entries from events we don't manage (e.g. a
	// PreCompact entry from an older install).
	for name, entries := range existingEvents {
		if _, ours := events[name]; ours {
			continue
		}
		filtered := filterPalateEntries(entries)
		if len(filtered) == 0 {
			delete(existingEvents, name)
		} else {
			existingEvents[name] = filtered
		}
	}
	for name, ours := range events {
		kept := filterPalateEntries(existingEvents[name])
		add, err := toJSONInterfaces(ours)
		if err != nil {
			return err
		}
		existingEvents[name] = append(kept, add...)
	}
	hooks["events"] = existingEvents
	root["hooks"] = hooks
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	mode := os.FileMode(0600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	return os.WriteFile(path, append(out, '\n'), mode)
}

// filterPalateEntries drops hook entries whose commands invoke palate,
// keeping foreign entries intact. Accepts the decoded []interface{} shape
// and returns the same shape (nil when nothing survives).
func filterPalateEntries(entries interface{}) []interface{} {
	list, ok := entries.([]interface{})
	if !ok {
		return nil
	}
	var out []interface{}
	for _, e := range list {
		entry, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		inner, _ := entry["hooks"].([]interface{})
		var kept []interface{}
		for _, h := range inner {
			hook, ok := h.(map[string]interface{})
			if !ok {
				continue
			}
			cmd, _ := hook["command"].(string)
			if isPalateCommand(cmd) {
				continue
			}
			kept = append(kept, hook)
		}
		if len(kept) == 0 {
			continue
		}
		entry["hooks"] = kept
		out = append(out, entry)
	}
	return out
}

func isPalateCommand(cmd string) bool {
	c := strings.ToLower(cmd)
	return strings.Contains(c, "palate hook ") || strings.Contains(c, "palate prewarm") ||
		strings.Contains(c, "/palate ") || c == "palate"
}

// WriteZCodeHooks merges palate hooks into the workspace config <dir>/.zcode/config.json.
func WriteZCodeHooks(dir, binaryPath string) error {
	return writeZCodeConfigAt(ZCodeWorkspaceConfigPath(dir), binaryPath)
}

// WriteZCodeHooksGlobal merges palate hooks into ~/.zcode/cli/config.json.
func WriteZCodeHooksGlobal(binaryPath string) error {
	path, err := ZCodeGlobalConfigPath()
	if err != nil {
		return err
	}
	return writeZCodeConfigAt(path, binaryPath)
}

// RemoveZCodeHooksAt removes palate entries from the ZCode config at path.
// Returns the number of palate entries removed. Foreign hooks and all other
// config keys are preserved; an events map left empty is removed entirely
// (restoring the hooks-disabled-by-default semantics).
func RemoveZCodeHooksAt(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	root := map[string]interface{}{}
	if err := json.Unmarshal(data, &root); err != nil {
		return 0, err
	}
	hooks, ok := root["hooks"].(map[string]interface{})
	if !ok {
		return 0, nil
	}
	events, _ := hooks["events"].(map[string]interface{})
	removed := 0
	for name, entries := range events {
		filtered := filterPalateEntries(entries)
		before, after := countEntries(entries), len(filtered)
		removed += before - after
		if after == 0 {
			delete(events, name)
		} else {
			events[name] = filtered
		}
	}
	if removed == 0 {
		return 0, nil
	}
	if len(events) == 0 {
		delete(hooks, "events")
		if len(hooks) <= 1 { // only "enabled" left
			delete(root, "hooks")
		}
	} else {
		hooks["events"] = events
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return removed, err
	}
	mode := os.FileMode(0600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	return removed, os.WriteFile(path, append(out, '\n'), mode)
}

// ZCodeEnabledWithForeignHooks reports whether the config at path has
// hooks.enabled == true while still carrying non-palate hook entries —
// the state uninstall leaves behind when foreign hooks exist.
func ZCodeEnabledWithForeignHooks(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	root := map[string]interface{}{}
	if json.Unmarshal(data, &root) != nil {
		return false
	}
	hooks, _ := root["hooks"].(map[string]interface{})
	if enabled, _ := hooks["enabled"].(bool); !enabled {
		return false
	}
	events, _ := hooks["events"].(map[string]interface{})
	for _, entries := range events {
		if len(filterPalateEntries(entries)) > 0 {
			return true
		}
	}
	return false
}

func countEntries(entries interface{}) int {
	list, ok := entries.([]interface{})
	if !ok {
		return 0
	}
	return len(list)
}

// toJSONInterfaces round-trips a typed value through JSON so it merges
// cleanly with values decoded into interface{} form.
func toJSONInterfaces(v interface{}) ([]interface{}, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out []interface{}
	return out, json.Unmarshal(b, &out)
}
