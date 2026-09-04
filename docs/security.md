# Security

## Threat model (MVP)

- **Secret filter**: every write boundary rejects `sk-`, `ghp_`/`gho_`, `PRIVATE KEY`, `AKIA`, `api_key`-shaped values on match (CLI + MCP `remember/correct/prefer/reject/accept`); the scanner redacts secrets from stored content.
- **Injection scanner**: flags instruction-override / role-spoof / `[PALATE:` / zero-width → `trust_tier=IMPORTED` quarantine + `[PALATE: FLAGGED]` banner; clean content passes through unwrapped.
- **MCP auth guard**: `PALATE_MCP_TOKEN` + loopback allowlist (`127.0.0.1, localhost, ::1, ip6-localhost`) + `hmac.compare_digest` + refusal to bind `0.0.0.0` — or an empty-host `:port` (binds all interfaces) — without a token.
- **Permissions**: `chmod 0700` on `.palate` dirs and `0600` on the DB/state files at creation; existing files keep their mode.
- **Trust tiers**: `STATED` / `IMPORTED` on every preference; SessionStart prewarm badges `IMPORTED` rows; conflicting preferences hide the loser until resolved.
- **Reimport quarantine**: `TASTE.md` reimport → `IMPORTED w=0.4` (content-hash IDs, statement-level dedup), promotable only via `palate promote`.

## What is not wired (honest)

- No hosted sync server in MVP; E2EE engine (Argon2id → AES-GCM) is implemented, unsent — server would see metadata only.
- Full harness matrix — learning is proven on Claude Code / Command Code / ZCode; the rest are recall-only until `docs/adapters/verification.md` proves hooks.

## Key handling

Key never leaves the machine: OS keychain or `file:<path>`; no plaintext in repo.
