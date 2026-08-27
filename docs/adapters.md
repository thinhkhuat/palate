# Adapters

> Single source of truth for harness support — README forwards here.

| Harness | Capture capability | Injection path | Install |
|---------|-------------------|----------------|---------|
| Claude Code | hook-based learning (PostToolUse/UserPromptSubmit/Stop/SessionStart) | push prewarm (SessionStart + UserPromptSubmit additionalContext) | `palate hook install` |
| Command Code | hook-based learning (mirrors Claude Code) | push prewarm (SessionStart + UserPromptSubmit additionalContext) | `palate hook install` |
| OMP (oh-my-pi) | recall-only (hooks unverified) — see `docs/adapters/verification.md` | MCP recall | `palate serve` |
| Hermes | recall-only (hooks unverified) — see `docs/adapters/verification.md` | MCP recall | `palate serve` |
| Pi | recall-only (hooks unverified) — see `docs/adapters/verification.md` | MCP recall | `palate serve` |
| OpenClaw | recall-only (hooks unverified) — see `docs/adapters/verification.md` | MCP recall | `palate serve` |
| OpenCode | recall-only (hooks unverified) — see `docs/adapters/verification.md` | MCP recall | `palate serve` |
| Codex | recall-only | MCP + AGENTS.md | `palate serve` |
| Cursor | recall-only | MCP + rules | `palate serve` |
| Windsurf | recall-only | MCP + rules | `palate serve` |
| OpenWebUI | recall-only | @tool bridge | `palate serve` |
| Generic MCP | recall-only | stdio/SSE | `palate serve` |

## What is not wired

- Pi/OpenClaw/OpenCode learning requires TASK-900 live verification (doc URL + hook name + capture mapping). Until then, they are honestly recall-only.
- No per-harness skill packaging beyond the generic MCP server.
