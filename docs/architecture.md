# Architecture

Single Go binary, no CGO, SQLite-only (FTS5 + JSON+cosine; `sqlite-vec` behind a build tag).

## Components

- **Storage** (`internal/storage`): `working_memory` / `episodic_memory` / `scratchpad` BEAM tiers, FTS5 triggers (insert/update/delete on all three content tables), `preferences` + `conflicts` bi-temporal (loser hidden, winner shown), `events` append-only truth, `capture_counters`. Recall = FTS + preference scan + RRF, with `--as-of` decay replay, conflict filtering, and optional freshness tiers (`freshness.enabled`).
- **Learn** (`internal/learn`): reinforce/weaken with final clamp (time decay off by default, `half_life_hours` knob), negation/antonym contradiction detection with category scan, `proposals` determinism boundary (apply-before-promote), signal weights, category inference.
- **Prewarm** (`internal/prewarm`): Mechanism A (SessionStart: the whole profile via `render.Body`, 2000-tok budget that drops whole lines only and names the count) + B (UserPromptSubmit ~200 tok, goal-hash dedup; `Deduper` instances for long-lived callers). SessionStart also emits a `systemMessage` (a conversation notice) with what was learned since the previous session (`cmd/palate/notice.go`, cursor in `state/notice_cursor`).
- **Render** (`internal/render`): deterministic `TASTE.md` render (header carries the last event id, bullets carry `(source, TRUST_TIER)`), auto-maintained — `storage.AppendEvent` marks the store dirty, `storage.Close` / `FlushIfDirty` re-render `<home>/TASTE.md` via a callback registered by the CLI and the MCP server (`render.auto: false` turns it off); `lint`; content-hash `reimport` (quarantine `IMPORTED w=0.4`).
- **Judge** (`internal/judge`): one seam for every explicit write. `LLMJudge` asks the gateway for `{statement, category, confidence, relation, target_id}` against the live rows of the category; `HeuristicJudge` reproduces the pre-0.3 keyword/weight×0.5/exact-contradiction-near-dup behaviour and is the fallback (no gateway, `judge.enabled: false`, or a failed call → stderr line + `judge_fallback` counter). `learn.ApplyJudged` stores the verdict through the same events/tiers as `ApplySignal`.
- **Consolidate** (`internal/consolidate`): whole-profile review through the gateway → `merge | rewrite | rescore | retire`; applied after a backup in `distill.mode: auto`, filed as `consolidate` proposals in `review`; guards (24 h retire block, 40 % touched cap); watermark in `consolidations`; auto-fired detached from SessionStart via `distill.SpawnConsolidate`.
- **Home** (`internal/config`): one durable home resolved in `config.ResolveHome` — `PALATE_HOME` > `home:` in `./config.yaml` > legacy detection (`./.palate/palate.db`) > `~/.claude/palate`; every path (DB, `state/`, `TASTE.md`, `backups/`, skills) derives from it through `Config.StatePath/TastePath/…`.
- **MCP** (`internal/mcp`): stdio + SSE, 8 tools with typed input schemas (the SDK derives them from the handler structs), `PALATE_MCP_TOKEN` + loopback allowlist bind guard (refuses empty-host binds without a token), and a per-request `Authorization: Bearer <token>` check on SSE whenever a token is set.
- **Safety** (`internal/safety`): secret censor + injection scanner (clean content unwrapped).
- **Distill** (`internal/distill`, optional): stdlib HTTP client for the user's own OpenAI-compatible gateway, taste-distilling prompt, cursor-tracked windowing, 8 transcript readers (Claude Code, Command Code, Pi, Codex, OpenCode, Gemini CLI, memsearch journals, ZCode-experimental) plus git history — turns evidence into `llm_distilled` signals at `trust_tier=DISTILLED`; silently inert with no `PALATE_LLM_*` set.
- **Verify** (`internal/verify`): blinded scenarios, hash, `--pure` flag, `IndependenceCheck`; `verify retrieval` reports `retrieval_hit_rate`.
- **Skills** (`internal/skills`): clustering (≥3), deterministic templating + hash, rumination gate.

## Data flow

Signal → safety gate → `learn.ApplySignal` → `preferences` + `events` → prewarm/recall/render.

Distiller path (optional, only when `PALATE_LLM_*` is set): Transcript/git
evidence → `distill.Distill` (secret-redact → prompt → gateway → parse) →
safety gate → `distill.Judge` seam (the CLI sets it to `judge.ApplyDistilled`:
one more gateway call that compares the statement with every active row and
returns new / same / refines / contradicts) → `learn.ApplyJudged`
(`llm_distilled`) → `preferences` + `events` — same downstream path as every
explicit write. Without the seam (tests, no gateway) `learn.ApplySignal`'s
heuristic matching runs instead. The hooks skip the spawn under
`distill.mode: off` or a cwd inside `distill.skip_dirs`; the next
UserPromptSubmit announces what landed and hands the model the new rows as
`<palate-learned>`.

## What is not wired

- Post-MVP adapters beyond Claude Code / Command Code / ZCode are recall-only until TASK-900 live verification.
- `sqlite-vec` native vectors require a build tag; default is FTS + JSON cosine (`VectorSearchFallback` is the seam).
- ZCode's transcript reader (`internal/distill`) is experimental and opt-in (`--include-experimental`) — separate from ZCode's hook-based live learning, which is proven (see `docs/adapters/verification.md`).
