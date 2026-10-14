---
goal: "Palate — an open, harness-agnostic 'coding taste' engine that learns a developer's style preferences from observable behavioral signals (model edits, user corrections, post-hoc diffs, explicit tools) and injects them into AI coding harnesses that support lifecycle hooks (Claude Code, Pi, OpenClaw, OpenCode), with confidence scores, human-readable git-shareable markdown output, recall tools for every MCP client, and honest verification."
version: "2.1 (master blueprint, revised — gap closure)"
date_created: 2026-08-15
last_updated: 2026-09-12
owner: "thinhkhuat"
status: 'Planned'
tags: [taste, memory, agent, mcp, blueprint, open-source, go, sqlite]
---

# Palate — Open "Taste" for Every Hook-Capable Harness

![Status: Planned](https://img.shields.io/badge/status-Planned-blue)

> **Historical design record — not current authority (note added 2026-09-12).** This blueprint is the 2026-08-15 design as planned. The code has moved on; where the two disagree, the code, `docs/architecture.md` and `docs/decisions.md` win. Items below that were built and later **removed** from the codebase:
>
> - **Sync / E2EE (SEC-002, TASK-802, DEP-006, FILE-010, `sync.md` in TASK-1001)** — `internal/sync` and `docs/sync.md` were deleted on 2026-09-12 and `golang.org/x/crypto` dropped; `palate push`/`pull` only render and reimport `TASTE.md`. Reason and revert path: `docs/decisions.md` row 7.
> - **Adapter helper files (FILE-014)** — `pi.go`, `openclaw.go`, `opencode.go`, `codex.go`, `cursor.go`, `windsurf.go`, `openwebui.go` (plus `omp.go`, `hermes.go`) were deleted on 2026-09-11 as uncalled code; the harness matrix lives in `internal/adapters/registry.go`.
>
> Do not re-create these from this document. Treat every task row below as "as planned in August", not "as shipped".

Palate replicates the *mechanism* of Command Code's closed `taste` feature — learning a developer's coding preferences from behavioral signals, storing them with confidence scores, and conditioning an agent on them — as an **open, local-first, self-hostable project**. It is the distillation of everything learned across this session's research into one master blueprint.

**Scope honesty up front:** a third-party tool cannot see a harness's internal accept/reject events. Command Code's taste can, because the learner *is* the harness. Palate instead observes the signals that are actually visible from outside: the model's own edits, the user's corrections (stated and diffed), and explicit tool calls. Harnesses with lifecycle hooks get *learning*; every MCP client gets *recall + explicit tools*. This boundary is a design fact, not a roadmap item.

**MVP cut:** Phases 1–8, Claude Code only — includes TASTE.md render + verification harness (hard-gate: scope was Phases 0–6 in v2.0; locked to 1–8 per 2026-08-15 interview). Phases 9–12 are post-MVP backlog. Every borrowed benchmark number in this document is treated as an unverified hypothesis until Palate measures it itself.

---

## 0. The Novelty Distillation (what we take from whom)

| Source | Novelty taken | How Palate uses it |
|--------|---------------|--------------------|
| **Command Code taste** (target) | Learned-from-behavior preferences; confidence scores; categorized `taste.md`; git push/pull sharing | The core output contract: `TASTE.md`, per-domain categories, confidence, shareable via git |
| **Mnemos (polyxmedia)** | **Push-based prewarm** (session-start injection, ~500 token budget — the agent never has to remember to ask); **structured correction journal** (`tried/wrong_because/fix/trigger_context`); **skill promotion** (3+ clustered corrections → skill); **compaction recovery**; **rumination** (Popperian falsifiability gate — a revised skill must name a new testable prediction); **behavioral A/B verification harness**; **prompt-injection scanner** at the write boundary; **bi-temporal store** (no destructive overwrite) | The *injection* and *correction-signal* architecture + the verification gate are Palate's spine. **Caveat: Mnemos's published numbers (53% capture, +40% lift, +100%/+0%) are self-reported n=5 measurements from a 21-star, 80-commit repo. Palate treats them as hypotheses, not baselines.** |
| **Mnemosyne (mnemosyne-oss, cloned)** | **BEAM tiers** (working/episodic/scratchpad + sleep() consolidation); **MEMORIA layer** (`memoria_preferences` with `evolution`, `memoria_instructions`); **persona tier** (confidence + reinforcement_count columns, prompt-injection markdown file); **veracity consolidation** (fixed-rate confidence bump `old+(1-old)*w*0.3` + mention_count + conflict rows); **hybrid scoring** 50% vec / 30% FTS / 20% importance; **binary-vector compression** (32x); **polyphonic recall** (4 voices + RRF); **client-side-encrypted sync** (event log, LWW conflict resolution); **honest "what is not wired" docs** | Palate's storage, confidence-update math, tier degradation, sync, and the honesty norm |
| **memsearch (zilliztech)** | **Markdown as readable artifact** (daily journals, shadow vector index rebuildable from raw memories); **USER.md/PROJECT.md maintenance layer** (reusable preferences + working style, background-refreshed); **skills-from-memory** (distill recurring workflows into installable skills); zero-config install; hooks+skills (no MCP server required) | TASTE.md = readable render artifact; the *event log* is the source of truth; skill distillation path |
| **Hindsight** | Multi-strategy retrieval (semantic/BM25/graph/temporal) + cross-encoder rerank; single-container embedded Postgres; **reflect** (auto-updating mental models); benchmark discipline (BEAM/LongMemEval, honest self-comparison) | Retrieval voice set + the "publish a benchmark you can reproduce" norm |
| **Claude Code auto-memory** | `MEMORY.md` index + topic files; first-200-lines/25KB load budget; on-demand topic reads; per-git-repo memory dir | The prewarm file's index/topic structure and load budget |
| **agent-memory-mcp (ipiton)** | **Knowledge stewardship** (dedup, conflict resolution, canonical promotion, drift scan, verification); **trust-aware retrieval** (confidence/freshness/owner/last_verified_at); session-close consolidation with explainable reports | Stewardship pass + trust metadata on every preference |
| **PRELUDE/CIPHER (arXiv 2404.15269)** | Learning latent preference from user edit deltas (the research-grade algorithm direction) | The formal framing for the edit-diff → preference learner |
| **User's own constraints** | Free = fully free when self-hosted; self-hosting preferred; harness/model-agnostic | No cloud requirement; MIT license; single binary |

---

## 1. Requirements & Constraints

- **REQ-001**: Learn preferences from *observable behavioral signals* — model edits, user corrections (stated and diffed), and explicit tool calls. "Accept/reject" is not a universal primitive; it is a Claude-Code-specific inference from correction absence + commit, treated as weak evidence only.
- **REQ-002**: Maintain a per-preference `confidence` (0..1) that is *reinforced* by confirmatory signals, *weakened* by contradiction, and *decayed* by elapsed time (bi-temporal: nothing is ever destructively deleted). The update math is fully specified in Phase 3.
- **REQ-003**: Inject a **push-based prewarm** block so the agent need not remember to call a tool; plus on-demand `recall`. Session-scoped prewarm via `SessionStart` hooks; goal-scoped prewarm via `UserPromptSubmit` `additionalContext`. Token budgets stated per mechanism.
- **REQ-004**: Render the learned profile as human-readable, git-shareable **`TASTE.md`** (per-domain categories) with one binary: `palate push/pull/list/lint`. TASTE.md is a **lossy render artifact**, not the source of truth.
- **REQ-005**: **Learning** on hook-capable harnesses: Claude Code, Pi, OpenClaw, OpenCode. **Recall + explicit tools** on every MCP client (stdio/SSE). This is stated honestly, not as "every harness learns."
- **REQ-006**: Free when self-hosted: MIT license, no cloud, no telemetry, single static binary, SQLite-only storage, optional client-side-encrypted sync.
- **REQ-007**: Model-agnostic (works with any LLM; local-first embeddings optional).
- **REQ-008**: A **verification harness** that measures capture rate and behavioral lift (A/B, on vs off) with an independence guarantee (blinded scenario set, third-party judge, `--pure` mode) and refuses to ship unmeasured claims.
- **SEC-001**: Prompt-injection scanner + **secret/credential filter** at every memory write boundary (MVP gate — not post-MVP); memory treated as untrusted input on read (quarantined until `palate promote`). Server sees metadata only when E2EE is on.
- **SEC-002**: Optional E2EE sync (server sees only metadata); key never leaves the machine (OS keychain or `file:<path>`; no plaintext in repo).
- **SEC-003**: **Trust tier on read injection (MVP)**: every preference carries `trust_tier` (STATED / IMPORTED / EXTERNAL_WRITE) + `veracity`; prewarm tier-aware ranking/banners; IMPORTED never auto-enters the write path.
- **SEC-004**: **MCP auth guard (MVP)**: loopback allowlist + `PALATE_MCP_TOKEN` + `hmac.compare_digest` + refusal to bind `0.0.0.0` without token (mirrors `mnemosyne/mcp_server.py:58-86`); every write tool binds `author_id/device_id` + rate limits.
- **CON-001**: One static Go binary per platform; no CGO requirement; cross-compile Linux/macOS/Windows × amd64/arm64. **Vector posture: FTS-only + JSON+cosine fallback is the MVP primary; `sqlite-vec` is an optional build-tag experiment (zero-CGO default carries no native vec).**
- **CON-002**: Zero external databases; SQLite single file (FTS5 built-in; vectors via JSON-vector cosine fallback as MVP primary — `sqlite-vec` only when loadable under a build tag).
- **CON-003**: Deterministic, auditable learning: same signals → same confidence updates. LLM-generated candidates **never** enter the deterministic path; they live in a separate review queue and only enter the event log via explicit user promotion (see Phase 3).
- **GUD-001**: Follow the honesty norm: every doc ships a "What is not wired" section; every benchmark lists judge/model/oracle caveats.
- **PAT-001**: Provider/adapter registry pattern (from mnemosyne-oss): core engine is harness-agnostic; adapters register lifecycle hooks per harness.
- **PAT-002**: Event-log mutation source of truth for sync (append-only, LWW conflict resolution with version chains).

---

## 2. Implementation Steps

### Implementation Phase 0 — Foundation & Skeleton

- GOAL-001: Scaffold the `palate` Go module, CLI, config, SQLite schema, and MCP server skeleton.

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-001 | Init repo: `go.mod` (module path `github.com/<user>/palate`, `<user>` filled at scaffold), MIT LICENSE, README, CONTRIBUTING, .goreleaser.yml, CI (test+lint+build matrix) | | |
| TASK-002 | CLI skeleton (`palate init/doctor/store/recall/prewarm/sleep/export/import/push/pull/stats/serve`) using cobra or stdlib flag. The `palate` binary deliberately mirrors `npx taste`'s subcommand surface for familiarity; the single-binary name avoids npm collision | | |
| TASK-003 | Config: `config.yaml` + env overrides, one source of truth for every tunable (weights, TTL, half-life, token caps, confidence floor) | | |
| TASK-004 | SQLite bootstrap via `modernc.org/sqlite` (no CGO): init_* idempotent DDL, WAL, migrations-free additive schema | | |
| TASK-005 | MCP server skeleton (official `modelcontextprotocol/go-sdk`): stdio + SSE, tool registry, per-connection instance, auth guard for non-loopback | | |
| TASK-006 | `palate doctor` self-diagnostic (db path, FTS5 available, vector backend, adapter wiring, sample smoke) | | |

Success Criteria: `palate doctor` passes on a clean machine; `palate serve` (MCP stdio) handshakes with a generic MCP client; zero CGO build for darwin/linux/windows.

---

### Implementation Phase 1 — Core Storage (BEAM-like tiers)

- GOAL-002: Working/episodic/scratchpad tiers + hybrid retrieval, bi-temporal model, memory banks.

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-101 | Tables: `working_memory`, `episodic_memory`, `scratchpad`, `consolidation_log`, `events` (append-only mutation log — **the source of truth**) | | |
| TASK-102 | Preference table `preferences`: `id, category, statement, confidence REAL, importance, source, provenance JSON (signals[]), evolution TEXT, valid_from/valid_until, created_at, last_reinforced_at, reinforcement_count, last_verified_at, owner` | | |
| TASK-103 | Knowledge-graph tables `triples` (temporal SPO), `facts`, `graph_edges` (for polyphonic voice) | | |
| TASK-104 | FTS5 virtual tables over working+episodic; vector store: sqlite-vec extension when loadable else `memory_embeddings` JSON + cosine fallback | | |
| TASK-105 | Hybrid scorer: `0.5*vec + 0.3*fts + 0.2*importance`, × recency decay × tier weight, + graph/fact bonuses; degradation ladder vec→JSON→FTS-only | | |
| TASK-106 | `sleep()` consolidation: working→episodic summarization (local LLM optional, else lossless keyword/AAAK-style fallback), additive (never deletes), freshness tier degradation (T1/T2/T3 multipliers + smart compress) | | |

Success Criteria: `palate store/recall` round-trips with and without embeddings; consolidation is additive; query with `as_of` timestamp returns historical state.

---

### Implementation Phase 2 — Observable Signal Capture (the Taste core, rewritten)

- GOAL-003: Capture the signals that are actually visible from outside a harness, and measure capture rate honestly.

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-201 | Signal schema (source-of-truth vocabulary): `model_edit(tool, file, before, after)` from PostToolUse; `user_correction{tried, wrong_because, fix, trigger_context}` (Mnemos schema) from UserPromptSubmit matching or explicit tools; `user_diff(file, before, after)` from a Stop-hook working-tree diff that captures post-hoc user touch-ups; `stated_preference(statement, category)` from regex/LLM over correction text; `explicit_prefer`, `explicit_reject`, `explicit_accept` from MCP tools. **No universal accept/reject primitive.** | | |
| TASK-202 | **Claude Code adapter (first, MVP-only)**: `PostToolUse Edit|Write` (record `model_edit{file, hash_after}` per tool call), `UserPromptSubmit` (correction-pattern match, Mnemos lever, 30s timeout), `Stop` (working-tree diff filtered to be a valid `user_diff` **only when**: (a) a `model_edit` for the same `file` occurred between the previous `Stop` and this `Stop`, (b) `git status --porcelain` + `git diff` shows a dirty change to that exact file, and (c) the diff is non-empty after the model's `hash_after`; otherwise the diff is dropped — never emitted as a signal. Captured `user_diff` carries **hypothesis `w=0.6`** (raise to 0.9 only if Phase 8 precision ≥0.7); a dropped diff emits nothing), `SessionStart` (prewarm, Phase 4), `PreCompact` (recovery checkpoint, Phase 4). Log per-file `user_diff` hit/drop counters for TASK-601 | | |
| TASK-203 | MCP tools: `palate_correct`, `palate_prefer`, `palate_reject`, `palate_accept`, `palate_edit`, `palate_remember`, `palate_recall`, `palate_forget`, `palate_stats` — the reliable floor when hooks miss (explicit > silent) | | |
| TASK-204 | Edit-diff → preference candidate extractor: deterministic rules first (regex/keyword like mnemosyne MEMORIA). Optional LLM extraction goes to the **proposal review queue**, never directly into `preferences` (see Phase 3, TASK-304) | | |
| TASK-205 | Capture-directive engine: when a harness lacks rich hooks, inject a `[palate: capture required]`-style directive into context on correction-shaped prompts (Mnemos lever); hook-based harnesses skip this | | |
| TASK-206 | **Capture-rate measurement built in from day one**: every signal type logs hit/miss; `palate verify capture` reports per-signal-type capture rates (explicit, inline correction, quiet convention, mid-work, buried decision). This data drives Phase 6, not the reverse | | |

Success Criteria: `palate verify capture` produces a per-signal-type table with counts; `palate_correct` with the 4-field schema lands a typed `user_correction` observation with retrieval-weight bonus; the Stop-hook diff correctly identifies a user edit that followed a model edit.

---

### Implementation Phase 3 — Learning Engine (confidence that compounds, fully specified)

- GOAL-004: Deterministic confidence updates from observable signals, with the math defined completely.

**Signal weights (w), fixed at ingest:**

| Signal | w | Rationale |
|--------|---|-----------|
| `explicit_correction` (palate_correct) | 1.0 | User stated a correction directly |
| `user_diff` (Stop-hook diff) | 0.6* | User physically changed code after the model; *hypothesis weight — raise to 0.9 only if Phase 8 `verify capture` precision ≥0.7 (RISK-006; 2026-08-15 interview) |
| `explicit_reject` (palate_reject) | 0.9 | User explicitly rejected |
| `stated_preference` ("I prefer X") | 0.8 | Stated, not yet behaviorally confirmed |
| `explicit_prefer` (palate_prefer) | 0.8 | User explicitly stated a preference |
| `explicit_accept` (palate_accept) | 0.6 | Weak: accept is low-information |
| `model_edit` (PostToolUse) | 0.4 | Weakest: model's own output, not user endorsement |
| `llm_proposal` | N/A | Never enters the deterministic path |

**Update rules (in order, per signal event):**

1. **Decay** for elapsed time since `last_reinforced_at`:
   - `c = max(c_floor, c * exp(-λ * Δt))`
   - `λ = ln(2) / half_life`, `half_life` default 168h (7 days, configurable), `c_floor` default 0.1 (configurable)
   - **Replay determinism (interview 2026-08-15):** live `Δt` is wall-clock (`now - last_reinforced_at`), but `events` replay / `TEST-008` must pin `Δt` to `as_of - last_reinforced_at` (`as_of` explicit; defaults to last event's timestamp). Same `events` + same `as_of` → same `c`.
2. **Reinforce** (confirmatory signal):
   - `c = c + (1 - c) * w * 0.3` — approaches 1 asymptotically, never exceeds it
3. **Weaken** (contradictory signal):
   - `c = max(c_floor, c * (1 - w * 0.3))` — multiplicative decay toward 0, symmetric to reinforce, never negative
4. Stamp `last_reinforced_at` and `reinforcement_count += 1`.

Every update is clamped to `[c_floor, 1.0]` as a final guard, so no formula can produce an out-of-range confidence regardless of signal ordering or weight.

The `0.3` is a fixed learning rate. This is a **fixed-rate confidence update**, not a Bayesian posterior — no prior/likelihood model is claimed.

**Contradiction rule:**

- New statement conflicts with an existing preference (same category, opposite polarity) at `c > 0.5`:
   - Create a `conflicts` row; the old preference gets `valid_until = now` (bi-temporal close) only if its weakened confidence falls below `c_floor`; otherwise it stays active but weakened
   - Create/update the new statement as a separate preference with initial `c = max(c_floor, w * 0.5)` for its signal weight `w`
   - Optional LLM validator (off by default; emits a **proposal**, never a decision)
- **Recall/prewarm precedence when both stay active:** per `conflicts` group, only the highest-`confidence` preference is eligible for prewarm injection and `TASTE.md` render; the lower-confidence peer remains queryable via `recall --include-conflicted` but is excluded from default context. Tie-break: higher `reinforcement_count`, then most recent `last_reinforced_at`

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-301 | Implement the update rules above as pure functions of (signal, prior state, config). Unit-test the arithmetic exactly | | |
| TASK-302 | Implement decay as specified; **decay applies to confidence, not to the fact's existence** — the preference row is never deleted; `valid_until` only closes on contradiction, never on decay | | |
| TASK-303 | Implement contradiction handling per the rule above; conflict rows are bi-temporal and auditable | | |
| TASK-304 | **Determinism boundary**: LLM extraction output lands in `proposals` table with `source: llm`. Proposals are excluded from the `events` replay path. Only an explicit `palate promote <proposal_id> [--as stated_preference|--as explicit_prefer]` creates a deterministic `events` entry with the chosen signal's weight (`w=0.8` default when `--as` is omitted, treated as `stated_preference`). The chosen `w` seeds the new preference's initial `c = max(c_floor, w*0.5)` per the contradiction rule. This is what makes CON-003 true | | |
| TASK-305 | Stewardship pass (agent-memory-mcp): stale detection, drift scan vs live repo, verification candidates, canonical promotion — all deterministic, none LLM-gated | | |
| TASK-306 | Audit trail: every mutation writes to `events`; the event log replays identically to the current state (determinism test) | | |

Success Criteria: a simulated 30-day signal stream produces monotonic-ish confidence convergence; contradicting signals weaken the old preference and promote the new one; full pipeline replays identically from the event log; LLM proposals are invisible to the replay path.

---

### Implementation Phase 4 — Push Prewarm + Recall (the Mnemos spine, correctly specified)

- GOAL-005: Session-scoped and goal-scoped prewarm, on-demand recall, compaction recovery, token budgeting.

**Two injection mechanisms, explicitly separated:**

- **Mechanism A — SessionStart (session-scoped):** `SessionStart` hook writes the prewarm block to stdout (Claude Code injects stdout as context). Matchers: `startup|resume|compact`. Budget ~500 tokens. Content: top-N preferences by `confidence × importance`, recent corrections, hot files. Fires once per session lifecycle event — no per-turn cost.
- **Mechanism B — UserPromptSubmit (goal-scoped):** returns `hookSpecificOutput.additionalContext`. Budget ~200 tokens. Fires **only when the goal signature changes** (hash of detected intent from the user prompt). Content: preferences matching the detected goal. **Honest cost: this is a per-trigger token cost, not free.** It exists because a session's goal changes mid-session and Mechanism A cannot see it.

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-401 | `prewarm(session_id, scope)` for Mechanism A: token-budgeted block from preferences + corrections + hot files | | |
| TASK-402 | `prewarm(goal_signature)` for Mechanism B: goal-scoped block via `UserPromptSubmit` `additionalContext`; goal-signature hashing to avoid firing every turn | | |
| TASK-403 | `context(recovery)` tool: reconstruct goal/decisions/in-session observations after compaction (Claude Code `PreCompact` hook) | | |
| TASK-404 | On-demand `recall(query, filters, token_budget)`: polyphonic voices (vector/graph/fact/temporal) fused with RRF + diversity re-rank (mnemosyne) or linear hybrid; `--json` for scripting | | |
| TASK-405 | Load budget discipline: prewarm index keeps first-200-lines/25KB rule (Claude Code auto-memory pattern); detail moved to topic files read on demand | | |

Success Criteria: Mechanism A injects once per session with no per-turn cost; Mechanism B fires only on goal-signature change; behavioral A/B (Phase 6) shows whether the prewarm changes agent behavior; compaction recovery restores goal context after `/compact`.

---

### Implementation Phase 5 — Human-Readable Profile Output (the Taste contract, corrected data model)

- GOAL-006: Render learned preferences as `TASTE.md` (categorized, confidence-scored, git-shareable), plus JSON export. **The event log is the source of truth; TASTE.md is a render artifact.**

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-501 | Renderer: `## <Category>` sections, `- <statement>. Confidence: 0.xx` lines (byte-compatible with taste's format; `palate lint` validates structure/confidence ranges/header format) | | |
| TASK-502 | Category taxonomy auto-maintenance: split/merge categories as learnings grow (taste's auto-taxonomy) | | |
| TASK-503 | **Data-model contract**: `.palate/` holds the SQLite DB (source of truth) + event log. `TASTE.md` is generated from the DB, reviewable in PRs. Human edits to `TASTE.md` are re-imported as `stated_preference` signals (w=0.8), **not** as authoritative state. `palate reindex` rebuilds the FTS/vector indexes from the DB, not from TASTE.md | | |
| TASK-504 | `palate push/pull/list` for sharing (git-based + optional sync server); merge semantics: new add, updated-confidence update, identical keep | | |
| TASK-505 | JSON export/import for programmatic consumption + Obsidian-vault-style markdown graph export (mnemos) | | |

Success Criteria: a week of simulated use produces a TASTE.md a human can read, edit, and re-import as signals; `palate lint` passes; push/pull round-trip merges correctly; deleting the DB loses the learning state (the render alone cannot restore it), and this is documented.

---

### Implementation Phase 6 — Verification Harness (independent by design, MVP gate)

- GOAL-007: Behavioral A/B, capture-rate measurement, retrieval benchmarks — with an independence guarantee.

**Independence design (the anti-oracle):**

- Scenario set is written **before** the learner is tuned, by a different process than the one tuning the learner, and versioned
- Judge is a model/configuration independent of the model being steered; judge model + version recorded on every report
- `--pure` mode disables all harness oracles (raw recent messages, side-index bypasses) — the same lesson learned from mnemosyne-oss's benchmark inflation
- Every report prints judge/model/oracle caveats, or refuses to print the number

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-601 | `palate verify capture`: measure capture rate across signal types (explicit, inline correction, quiet convention, mid-work, buried decision); reads the counters built in TASK-206 | | |
| TASK-602 | `palate verify behavior`: paired on/off runs against Claude Code on the fixed scenario set; report lift per scenario, not a single headline; publish the n | | |
| TASK-603 | `palate verify retrieval`: Hit@K/MRR on a labeled corpus; `--pure` mode with zero harness oracles | | |
| TASK-604 | CI gate: small smoke suite runs on every commit; any regression blocks | | |

Success Criteria: capture and lift numbers published with full methodology and an n; no headline number without a caveats block; the verification harness passes its own independence checklist.

---

### Implementation Phase 7 — Skill Promotion + Rumination (post-MVP)

- GOAL-008: Promote clustered repeated corrections into skills; guard skill revision with a falsifiability gate.

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-701 | Clustering: corrections grouped by (agent, project, topic); ≥3 in a cluster → skill candidate (deterministic templating, stable origin hash → extends not duplicates) | | |
| TASK-702 | Skill store: standard SKILL.md (agentskills.io frontmatter — portable across Claude Code/Codex/OpenClaw/OpenCode, memsearch's insight); git-backed candidates dir, inert until installed | | |
| TASK-703 | Rumination gate: a skill flagged for revision must state `why_better` — a new testable prediction, min length enforced (Mnemos's Popperian guard) | | |
| TASK-704 | Effectiveness tracking: success/use counts per skill; threshold monitors flag rot/stale/contradiction | | |

Success Criteria: 3 clustered corrections produce a portable SKILL.md; a cosmetic skill rewrite is rejected by the gate; effectiveness metrics feed the monitor.

---

### Implementation Phase 8 — Sync + Security (post-MVP)

- GOAL-009: Event-log sync with client-side encryption; prompt-injection defense at the boundary.

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-801 | Sync engine: append-only `events` log replicated bidirectionally; LWW conflict resolution with (timestamp, importance, device_id) tiebreak + version chains | | |
| TASK-802 | E2EE: optional client-side encryption (Fernet/XSalsa20-Poly1305 class), key derived via Argon2id/PBKDF2, never leaves machine; server sees metadata only | REMOVED 2026-09-12 | see banner; `docs/decisions.md` row 7 |
| TASK-803 | Prompt-injection scanner at write boundary (mnemos): instruction-override phrases, role-spoofing, fake tool syntax, zero-width/bidi; flag → `[PALATE: FLAGGED]` banner; low-risk sanitized | | |
| TASK-804 | Read-side trust: memory injected as untrusted content (OpenHands `<UNTRUSTED_CONTENT>` pattern); provenance + `last_verified_at` on every preference | | |

Success Criteria: two instances sync bidirectionally with encrypted payloads (server cannot read content); injection payload planted in a memory is either sanitized or visibly flagged before reaching context.

---

### Implementation Phase 9 — Remaining Harness Adapters (post-MVP, honest matrix)

- GOAL-010: Pi, OpenClaw, OpenCode adapters (learning-capable *if verified*) + recall-only paths for everything else. **No post-MVP adapter may claim learning until its hook surface is verified against live docs at build time.**

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-900 | **Hook-surface verification gate (precondition for 901–903)**: for each post-MVP harness (Pi, OpenClaw, OpenCode), verify against the live harness docs/cloned repo that concrete lifecycle hooks exist for the signal set Palate needs (model-edit, user-prompt, session-start/stop). Record the evidence (doc URL + hook name + capture mapping) in `docs/adapters/verification.md`. Any harness failing this gate is demoted to recall-only (TASK-904 pattern) and its matrix row is marked `recall-only (hooks unverified)` — no learning claimed | | |
| TASK-901 | **Pi**: extension + skill (`pi install npm:@<user>/pi-palate` pattern, pi.dev publish) — learning only if TASK-900 passes for Pi; otherwise recall-only | | |
| TASK-902 | **OpenClaw**: plugin (hook allowConversationAccess) — learning only if TASK-900 passes for OpenClaw; otherwise recall-only | | |
| TASK-903 | **OpenCode**: plugin — learning only if TASK-900 passes for OpenCode; otherwise recall-only | | |
| TASK-904 | **Recall-only paths** (no learning, documented as such): Codex (MCP in `~/.codex/config.toml` + AGENTS.md), Cursor (`.cursor/mcp.json` + rules), Windsurf/Devin (rules + MCP), OpenWebUI (`@tool` bridge), generic MCP (stdio/SSE). Each gets recall tools + explicit capture tools only | | |
| TASK-905 | Adapter matrix table in README: one column for **capture capability** (hook-based learning vs recall-only, with `verification.md` evidence link per learning-capable row), one for **injection path**, one for **install command**. No adapter claims learning it doesn't have | | |

Success Criteria: TASK-900 verification evidence exists for every post-MVP harness before learning is claimed; each learning-capable adapter that passed the gate installs via one command and `palate doctor` verifies wiring; any failed gate is honestly recorded as recall-only in the matrix.

---

### Implementation Phase 10 — Docs, Packaging, Release (post-MVP)

- GOAL-011: Honest docs, release pipeline, first tagged release.

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-1001 | Docs: architecture.md, TASTE-format.md, adapters matrix (with capture-capability column), security.md (threat model), ~~sync.md~~ (deleted 2026-09-12), benchmarking.md, each with a "What is not wired" honesty section | | |
| TASK-1002 | Packaging: goreleaser (Linux/macOS/Windows amd64+arm64), Homebrew tap, install script, `palate update` self-update with sha256 verify | | |
| TASK-1003 | Release v0.1.0 with the verification numbers, then iterate | | |

Success Criteria: clean install on macOS/Linux/Windows from the docs; `palate doctor` green; v0.1.0 tagged.

---

## 3. Alternatives

- **ALT-001 (Language: Rust)** — Mimir/timwood-mnemosyne prove single-binary Rust works; stronger memory-safety; but slower iteration and a less turnkey pure-Rust SQLite+FTS+vector story than Go's `modernc.org/sqlite`. Chosen: **Go** (fastest path to a correct, cross-compiled, zero-CGO binary; strongest prior art in this exact niche: Mnemos, engram, agent-memory-mcp).
- **ALT-002 (Language: TypeScript/Node)** — memsearch/claude-mem/pi-mem are TS; fast to write, but node_modules + runtime weight breaks the "one static binary, zero deps" constraint (CON-001) and the self-host story.
- **ALT-003 (Storage: embedded Postgres, Hindsight-style)** — excellent retrieval; but requires a container, violating "zero external DBs" and the single-binary ideal.
- **ALT-004 (Memory-as-pull-only, Mem0-style)** — rejected on the evidence of Mnemos's critique and the user's own claude-mem experience: pull-only layers are silently never invoked. Prewarm is push.
- **ALT-005 (Pure-LLM learner, no deterministic core)** — rejected: non-deterministic, un-auditable, and conflicts with CON-003. LLM used only for open-ended candidate *proposals* in a review queue; confidence math stays deterministic.
- **ALT-006 (SaaS with free tier)** — rejected outright under the user's rule: free must mean fully free when self-hosted.

---

## 4. Dependencies

- **DEP-001**: `modernc.org/sqlite` (pure-Go SQLite, no CGO)
- **DEP-002**: `modelcontextprotocol/go-sdk` (official MCP server SDK)
- **DEP-003**: FTS5 (built into SQLite ≥3.35) — bundled via modernc
- **DEP-004**: Optional `sqlite-vec` extension for native vectors; fallback: JSON vectors + cosine (pure Go)
- **DEP-005**: Optional local embeddings via Ollama/llama.cpp (bge-m3 class); remote OpenAI-compatible as opt-in
- **DEP-006**: `cryptography`-class E2EE (Go stdlib `crypto/` + Argon2id via `golang.org/x/crypto`) — **REMOVED 2026-09-12** (`x/crypto` no longer a dependency)
- **DEP-007**: cobra or stdlib `flag` for CLI; `gopkg.in/yaml.v3` for config
- **DEP-008**: goreleaser, GitHub Actions, Homebrew tap for packaging

---

## 5. Files (initial tree)

- **FILE-001**: `cmd/palate/main.go` — CLI entry
- **FILE-002**: `internal/storage/schema.go` — idempotent DDL (init_* pattern)
- **FILE-003**: `internal/storage/beam.go` — working/episodic/scratchpad tiers + consolidation
- **FILE-004**: `internal/learn/preferences.go` — preference model + confidence math (reinforce/weaken/decay/contradiction)
- **FILE-005**: `internal/learn/signals.go` — signal types + weights + edit-diff extractor
- **FILE-006**: `internal/learn/proposals.go` — LLM proposal review queue (determinism boundary)
- **FILE-007**: `internal/prewarm/prewarm.go` — token-budgeted context composer (Mechanism A + B) + compaction recovery
- **FILE-008**: `internal/render/taste_md.go` — TASTE.md renderer + `lint`
- **FILE-009**: `internal/skills/promote.go` + `internal/skills/ruminate.go` — promotion + falsifiability gate
- **FILE-010**: `internal/sync/engine.go` + `internal/sync/encryption.go` — event-log sync + E2EE — **REMOVED 2026-09-12** (`internal/sync` deleted; `docs/decisions.md` row 7)
- **FILE-011**: `internal/safety/scanner.go` — prompt-injection scanner
- **FILE-012**: `internal/mcp/server.go` — MCP server (stdio/SSE) + tool registry
- **FILE-013**: `internal/adapters/claude_code.go` — hooks config generator (UserPromptSubmit/PostToolUse/Stop/SessionStart/PreCompact) + skill
- **FILE-014**: `internal/adapters/pi.go`, `openclaw.go`, `opencode.go` (learning-capable) + `codex.go`, `cursor.go`, `windsurf.go`, `openwebui.go` (recall-only) — **REMOVED 2026-09-11** (all seven files deleted as uncalled; matrix lives in `registry.go`)
- **FILE-015**: `internal/verify/harness.go` — capture/behavior/retrieval verification + independence checklist
- **FILE-016**: `internal/verify/scenarios.go` — the fixed, versioned scenario set
- **FILE-017**: `config.yaml` + `internal/config/config.go`
- **FILE-018**: docs/ (architecture, taste-format, adapters matrix, security, sync, benchmarking, "What is not wired")
- **FILE-019**: `.goreleaser.yml`, `install.sh`, CI workflows

---

## 6. Testing

- **TEST-001**: Unit — confidence math (reinforce/weaken/decay/contradiction) property tests + determinism/replay tests
- **TEST-002**: Unit — edit-diff extractor against a corpus of before/after pairs (PRELUDE-style)
- **TEST-003**: Integration — store/recall/consolidate round-trips with each vector backend (extension / JSON / FTS-only)
- **TEST-004**: Integration — MCP handshake + tool calls over stdio/SSE; adapter config generation is idempotent
- **TEST-005**: Integration — sync two instances bidirectionally, encrypted payload unreadable by server
- **TEST-006**: Integration — injection-scan: planted payloads sanitized or flagged
- **TEST-007**: E2E — `palate verify capture|behavior|retrieval` with the shipped, versioned scenario set (CI smoke); report includes n and methodology
- **TEST-008**: E2E — 30-day simulated signal stream converges; TASTE.md renders; `lint` passes; push/pull merges; event-log replay reproduces state identically
- **TEST-009**: Unit — LLM proposals never appear in the deterministic replay path; only `palate promote` brings them in

---

## 7. Risks & Assumptions

- **RISK-001**: Capture is the hard part. The only existing measurement — Mnemos's self-reported 53% with buried decisions at 0% — is **an unverified n=5 anecdote, not a baseline**. Palate must measure its own capture rate per signal type from TASK-206 onward. Mitigation: explicit `palate_correct`/`palate_prefer` tools are the reliable floor; the UserPromptSubmit directive lever is the first optimization to test, not a proven fix.
- **RISK-002**: Behavioral lift may concentrate on contrarian conventions. Mnemos's "+100% on contrarian / +0% on common" claim is **self-reported and unverified**. Palate's verification harness (Phase 6) measures per-scenario and publishes the distribution, not a single headline.
- **RISK-003**: Multi-harness scope creep. Mitigation: MVP is Phases 1–8, Claude Code only (TASTE.md + verify harness are P1 per interview). Post-MVP adapters are thin config generators + honest matrix entries. Learning is claimed only for hook-capable harnesses.
- **RISK-004**: Vector-search quality without a vector DB. Mitigation: proven degradation ladder; FTS5 is surprisingly good for preference-shaped queries; optional sqlite-vec.
- **RISK-005**: Confidence math could silently poison context with a wrong high-confidence rule. Mitigation: bi-temporal no-delete, conflict rows, verification candidates, trust metadata, injection scanner, and the proposal review queue (LLM never writes directly).
- **RISK-006**: The `Stop`-hook diff (TASK-202) is the least-proven mechanism — it must distinguish user touch-ups from model edits, and a `Stop` hook fires on every turn end. Mitigation: **MVP ships `user_diff` at `w=0.6` (hypothesis, per 2026-08-15 interview); raise to 0.9 only if Phase 8 `verify capture` precision ≥0.7.** Phase 8 must decide based on measured precision — the weight table is a hypothesis, not a frozen constant.
- **ASSUMPTION-001**: The user will build this as a new Go project at `~/in-PROD/MY-WORKBENCH/tk-taste/` (the directory where this blueprint lives). Working title "Palate."
- **ASSUMPTION-002**: Claude Code hook surfaces (UserPromptSubmit/PostToolUse/Stop/SessionStart/PreCompact) remain available for adapter wiring. This is verified against the current hooks reference at plan time, not assumed permanently.
- **ASSUMPTION-003**: "Every harness" is satisfied by the honest matrix: learning on hook-capable harnesses, recall + explicit tools everywhere else. Not bespoke deep integrations with every tool ever.

---

## 8. Related Specifications / Further Reading

- Command Code taste docs: https://commandcode.ai/docs/taste
- Mnemos: https://github.com/polyxmedia/mnemos | "Agent memory is push, not pull": https://buildingbetter.tech/p/agent-memory-is-push-not-pull
- mnemosyne-oss (cloned at `~/in-PROD/MY-WORKBENCH/CLONED/mnemosyne`): docs/architecture.md, docs/persona.md, mnemosyne/core/veracity_consolidation.py
- memsearch: https://github.com/zilliztech/memsearch
- Hindsight OSS comparison: https://hindsight.vectorize.io/blog/2026/08/11/open-source-agent-memory-systems
- agent-memory-mcp: https://github.com/ipiton/agent-memory-mcp
- PRELUDE/CIPHER (learning preference from user edits): https://arxiv.org/abs/2404.15269
- Claude Code memory/hooks docs: https://code.claude.com/docs/en/memory | https://code.claude.com/docs/en/hooks
- OpenHands persistent memory (untrusted-content pattern): https://docs.openhands.dev/sdk/guides/persistent-memory

## 9. Open Decisions (locked 2026-08-15 interview)

1. **Name** — working title **Palate**; alternatives: Taster, Savor, Acquired. (Pick one before Phase 1 close.)
2. **Language** — Go (recommended, per ALT-001); Rust if you prefer memory-safety over iteration speed.
3. **Module path** — fill `<user>` in `github.com/<user>/palate` at scaffold.
4. **MVP scope — LOCKED: Phases 1–8, Claude Code only (TASTE.md + verification harness are P1).** Prior draft listed 0–6; interview 2026-08-15 corrected the single definition. Phases 9–12 are post-MVP backlog. (Interview answer: "Lock to Phases 1-8".)
