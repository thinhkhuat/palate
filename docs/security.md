# Security

## Threat model (MVP)

- **Secret filter**: every write boundary rejects `sk-` (hyphenated vendor shapes such as `sk-ant-api03-…` and `sk-proj-…` included), `ghp_`/`gho_`, `PRIVATE KEY`, `AKIA`, `api_key`-shaped values on match (CLI `prefer`/`correct`; MCP `remember`/`correct`/`prefer`/`reject`/`accept`; the `hook user-prompt` chat-capture path drops the extracted clause when the whole prompt matches); the scanner redacts secrets from stored content.
- **Injection scanner**: flags instruction-override / role-spoof / `[PALATE:` / zero-width. Explicit writes (`prefer`, `correct` incl. all four fields, `reject`, `accept`, CLI and MCP) are **refused** on a match, because `STATED` content is pushed unredacted into prewarm. Chat capture (`hook user-prompt`) scans the whole prompt and drops the clause on a match, so an injection prefix cannot be laundered into a `STATED` preference. `remember` quarantines with a `[PALATE: FLAGGED]` banner (`working_memory` has no trust tier column); TASTE.md reimport lands as `trust_tier=IMPORTED`. Clean content passes through unwrapped.
- **MCP auth**: bind guard — `PALATE_MCP_TOKEN` + loopback allowlist (`127.0.0.1, localhost, ::1, ip6-localhost`), refusal to bind `0.0.0.0` or an empty-host `:port` (binds all interfaces) without a token. Request guard — once a token is set, every SSE request must carry `Authorization: Bearer <token>`, compared with `hmac.Equal`; anything else is 401.
- **Permissions**: `chmod 0700` on `.palate` and `0600` on the DB are re-applied on every open, including read-only commands — a looser mode you set by hand is reverted on the next invocation.
- **Trust tiers**: `STATED` / `IMPORTED` on every preference; SessionStart prewarm badges `IMPORTED` rows; conflicting preferences hide the loser until resolved.
- **Reimport quarantine**: `TASTE.md` reimport → `IMPORTED w=0.4` (content-hash IDs, statement-level dedup), promotable only via `palate promote`.

## What is not wired (honest)

- Full harness matrix — learning is proven on Claude Code / Command Code / ZCode; the rest are recall-only until `docs/adapters/verification.md` proves hooks.

## Key handling

Key never leaves the machine: OS keychain or `file:<path>`; no plaintext in repo.
