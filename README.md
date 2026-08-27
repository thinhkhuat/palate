# Palate — Open Taste Engine

Open, local-first taste engine — learns your coding style from observable signals and injects it into AI harnesses via push prewarm.

- **Single Go binary**, no CGO, SQLite-only (FTS5 + JSON+cosine; `sqlite-vec` behind a build tag).
- **Observable signals only**: `model_edit`, `user_correction`, `user_diff`, `stated_preference`, explicit tools.
- **Push prewarm** (SessionStart ~500 tok + UserPromptSubmit ~200 tok goal-scoped) + on-demand `recall`.
- **Human-readable `TASTE.md`** — lossy render artifact; `events` log is truth.

> MVP: Phases 1–8, Claude Code only. Post-MVP: skill promotion, sync/E2EE, remaining adapters.

## Quickstart

```sh
palate init
palate doctor          # green?
palate hook install    # wire Claude Code hooks
palate serve           # MCP stdio (for generic clients)
```

```sh
palate prefer "use table-driven tests"
palate recall "testing" --json
palate prewarm
palate sleep
palate verify capture --pure
```

## Adapter matrix

> Single source of truth: `docs/adapters.md`. Summary below; details + verification gate there.

| Harness | Capture capability | Injection path | Install |
|---------|-------------------|----------------|---------|
| Claude Code | hook-based learning (PostToolUse/UserPromptSubmit/Stop/SessionStart) | push prewarm (SessionStart + UserPromptSubmit additionalContext) | `palate hook install` |
| Command Code | hook-based learning (mirrors Claude Code) | push prewarm | `palate hook install` |
| OMP (oh-my-pi) | recall-only (hooks unverified) | MCP recall | `palate serve` |
| Hermes | recall-only (hooks unverified) | MCP recall | `palate serve` |
| Pi / OpenClaw / OpenCode | recall-only (hooks unverified) — see `docs/adapters/verification.md` | MCP recall | `palate serve` |
| Codex / Cursor / Windsurf / OpenWebUI | recall-only | MCP + rules | `palate serve` |
| Generic MCP | recall-only | stdio/SSE | `palate serve` |

> Honest: OMP, Hermes, Pi, OpenClaw, OpenCode are **recall-only until `docs/adapters/verification.md` TASK-900 passes** — no learning claimed until live hook evidence lands.

## Docs

- `open-taste-blueprint.md` — master blueprint (source of truth)
- `docs/decisions.md` — 4 closed decisions
- `docs/security.md` — threat model + trust tiers
- `docs/adapters/verification.md` — hook-surface gate (Phase 11)
- `docs/architecture.md`, `docs/TASTE-format.md`, `docs/sync.md`, `docs/benchmarking.md`

## Install

```sh
brew install thinhkhuat/palate/palate
# or
curl -fsSL https://raw.githubusercontent.com/thinhkhuat/palate/main/install.sh | sh
# or
go install github.com/thinhkhuat/palate/cmd/palate@latest
```

## License

MIT
