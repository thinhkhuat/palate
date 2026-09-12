# Install — Palate (every harness)

Palate is a **single Go binary, no CGO, SQLite-only**. Two install paths — pick one for humans, one for agents. Both end at `palate doctor → green` before you wire any harness.

## 0. Get the binary

| Path | Command | When to use |
|------|---------|-------------|
| Homebrew | `brew install thinhkhuat/palate/palate` | You have Homebrew + want upgrades via `brew upgrade` |
| Head (pre-release) | `brew install --HEAD thinhkhuat/palate/palate` | You're on `main` before `v0.1.0` is published (real release SHA256s are filled by GoReleaser on publish) |
| Install script | `curl -fsSL https://raw.githubusercontent.com/thinhkhuat/palate/main/install.sh \| sh` | Any Unix with `curl` |
| From source | `go install github.com/thinhkhuat/palate/cmd/palate@latest` | You have Go 1.26+ |
| Agent (no prompt) | `go install github.com/thinhkhuat/palate/cmd/palate@latest && palate doctor` | CI / agent bootstrap — single shot, no TTY |

Verify:

```sh
palate --help
palate doctor   # expect: sqlite 3.5x, fts5 ok, vector json+cosine, 13 adapters, scanner ok, doctor: green
```

> The `stable` block in `homebrew/palate.rb` is a placeholder until `v0.1.0` is actually published. Don't plain `brew install` from that blob — use `--HEAD` or `go install` until the release lands (GoReleaser fills real sha256 on publish).

## 1. First run (all harnesses)

```sh
palate init                 # creates .palate/palate.db (0700/0600) + DDL (WAL, FTS5)
palate prefer "use table-driven tests"   # smoke: writes a STATED preference (w=0.8 → c=0.4)
palate recall "testing" --json            # smoke: recall
palate prewarm                             # smoke: <palate-prewarm scope="session">…</palate-prewarm>
```

Config is `config.yaml` in the repo root (weights, half-life 168h, token budgets, trust tiers). Env overrides: `PALATE_DB_PATH`, `PALATE_C_FLOOR`, `PALATE_HALF_LIFE_HOURS`, `PALATE_MCP_TOKEN`.

## 2. Harness matrix — who learns vs who recalls

Single source of truth: [`docs/adapters.md`](docs/adapters.md). Verification gate: [`docs/adapters/verification.md`](docs/adapters/verification.md) (TASK-900 — no learning claim until hook evidence lands).

| Harness | Capture capability (honest) | Injection path | Install hint |
|---------|----------------------------|----------------|--------------|
| **Claude Code** | hook-based learning (PostToolUse / UserPromptSubmit / Stop / SessionStart + PreCompact) | push prewarm (SessionStart ~500 tok + UserPromptSubmit `additionalContext` ~200 tok) | [§2.1](#21-claude-code--command-code--zcode--hook-based-learning) |
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

**What you get:** passive learning (`model_edit`, `user_correction`, `user_diff w=0.6`, `stated_preference`) + `explicit_*` tools + prewarm injection.

**Human:**

```sh
palate hook install          # .claude/settings.local.json + .commandcode/settings.local.json + .zcode/config.json (workspace)
# restart the harness so it picks up the hooks
palate doctor                # adapter: Claude Code learning · Command Code learning · ZCode learning
```

> Hook handlers open the DB with the same auto-create as `palate init`: the first hook that fires in a workspace without `.palate/` creates it (0700/0600). Run `palate init` first if you want that moment to be explicit.

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

Implementation: `internal/adapters/claude_code.go`, `internal/adapters/command_code.go`, `internal/adapters/zcode.go` behind PAT-001 `registry.go`. Hooks: `SessionStart` → `palate hook session-start` (JSON prewarm + cross-session `user_diff`), `UserPromptSubmit` → lever + `additionalContext`, `PostToolUse Edit|Write` → `model_edit{hash_after}`, `Stop` → filtered `user_diff` (state-walk against `.palate/model_edits.json`), `PreCompact` → recovery (Claude Code / Command Code only).

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
| `user_diff` | 0.6* | *hypothesis — content-bearing: Stop/SessionStart snapshot the model-edited file (4KB bound) and learn the before→after diff via `ExtractCandidates`; promote to 0.9 only if `verify capture` precision ≥0.7 |
| `explicit_accept` | 0.6 | weak |
| `model_edit` | 0.4 | weakest |
| `llm_proposal` | — | never deterministic until `palate promote --as` |

Update: `c ← decay(c, c_floor, half_life=168h, Δt)`, `reinforce c + (1-c)·w·0.3`, `weaken max(c_floor, c·(1-w·0.3))` on contradiction (negation/antonym, same-category scan; loser hidden, winner shown in a `conflicts` row), clamp `[c_floor,1.0]`, `last_reinforced_at`+`reinforcement_count` bump. New pref: `max(c_floor, w·0.5)`. Recall replays decay pinned to `--as-of` (RFC3339); `--include-conflicted` shows hidden losers. Freshness tiers (`freshness.enabled`, T1/T2/T3 weights) multiply the decayed score when enabled.

## 5. After install — smoke checklist (run for every harness)

```sh
palate doctor                         # green
palate store "hello" && palate recall "hello" --json
palate prefer "chat test" && palate recall "chat" --json
palate prewarm | head -5              # <palate-prewarm …>
palate sleep && palate stats
palate verify capture --pure          # caveats: n=6, hash, --pure
TASTE.md                               # after `palate push` → lint ok
```

## 6. Uninstall

```sh
# hook-based harnesses (removes only palate entries; foreign hooks preserved)
palate hook uninstall                    # claude_code + command_code + zcode (workspace)
palate hook uninstall --harness zcode-global
# recall-only harnesses: remove the {"palate": {"command":"palate",...}} entry from the harness MCP config
rm -rf .palate TASTE.md               # local data only; events log is gone with the DB
```

## 7. What is not wired (so you don't chase it)

- OMP, Hermes, Pi, OpenClaw, OpenCode are **recall-only until `docs/adapters/verification.md` proves hooks** — no hidden learning.
- Vectors are FTS5 + JSON cosine by default (`sqlite-vec` behind a build tag — zero-CGO default stays CGO-free).

Further reading: [`docs/adapters.md`](docs/adapters.md) · [`docs/adapters/verification.md`](docs/adapters/verification.md) · [`docs/architecture.md`](docs/architecture.md) · [`docs/security.md`](docs/security.md)
