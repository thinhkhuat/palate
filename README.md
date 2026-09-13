# Palate — Open Taste Engine

> **Your coding taste, learned — not configured.** Palate watches how you edit, correct, and state preferences, then injects that taste back into your AI harness so you don't have to repeat yourself. Open, local-first, harness-agnostic.

[![Status](https://img.shields.io/badge/status-v0.1.0-blue)](#)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8)](#)
[![No CGO](https://img.shields.io/badge/CGO-free-brightgreen)](#)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow)](#)

---

## Why Palate

Harness-native "taste" (like Command Code's closed one) works because the harness *is* the learner — it sees every accept/reject. A portable taste engine can't. **Palate's trick is to learn only from what any harness can actually observe** — your edits, your diffs, your explicit `prefer`/`correct` — score each preference with a confidence that decays and reinforces over time, and *push* it back via a prewarm block so the agent doesn't have to remember to ask.

- **Single Go binary. No CGO. SQLite-only.** `modernc.org/sqlite` + FTS5 + JSON+cosine (and `sqlite-vec` behind a build tag when you want it).
- **Observable signals only:** `model_edit`, `user_correction`, `user_diff (w=0.6 hypothesis)`, `stated_preference`, `explicit_prefer/reject/accept`, `llm_distilled` (optional) — no invented `accept/reject` primitive.
- **Push prewarm:** SessionStart ~500 tok once per session + UserPromptSubmit ~200 tok only when your *intent* changes. Honest budgets, disclosed.
- **TASTE.md is a render, not the truth.** The `events` log in `.palate/*.db` is the source of truth; `TASTE.md` is a reviewable, git-shareable artifact.

---

## Install as a plugin

Claude Code and Codex both install Palate as a single plugin — manifests,
hooks, MCP server, the `palate` skill, and `/palate:learn` / `/palate:taste`
all come from this repo, no manual wiring:

```sh
# Claude Code
/plugin marketplace add thinhkhuat/palate
/plugin install palate@palate

# Codex
codex plugin marketplace add thinhkhuat/palate
codex plugin add palate@palate
```

The plugin's `bin/palate` launcher resolves a binary itself (an existing
install on `PATH`, or a local `go build` fallback) — you don't need the
binary preinstalled first. Command Code, ZCode, and no-plugin setups still
use `palate hook install` — see [INSTALL.md](INSTALL.md) §2.1.

## 30-second start (no plugin — manual wiring)

```sh
brew install thinhkhuat/palate/palate   # or: go install github.com/thinhkhuat/palate/cmd/palate@latest
palate init
palate doctor                          # → green? (sqlite, fts5, vector, 13 adapters, scanner)
palate hook install                    # Claude Code + Command Code + ZCode — one command

palate prefer "use table-driven tests"
palate recall "testing" --json
palate prewarm
palate verify capture --pure
```

Detailed install for every harness: **[INSTALL.md](INSTALL.md)**

### LLM distiller (optional)

A second, opt-in learning path: point `PALATE_LLM_ENDPOINT` /
`PALATE_LLM_MODEL` / `PALATE_LLM_API_KEY` at your own OpenAI-compatible
gateway and `palate learn session/sessions/repo/journals` (or the `Stop` /
`SessionStart` hooks, once those env vars are set) turn transcripts, git
history, and memsearch journals into `llm_distilled` preferences —
`trust_tier=DISTILLED`, same confidence/decay math as everything else. **Off
by default** — no env set, no distiller, today's regex/verbatim paths keep
working unchanged. Details: [INSTALL.md §5](INSTALL.md#5-llm-taste-distiller-optional).

---

## Harnesses — honest support

**Source of truth:** [`docs/adapters.md`](docs/adapters.md) · **Verification gate:** [`docs/adapters/verification.md`](docs/adapters/verification.md) (TASK-900 — no learning claim without live hook evidence)

| Harness | What you get | How |
|---------|-------------|-----|
| **Claude Code** | **Learning** — hook-based (`PostToolUse` / `UserPromptSubmit` / `Stop` / `SessionStart` + `PreCompact`) | `palate hook install` |
| **Command Code** | **Learning** — mirrors Claude Code via `~/.commandcode` | `palate hook install` |
| **ZCode** | **Learning** — hook-based (`SessionStart` / `UserPromptSubmit` / `PostToolUse` / `Stop`; strict-JSON hook output) | `palate hook install` |
| **OMP (oh-my-pi)** | Recall-only *(hooks unverified)* | `palate serve` (MCP) |
| **Hermes** | Recall-only *(hooks unverified)* | `palate serve` (MCP) |
| **Pi / OpenClaw / OpenCode** | Recall-only *(hooks unverified)* | `palate serve` (MCP) |
| **Codex / Cursor / Windsurf / OpenWebUI** | Recall-only | `palate serve` (MCP) |
| **Generic MCP** | Recall-only | `palate serve` (stdio / `--port` SSE) |

> Beyond Claude Code / Command Code / ZCode, every adapter is **recall-only until its hooks are proven**. Palate never overclaims.

Per-harness wiring (human + agent copy-paste): [`INSTALL.md`](INSTALL.md) §2

---

## How it works

```
Observable signal → safety gate (secret + injection scan) → confidence update → store
                                                                      ↓
                                              push prewarm (session / intent) ←→ recall / TASTE.md
```

**Confidence math:** `decay(c, c_floor, half_life=168h, Δt) → reinforce/ weaken (w·0.3) → clamp [c_floor,1.0]`, bump `last_reinforced_at` + `reinforcement_count`. Contradictions (negation/antonym, same-category scan) weaken the old row and open a `conflicts` row — the loser hides, the winner shows. New preference seeds at `max(c_floor, w·0.5)`; a distilled statement (`llm_distilled`) seeds at the model's own confidence, clamped to `[c_floor, 0.9]`. Tie-break: `confidence → reinforcement_count → last_reinforced_at`. Every mutation is append-only in `events` — replay is deterministic when pinned to an `as_of` instant (`recall --as-of RFC3339`).

**Storage:** BEAM-like tiers `working → episodic → scratchpad`, FTS5 triggers on all content tables, `preferences` + `conflicts` bi-temporal, `capture_counters`, recall = FTS + preference scan + RRF with decay replay, conflict filtering, and optional freshness tiers (JSON+cosine vec seam for the `sqlite-vec` build).

**Skills:** Corrections with `reinforcement_count ≥2` cluster by category; ≥3 → deterministic `SKILL.md` (`agentskills.io` frontmatter) via `palate skill cluster`. Candidates are inert until `palate skill install`. Rumination gate rejects cosmetic edits and demands `why_better ≥40` + a testable prediction.

See [`docs/architecture.md`](docs/architecture.md), [`docs/TASTE-format.md`](docs/TASTE-format.md).

---

## CLI — the surface you'll actually use

| Command | What it does |
|---------|-------------|
| `palate init` | Create `.palate/palate.db` (0700/0600, WAL) |
| `palate doctor` | Check sqlite, fts5, vector, 13 adapters, scanner; exits non-zero on a missing or corrupt DB and never creates one |
| `palate store / recall / sleep / stats / forget` | Memory tiers — `recall --as-of RFC3339 --include-conflicted`; `recall` prints the `id=` that `forget` takes, for memories and preferences alike |
| `palate prefer / correct` | Explicit signals (`prefer` / 4-field `correct [tried] [wrong_because] [fix] [trigger_context]`); both refuse secret and injection patterns |
| `palate propose / promote --as` | Determinism boundary — proposals never auto-enter `events` |
| `palate prewarm` | Emit `<palate-prewarm scope="…">` (SessionStart + intent; `--scope goal` needs `--prompt`) |
| `palate lint / push / pull / reindex` | `TASTE.md` is lossy; `reindex` rebuilds FTS from DB |
| `palate skill {cluster,list,install,ruminate,stats}` | Promotion + rumination |
| `palate verify {capture,retrieval} --pure` | Blinded scenarios, `n` + `hash` + caveats (never a headline without them) |
| `palate hook install [--harness H]` | Wire Claude Code + Command Code + ZCode hooks (idempotent; `--harness zcode-global` for `~/.zcode/cli/config.json`) |
| `palate serve [--port]` | MCP stdio/SSE — `--port 4317` or `--port 127.0.0.1:4317`; with `PALATE_MCP_TOKEN` set every SSE request needs `Authorization: Bearer` (bare `:port` binds all interfaces: token or `127.0.0.1:`) |

Config: [`config.yaml`](config.yaml) (weights, half-life, token budgets, trust tiers) + env overrides (`PALATE_DB_PATH`, `PALATE_C_FLOOR`, `PALATE_HALF_LIFE_HOURS`, `PALATE_MCP_TOKEN`).

---

## Security

- Explicit writes (`palate prefer` / `palate correct`, MCP `palate_prefer` / `palate_correct` / `palate_reject` / `palate_accept`) are refused on any secret or injection match. The `hook user-prompt` chat-capture path drops an "I prefer …" clause when the whole prompt matches either scanner. `remember` rejects secrets and quarantines injections behind a `[PALATE: FLAGGED]` banner; `TASTE.md` reimport lands as `trust_tier=IMPORTED`, promotable only via `palate promote`.
- MCP: bind guard — `PALATE_MCP_TOKEN` + loopback allowlist (`127.0.0.1, localhost, ::1, ip6-localhost`) + refusal to bind `0.0.0.0` or empty-host `:port` without a token. Request guard — once a token is set, every SSE request needs `Authorization: Bearer <token>` (`hmac.Equal`); anything else is 401.
- Reimport quarantine: `TASTE.md → IMPORTED w≤0.4`.
- Prewarm pushes preferences only; quarantined `remember` rows keep their `[PALATE: FLAGGED]` banner wherever they are recalled.

Details: [`docs/security.md`](docs/security.md)

---

## Docs

- **Install for every harness (human + agent):** [`INSTALL.md`](INSTALL.md)
- **Adapters — the single source:** [`docs/adapters.md`](docs/adapters.md)
- **Verification gate (honest row per harness):** [`docs/adapters/verification.md`](docs/adapters/verification.md)
- **Architecture / TASTE format / Benchmarking:** [`docs/architecture.md`](docs/architecture.md) · [`docs/TASTE-format.md`](docs/TASTE-format.md) · [`docs/benchmarking.md`](docs/benchmarking.md)
- **Blueprint (original design, frozen 2026-08-15; historical, not current authority):** [`open-taste-blueprint.md`](open-taste-blueprint.md) v2.1 · **Decisions:** [`docs/decisions.md`](docs/decisions.md) · **Plan:** [`plans/260815-1906-palate-v21-open-taste-engine/plan.md`](plans/260815-1906-palate-v21-open-taste-engine/plan.md) (13/13, 133/133)

---

## Development

```sh
go vet ./... && go test ./... && CGO_ENABLED=0 go build -o /tmp/palate ./cmd/palate && /tmp/palate doctor
```

Build: `CGO_ENABLED=0` across macOS/Linux × amd64/arm64. CI: `go vet`, `go test`, `CGO_ENABLED=0 go build`, `palate doctor`. Release: GoReleaser (`--brews` tap `thinhkhuat/homebrew-palate`) + `install.sh`; Homebrew `stable` sha is filled on publish — until `v0.1.0` is actually published, use `brew install --HEAD` or `go install`.

## License

MIT — see [`LICENSE`](LICENSE)
