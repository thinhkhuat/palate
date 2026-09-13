# Security

## Threat model (MVP)

- **Secret filter**: every write boundary rejects `sk-` (hyphenated vendor shapes such as `sk-ant-api03-…` and `sk-proj-…` included), `ghp_`/`gho_`, `PRIVATE KEY`, `AKIA`, `api_key`-shaped values on match (CLI `prefer`/`correct`; MCP `remember`/`correct`/`prefer`/`reject`/`accept`; the `hook user-prompt` chat-capture path drops the extracted clause when the whole prompt matches); the scanner redacts secrets from stored content.
- **Injection scanner**: flags instruction-override / role-spoof / `[PALATE:` / zero-width. Explicit writes (`prefer`, `correct` incl. all four fields, `reject`, `accept`, CLI and MCP) are **refused** on a match, because `STATED` content is pushed unredacted into prewarm. Chat capture (`hook user-prompt`) scans the whole prompt and drops the clause on a match, so an injection prefix cannot be laundered into a `STATED` preference. `remember` quarantines with a `[PALATE: FLAGGED]` banner (`working_memory` has no trust tier column); TASTE.md reimport lands as `trust_tier=IMPORTED`. Clean content passes through unwrapped.
- **MCP auth**: bind guard — `PALATE_MCP_TOKEN` + loopback allowlist (`127.0.0.1, localhost, ::1, ip6-localhost`), refusal to bind `0.0.0.0` or an empty-host `:port` (binds all interfaces) without a token. Request guard — once a token is set, every SSE request must carry `Authorization: Bearer <token>`, compared with `hmac.Equal`; anything else is 401.
- **Permissions**: `chmod 0700` on `.palate` and `0600` on the DB are re-applied on every open, including read-only commands — a looser mode you set by hand is reverted on the next invocation.
- **Trust tiers**: `STATED` / `IMPORTED` / `DISTILLED` (LLM distiller, below) on every preference; SessionStart prewarm badges `IMPORTED` rows and marks `DISTILLED` ones `, distilled`; conflicting preferences hide the loser until resolved.
- **Reimport quarantine**: `TASTE.md` reimport → `IMPORTED w=0.4` (content-hash IDs, statement-level dedup), promotable only via `palate promote`.

## What the LLM distiller sends (optional feature)

Only active when `PALATE_LLM_ENDPOINT` + `PALATE_LLM_MODEL` are both set —
otherwise this whole path is dead code, nothing leaves the machine.

- **Sent**: the taste-distilling system prompt; the user's current
  preferences (statement, category, confidence — Palate's own already-
  scanned data); and a windowed evidence block — the transcript's last ~20
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
  `STATED` rows) and is surfaced in prewarm with a `, distilled` marker.
- **Human-gate option**: `distill.mode: review` in `config.yaml` routes
  every distilled statement into `palate propose`/`promote` instead of
  applying it automatically — nothing lands until a human approves it.

## What is not wired (honest)

- Full harness matrix — learning is proven on Claude Code / Command Code / ZCode; the rest are recall-only until `docs/adapters/verification.md` proves hooks.

## Key handling

Key never leaves the machine: OS keychain or `file:<path>`; no plaintext in repo.
