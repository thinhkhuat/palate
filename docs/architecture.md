# Architecture

Single Go binary, no CGO, SQLite-only (FTS5 + JSON+cosine; `sqlite-vec` behind a build tag).

## Components

- **Storage** (`internal/storage`): `working_memory` / `episodic_memory` / `scratchpad` BEAM tiers, FTS5 triggers (insert/update/delete on all three content tables), `preferences` + `conflicts` bi-temporal (loser hidden, winner shown), `events` append-only truth, `capture_counters`. Recall = FTS + preference scan + RRF, with `--as-of` decay replay, conflict filtering, and optional freshness tiers (`freshness.enabled`).
- **Learn** (`internal/learn`): decay/reinforce/weaken with final clamp, negation/antonym contradiction detection with category scan, `proposals` determinism boundary (apply-before-promote), signal weights, category inference.
- **Prewarm** (`internal/prewarm`): Mechanism A (SessionStart ~500 tok) + B (UserPromptSubmit ~200 tok, goal-hash dedup; `Deduper` instances for long-lived callers).
- **Render** (`internal/render`): deterministic `TASTE.md` render, `lint`, content-hash `reimport` (quarantine `IMPORTED w=0.4`).
- **MCP** (`internal/mcp`): stdio + SSE, 8 tools with typed input schemas (the SDK derives them from the handler structs), `PALATE_MCP_TOKEN` + loopback allowlist bind guard (refuses empty-host binds without a token), and a per-request `Authorization: Bearer <token>` check on SSE whenever a token is set.
- **Safety** (`internal/safety`): secret censor + injection scanner (clean content unwrapped).
- **Verify** (`internal/verify`): blinded scenarios, hash, `--pure` flag, `IndependenceCheck`; `verify retrieval` reports `retrieval_hit_rate`.
- **Skills** (`internal/skills`): clustering (≥3), deterministic templating + hash, rumination gate.

## Data flow

Signal → safety gate → `learn.ApplySignal` → `preferences` + `events` → prewarm/recall/render.

## What is not wired

- Post-MVP adapters beyond Claude Code / Command Code / ZCode are recall-only until TASK-900 live verification.
- `sqlite-vec` native vectors require a build tag; default is FTS + JSON cosine (`VectorSearchFallback` is the seam).
