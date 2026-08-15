# Adapter Hook-Surface Verification (TASK-900)

> **Gate:** No post-MVP adapter claims learning until its hook surface is verified
> against live docs at build time. Failing harnesses are demoted to recall-only.

## Evidence

| Harness | Doc URL | Hook / mechanism | Signal mapping | Verdict |
|---------|---------|------------------|----------------|---------|
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
# internal/adapters/pi.go behind the PAT-001 registry
```
