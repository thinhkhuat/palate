# Adapter Hook-Surface Verification (TASK-900)

> **Gate:** No post-MVP adapter claims learning until its hook surface is verified
> against live docs at build time. Failing harnesses are demoted to recall-only.

## Evidence

| Harness | Doc URL | Hook / mechanism | Signal mapping | Verdict |
|---------|---------|------------------|----------------|---------|
| **Claude Code** | `https://code.claude.com/docs/hooks` | PostToolUse, UserPromptSubmit, Stop, SessionStart, PreCompact | model_edit, user_correction, user_diff, session_start, compaction | **learning** |
| **Command Code** | bundled `command-code-knowledge/reference/hooks.md` "Supported events" + CLI `isHookEvent()` allowlist (verified live 2026-09-18) | PreToolUse, PostToolUse, Stop, SessionStart (exactly 4; **no PreCompact, no UserPromptSubmit** — settings keys for either are reported `unknown hook event "X" — skipped`) | same as Claude Code | **learning** (verified via local `~/.commandcode/settings.json` hooks; the per-turn surface is the mod's `transformInput`, not a settings hook) |
| **ZCode** | zcode-guide plugin v0.1.0, `diagnosing-hooks` skill (`~/.zcode/cli/plugins/cache/zcode-plugins-official/zcode-guide/0.1.0/skills/diagnosing-hooks/SKILL.md`) | SessionStart, UserPromptSubmit, PostToolUse, Stop (exactly 7 events exist; **no PreCompact**; config-file hooks require `hooks.enabled: true`; stdout is strict-JSON-parsed) | model_edit (PostToolUse `Edit|Write`, covers ApplyPatch alias), stated_preference (UserPromptSubmit), user_diff (UserPromptSubmit + SessionStart state-walk of `.palate/model_edits.json`; Stop re-baselines), session_start prewarm (`hookSpecificOutput.additionalContext`) | **learning** (hook surface verified against the official plugin docs at build time; contract simulated end-to-end: strict-JSON output, exit 0, merge preserves foreign config keys) |
| **Cline** | `https://github.com/cline/cline/blob/main/sdk/packages/shared/src/hooks/events.ts`, `.../sdk/packages/core/src/hooks/hook-file-config.ts`, `.../sdk/packages/core/src/hooks/subprocess.ts` (verified against live sources 2026-09-16) | TaskStart, TaskResume, UserPromptSubmit, PostToolUse, TaskComplete, TaskCancel (file hooks in `~/.cline/hooks`, discovered by basename-sans-extension over a fixed extension allowlist; every matching file fires and `contextModification` strings merge — palate installs collision-free `<Event>.mjs` shims; payload on stdin: `taskId`, `workspaceRoots`, per-event keys; no Stop/PreCompact file-hook dispatch) | session_start prewarm (TaskStart/TaskResume → `hook session-start`, source startup/resume), stated_preference (UserPromptSubmit → `hook user-prompt`), model_edit (PostToolUse edit-file tools → `hook post-tool-use`), user_diff (state-walk), transcript distill (TaskComplete/TaskCancel → `hook stop`, transcript resolved from the session metadata record) | **learning** (hook surface verified against the SDK sources at build time; shim translation + install/uninstall idempotency covered by unit tests including a node-executed end-to-end run of every shim; live task verified on this machine) |
| **OMP (oh-my-pi)** | TBD — local CLI `omp` not found; check `pi`/`omp` docs at build | TBD — requires PostToolUse/UserPromptSubmit evidence | TBD | **recall-only (hooks unverified)** |
| **Hermes** | TBD — local CLI `hermes` not found | TBD | TBD | **recall-only (hooks unverified)** |
| **Pi** | `https://pi.dev` (verify at build) | TBD — fetch live extension API | `model_edit`? `user_prompt`? `session_start`? | **recall-only (hooks unverified)** |
| **OpenClaw** | `https://openclaw.dev` (verify at build) | `allowConversationAccess` hook (claimed in blueprint) | TBD | **recall-only (hooks unverified)** |
| **OpenCode** | `https://opencode.ai` (verify at build) | TBD — fetch live plugin API | TBD | **recall-only (hooks unverified)** |

> **Current status:** All three post-MVP harnesses are **recall-only** until live
> verification is performed. CI must re-fetch docs and re-evaluate before any
> learning claim. This file is the gate artifact; `ak plan check` for Phase 11
> requires it to exist.

## What recall-only means

- MCP tools (`palate_recall`, `palate_remember`, explicit prefer/reject) work.
- No hook-driven capture; no `user_diff` or `model_edit` signals.
- Matrix row must read `recall-only (hooks unverified)` — never `learning`.

## How to verify

```sh
# manual check — fetch docs and confirm hook names
curl -s https://pi.dev/docs/extensions | grep -i hook
# record the exact hook name + URL in the table above, then implement
# a new internal/adapters/pi.go behind the PAT-001 registry (registry.go)
```

## Observed live (this machine, 2026-09-13 → 14)

Evidence from the deployed plugin, not from docs — recorded so the table above is not the only proof.

| Harness | What fired | Evidence |
|---------|-----------|----------|
| **Claude Code** | PostToolUse → `state/model_edits.json` snapshot; Stop re-baseline; UserPromptSubmit + SessionStart prewarm injection; SessionStart detached distiller (`learn session`); SessionStart auto consolidation past the watermark | `learn_cursors` row `claude_code \| …/3ccfd539….jsonl \| position 78 \| attempts 0` after a fresh-shell relaunch with `PALATE_LLM_*` exported; 10 `llm_distilled` preferences with the user's own words as evidence; `consolidations` row `#72 \| 0 actions \| auto`; `palate doctor` → `judge: llm (<model>)`. |
| **Codex** | MCP `serve` (recall-only) | Manifest `.codex-plugin/mcp.json` resolves; `scripts/verify-plugin-mcp.sh` form B (relative cwd) passes. Live Codex hooks **not observed** — stays recall-only |

Prerequisite that bit once: the distiller only runs when `PALATE_LLM_ENDPOINT/MODEL/API_KEY` reach the harness process. A terminal opened before `~/.config/harness-env.sh` gained the block silently leaves them unset — relaunch the harness from a fresh shell and check `palate doctor` → `judge:` before concluding the distiller is broken.

## Live-wired vs verified-at-build (2026-09-16)

The table above records **build-time** claims: a harness whose hook surface was
checked against live docs *can* learn once wired. It says nothing about what is
actually installed on any given machine. `palate doctor` therefore prints the
two claims separately:

```
adapters: 14 registry (4 verified for learning; live-wired: Claude Code, Command Code, ZCode, Codex, Cline)
adapter: Claude Code      learning     wired            (plugin hooks v0.1.0) — palate hook install
adapter: Codex            recall-only  wired            (MCP stdio (config.toml mcp_servers.palate)) — palate serve (MCP)
```

- `learning` / `recall-only` — the build-time `Verified` flag from
  `internal/adapters/registry.go` (this file's evidence table).
- `wired` / `not wired` / `harness absent` — live wiring computed from the
  machine at doctor time (`internal/adapters/wiring.go`): plugin-cache hooks,
  `enabledPlugins`, global settings hooks, ZCode config hooks, or a Codex
  `mcp_servers.palate` stdio table.

A row can be `learning` yet `not wired` (verified docs, nothing installed) —
that combination is the review finding that motivated this split: doctor used
to report "3 verified for learning" in a way that read as three harnesses
capturing, while only Claude Code was actually wired. Every wiring write backs
up the target config first (`<config>.bak-palate-<timestamp>`).
