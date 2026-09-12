# Security

## Threat model (MVP)

- **Secret filter**: every write boundary rejects `sk-`, `ghp_`/`gho_`, `PRIVATE KEY`, `AKIA`, `api_key`-shaped values on match (CLI `prefer`/`correct`; MCP `remember`/`correct`/`prefer`/`reject`/`accept`); the scanner redacts secrets from stored content.
- **Injection scanner**: flags instruction-override / role-spoof / `[PALATE:` / zero-width. Explicit writes (`prefer`, `correct` incl. all four fields, `reject`, `accept`, CLI and MCP) are **refused** on a match, because `STATED` content is pushed unredacted into prewarm. `remember` and reimport keep the quarantine path: `trust_tier=IMPORTED` + `[PALATE: FLAGGED]` banner. Clean content passes through unwrapped.
- **MCP auth**: bind guard — `PALATE_MCP_TOKEN` + loopback allowlist (`127.0.0.1, localhost, ::1, ip6-localhost`), refusal to bind `0.0.0.0` or an empty-host `:port` (binds all interfaces) without a token. Request guard — once a token is set, every SSE request must carry `Authorization: Bearer <token>`, compared with `hmac.Equal`; anything else is 401.
- **Permissions**: `chmod 0700` on `.palate` dirs and `0600` on the DB/state files at creation; existing files keep their mode.
- **Trust tiers**: `STATED` / `IMPORTED` on every preference; SessionStart prewarm badges `IMPORTED` rows; conflicting preferences hide the loser until resolved.
- **Reimport quarantine**: `TASTE.md` reimport → `IMPORTED w=0.4` (content-hash IDs, statement-level dedup), promotable only via `palate promote`.

## What is not wired (honest)

- Full harness matrix — learning is proven on Claude Code / Command Code / ZCode; the rest are recall-only until `docs/adapters/verification.md` proves hooks.

## Key handling

Key never leaves the machine: OS keychain or `file:<path>`; no plaintext in repo.
