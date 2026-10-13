# Palate — Open Taste Engine

> **Your coding taste, learned — not configured.** Palate watches how you edit, correct, and state preferences, then injects that taste back into your AI harness so you don't have to repeat yourself. Open, local-first, harness-agnostic.

[![Status](https://img.shields.io/badge/status-v0.6.0-blue)](#)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8)](#)
[![No CGO](https://img.shields.io/badge/CGO-free-brightgreen)](#)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow)](#)

---

## Why Palate

Harness-native "taste" (like Command Code's closed one) works because the harness *is* the learner — it sees every accept/reject. A portable taste engine can't. **Palate's trick is to learn only from what any harness can actually observe** — your edits, your diffs, your explicit `prefer`/`correct` — score each preference with a confidence that decays and reinforces over time, and *push* it back via a prewarm block so the agent doesn't have to remember to ask.

- **Single Go binary. No CGO. SQLite-only.** `modernc.org/sqlite` + FTS5 + JSON+cosine (and `sqlite-vec` behind a build tag when you want it).
- **Observable signals only:** `model_edit`, `user_correction`, `stated_preference`, `explicit_prefer/reject/accept`, `llm_distilled` — no invented `accept/reject` primitive. The old `user_diff (w=0.6 hypothesis)` verbatim file-diff path is retired: it stored added lines as `prefers: <code>` and poisoned the store (53 rows bulk-retired 2026-09-15); the same edits are covered by the LLM distiller via the transcript.
- **Your LLM gateway is the taste brain.** With `PALATE_LLM_*` set, the model judges every write — explicit *and* distilled — against the profile plus the best full-text matches outside it (wording, category, confidence, and whether it is new / the same / a refinement / a contradiction of what is stored, so a paraphrase reinforces the row instead of becoming a second bullet), distills transcripts, journals and git, and runs a batched dream pass over the profile and its candidates (`palate consolidate`, alias `palate dream`: merge, rewrite, rescore, retire — review mode files proposals; a failed or skipped pass is reported at your next session or prompt, never silent). Without a gateway the built-in heuristics run exactly as before.
- **Push prewarm:** SessionStart injects your **profile** — a code-budgeted subset of what Palate has learned (2 000 estimated tokens by default), the same lines as `TASTE.md`, grouped by category — once per session, with no further cut. A row is in it when you stated it or it was seen in two sessions; everything else stays in the ledger as a candidate (`palate taste --cut` lists them with the reason). There is no per-prompt goal block unless you set `token_budgets.prewarm_goal` above 0. Each time rows enter or leave the profile, the next prompt announces it (`palate: since your last prompt — profile +1 −0; ledger: learned 1 …`) and hands the model the rows that entered as a `<palate-learned>` block, so a preference learned at the previous turn applies from the next — Command Code's inline "taste learned" notices. An empty profile says so and that it fills itself in. What you can review is exactly what the agent gets (Command Code parity).
- **One durable home, every harness.** `~/.claude/palate/` (override `PALATE_HOME`) holds the store for Claude Code, Codex, Command Code, ZCode and Cline alike — same placement as skill-concierge. A repo that still has `./.palate/palate.db` keeps that legacy layout by detection.
- **TASTE.md is a render of the profile, kept current — and you can edit it.** The `events` log in `palate.db` is the source of truth; `TASTE.md` beside it is re-rendered after every write (hooks, CLI, MCP, distiller), one `- statement. Confidence: 0.xx` line per row (source and trust tier stay in the DB), so you can review what Palate learned without opening SQLite. A line you add or reword is read back as your own stated taste; a line you delete is forgotten softly and `palate restore <id>` brings it back ([format and safety holds](docs/TASTE-format.md)). `palate push` exports a git-shareable copy into the repo.

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
binary preinstalled first. If a launcher is ever left behind by a closed session
without reaching the binary, the next `palate serve` stops it and records why in
`<palate home>/state/launcher-ghosts.log` (default home `~/.claude/palate`;
docs/decisions.md #33).
Command Code, ZCode, Cline, and no-plugin setups still
use `palate hook install` — see [INSTALL.md](INSTALL.md) §2.1.

## 30-second start (no plugin — manual wiring)

```sh
sh install.sh                           # private repo: needs `gh auth login` or GITHUB_TOKEN; or: GOPRIVATE=github.com/thinhkhuat go install github.com/thinhkhuat/palate/cmd/palate@latest
palate init
palate doctor                          # → green? (sqlite, fts5, vector, 14 adapters, scanner)
palate hook install                    # Claude Code + Command Code + ZCode + Cline — one command

palate prefer "use table-driven tests"
palate recall "testing" --json
palate prewarm
palate verify capture --pure
```

Detailed install for every harness: **[INSTALL.md](INSTALL.md)**

### LLM distiller (optional)

A second, opt-in learning path: point `PALATE_LLM_ENDPOINT` /
`PALATE_LLM_MODEL` / `PALATE_LLM_API_KEY` (and optionally `PALATE_LLM_TIMEOUT`,
seconds per request for the distiller and the judge — default 30; on a streamed reply it limits connecting and the longest silence with no bytes received, with a total cap of 4x; the dream pass has its own `consolidate.timeout_seconds`) at your own OpenAI-compatible
gateway and `palate learn session/sessions/repo/journals` (or the `Stop` /
`SessionStart` hooks, once those env vars are set) turn transcripts, git
history, and memsearch journals into `llm_distilled` preferences —
`trust_tier=DISTILLED`, same confidence/decay math as everything else. **Off
by default** — no env set, no distiller, today's regex/verbatim paths keep
working unchanged. Details: [INSTALL.md §5](INSTALL.md#5-llm-gateway--the-taste-brain).

### Rulebook dedupe (optional)

Set `rulebook: ~/.claude/CLAUDE.md` in `config.yaml` to point palate at your
curated rulebook. Distilled or judged preferences that merely restate one of
its rules are tagged in provenance, never rendered into `TASTE.md` (or the
SessionStart prewarm), and retired by the next dream pass — the
consolidator sees the rulebook too, so already-stored restatements are caught
without re-learning. The rulebook is read with its `` imports followed (up
to 256 KB in all). Unset (the default), the feature is off.

---

## Harnesses — honest support

**Source of truth:** [`docs/adapters.md`](docs/adapters.md) · **Verification gate:** [`docs/adapters/verification.md`](docs/adapters/verification.md) (TASK-900 — no learning claim without live hook evidence)

| Harness | What you get | How |
|---------|-------------|-----|
| **Claude Code** | **Learning** — hook-based (`PostToolUse` / `UserPromptSubmit` / `Stop` / `SessionStart` + `PreCompact`) | `palate hook install` |
| **Command Code** | **Learning** — hook-based (`SessionStart` / `PostToolUse` / `Stop` via `~/.commandcode`; it has no `UserPromptSubmit`/`PreCompact` event — the per-turn surface is the mod) | `palate hook install` |
| **ZCode** | **Learning** — hook-based (`SessionStart` / `UserPromptSubmit` / `PostToolUse` / `Stop`; strict-JSON hook output) | `palate hook install` |
| **Cline** | **Learning** — hook-based (`TaskStart` / `TaskResume` / `UserPromptSubmit` / `PostToolUse` / `TaskComplete` / `TaskCancel` — file-hook shims in `~/.cline/hooks`; `contextModification` injection) | `palate hook install --harness cline` |
| **OMP (oh-my-pi)** | Recall-only *(hooks unverified)* | `palate serve` (MCP) |
| **Hermes** | Recall-only *(hooks unverified)* | `palate serve` (MCP) |
| **Pi / OpenClaw / OpenCode** | Recall-only *(hooks unverified)* | `palate serve` (MCP) |
| **Codex / Cursor / Windsurf / OpenWebUI** | Recall-only | `palate serve` (MCP) |
| **Generic MCP** | Recall-only | `palate serve` (stdio / `--port` SSE) |

> Beyond Claude Code / Command Code / ZCode / Cline, every adapter is **recall-only until its hooks are proven**. Palate never overclaims.

Per-harness wiring (human + agent copy-paste): [`INSTALL.md`](INSTALL.md) §2

---

## How it works

```
Observable signal → safety gate (secret + injection scan) → confidence update → store
                                                                      ↓
                                              push prewarm (session profile) ←→ recall / TASTE.md
```

**Confidence math:** confidence moves only on evidence — a re-confirmation reinforces (`c + (1-c)·w·0.3`), a contradiction weakens (`c·(1-w·0.3)`), clamp `[c_floor,1.0]`. Time decay is **off by default** (`half_life_hours: 0`; set e.g. `168` to halve an untouched row weekly — with it on, re-confirming an old row used to *lower* it), bump `last_reinforced_at` + `reinforcement_count`. Contradictions (negation/antonym, same-category scan) weaken the old row and open a `conflicts` row — the loser hides, the winner shows. New preference seeds at `max(c_floor, w·0.5)`; a distilled statement (`llm_distilled`) seeds at the model's own confidence, clamped to `[c_floor, 0.9]`. Tie-break: `confidence → reinforcement_count → last_reinforced_at`. Every mutation is append-only in `events` — replay is deterministic when pinned to an `as_of` instant (`recall --as-of RFC3339`).

**Storage:** BEAM-like tiers `working → episodic → scratchpad`, FTS5 triggers on all content tables, `preferences` + `conflicts` bi-temporal, `capture_counters`, recall = FTS + preference scan + RRF with decay replay, conflict filtering, and optional freshness tiers (JSON+cosine vec seam for the `sqlite-vec` build).

**Skills:** Corrections with `reinforcement_count ≥2` cluster by category; ≥3 → deterministic `SKILL.md` (`agentskills.io` frontmatter) via `palate skill cluster`. Candidates are inert until `palate skill install`. Rumination gate rejects cosmetic edits and demands `why_better ≥40` + a testable prediction.

See [`docs/architecture.md`](docs/architecture.md), [`docs/TASTE-format.md`](docs/TASTE-format.md).

---

## CLI — the surface you'll actually use

| Command | What it does |
|---------|-------------|
| `palate init` | Create the Palate home (`~/.claude/palate/`: `palate.db` 0700/0600 WAL, `state/`, `backups/`, seeded `config.yaml`) |
| `palate doctor` | Check sqlite, fts5, vector, 14 adapters, scanner; exits non-zero on a missing or corrupt DB and never creates one |
| `palate store / recall / sleep / stats / forget` | Memory tiers — `recall --as-of RFC3339 --include-conflicted`; `recall` prints the `id=` that `forget` takes, for memories and preferences alike. `forget` deletes a memory but only retires a preference; `forget --purge` deletes a preference for good |
| `palate prefer / correct` | Explicit signals (`prefer` / 4-field `correct [tried] [wrong_because] [fix] [trigger_context]`); both refuse secret and injection patterns; with a gateway the model judges wording, category, confidence and relation, and the verdict is printed |
| `palate learn {session,sessions,journals,repo}` | The distiller on demand: one transcript, every discovered transcript (9 readers, Cline included), memsearch journals, or a git history — `repo` takes a local path, a GitHub `owner/repo`, or a clone URL (temp clone, cursor kept under the URL). The hooks run `session` for you unless `distill.mode: off` or the cwd is under `distill.skip_dirs` |
| `palate consolidate` (alias `palate dream`) | The dream pass: the gateway reviews the whole profile plus batches of new or changed candidate rows — merge / rewrite / rescore / retire / project (auto after a backup, or proposals in `distill.mode: review`) — then a code-only step forgets stale one-session candidates. Auto-run by SessionStart (in the learner's child process) every `consolidate.every_events` events; also run by `sleep`; every attempt is logged to `state/consolidate.log`. `--rebuild` reviews each live row in one supervised pass |
| `palate taste [--cut] [--accept-edits]` | Read back hand edits, re-render and print the profile. `--cut`: id and reason for each live row left out. `--accept-edits`: apply a hand edit that was held for safety, against the profile it was written from (plain `taste` discards it and keeps a copy under `state/`; a hold with no profile to compare against can only be discarded; `--yes` also brings back retired or reworded lines any held edit would restore, and accepts more than 10 added lines; all are listed first; `--cut` and `--path` leave a hold alone and print a warning) |
| `palate restore <id>` | Bring back a retired preference, or promote a candidate that fell out of the profile to STATED. A row the dream pass forgot as stale comes back as STATED, so the next pass keeps it |
| `palate propose / promote --as` | Determinism boundary — proposals never auto-enter `events` |
| `palate prewarm` | Emit `<palate-prewarm scope="…">` (the session profile; `--scope goal` needs `--prompt` and prints nothing while `token_budgets.prewarm_goal` is 0) |
| `palate lint / push / pull / reindex` | `lint` checks the auto-maintained `TASTE.md`; `push` exports a git-shareable copy; `pull` reimports one (`IMPORTED w≤0.4`); `reindex` rebuilds FTS from DB |
| `palate skill {cluster,list,install,ruminate,stats}` | Promotion + rumination |
| `palate verify {capture,retrieval} --pure` | Blinded scenarios, `n` + `hash` + caveats (never a headline without them) |
| `palate hook install [--harness H]` | Wire Claude Code + Command Code + ZCode hooks (idempotent; `--harness zcode-global` for `~/.zcode/cli/config.json`) |
| `palate serve [--port]` | MCP stdio/SSE — `--port 4317` or `--port 127.0.0.1:4317`; with `PALATE_MCP_TOKEN` set every SSE request needs `Authorization: Bearer` (bare `:port` binds all interfaces: token or `127.0.0.1:`) |

Config: [`config.yaml`](config.yaml) (weights, half-life — off by default, token budgets, trust tiers, `distill.mode: auto|review|off`, `distill.skip_dirs`, `profile.*`, `consolidate.*` — see [INSTALL.md §5.2](INSTALL.md#52-the-profile-hand-edits-and-the-dream-pass)) + env overrides (`PALATE_DB_PATH`, `PALATE_C_FLOOR`, `PALATE_HALF_LIFE_HOURS`, `PALATE_DISTILL_MODE`, `PALATE_MCP_TOKEN`).

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

Build: `CGO_ENABLED=0` across macOS/Linux × amd64/arm64. CI: `go vet`, `go test`, `CGO_ENABLED=0 go build`, `palate doctor`. Release: pushing a `v*` tag runs `.github/workflows/release.yml` (GoReleaser → GitHub Release with `palate_<ver>_<os>_<arch>.tar.gz` + `checksums.txt`, which `install.sh` downloads through an authenticated `gh` or `GITHUB_TOKEN`, the repo being private). Install paths: `install.sh` (authenticated, private repo) or `go install` with `GOPRIVATE`.

## License

MIT — see [`LICENSE`](LICENSE)
