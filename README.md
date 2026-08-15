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

## Docs

- `open-taste-blueprint.md` — master blueprint (source of truth)
- `docs/decisions.md` — 4 closed decisions
- `docs/security.md` — stub (Phase 2)
- `docs/adapters/verification.md` — hook-surface gate (Phase 11 gate)

## License

MIT
