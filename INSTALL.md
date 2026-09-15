# Install — Palate (every harness)

Palate is a **single Go binary, no CGO, SQLite-only**. Two install paths — pick one for humans, one for agents. Both end at `palate init && palate doctor → green` before you wire any harness.

## 0. Get the binary

| Path | Command | When to use |
|------|---------|-------------|
| Install script | `gh api repos/thinhkhuat/palate/contents/install.sh -H 'Accept: application/vnd.github.raw' \| sh` (or `sh install.sh` from a clone) | Installs the latest GitHub Release binary. Needs an authenticated `gh` or `GITHUB_TOKEN`/`GH_TOKEN` (private repo). `sh install.sh v0.4.1` pins a tag; `PALATE_INSTALL_DIR` overrides `/usr/local/bin` |
| From source | `GOPRIVATE=github.com/thinhkhuat go install github.com/thinhkhuat/palate/cmd/palate@latest` | You have Go 1.26+ and git access to the private repo |
| Agent (no prompt) | `GOPRIVATE=github.com/thinhkhuat go install github.com/thinhkhuat/palate/cmd/palate@latest && palate init && palate doctor` | CI / agent bootstrap — single shot, no TTY |

Verify:

```sh
palate --help
palate init     # creates the home (~/.claude/palate), the DB and a commented config.yaml (unset keys follow the binary defaults); doctor never creates it as a side effect
palate doctor   # expect: sqlite 3.5x, fts5 ok, vector json+cosine, 13 adapters, scanner ok, doctor: green
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
| **Claude Code** | hook-based learning (PostToolUse / UserPromptSubmit / Stop / SessionStart + PreCompact) | push prewarm (SessionStart: the whole profile, 2000-tok budget dropping whole lines only + UserPromptSubmit `additionalContext` ~200 tok; SessionStart also posts a `systemMessage` with what was learned since last time) | [§2.1](#21-claude-code--command-code--zcode--hook-based-learning) |
| **Command Code** | hook-based learning (mirrors Claude Code via `~/.commandcode`) | same as Claude Code | [§2.1](#21-claude-code--command-code--zcode--hook-based-learning) |
| **ZCode** | hook-based learning (SessionStart / UserPromptSubmit / PostToolUse / Stop — no PreCompact event exists) | push prewarm (strict-JSON `additionalContext`) | [§2.1](#21-claude-code--command-code--zcode--hook-based-learning) |
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

### 2.1 Claude Code + Command Code + ZCode — hook-based learning

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

**Command Code / ZCode / no-plugin setups — `palate hook install`:**

Command Code has no plugin system here (out of scope — only Claude Code and
Codex have one); ZCode and any setup that isn't using the plugin path still
wire hooks by hand:

```sh
palate hook install          # .claude/settings.local.json + .commandcode/settings.local.json + .zcode/config.json (workspace)
# restart the harness so it picks up the hooks
palate doctor                # adapter: Claude Code learning · Command Code learning · ZCode learning
```

> Hook handlers open the DB with the same auto-create as `palate init`: the first hook that fires creates the Palate home (0700/0600) — `~/.claude/palate/` by default, or the repo's own `.palate/` when that legacy layout already exists. Run `palate init` first if you want that moment to be explicit.

**Agent (explicit targets):**

```sh
# idempotent — rerun safe; preserves foreign hooks, replaces stale palate entries
palate hook install --harness claude_code    # .claude/settings.local.json
palate hook install --harness command_code   # .commandcode/settings.local.json
palate hook install --harness zcode          # .zcode/config.json (workspace)
palate hook install --harness zcode-global   # ~/.zcode/cli/config.json (all workspaces)
# verify what was written
palate hook help                             # wiring status per harness
palate doctor | grep -E "adapter: (Claude Code|Command Code|ZCode)"
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

**Smoke:**

```sh
palate hook help   # wiring status per harness
palate prewarm --scope session | head -20
palate prewarm --scope goal --prompt "write table-driven tests" | head -20
```

