# Adapters

> Single source of truth for harness support — README forwards here.

| Harness | Capture capability | Injection path | Install |
|---------|-------------------|----------------|---------|
| Claude Code | hook-based learning (PostToolUse/UserPromptSubmit/Stop/SessionStart) | push prewarm (SessionStart + UserPromptSubmit additionalContext) | `palate hook install` |
| Command Code | hook-based learning (mirrors Claude Code) | push prewarm (SessionStart + UserPromptSubmit additionalContext) | `palate hook install` |
| ZCode | hook-based learning (SessionStart/UserPromptSubmit/PostToolUse/Stop — no PreCompact event) | push prewarm (strict-JSON `additionalContext`; `hooks.enabled: true` required) | `palate hook install` |
| OMP (oh-my-pi) | recall-only (hooks unverified) — see `docs/adapters/verification.md` | MCP recall | `palate serve` |
| Hermes | recall-only (hooks unverified) — see `docs/adapters/verification.md` | MCP recall | `palate serve` |
| Pi | recall-only (hooks unverified) — see `docs/adapters/verification.md` | MCP recall | `palate serve` |
| OpenClaw | recall-only (hooks unverified) — see `docs/adapters/verification.md` | MCP recall | `palate serve` |
| OpenCode | recall-only (hooks unverified) — see `docs/adapters/verification.md` | MCP recall | `palate serve` |
| Codex | recall-only¹ | MCP + AGENTS.md | `palate serve` |
| Cursor | recall-only | MCP + rules | `palate serve` |
| Windsurf | recall-only | MCP + rules | `palate serve` |
| OpenWebUI | recall-only | @tool bridge | `palate serve` |
| Generic MCP | recall-only | stdio/SSE | `palate serve` |

## Historical transcript learning (optional, separate from the table above)

`palate learn sessions` (and `session`/`repo`/`journals`) additionally
covers **Codex, Pi, OpenCode, Gemini CLI, and memsearch journals** — reading
their *stored* transcripts/history through an LLM on the user's own gateway,
distinct from the hook-based *live* capture the table above describes.
Recall/injection for every harness in the table is unchanged by this. Per
reader: Claude Code, Command Code, Codex, Pi (shares Command Code's
envelope reader), OpenCode, and memsearch are confirmed against a live
transcript/db on this machine; Gemini CLI's `user`-role extraction is
confirmed live but its assistant/model role name was never observed on disk
(documented gap, shipped as a subset rather than a guess). **ZCode
transcript reading is experimental and opt-in** (`--include-experimental`)
— it is registered separately from ZCode's proven hook-based live learning
above and stays off by default. See `internal/distill/reader_*.go` and
`docs/security.md` for what is sent to the gateway.

## What is not wired

- Pi/OpenClaw/OpenCode learning requires TASK-900 live verification (doc URL + hook name + capture mapping). Until then, they are honestly recall-only.
- No per-harness skill packaging beyond the generic MCP server (Claude Code and Codex now get the `palate` skill and `/palate:*` commands via plugin packaging, `.claude-plugin/` / `.codex-plugin/` — but that is skill delivery, not a learning-capture claim).

¹ As of the plugin packaging phase, Codex auto-discovers `hooks/hooks.json` and expands `${CLAUDE_PLUGIN_ROOT}` there the same way Claude Code does (`scripts/verify-plugin-hooks.sh` proves the commands run and produce well-formed output). The row stays `recall-only` regardless — this is confirmed manifest wiring, not TASK-900's live-firing-inside-a-real-Codex-session evidence. Flip the row only once that evidence is recorded.
