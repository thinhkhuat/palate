# Security

## Threat model (MVP)

- **Secret filter**: every write boundary rejects `sk-` (hyphenated vendor shapes such as `sk-ant-api03-…` and `sk-proj-…` included), `ghp_`/`gho_`, `PRIVATE KEY`, `AKIA`, `api_key`-shaped values on match (CLI `prefer`/`correct`; MCP `remember`/`correct`/`prefer`/`reject`/`accept`; the `hook user-prompt` chat-capture path drops the extracted clause when the whole prompt matches); the scanner redacts secrets from stored content.
- **Injection scanner**: flags instruction-override / role-spoof / `[PALATE:` / zero-width. Explicit writes (`prefer`, `correct` incl. all four fields, `reject`, `accept`, CLI and MCP) are **refused** on a match, because `STATED` content is pushed unredacted into prewarm. Chat capture (`hook user-prompt`) scans the whole prompt and drops the clause on a match, so an injection prefix cannot be laundered into a `STATED` preference. `remember` quarantines with a `[PALATE: FLAGGED]` banner (`working_memory` has no trust tier column); TASTE.md reimport lands as `trust_tier=IMPORTED`. Clean content passes through unwrapped.
- **MCP auth**: bind guard — `PALATE_MCP_TOKEN` + loopback allowlist (`127.0.0.1, localhost, ::1, ip6-localhost`), refusal to bind `0.0.0.0` or an empty-host `:port` (binds all interfaces) without a token. Request guard — once a token is set, every SSE request must carry `Authorization: Bearer <token>`, compared with `hmac.Equal`; anything else is 401.
- **Permissions**: `chmod 0700` on the Palate home (`~/.claude/palate/`, or a legacy repo `.palate/`) and `0600` on the DB are re-applied on every open, including read-only commands — a looser mode you set by hand is reverted on the next invocation. `state/` and `backups/` are created `0700` once and not re-chmodded; DB backups are written `0600`; `TASTE.md` in the home is `0644` (a render, holds no secrets — the scanner refuses them before they reach the store).
- **Trust tiers**: `STATED` / `IMPORTED` / `DISTILLED` (LLM distiller, below) on every preference; the profile lines carry only statement and confidence, so tier and source are not shown to the agent (`internal/learn/profile.go:56`, `internal/render/taste_md.go:171`); conflicting preferences hide the loser until resolved.
- **Reimport quarantine**: `TASTE.md` reimport → tier `IMPORTED` (confidence from the judge when a gateway is configured, 0.4 otherwise), promotable via `palate promote`.
- **What the judge and the consolidator send**: the judge sends one candidate statement plus the id, category, statement and confidence of its compare set (the profile plus up to 20 full-text matches outside it, any category: `internal/learn/compare.go:69`, `internal/judge/prompt.go:70`); the dream pass sends the whole profile plus a batch of up to 40 candidate rows, each with id, category, statement, confidence, reinforcement count, dates, source/tier, session count and stated/rulebook/conflict markers, and the curated rulebook text when one is configured (`internal/consolidate/consolidate.go:1087`). Both pass every string through `safety.Censor` first, and both prompts declare the payload as data, never instructions. Nothing else — no transcripts, no file contents — leaves the machine on these paths.

## What the LLM distiller sends (optional feature)

Only active when `PALATE_LLM_ENDPOINT` + `PALATE_LLM_MODEL` are both set —
otherwise this whole path is dead code, nothing leaves the machine.

- **Sent**: the taste-distilling system prompt; the compare set (the profile plus up to 20 full-text matches outside it; statement, category, confidence — Palate's own already-
  scanned data; `internal/learn/compare.go:69`, `internal/distill/prompt.go:64`); and a windowed evidence block — the transcript's last ~20
  learnable user/assistant lines (or a git commit's before→after diff
  substitution, or memsearch journal bullets) — split into "previously
  analyzed" (context only) and "new" (what to learn from).
- **Stripped before it is ever assembled into a prompt**: tool-result
  blocks, `<system-reminder>`/`<command-message>`/`<teammate-message>`/
  `<hook_context>`-wrapped hook injections, assistant `thinking`/`tool_use`
  blocks, and any `role` other than `user`/`assistant` (e.g. Codex's
  `developer`, ZCode's `role:"lite"` housekeeping calls) — a reader that
  doesn't recognize a shape drops it and continues rather than guessing.
- **Redacted line-for-line before it is sent**: every evidence line is
  scanned by the same secret pattern as every explicit write boundary
  (`sk-`/`ghp_`/`gho_`/`PRIVATE KEY`/`AKIA`/`api_key`-shaped) — a matching
  line is replaced whole with `[redacted secret]`, never partially censored,
  before the gateway request is built.
- **Never sent**: `PALATE_LLM_API_KEY` is read from the environment only —
  never written to `config.yaml`, never logged, never included in the
  prompt body (it travels solely as the request's `Authorization: Bearer`
  header). A plaintext `http://` endpoint to a non-loopback host prints one
  warning (advisory, not a refusal — it's the user's own infrastructure).
- **After the reply comes back**: every distilled statement passes
  `safety.Scan` — the same gate as `prefer`/`correct` — before storage; a
  statement that is, after trimming, identical to its own evidence quote is
  refused as a verbatim leak rather than stored as a "distilled" preference.
  What survives lands at `trust_tier=DISTILLED` (distinct from user-typed
  `STATED` rows) and is shown in `TASTE.md` and the prewarm like any other row once it qualifies for the profile (no marker; tier stays in the DB).
- **Human-gate option**: `distill.mode: review` in `config.yaml` routes
  every distilled statement into `palate propose`/`promote` instead of
  applying it automatically — nothing lands until a human approves it.

## What is not wired (honest)

- Full harness matrix — learning is proven on Claude Code / Command Code / ZCode / Cline; the rest are recall-only until `docs/adapters/verification.md` proves hooks.

## Key handling

Key never leaves the machine: OS keychain or `file:<path>`; no plaintext in repo.
