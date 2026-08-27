package adapters

import (
	"encoding/json"
	"fmt"
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
	for k, v := range parsed {
		existing[k] = v
	}
	out, _ := json.MarshalIndent(existing, "", "  ")
	_ = fmt.Sprintf("%v", out)
	return os.WriteFile(path, out, 0644)
}
