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
palate doctor   # expect: sqlite 3.5x, fts5 ok, vector json+cosine, 12 adapters, scanner ok, doctor: green
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
| **Claude Code** | hook-based learning (PostToolUse / UserPromptSubmit / Stop / SessionStart + PreCompact) | push prewarm (SessionStart ~500 tok + UserPromptSubmit `additionalContext` ~200 tok) | [§2.1](#21-claude-code--command-code--hook-based-learning) |
| **Command Code** | hook-based learning (mirrors Claude Code via `~/.commandcode`) | same as Claude Code | [§2.1](#21-claude-code--command-code--hook-based-learning) |
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

### 2.1 Claude Code + Command Code — hook-based learning

**What you get:** passive learning (`model_edit`, `user_correction`, `user_diff w=0.6`, `stated_preference`) + `explicit_*` tools + prewarm injection.

**Human:**

```sh
palate hook install          # writes .claude/settings.local.json (Claude Code) + .commandcode/settings.local.json (Command Code)
# restart the harness so it picks up the hooks
palate doctor                # adapter: Claude Code learning · Command Code learning
```

**Agent (YAML shape — adapt to your harness's settings file):**

```sh
# idempotent — rerun safe
palate hook install --harness claude_code   # .claude/settings.local.json
palate hook install --harness command_code  # .commandcode/settings.local.json
# verify what was written
palate doctor | grep -E "adapter: (Claude Code|Command Code)"
```

**Smoke:**

```sh
palate hook help   # lists installed hooks
palate prewarm --scope session | head -20
```

Implementation: `internal/adapters/claude_code.go` + `internal/adapters/command_code.go` behind PAT-001 `registry.go`. Hooks: `SessionStart` → `palate prewarm`, `UserPromptSubmit` → lever + `additionalContext`, `PostToolUse Edit|Write` → `model_edit{hash_after}`, `Stop` → filtered `user_diff` (a/b/c), `PreCompact` → recovery.

---

### 2.2 OMP (oh-my-pi) + Hermes — recall-only (until verified)

**What you get today:** explicit tools + recall only. No passive `model_edit`/`user_diff` until `docs/adapters/verification.md` proves a hook surface.

**Human — MCP wire (both):**

Add an MCP server entry in the harness's MCP config (path varies by harness — typically a JSON file like `mcp.json` / `settings.json`):

```json
{ "mcpServers": { "palate": { "command": "palate", "args": ["serve"] } } }
```

Constants: `internal/adapters/omp.go:OMPMCPConfig`, `internal/adapters/hermes.go:HermesMCPConfig` (same shape).

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

Implementation: `internal/adapters/omp.go`, `internal/adapters/hermes.go` — hints only; no learning claim until TASK-900 passes.

---

### 2.3 Pi / OpenClaw / OpenCode — recall-only

Same MCP pattern as §2.2. Register `palate serve` as an MCP server in the harness's MCP config.

| Harness | Config hint | Module |
|---------|------------|--------|
| Pi | `pi install npm:@<user>/pi-palate` pattern (when published) — until then MCP | `internal/adapters/pi.go` |
| OpenClaw | plugin `allowConversationAccess` — until verified, MCP only | `internal/adapters/openclaw.go` |
| OpenCode | plugin — until verified, MCP only | `internal/adapters/opencode.go` |

```sh
palate doctor  # Pi/OpenClaw/OpenCode: recall-only (hooks unverified)
```

---

### 2.4 Codex / Cursor / Windsurf / OpenWebUI — recall-only

**Codex:** `~/.codex/config.toml` (`[mcp_servers.palate]`) + `AGENTS.md` note → `internal/adapters/codex.go`
**Cursor:** `.cursor/mcp.json` + rules → `internal/adapters/cursor.go`
**Windsurf / Devin:** rules + MCP → `internal/adapters/windsurf.go`
**OpenWebUI:** `@tool` bridge → `internal/adapters/openwebui.go`

All: `palate serve` as the MCP server; `palate doctor` shows `recall-only`.

---

### 2.5 Generic MCP (stdio / SSE) — recall-only

Any MCP-capable host:

```sh
# stdio (default)
palate serve

# SSE on a port (refuses 0.0.0.0 without PALATE_MCP_TOKEN)
PALATE_MCP_TOKEN=... palate serve --port 4317
```

Tools exposed: `palate_recall`, `palate_remember`, `palate_correct`, `palate_prefer`, `palate_reject`, `palate_accept`, `palate_forget`, `palate_stats` — all with secret/injection gates.

## 3. MCP auth (all harnesses)

```
PALATE_MCP_TOKEN=<secret>          # required for non-loopback SSE
allowlist: 127.0.0.1, localhost, ::1, ip6-localhost
rule: hmac.compare_digest; refuse to bind 0.0.0.0 without token
```

Keep the token out of `config.yaml` — use env only. See `docs/security.md`.

## 4. Learning knobs (same across harnesses)

| Signal | w | Meaning |
|--------|---|---------|
| `explicit_correction` | 1.0 | you said "fix this" |
| `explicit_reject` | 0.9 | |
| `stated_preference` / `explicit_prefer` | 0.8 | |
| `user_diff` | 0.6* | *hypothesis — promote to 0.9 only if `verify capture` precision ≥0.7 |
| `explicit_accept` | 0.6 | weak |
| `model_edit` | 0.4 | weakest |
| `llm_proposal` | — | never deterministic until `palate promote --as` |

Update: `c ← decay(c, c_floor, half_life=168h, Δt)`, `reinforce c + (1-c)·w·0.3`, `weaken max(c_floor, c·(1-w·0.3))`, clamp `[c_floor,1.0]`, `last_reinforced_at`+`reinforcement_count` bump. New pref: `max(c_floor, w·0.5)`.

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
# hook-based harnesses
palate hook uninstall                  # removes Palate entries from settings.local.json
# recall-only harnesses: remove the {"palate": {"command":"palate",...}} entry from the harness MCP config
rm -rf .palate TASTE.md               # local data only; events log is gone with the DB
```

## 7. What is not wired (so you don't chase it)

- OMP, Hermes, Pi, OpenClaw, OpenCode are **recall-only until `docs/adapters/verification.md` proves hooks** — no hidden learning.
- Vectors are FTS5 + JSON cosine by default (`sqlite-vec` behind a build tag — zero-CGO default stays CGO-free).
- Sync E2EE: events are encrypted, but the sync server sees metadata only — see `docs/sync.md` / `docs/security.md`.

Further reading: [`docs/adapters.md`](docs/adapters.md) · [`docs/adapters/verification.md`](docs/adapters/verification.md) · [`docs/architecture.md`](docs/architecture.md) · [`docs/security.md`](docs/security.md)
