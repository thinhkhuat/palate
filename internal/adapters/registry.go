package adapters

type Adapter struct {
	Name        string `json:"name"`
	Capture     string `json:"capture"`
	Injection   string `json:"injection"`
	Install     string `json:"install"`
	Verified    bool   `json:"verified"`
}

var Registry = []Adapter{
	{Name: "Claude Code", Capture: "hook-based learning (PostToolUse/UserPromptSubmit/Stop/SessionStart)", Injection: "push prewarm (SessionStart + UserPromptSubmit additionalContext)", Install: "palate hook install", Verified: true},
	{Name: "Pi", Capture: "recall-only (hooks unverified)", Injection: "MCP recall", Install: "palate serve (MCP)", Verified: false},
	{Name: "OpenClaw", Capture: "recall-only (hooks unverified)", Injection: "MCP recall", Install: "palate serve (MCP)", Verified: false},
	{Name: "OpenCode", Capture: "recall-only (hooks unverified)", Injection: "MCP recall", Install: "palate serve (MCP)", Verified: false},
	{Name: "Codex", Capture: "recall-only", Injection: "MCP + AGENTS.md", Install: "palate serve (MCP)", Verified: false},
	{Name: "Cursor", Capture: "recall-only", Injection: "MCP + rules", Install: "palate serve (MCP)", Verified: false},
	{Name: "Windsurf", Capture: "recall-only", Injection: "MCP + rules", Install: "palate serve (MCP)", Verified: false},
	{Name: "OpenWebUI", Capture: "recall-only", Injection: "@tool bridge", Install: "palate serve (MCP)", Verified: false},
	{Name: "Generic MCP", Capture: "recall-only", Injection: "stdio/SSE", Install: "palate serve", Verified: false},
}
