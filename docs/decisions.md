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

Auditable: who `thinhkhuat`, when `2026-08-15`, why as above. Reversible before Phase 2 `go mod init` (rename/module path); scope lock is binding.

Next: Phase 2 scaffold at `github.com/thinhkhuat/palate` with `--auto` cook.
