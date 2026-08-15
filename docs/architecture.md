# Architecture

Single Go binary, no CGO, SQLite-only (FTS5 + JSON+cosine; `sqlite-vec` behind a build tag).

## Components

- **Storage** (`internal/storage`): `working_memory` / `episodic_memory` / `scratchpad` BEAM tiers, FTS5 triggers (`UPDATE OF content` only), `preferences` + `conflicts` bi-temporal, `events` append-only truth, `capture_counters`.
- **Learn** (`internal/learn`): decay/reinforce/weaken with final clamp, `proposals` determinism boundary, signal weights, category inference.
- **Prewarm** (`internal/prewarm`): Mechanism A (SessionStart ~500 tok) + B (UserPromptSubmit ~200 tok, goal-hash dedup).
- **Render** (`internal/render`): deterministic `TASTE.md` render, `lint`, diff-aware `reimport` (quarantine `IMPORTED w=0.4`).
- **MCP** (`internal/mcp`): stdio + SSE, 8 tools wired to DB, `PALATE_MCP_TOKEN` + loopback allowlist auth guard.
- **Safety** (`internal/safety`): secret censor + injection scanner.
- **Verify** (`internal/verify`): blinded scenarios, hash, `--pure` flag, `IndependenceCheck`.
- **Sync** (`internal/sync`): event log `EventHash`, cursor pagination, `golang.org/x/crypto` Argon2id + AES-GCM E2EE.
- **Skills** (`internal/skills`): clustering (≥3), deterministic templating + hash, rumination gate.

## Data flow

Signal → safety gate → `learn.ApplySignal` → `preferences` + `events` → prewarm/recall/render.

## What is not wired

- Post-MVP adapters beyond Claude Code are recall-only until TASK-900 live verification.
- `sqlite-vec` native vectors require a build tag; default is FTS + JSON cosine.
