# Architecture

Single Go binary, no CGO, SQLite-only (FTS5 + JSON+cosine; `sqlite-vec` behind a build tag).

## Components

- **Storage** (`internal/storage`): `working_memory` / `episodic_memory` / `scratchpad` BEAM tiers, FTS5 triggers (insert/update/delete on all three content tables), `preferences` + `conflicts` bi-temporal (loser hidden, winner shown), `events` append-only truth, `capture_counters`. Recall = FTS + preference scan + RRF, with `--as-of` decay replay, conflict filtering, and optional freshness tiers (`freshness.enabled`).
- **Learn** (`internal/learn`): reinforce/weaken with final clamp (time decay off by default, `half_life_hours` knob), negation/antonym contradiction detection with category scan, `proposals` determinism boundary (apply-before-promote), signal weights, category inference. It also owns the profile: `SelectProfile` (`profile.go`) decides eligibility, rank and budget, `CompareSet` (`compare.go`) is what intake compares a candidate against, `RewriteAllowed` (`rewrite_guard.go`) gates a rewrite of stored text by the judge or the dream pass, `Restore` (`restore.go`) reverses a soft forget. Why: decisions rows 35-37, 40, 42.
- **Prewarm** (`internal/prewarm`): Mechanism A is SessionStart, which injects the budgeted profile: `render.Body`, the same body as `TASTE.md`, with no cut of its own (the budget is applied once, in selection). Mechanism B is the per-prompt UserPromptSubmit output: the `<palate-learned>` block (rows that entered the profile since the last notice) and a goal block that is off unless `token_budgets.prewarm_goal` is above 0 (`Deduper` instances for long-lived callers). SessionStart and UserPromptSubmit also emit a `systemMessage` with what entered or left the profile and, when the background clean-up failed or was skipped, a line saying so (`cmd/palate/notice.go`, cursor in `state/notice_cursor`). Why: rows 35, 37, 41.
- **Render** (`internal/render`): deterministic `TASTE.md` render of the profile (header carries the last event id and a `sha256=` of the body; command-code bullets `- statement. Confidence: 0.xx` with no metadata suffix; provenance stays in the DB). It is auto-maintained: `storage.AppendEvent` marks the store dirty, `storage.Close` / `FlushIfDirty` call `render.Sync` through a callback registered by the CLI and the MCP server (`render.auto: false` turns it off). `Sync` runs under `state/render.lock`, reads back the owner's hand edits first (`ReadBack`, `readback.go`), appends the `profile_add` / `profile_cut` events (`profile_sync.go`), writes `TASTE.md` and then `state/TASTE.base.md`, the body it last rendered, which the next read-back diffs against. Edits it cannot apply safely are held in `state/TASTE.held-*.md` beside the base they were compared with; accepting applies against that base, and a hold with no verifiable base is discard-only. Rendered lines and stored statements are matched by one normalisation (`normStatement`, trailing period and spaces dropped). Also `lint` and content-hash `reimport` (quarantine `IMPORTED w=0.4`). Format: [TASTE-format.md](TASTE-format.md). Why: rows 13, 35, 37, 39.
- **Judge** (`internal/judge`): one seam for every explicit write. `LLMJudge` asks the gateway for `{statement, category, confidence, relation, target_id}` against the compare set (the profile plus the best full-text matches outside it, any category); `HeuristicJudge` reproduces the pre-0.3 keyword/weight×0.5/exact-contradiction-near-dup behaviour and is the fallback (no gateway, `judge.enabled: false`, or a failed call → stderr line + `judge_fallback` counter). `learn.ApplyJudged` stores the verdict through the same events/tiers as `ApplySignal`. Why: row 40.
- **Consolidate** (`internal/consolidate`): the dream pass. It sends the whole profile plus batches of candidate rows (live rows outside the profile that are new or changed since the last applied pass) to the gateway and applies `merge | rewrite | rescore | retire | project` actions after a backup in `distill.mode: auto`, or files them as `consolidate` proposals in `review`. Guards: a 24 h retire shield that also needs two sessions, a cumulative 40 % touched cap per batch, `RewriteAllowed`, and no auto-applied retire, project tag, confidence-lowering rescore or confidence-lowering / re-filing merge on a `STATED` row (those become proposals, re-checked at write time). A code-only step then retires stale single-session candidates, re-reading each row just before it closes it (a merge writes in one transaction, each write conditional on the row's tier). Every attempt is one line in `state/consolidate.log` (`attempt.go`, with applied / proposed / skipped counts that add up to `actions`, `batch_ms` and `apply_errors`, up to 20 samples of `{type, id, error}` for the actions that failed or were malformed; each also prints one `palate: consolidate: <type> of <id> failed: <error>` line to stderr) and a failed or skipped one is shown in the next notice. SessionStart starts it detached, inside the learner's child process (`learn session --then-consolidate`); `palate consolidate` and `palate dream` run it by hand. Why: row 38.
- **Home** (`internal/config`): one durable home resolved in `config.ResolveHome` — `PALATE_HOME` > `home:` in `./config.yaml` > legacy detection (`./.palate/palate.db`) > `~/.claude/palate`; every path (DB, `state/`, `TASTE.md`, `backups/`, skills) derives from it through `Config.StatePath/TastePath/…`.
- **MCP** (`internal/mcp`): stdio + SSE, 8 tools with typed input schemas (the SDK derives them from the handler structs; `palate_recall` marks profile members with `[profile]`, `palate_forget` retires a preference softly, and there is no MCP restore tool), `PALATE_MCP_TOKEN` + loopback allowlist bind guard (refuses empty-host binds without a token), and a per-request `Authorization: Bearer <token>` check on SSE whenever a token is set.
- **Safety** (`internal/safety`): secret censor + injection scanner (clean content unwrapped).
- **Distill** (`internal/distill`, optional): stdlib HTTP client for the user's own OpenAI-compatible gateway, taste-distilling prompt, cursor-tracked windowing, 9 transcript readers (Claude Code, Command Code, Cline, Pi, Codex, OpenCode, Gemini CLI, memsearch journals, ZCode-experimental) plus git history — turns evidence into `llm_distilled` signals at `trust_tier=DISTILLED`, each tagged with a session key; it drops a statement whose only evidence is assistant-authored text, passes a `project` flag through, and compares against the compare set. Silently inert with no `PALATE_LLM_*` set.
- **Verify** (`internal/verify`): blinded scenarios, hash, `--pure` flag, `IndependenceCheck`; `verify retrieval` reports `retrieval_hit_rate`.
- **Skills** (`internal/skills`): clustering (≥3), deterministic templating + hash, rumination gate.

## Data flow

Signal → safety gate → `learn.ApplySignal` → `preferences` + `events` → prewarm/recall/render.

Distiller path (optional, only when `PALATE_LLM_*` is set): Transcript/git
evidence → `distill.Distill` (secret-redact → prompt → gateway → parse) →
safety gate → `distill.Judge` seam (the CLI sets it to `judge.ApplyDistilled`:
one more gateway call that compares the statement with the compare set and
returns new / same / refines / contradicts) → `learn.ApplyJudged`
(`llm_distilled`) → `preferences` + `events` — same downstream path as every
explicit write. Without the seam (tests, no gateway) `learn.ApplySignal`'s
heuristic matching runs instead. The hooks skip the spawn under
`distill.mode: off` or a cwd inside `distill.skip_dirs`; the next
UserPromptSubmit announces what changed and hands the model the rows that
entered the profile as `<palate-learned>`.

Profile path: `render.Sync` selects the profile from the live rows
(`learn.SelectProfile`), appends `profile_add` / `profile_cut` events for the
difference from the previous membership, and writes `TASTE.md`. Only `Sync`
writes membership events and the file; the SessionStart block reads the same
selection through `render.Body` and writes nothing.

## What is not wired

- Post-MVP adapters beyond Claude Code / Command Code / ZCode / Cline are recall-only until TASK-900 live verification.
- `sqlite-vec` native vectors require a build tag; default is FTS + JSON cosine (`VectorSearchFallback` is the seam).
- ZCode's transcript reader (`internal/distill`) is experimental and opt-in (`--include-experimental`) — separate from ZCode's hook-based live learning, which is proven (see `docs/adapters/verification.md`).