Implementation: `internal/adapters/claude_code.go`, `internal/adapters/command_code.go`, `internal/adapters/zcode.go` behind PAT-001 `registry.go`. Hooks: `SessionStart` → `palate hook session-start` (JSON prewarm + cross-session `user_diff`), `UserPromptSubmit` → `user_diff` state-walk against `<home>/state/model_edits.json` (a snapshot that drifted since Stop is a hand-edit) + lever + `additionalContext`, `PostToolUse Edit|Write` → `model_edit{hash_after}`, `Stop` → re-baseline every snapshot to disk (drift during the assistant's own turn is its own edit, never learned), `PreCompact` → recovery (Claude Code / Command Code only).

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

Tools exposed: `palate_recall`, `palate_remember`, `palate_correct`, `palate_prefer`, `palate_reject`, `palate_accept`, `palate_forget`, `palate_stats`. `correct`/`prefer`/`reject`/`accept` refuse on any secret or injection match; `remember` refuses secrets and quarantines injections (`[PALATE: FLAGGED]`); `recall`/`forget`/`stats` take no free-text content.

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
2. **Distiller over transcripts, journals and git** (below) — unchanged.
3. **Consolidator over the whole profile** — `palate consolidate` (also run by
   `palate sleep`, and auto-fired detached by `SessionStart` once
   `consolidate.every_events` new events have landed, default 25): the model
   reads every active preference and returns the smallest set of `merge`,
   `rewrite`, `rescore`, `retire` actions. `distill.mode: auto` applies them
   after a DB backup; `review` files each as a proposal for `palate promote`.
   Guards: a row reinforced in the last 24 h is never retired; a pass touching
   more than 40 % of the rows is downgraded to proposals whatever the mode.

**No gateway = the heuristics, exactly as 0.2.x behaved**: keyword category,
weight×0.5 confidence, exact/negation/near-duplicate matching, regex stated
preferences and raw diffs. A gateway that errors mid-call falls back to the
same heuristics for that write, prints one `judge fell back to heuristics`
line and bumps the `judge_fallback` counter (`palate stats`). `palate doctor`
prints `judge: llm (<model>)` or `judge: heuristic (…)` and the consolidation
watermark.

### 5.1 Distiller (transcripts, journals, git)

The distiller sends transcript/git evidence to the gateway and stores what
comes back as natural-language preferences at `trust_tier=DISTILLED` — same
decay/reinforce math, same `preferences` table, same `TASTE.md` render.

```sh
export PALATE_LLM_TIMEOUT=120                    # optional — per-request seconds, default 30; raise it for whole-profile consolidate passes
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
detached, fire-and-forget child on every turn — the hook itself never waits
on the LLM call, so a slow or dead gateway never adds hook latency. Each
distilled statement is then judged against the whole profile before it is
stored (same / refines / contradicts / new), exactly like an explicit write,
so a paraphrase reinforces the existing row instead of adding a duplicate.
What landed is announced at your next prompt (`palate: since your last prompt
— learned 1, …`) and handed to the model as a `<palate-learned>` block.

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
call for each statement it returns (the judge sees the whole profile, which
is a few dozen lines for a personal taste store); one per consolidation pass.

## 6. After install — smoke checklist (run for every harness)

```sh
palate init                           # no-op if the home already exists
palate doctor                         # green
palate store "hello" && palate recall "hello" --json
palate prefer "chat test" && palate recall "chat" --json
palate prewarm | head -5              # <palate-prewarm …>
palate sleep && palate stats
palate verify capture --pure          # caveats: n=7, hash, --pure
cat "$(palate doctor | sed -n 's/^taste: \([^ ]*\).*/\1/p')"   # TASTE.md is already current — no push needed
palate lint                            # lints the auto-maintained file → lint: ok
```

## 7. Uninstall

```sh
# hook-based harnesses (removes only palate entries; foreign hooks preserved)
palate hook uninstall                    # claude_code + command_code + zcode (workspace)
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
