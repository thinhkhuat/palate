# Install — Palate (every harness)

Palate is a **single Go binary, no CGO, SQLite-only**. Two install paths — pick one for humans, one for agents. Both end at `palate init && palate doctor → green` before you wire any harness.

## 0. Get the binary

| Path | Command | When to use |
|------|---------|-------------|
| Install script | `curl -fsSL https://raw.githubusercontent.com/thinhkhuat/palate/main/install.sh \| sh` (or `sh install.sh` from a clone) | Installs the latest GitHub Release binary. No auth needed. `sh install.sh v0.4.1` pins a tag; `PALATE_INSTALL_DIR` overrides `/usr/local/bin` |
| From source | `go install github.com/thinhkhuat/palate/cmd/palate@latest` | You have Go 1.26+ |
| Agent (no prompt) | `go install github.com/thinhkhuat/palate/cmd/palate@latest && palate init && palate doctor` | CI / agent bootstrap — single shot, no TTY |

Verify:

```sh
palate --help
palate init     # creates the home (~/.claude/palate), the DB and a commented config.yaml (unset keys follow the binary defaults); doctor never creates it as a side effect
palate doctor   # expect: sqlite 3.5x, fts5 ok, vector json+cosine, 14 adapters, scanner ok, doctor: green
```

## 1. First run (all harnesses)

```sh
palate init                 # creates the Palate home: palate.db (0700/0600, WAL, FTS5), state/, backups/, config.yaml
palate prefer "use table-driven tests"   # smoke: writes a STATED preference (w=0.8 → c=0.4) and refreshes TASTE.md
palate recall "testing" --json            # smoke: recall
palate prewarm                             # smoke: <palate-prewarm scope="session">…</palate-prewarm>
cat ~/.claude/palate/TASTE.md              # the human-readable profile, kept current after every write
```

**Where everything lives — one durable home, every harness.** Palate keeps a single machine-wide store at
`~/.claude/palate/` (the same placement skill-concierge uses), shared by Claude Code, Codex, Command Code and
ZCode: `palate.db` (the events log — the source of truth), `TASTE.md` (auto-maintained render), `config.yaml`
(seeded once as a fully commented template, never overwritten — unset keys follow the binary's current defaults, so upgrades reach you), `state/` (hook snapshots, recovery block, distiller
lock), `skills/`, `skill-candidates/`, `backups/`. Override the location with `PALATE_HOME=<dir>` (or `home:` in
a repo-local `config.yaml`). `palate doctor` prints `home: … mode: global`.

**Legacy per-repo layout (0.1.x) keeps working by detection.** A directory that already carries
`./.palate/palate.db` stays in `mode: legacy-repo` — DB, state and `TASTE.md` under `./.palate/` exactly as before,
the global home untouched. To fold such a repo into the global store once, run `palate migrate` in the repo: it
moves the DB losslessly (events, trust tiers and confidences intact), parks the old `.palate/` under the home's
`backups/`, and renders `TASTE.md`. It refuses when the global store already holds events — then merge by
reimport (`palate push` in the repo, `cd ~ && palate pull <repo>/TASTE.md`), which lands rows as `IMPORTED w≤0.4`,
promotable with `palate promote`.

Config precedence: `--config` > `./config.yaml` > `<home>/config.yaml` > defaults; env overrides `PALATE_HOME`,
`PALATE_DB_PATH`, `PALATE_RENDER_AUTO=0` (turn the auto TASTE.md off), `PALATE_C_FLOOR`, `PALATE_HALF_LIFE_HOURS`,
`PALATE_MCP_TOKEN`.

## 2. Harness matrix — who learns vs who recalls

Single source of truth: [`docs/adapters.md`](docs/adapters.md). Verification gate: [`docs/adapters/verification.md`](docs/adapters/verification.md) (TASK-900 — no learning claim until hook evidence lands).

