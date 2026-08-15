package adapters

const PiMCPConfig = `{"mcpServers":{"palate":{"command":"palate","args":["serve"]}}}`

func PiInstallHint() string { return "pi: recall-only — add MCP config to pi settings; learning requires TASK-900 verification (see docs/adapters/verification.md)" }
