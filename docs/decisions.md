# Decisions — Palate v2.1

Date: 2026-08-15
Owner: thinhkhuat
Status: 4/4 closed — blocks Phase 2 scaffold
Source: `open-taste-blueprint.md` §9 + Validation interview 2026-08-15 (Phases 1–8 lock)

| # | Decision | Choice | Rationale | When |
|---|----------|--------|-----------|------|
| 1 | **Name** | **Palate** | Working title from blueprint v2.1. Alternatives (Taster/Savor/Acquired) retained as aliases; rename is cheap before `go mod init`. | 2026-08-15 |
| 2 | **Language** | **Go** | Per ALT-001: fastest path to zero-CGO static binary; strongest prior art in this niche (Mnemos, engram, agent-memory-mcp); turnkey `modernc.org/sqlite` + FTS5 vs slower Rust iteration / Node weight. | 2026-08-15 |
| 3 | **Module path** | `github.com/thinhkhuat/palate` | Owner `thinhkhuat` from `open-taste-blueprint.md:owner`; fills the `<user>` placeholder. Change before publish if org differs. | 2026-08-15 |
| 4 | **MVP scope** | **Phases 1–8, Claude Code only** (TASTE.md + verification harness are P1) — Phases 9–12 are post-MVP backlog | Locked in Validation interview 2026-08-15 answer "Lock to Phases 1-8"; prior draft listed 0–6. Single definition in `open-taste-blueprint.md` §0/§9 + `plans/.../plan.md:Goals/Phases`. | 2026-08-15 |
| 5 | **ZCode adapter = hook-based learning, not a Claude Code byte-copy** | 4 events (`SessionStart`/`UserPromptSubmit`/`PostToolUse`/`Stop`); config in `.zcode/config.json` (workspace) or `~/.zcode/cli/config.json` (global) under `hooks.events.*` with `hooks.enabled: true`; strict-JSON hook output via `palate hook session-start`; `Stop`/`SessionStart` diff `.palate/model_edits.json` state against disk | ZCode's contract differs where it hurts: config-file hooks are disabled by default, there is no `PreCompact` event, and non-JSON stdout marks the hook failed. No live harness sends a `files[]` Stop payload, so user_diff must be state-driven. Verified against the official zcode-guide v0.1.0 `diagnosing-hooks` doc; evidence in `docs/adapters/verification.md`. | 2026-08-28 |
| 6 | **Contradictions weaken + conflict, never silently coexist** | Negation/antonym detection with same-category scan; old row weakened, `conflicts` row opened, loser hidden / winner shown; `recall --include-conflicted` reveals | Without this the learner only ever reinforces and stale taste accumulates side-by-side in TASTE.md. Fixed 2026-09-04 (F2); evidence `plans/reports/fix-report.md`. | 2026-09-04 |

Auditable: who `thinhkhuat`, when `2026-08-15`, why as above. Reversible before Phase 2 `go mod init` (rename/module path); scope lock is binding.

Next: Phase 2 scaffold at `github.com/thinhkhuat/palate` with `--auto` cook.