| Harness | Capture capability (honest) | Injection path | Install hint |
|---------|----------------------------|----------------|--------------|
| **Claude Code** | hook-based learning (PostToolUse / UserPromptSubmit / Stop / SessionStart + PreCompact) | push prewarm (SessionStart: the budgeted profile, the same body as `TASTE.md`; UserPromptSubmit `additionalContext`: the `<palate-learned>` block with rows that just entered the profile; both hooks post a `systemMessage` with what entered or left the profile since last time) | [§2.1](#21-claude-code--command-code--zcode--cline--hook-based-learning) |
| **Command Code** | hook-based learning (SessionStart / PostToolUse / Stop via `~/.commandcode`; Command Code has no `UserPromptSubmit` or `PreCompact` event — the per-turn surface is the mod's `transformInput`) | push prewarm (SessionStart `additionalContext`; per-turn context via the mod) | [§2.1](#21-claude-code--command-code--zcode--cline--hook-based-learning) |
| **ZCode** | hook-based learning (SessionStart / UserPromptSubmit / PostToolUse / Stop — no PreCompact event exists) | push prewarm (strict-JSON `additionalContext`) | [§2.1](#21-claude-code--command-code--zcode--cline--hook-based-learning) |
| **Cline** | hook-based learning (TaskStart / TaskResume / UserPromptSubmit / PostToolUse / TaskComplete / TaskCancel — file hooks in `~/.cline/hooks`; no Stop/PreCompact file-hook dispatch) | push prewarm (TaskStart/TaskResume + UserPromptSubmit `contextModification`) | [§2.1](#21-claude-code--command-code--zcode--cline--hook-based-learning) |
| **OMP (oh-my-pi)** | recall-only (hooks unverified) | MCP recall (`palate serve`) | [§2.2](#22-omp-oh-my-pi--hermes--recall-only-until-verified) |
| **Hermes** | recall-only (hooks unverified) | MCP recall (`palate serve`) | [§2.2](#22-omp-oh-my-pi--hermes--recall-only-until-verified) |
| **Pi** | recall-only (hooks unverified) | MCP recall | [§2.3](#23-pi--openclaw--opencode--recall-only) |
| **OpenClaw** | recall-only (hooks unverified) | MCP recall | [§2.3](#23-pi--openclaw--opencode--recall-only) |
| **OpenCode** | recall-only (hooks unverified) | MCP recall | [§2.3](#23-pi--openclaw--opencode--recall-only) |
| **Codex** | recall-only | MCP + `AGENTS.md` | [§2.4](#24-codex--cursor--windsurf--openwebui--recall-only) |
| **Cursor** | recall-only | MCP + rules | [§2.4](#24-codex--cursor--windsurf--openwebui--recall-only) |
| **Windsurf** | recall-only | MCP + rules | [§2.4](#24-codex--cursor--windsurf--openwebui--recall-only) |
| **OpenWebUI** | recall-only | `@tool` bridge | [§2.4](#24-codex--cursor--windsurf--openwebui--recall-only) |
| **Generic MCP** | recall-only | stdio / SSE | [§2.5](#25-generic-mcp-stdiosse--recall-only) |

> **No harness is oversold.** Everything beyond Claude Code / Command Code is **recall-only until `docs/adapters/verification.md` TASK-900 passes** — no `learning` claimed until live hook evidence (`doc URL + hook name + signal mapping`) is recorded.

---

### 2.1 Claude Code + Command Code + ZCode + Cline — hook-based learning

**What you get:** passive learning (`model_edit`, `user_correction`, `stated_preference`) + `explicit_*` tools + prewarm injection. (The old `user_diff` verbatim file-diff path is retired — see `docs/TASTE-format.md`.)

**Claude Code and Codex — install as a plugin (preferred):**

```sh
# Claude Code
/plugin marketplace add thinhkhuat/palate
/plugin install palate@palate

# Codex
codex plugin marketplace add thinhkhuat/palate
codex plugin add palate@palate
```

This wires the five hooks (`hooks/hooks.json`, generated from
`adapters.GenerateClaudeCodeHooks` — see `internal/adapters/plugin_manifests_test.go`),
the MCP server (`.mcp.json` / `.codex-plugin/mcp.json`), the `palate` skill,
and the `/palate:learn` / `/palate:taste` commands in one step — `bin/palate`
resolves a binary itself (`PALATE_BIN` → staged per-platform binary → `PATH`
→ a locked local `go build` fallback → one stderr line, fail-open). Verify
with `palate hook help` (warns if a workspace-level install is *also* wired —
see the migration note below) or `claude plugin list` / `codex plugin list`.

> **Plugin update ≠ binary update.** `bin/palate` prefers a `palate` already on `PATH` (`go install`, `install.sh`) over building
> from the plugin's own tree, so after `/plugin marketplace update` the hooks keep running whatever that binary was built from.
> Refresh it too — `go install github.com/thinhkhuat/palate/cmd/palate@latest` (or re-run `install.sh`) — or remove it and let the
> launcher build from the updated tree.

**Migrating from a manual `palate hook install`:** once the plugin is
installed, retire the workspace-level entries it now duplicates:

```sh
palate hook uninstall --harness claude_code
```

**Command Code / ZCode / Cline / no-plugin setups — `palate hook install`:**

Command Code has no plugin system here (out of scope — only Claude Code and
Codex have one); ZCode, Cline, and any setup that isn't using the plugin path
still wire hooks by hand:

```sh
palate hook install          # .claude/settings.local.json + .commandcode/settings.local.json + .zcode/config.json (workspace) + ~/.cline/hooks (event shims)
# restart the harness so it picks up the hooks
palate doctor                # adapter: Claude Code learning · Command Code learning · ZCode learning · Cline learning
```

> Hook handlers open the DB with the same auto-create as `palate init`: the first hook that fires creates the Palate home (0700/0600) — `~/.claude/palate/` by default, or the repo's own `.palate/` when that legacy layout already exists. Run `palate init` first if you want that moment to be explicit.

**Agent (explicit targets):**

```sh
# idempotent — rerun safe; preserves foreign hooks, replaces stale palate entries
palate hook install --harness claude_code    # .claude/settings.local.json
palate hook install --harness command_code   # .commandcode/settings.local.json
palate hook install --harness zcode          # .zcode/config.json (workspace)
palate hook install --harness zcode-global   # ~/.zcode/cli/config.json (all workspaces)
palate hook install --harness cline          # ~/.cline/hooks (one <Event>.mjs shim per event)
# verify what was written
palate hook help                             # wiring status per harness
palate doctor | grep -E "adapter: (Claude Code|Command Code|ZCode|Cline)"
```

> **Reinstall after upgrading:** `SessionStart` now runs `palate hook session-start`
> (strict-JSON prewarm + cross-session `user_diff`) instead of the old
> `palate prewarm --scope session`. Rerun `palate hook install` so the new
> command is picked up; stale palate entries are replaced, foreign hooks kept.

**ZCode specifics** (why it is not a byte-copy of the Claude Code wiring):

- Config lives in `.zcode/config.json` (workspace) or `~/.zcode/cli/config.json` (global), under `hooks.events.<Event>` — not `hooks.<Event>` — and **`hooks.enabled: true` must be set** (config-file hooks are disabled by default).
- Exactly seven events exist; there is **no `PreCompact`** (SessionStart fires on `compact` via its source, so prewarm still covers compaction).
- The hook runner parses stdout as **strict JSON** (extra keys fail validation), so `palate hook session-start` emits `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":…}}` instead of plain text.
- `PostToolUse` matcher `Edit|Write` is a case-sensitive regex that also covers the `ApplyPatch` alias.

**Cline specifics** (why it is not a byte-copy of the Claude Code wiring):

- Hook files live in `~/.cline/hooks` (plus workspace search paths) and are discovered by **basename minus extension**, matched case-insensitively against the event names, over a fixed extension allowlist. Every matching file fires and its returned `contextModification` strings are merged — palate installs **collision-free `<Event>.mjs` shims** next to foreign hook files (skill-concierge's bare-name and `.cjs` shims keep firing).
- Events map to palate's unchanged subcommands: `TaskStart`/`TaskResume` → `hook session-start` (source `startup`/`resume`), `UserPromptSubmit` → `hook user-prompt`, `PostToolUse` → `hook post-tool-use` (edit-file tools; Cline names them `write_to_file`/`replace_in_file`/`apply_diff` and may JSON-quote parameter values — the shim absorbs both), `TaskComplete`/`TaskCancel` → `hook stop` (re-baseline + detached distill). There is **no `Stop` event and no file-hook `PreCompact` dispatch** — the pre-compact gateway is in-process, so palate's recovery handler is not wired here.
- A foreign file occupying exactly `<Event>.mjs` makes the install **fail loudly** — Cline fires every match, so overwriting it would silently kill that foreign hook; the operator resolves the conflict by hand.
- The shim resolves the transcript for `taskId` from the session metadata record (`session_id` / `messages_path` in `~/.cline/data/sessions/<dir>/<dir>.json`), so the distiller reads the exact transcript of the task that just ended.
- Injection is the `contextModification` field (50 000-char cap) of the one-JSON-line stdout; everything is fail-open — an error emits `{"cancel":false}` and never blocks the session.

**Smoke:**

```sh
palate hook help   # wiring status per harness
palate prewarm --scope session | head -20
palate prewarm --scope goal --prompt "write table-driven tests" | head -20
```

Implementation: `internal/adapters/claude_code.go`, `internal/adapters/command_code.go`, `internal/adapters/zcode.go`, `internal/adapters/cline.go` behind PAT-001 `registry.go`. Hooks: `SessionStart` → `palate hook session-start` (JSON prewarm + cross-session `user_diff`), `UserPromptSubmit` → read back hand edits to `TASTE.md`, then the `user_diff` state-walk against `<home>/state/model_edits.json` (a snapshot that drifted since Stop is a hand-edit), then the notice and the `<palate-learned>` `additionalContext`, `PostToolUse Edit|Write` → `model_edit{hash_after}`, `Stop` → re-baseline every snapshot to disk (drift during the assistant's own turn is its own edit, never learned), `PreCompact` → recovery (Claude Code only — Command Code has no such event, and its `UserPromptSubmit` is served by the mod's `transformInput`, not a settings hook). Cline: `TaskStart`/`TaskResume`/`UserPromptSubmit`/`PostToolUse`/`TaskComplete`/`TaskCancel` shim the same four subcommands (`session-start` with source `startup`/`resume`, `user-prompt`, `post-tool-use`, `stop`); the distill transcript comes from the session metadata record.

---

### 2.2 OMP (oh-my-pi) + Hermes — recall-only (until verified)

**What you get today:** explicit tools + recall only. No passive `model_edit`/`user_diff` until `docs/adapters/verification.md` proves a hook surface.

**Human — MCP wire (both):**

Add an MCP server entry in the harness's MCP config (path varies by harness — typically a JSON file like `mcp.json` / `settings.json`):

```json
{ "mcpServers": { "palate": { "command": "palate", "args": ["serve"] } } }
```

Registry entries: `internal/adapters/registry.go` (OMP, Hermes — recall-only).

**Agent:**

```sh
# copy the constant into the harness's MCP config and restart
jq -s '.[0] * .[1]' /path/to/harness/mcp.json <(echo '{"mcpServers":{"palate":{"command":"palate","args":["serve"]}}}') > /tmp/mcp.json && mv /tmp/mcp.json /path/to/harness/mcp.json
palate doctor   # OMP recall-only · Hermes recall-only
```

**Smoke:**

```sh
palate serve --help
palate recall "testing" --json
palate prefer "I prefer X"   # explicit floor, always works
```

Implementation: registry data only (`internal/adapters/registry.go`); no learning claim until TASK-900 passes.

---

### 2.3 Pi / OpenClaw / OpenCode — recall-only

Same MCP pattern as §2.2. Register `palate serve` as an MCP server in the harness's MCP config.

| Harness | Config hint | Module |
|---------|------------|--------|
| Pi | `pi install npm:@<user>/pi-palate` pattern (when published) — until then MCP | `internal/adapters/registry.go` |
| OpenClaw | plugin `allowConversationAccess` — until verified, MCP only | `internal/adapters/registry.go` |
| OpenCode | plugin — until verified, MCP only | `internal/adapters/registry.go` |

```sh
palate doctor  # Pi/OpenClaw/OpenCode: recall-only (hooks unverified)
```

---

### 2.4 Codex / Cursor / Windsurf / OpenWebUI — recall-only

**Codex:** `~/.codex/config.toml` (`[mcp_servers.palate]`) + `AGENTS.md` note
**Cursor:** `.cursor/mcp.json` + rules
**Windsurf / Devin:** rules + MCP
**OpenWebUI:** `@tool` bridge

All: `palate serve` as the MCP server; `palate doctor` shows `recall-only`.

---

### 2.5 Generic MCP (stdio / SSE) — recall-only

Any MCP-capable host:

```sh
# stdio (default)
palate serve

# SSE on loopback (no token needed)
palate serve --port 127.0.0.1:4317

# SSE on a port (refuses 0.0.0.0 or bare :port without PALATE_MCP_TOKEN —
# an empty host binds all interfaces). With a token set, every request must
# carry "Authorization: Bearer <token>" or it gets 401.
PALATE_MCP_TOKEN=... palate serve --port 4317
```

Tools exposed: `palate_recall`, `palate_remember`, `palate_correct`, `palate_prefer`, `palate_reject`, `palate_accept`, `palate_forget`, `palate_stats`. `correct`/`prefer`/`reject`/`accept` refuse on any secret or injection match; `remember` refuses secrets and quarantines injections (`[PALATE: FLAGGED]`); `recall`/`forget`/`stats` take no free-text content. `palate_recall` searches the whole ledger and marks preferences that are also in the profile with `[profile]`; `palate_forget` deletes a memory but only retires a preference (restore it with the CLI `palate restore`; there is no MCP restore tool).

## 3. MCP auth (all harnesses)

```
PALATE_MCP_TOKEN=<secret>          # required for non-loopback SSE (incl. bare :port)
allowlist: 127.0.0.1, localhost, ::1, ip6-localhost
bind rule: refuse 0.0.0.0 or empty-host :port without a token
request rule: when a token is set, every SSE request needs "Authorization: Bearer <token>"
              (constant-time compare via hmac.Equal); anything else is 401
```

Keep the token out of `config.yaml` — use env only. See `docs/security.md`.

## 4. Learning knobs (same across harnesses)

| Signal | w | Meaning |
|--------|---|---------|
| `explicit_correction` | 1.0 | you said "fix this" |
| `explicit_reject` | 0.9 | |
| `stated_preference` / `explicit_prefer` | 0.8 | |
| `user_diff` (retired 2026-09-15) | 0.6* | *was hypothesis content-bearing; retired because it stored verbatim `prefers: <added lines>` dumps instead of preferences. Drifted files are now counted and cleared (`user_diff` drop counter) and the same edits are distilled from the transcript. Any `user_diff` row still in the DB is `OBSERVED`-tiered; clean leftovers with `palate consolidate --retire-verbatim`. |
| `explicit_accept` | 0.6 | weak |
| `model_edit` | 0.4 | weakest |
| `llm_proposal` | — | never deterministic until `palate promote --as` |

Update: `reinforce c + (1-c)·w·0.3` on confirmation, `weaken max(c_floor, c·(1-w·0.3))` on contradiction (negation/antonym, same-category scan; loser hidden, winner shown in a `conflicts` row), clamp `[c_floor,1.0]`, `last_reinforced_at`+`reinforcement_count` bump. New pref: `max(c_floor, w·0.5)`; `llm_distilled` seeds at the model's confidence clamped to `[c_floor, 0.9]`. Time decay is off by default (`half_life_hours: 0`; set e.g. `168` to halve an untouched row weekly — the old default made a re-confirmation after three weeks *lower* a 0.80 row to 0.26). Recall replays events pinned to `--as-of` (RFC3339); `--include-conflicted` shows hidden losers. Freshness tiers (`freshness.enabled`, T1/T2/T3 weights) multiply the decayed score when enabled.

## 5. LLM gateway — the taste brain

With a gateway configured, the model on **your own infrastructure** does what
Command Code's own model does for its taste file — it is the primary judge,
not an add-on. Three roles, all behind the same `safety.Scan`/`Censor` gate:

1. **Judge on every explicit write** — `palate prefer/correct/reject/accept`,
   the six MCP tools, `palate pull`, `palate promote`. The model rewrites the
   candidate into one durable third-person statement (your language is kept),
   picks the category, assigns the confidence from the wording (an explicit
   "always/never" ≈ 0.8, a hedged remark ≈ 0.4; capped at 0.95 so a single
   verdict never looks certain), and decides its relation to the rows already
   in that category: `new`, `same` (reinforces it), `refines` (rewrites it in
   place, the old text kept in a `refine` event), or `contradicts` (weakens it,
   opens a conflict, stores the new side). `palate prefer` prints the verdict:
   `prefer recorded (Testing, refines, c=0.85, llm)`. `judge.enabled: false` or
   `PALATE_JUDGE=0` switches back to the heuristics.
2. **Distiller over transcripts, journals and git** (§5.1).
3. **Dream pass over the profile and its candidates** — `palate consolidate`
   (alias `palate dream`; also run by `palate sleep`, and started by
   `SessionStart` once `consolidate.every_events` new events have landed,
   default 25): the model sees the whole profile plus the candidates (rows outside
   it that are new or changed since the last applied pass) in batches of
   40, is told the token budget and the profile's current size, and
   returns `merge`, `rewrite`, `rescore`, `retire` and `project` actions.
   `distill.mode: auto` applies them after a DB backup; `review` files each as
   a proposal for `palate promote`. Guards: a row reinforced in the last 24 h
   *and* seen in two sessions is never retired; a pass that would touch more
   than 40 % of the ledger is downgraded to proposals for that batch; a
   `STATED` row's text is never rewritten, and a `retire`, `project`,
   confidence-lowering `rescore`, or a `merge` that would lower a `STATED`
   row's confidence or re-file it, is always a proposal, never applied by the
   pass (checked again against the row at the moment of writing). An action on
   a row an earlier batch already closed is skipped and writes no event. A failed backup stops the pass before any write. After
   the batches, a step with no model call forgets candidates seen in only one
   session and older than `consolidate.stale_candidate_days` (default 30; 0
   turns it off); it re-reads each row just before closing it, so it never
   retires a row the pass itself merged or the owner just made `STATED`.
   `palate restore <id>` on a row it forgot promotes the row to `STATED`, so
   the next pass leaves it alone. Every
   attempt, including a failed or skipped one, is a line in
   `state/consolidate.log`, and the next notice reports a failure once. The
   line counts every action the model returned in exactly one bucket
   (`applied`, `proposed` or `skipped`, so they add up to `actions`;
   `skip_reasons` says why each skipped one was) and lists each batch's gateway
   wait in `batch_ms`, beside the whole attempt's `duration_ms`.

**No gateway = the heuristics, exactly as 0.2.x behaved**: keyword category,
weight×0.5 confidence, exact/negation/near-duplicate matching, regex stated
preferences and raw diffs. A gateway that errors mid-call falls back to the
same heuristics for that write, prints one `judge fell back to heuristics`
line and bumps the `judge_fallback` counter (`palate stats`). `palate doctor`
prints `judge: llm (<model>)` or `judge: heuristic (…)` and how many events
have landed since the last dream pass.

### 5.1 Distiller (transcripts, journals, git)

The distiller sends transcript/git evidence to the gateway and stores what
comes back as natural-language preferences at `trust_tier=DISTILLED` — same
decay/reinforce math, same `preferences` table, same `TASTE.md` render.

```sh
export PALATE_LLM_TIMEOUT=120                    # optional — per-request seconds for the distiller and the judge, default 30; on a streamed reply it limits connecting and the longest silence with no bytes received, and one call is capped at 4x it (the dream pass has its own consolidate.timeout_seconds)
export PALATE_LLM_ENDPOINT=https://your-gateway/v1/chat/completions
export PALATE_LLM_MODEL=your-model-id
export PALATE_LLM_API_KEY=...            # optional — some gateways need none
```

`PALATE_LLM_ENDPOINT` + `PALATE_LLM_MODEL` must both be set for the
gateway roles to activate; a plaintext `http://` endpoint to a non-loopback host
prints one warning (not a refusal) since it's your own infrastructure. See
[`docs/security.md`](docs/security.md) for exactly what is sent and what is
stripped before it leaves the machine.

**Live learning (no command to run):** once `PALATE_LLM_*` is set, the
`Stop` and `SessionStart` hooks (§2.1) spawn `palate learn session` as a
detached, fire-and-forget child — the hook itself never waits on the LLM
call, so a slow or dead gateway never adds hook latency. `SessionStart`
spawns exactly one child: it learns from the just-closed transcript and then,
if a dream pass is due, runs it in the same process under the same lock
(`learn session --then-consolidate`); `Stop` only learns. Each distilled
statement is judged against the profile plus the best full-text matches
outside it (same / refines / contradicts / new), exactly like an explicit
write, so a paraphrase reinforces the existing row instead of adding a
duplicate. A statement whose only evidence is text the assistant wrote is
dropped. A new row is a candidate: it enters the profile when you state it
explicitly, when it is seen in a second session, or when you add it to
`TASTE.md` yourself (§5.2). What changed is announced at your next prompt
(`palate: since your last prompt — profile +1 −0; ledger: learned 1, …`), and
the rows that entered the profile are handed to the model as a
`<palate-learned>` block.

Turn the automatic learner off with `distill.mode: off` (or
`PALATE_DISTILL_MODE=off`), or only for some directories with
`distill.skip_dirs: ["~/ref", "~/in-PROD/CLONED"]` — a session whose cwd is
under one of them is never mined (reference clones, other people's repos).
Explicit `palate learn …` commands ignore both. `palate doctor` prints
`learning: auto|review|off (+N skipped dirs)`.

**On-demand learning (explicit commands):**

```sh
palate learn session <path> [--harness H] [--verbose]     # one transcript
palate learn sessions [--harness H] [--project P] [--all] [--include-experimental]
palate learn journals [--include-global]                   # memsearch only
palate learn repo [path|owner/repo|url] [--max-commits N] [--max-signals N] [--all-authors]
```

`sessions` runs discovery + windowing across every registered reader (Claude
Code, Command Code, Pi, Codex, OpenCode, Gemini CLI, memsearch — capped to
the 20 most-recently-modified transcripts per harness unless `--all`).
**ZCode transcript reading is experimental and off by default** — it only
runs under `--include-experimental` (distinct from ZCode's hook-based
*live* learning in §2.1, which is unaffected). `journals` scans
project-local `.memsearch/memory/*.md` only, unless `--include-global` opts
into the user's global journal directory (printed once, never silent).
`repo` walks git history (default: current author's own commits only —
`--all-authors` opts out) and is capped by both `--max-commits` and
`--max-signals`. A GitHub `owner/repo` or a clone URL is cloned into a temp
dir (full history, removed afterwards; private repos authenticate through
git's own credential helper), learned from every author (a fresh clone
cannot tell which commits are yours), and its cursor is kept under the URL,
so a second run only sees new commits.

Re-running the same command is a no-op once a transcript/commit range has
already been distilled — a per-source cursor tracks how far each was
analyzed, so nothing is re-sent to the gateway or re-learned twice.

`distill.mode` in [`config.yaml`](config.yaml) controls whether a distilled
statement — and every consolidation action — lands automatically (`auto`, the
default), as a proposal awaiting `palate promote` (`review`), the human-gate
option if you'd rather approve each one, or not at all from the hooks
(`off`). Cost: one gateway call per explicit
write; per distilled transcript batch, one extraction call plus one judge
call for each statement it returns (the judge sees the profile plus at most
20 other matching rows, so its prompt does not grow with the ledger); one
gateway call per batch of 40 candidates in a dream pass (the profile is
repeated in each).

### 5.2 The profile, hand edits and the dream pass

`TASTE.md` and the `SessionStart` block carry the same thing: the **profile**,
a budgeted subset of your live preferences. A row is in it when it is `STATED`
(you said it, or you added it to `TASTE.md`) or has been seen in two sessions,
and it is not project-specific, not a restatement of your rulebook and not the
losing side of a conflict. Every other live row stays in the ledger as a
candidate. The budget is `token_budgets.prewarm_session` (default 2000
estimated tokens, header included); rows are ranked, and what does not fit is
left out whole. Why: [`docs/decisions.md`](docs/decisions.md) rows 35-37.

```sh
palate taste                # read back hand edits, re-render, print the profile
palate taste --cut          # id and reason for each live row left out of it
palate restore <id>         # bring back a forgotten row, or promote a candidate to STATED
palate forget <id>          # soft: the row is retired and can be restored; --purge deletes it for good
palate consolidate          # run the dream pass now (alias: palate dream)
palate consolidate --rebuild   # one supervised pass over all live rows, no 40 % guard
```

Every row that enters or leaves the profile is an event with a reason, and
the next notice lists them. **Hand edits:** you can edit `TASTE.md` directly.
An added or reworded line becomes a `STATED` preference; a deleted line is
forgotten softly. An edit Palate cannot apply safely (a large addition, a mass
deletion, a file the assistant wrote) is held until you run
`palate taste --accept-edits`, or discarded by plain `palate taste`. A held
file with no profile to compare against (an older palate's full dump, a lost
base) can only be discarded; a discarded edit is kept under `state/`.
Accepting any held edit lists every retired or reworded line it would bring
back, and any added lines beyond 10, and needs `palate taste --accept-edits
--yes` for those; `palate taste --cut` and `--path` leave a hold alone. A `TASTE.md` you saved
again after the hold is copied to `state/TASTE.after-hold-*.md` before the hold
is accepted or discarded. Details:
[`docs/TASTE-format.md`](docs/TASTE-format.md).

| `config.yaml` key | Default | Effect |
|---|---|---|
| `token_budgets.prewarm_session` | 2000 | Size of the profile in estimated tokens, headers included. Raise it to fit more rows |
| `token_budgets.prewarm_goal` | 0 | `0` keeps the per-prompt goal block off; any positive value turns it on |
| `profile.exclude_project_specific` | true | Keep rows tagged as true only for one project out of the profile |
| `profile.read_back_edits` | true | Read hand edits to `TASTE.md` back into the ledger |
| `profile.prompt_block` | true | The per-prompt `<palate-learned>` block holds only rows that entered the profile; `false` also lists every row created or reworded |
| `consolidate.every_events` | 25 | Events between automatic dream passes (`0` = never; `PALATE_CONSOLIDATE_EVERY`) |
| `consolidate.timeout_seconds` | 300 | Gateway timeout for the dream pass: connect, then the longest silence with no bytes received; a call is capped at 4x (distiller and judge calls use `PALATE_LLM_TIMEOUT`) |
| `consolidate.stale_candidate_days` | 30 | A candidate seen in only one session and older than this is forgotten by the dream pass (`0` = off) |

## 6. After install — smoke checklist (run for every harness)

```sh
palate init                           # no-op if the home already exists
palate doctor                         # green
palate store "hello" && palate recall "hello" --json
palate prefer "chat test" && palate recall "chat" --json
palate prewarm | head -5              # <palate-prewarm …>
palate taste --cut                  # why each live row is not in the profile (says so when all fit)
palate sleep && palate stats
palate verify capture --pure          # caveats: n=7, hash, --pure
cat "$(palate doctor | sed -n 's/^taste: \([^ ]*\).*/\1/p')"   # TASTE.md is already current — no push needed
palate lint                            # lints the auto-maintained file → lint: ok
```

## 7. Uninstall

```sh
# hook-based harnesses (removes only palate entries; foreign hooks preserved)
palate hook uninstall                    # claude_code + command_code + zcode (workspace) + cline (~/.cline/hooks shims)
palate hook uninstall --harness zcode-global
# recall-only harnesses: remove the {"palate": {"command":"palate",...}} entry from the harness MCP config
rm -rf ~/.claude/palate               # the global home (DB, TASTE.md, state, backups) — events log is gone with the DB
rm -rf .palate TASTE.md               # per-repo leftovers from the legacy layout, if any
```

## 8. What is not wired (so you don't chase it)

- OMP, Hermes, Pi, OpenClaw, OpenCode are **recall-only until `docs/adapters/verification.md` proves hooks** — no hidden learning.
- Vectors are FTS5 + JSON cosine by default (`sqlite-vec` behind a build tag — zero-CGO default stays CGO-free).
- ZCode **transcript** reading (`palate learn sessions --include-experimental`,
  §5) is experimental and opt-in — it is off by default and unrelated to
  ZCode's hook-based *live* learning in §2.1, which is unaffected.

Further reading: [`docs/adapters.md`](docs/adapters.md) · [`docs/adapters/verification.md`](docs/adapters/verification.md) · [`docs/architecture.md`](docs/architecture.md) · [`docs/security.md`](docs/security.md)
