package adapters

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// pluginHooksDescription must match hooks/hooks.json's "description" field
// byte-for-byte — see TestHooksJSONMatchesGenerator.
const pluginHooksDescription = "palate — SessionStart injects the current prewarm block (session-scope preferences, ~500 tok) and, cross-session, replays any model_edits.json state left from a prior run; when an LLM gateway is configured it also fires a detached, lock-guarded distiller pass over the just-closed transcript. UserPromptSubmit first walks model_edits.json — a snapshot that drifted from disk since Stop can only be a hand-edit, so it is learned as user_diff (w=0.6) and cleared — then captures an \"I prefer …\" clause as a STATED preference (behind the same secret/injection scanner as the CLI) and, on an intent shift, injects a goal-scope prewarm block (~200 tok). PostToolUse (Edit|Write) snapshots the edited file (hash + 4KB bound) so a later hand-edit can be diffed. Stop re-baselines every snapshot to disk (the assistant still owns the turn, so drift here is its own sed/python edit, never the user's) and — with an LLM gateway configured — fires the same detached distiller pass. PreCompact writes a recovery prewarm block to state/recovery.md under the Palate home (~/.claude/palate by default, PALATE_HOME to override, or a repo's legacy .palate/). Every write re-renders the home's TASTE.md on exit. Every handler degrades to empty output on error; hooks never fail the session."

// pluginHooksFile mirrors hooks/hooks.json's top-level shape: a
// human-readable description plus the same hooks map GenerateClaudeCodeHooks
// produces for .claude/settings.local.json.
type pluginHooksFile struct {
	Description string                 `json:"description"`
	Hooks       map[string][]HookEntry `json:"hooks"`
}

// TestHooksJSONMatchesGenerator asserts hooks/hooks.json (the plugin's
// shared, auto-discovered hook table) is byte-identical to what
// GenerateClaudeCodeHooks produces for "${CLAUDE_PLUGIN_ROOT}/bin/palate" —
// the file is generated, not hand-maintained, and this test is the drift
// gate a `go:generate` step would otherwise need someone to remember to run.
func TestHooksJSONMatchesGenerator(t *testing.T) {
	generated := pluginHooksFile{
		Description: pluginHooksDescription,
		Hooks:       GenerateClaudeCodeHooks("${CLAUDE_PLUGIN_ROOT}/bin/palate").Hooks,
	}
	want, err := json.MarshalIndent(generated, "", "  ")
	if err != nil {
		t.Fatalf("marshal generated hooks: %v", err)
	}
	want = append(want, '\n')

	got, err := os.ReadFile("../../hooks/hooks.json")
	if err != nil {
		t.Fatalf("read hooks/hooks.json: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("hooks/hooks.json has drifted from GenerateClaudeCodeHooks; want:\n%s\ngot:\n%s", want, got)
	}
}

func manifestVersion(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	v, ok := m["version"].(string)
	if !ok {
		t.Fatalf("%s: no top-level string \"version\" field", path)
	}
	return v
}

// TestPluginManifestVersionsAgree asserts the three manifests' version
// fields agree with each other. It cannot check the built binary's
// -X main.version, set by the release tag at `goreleaser build` time — a
// manifest bumped without a matching tag is out of this test's scope.
func TestPluginManifestVersionsAgree(t *testing.T) {
	claudePlugin := manifestVersion(t, "../../.claude-plugin/plugin.json")
	codexPlugin := manifestVersion(t, "../../.codex-plugin/plugin.json")

	b, err := os.ReadFile("../../.claude-plugin/marketplace.json")
	if err != nil {
		t.Fatalf("read marketplace.json: %v", err)
	}
	var marketplace struct {
		Plugins []struct {
			Version string `json:"version"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(b, &marketplace); err != nil {
		t.Fatalf("unmarshal marketplace.json: %v", err)
	}
	if len(marketplace.Plugins) != 1 {
		t.Fatalf("marketplace.json: expected exactly 1 plugin entry, got %d", len(marketplace.Plugins))
	}
	marketplaceVersion := marketplace.Plugins[0].Version

	if claudePlugin != codexPlugin || claudePlugin != marketplaceVersion {
		t.Fatalf("manifest version drift: .claude-plugin/plugin.json=%q .codex-plugin/plugin.json=%q .claude-plugin/marketplace.json plugins[0].version=%q",
			claudePlugin, codexPlugin, marketplaceVersion)
	}
}

type mcpServerEntry struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Cwd     string   `json:"cwd,omitempty"`
}

type mcpDescriptor struct {
	MCPServers map[string]mcpServerEntry `json:"mcpServers"`
}

func loadMCPDescriptor(t *testing.T, path string) mcpDescriptor {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var d mcpDescriptor
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return d
}

// TestMCPDescriptorsParity asserts .mcp.json (Claude Code) and
// .codex-plugin/mcp.json (Codex, ADR-0035's relative-cwd pattern) describe
// the same "palate" MCP server: same args, and a command that resolves to
// bin/palate under each harness's own plugin-root convention.
func TestMCPDescriptorsParity(t *testing.T) {
	claude := loadMCPDescriptor(t, "../../.mcp.json")
	codex := loadMCPDescriptor(t, "../../.codex-plugin/mcp.json")

	c, ok := claude.MCPServers["palate"]
	if !ok {
		t.Fatal(".mcp.json: missing mcpServers.palate")
	}
	x, ok := codex.MCPServers["palate"]
	if !ok {
		t.Fatal(".codex-plugin/mcp.json: missing mcpServers.palate")
	}

	if !reflect.DeepEqual(c.Args, x.Args) {
		t.Fatalf("args drift: .mcp.json=%v .codex-plugin/mcp.json=%v", c.Args, x.Args)
	}
	if !strings.HasSuffix(c.Command, "/bin/palate") || !strings.Contains(c.Command, "CLAUDE_PLUGIN_ROOT") {
		t.Fatalf(".mcp.json command must be ${CLAUDE_PLUGIN_ROOT}-rooted at bin/palate, got %q", c.Command)
	}
	if x.Command != "./bin/palate" {
		t.Fatalf(".codex-plugin/mcp.json command must be the relative \"./bin/palate\" (upstream openai/codex#35762 — Codex never expands ${CLAUDE_PLUGIN_ROOT} in mcpServers), got %q", x.Command)
	}
	if x.Cwd != "." {
		t.Fatalf(".codex-plugin/mcp.json cwd must be \".\" (ADR-0035 pattern), got %q", x.Cwd)
	}
}
