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
				Hooks: []Hook{{Type: "command", Command: binaryPath + " prewarm --scope session", Timeout: 10}},
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
	for k, v := range parsed {
		existing[k] = v
	}
	out, _ := json.MarshalIndent(existing, "", "  ")
	return os.WriteFile(path, out, 0644)
}

func FileHash(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h[:]), nil
}
