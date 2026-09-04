package adapters

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type ClaudeCodeHooks struct {
	Hooks map[string][]HookEntry `json:"hooks"`
}

type HookEntry struct {
	Matcher string `json:"matcher,omitempty"`
	Hooks   []Hook `json:"hooks"`
}

type Hook struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

func GenerateClaudeCodeHooks(binaryPath string) ClaudeCodeHooks {
	if binaryPath == "" {
		binaryPath = "palate"
	}
	return ClaudeCodeHooks{
		Hooks: map[string][]HookEntry{
			"SessionStart": {{
				// JSON form (hookSpecificOutput.additionalContext) — valid on both
				// Claude Code and ZCode, whose runner parses stdout as strict JSON.
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
			"PreCompact": {{
				Hooks: []Hook{{Type: "command", Command: binaryPath + " hook pre-compact", Timeout: 10}},
			}},
		},
	}
}

func WriteClaudeCodeHooks(dir, binaryPath string) error {
	hooks := GenerateClaudeCodeHooks(binaryPath)
	b, err := json.MarshalIndent(hooks, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, ".claude", "settings.local.json")
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

// mergeTopLevelHooks merges palate hook entries into the Claude-shaped
// settings map per event: foreign entries are kept, stale palate entries
// are replaced, and events we no longer manage are swept of palate entries.
// Every other settings key is untouched.
func mergeTopLevelHooks(existing, parsed map[string]interface{}) {
	ours, _ := parsed["hooks"].(map[string]interface{})
	if ours == nil {
		return
	}
	hooks, _ := existing["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = map[string]interface{}{}
	}
	for name, entries := range hooks {
		if _, managed := ours[name]; managed {
			continue
		}
		filtered := filterPalateEntries(entries)
		if len(filtered) == 0 {
			delete(hooks, name)
		} else {
			hooks[name] = filtered
		}
	}
	for name, entries := range ours {
		kept := filterPalateEntries(hooks[name])
		existingEntries, _ := entries.([]interface{})
		hooks[name] = append(kept, existingEntries...)
	}
	existing["hooks"] = hooks
}

func FileHash(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h[:]), nil
}

// RemoveClaudeCodeHooks removes palate entries from <dir>/.claude/settings.local.json,
// preserving foreign hooks and every other settings key. Returns entries removed.
func RemoveClaudeCodeHooks(dir string) (int, error) {
	return removePalateHooksTopLevel(filepath.Join(dir, ".claude", "settings.local.json"))
}

// removePalateHooksTopLevel handles the Claude Code settings shape:
// { ..., "hooks": { "<Event>": [ { matcher?, hooks: [...] } ] } }.
func removePalateHooksTopLevel(path string) (int, error) {
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
	removed := 0
	for name, entries := range hooks {
		filtered := filterPalateEntries(entries)
		removed += countEntries(entries) - len(filtered)
		if len(filtered) == 0 {
			delete(hooks, name)
		} else {
			hooks[name] = filtered
		}
	}
	if removed == 0 {
		return 0, nil
	}
	if len(hooks) == 0 {
		delete(root, "hooks")
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return removed, err
	}
	return removed, os.WriteFile(path, append(out, '\n'), 0644)
}

// CountPalateHooksTopLevel reports how many palate entries exist in a
// Claude-shaped settings file (0 when the file or hooks are absent).
func CountPalateHooksTopLevel(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	root := map[string]interface{}{}
	if json.Unmarshal(data, &root) != nil {
		return 0
	}
	hooks, _ := root["hooks"].(map[string]interface{})
	total := 0
	for _, entries := range hooks {
		total += countEntries(entries) - len(filterPalateEntries(entries))
	}
	return total
}

// CountPalateHooksZCode reports how many palate entries exist in a
// ZCode-shaped config (hooks.events.<Event>), 0 when absent.
func CountPalateHooksZCode(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	root := map[string]interface{}{}
	if json.Unmarshal(data, &root) != nil {
		return 0
	}
	hooks, _ := root["hooks"].(map[string]interface{})
	events, _ := hooks["events"].(map[string]interface{})
	total := 0
	for _, entries := range events {
		total += countEntries(entries) - len(filterPalateEntries(entries))
	}
	return total
}
