# Security

## Threat model (MVP)

- **Secret filter**: every write boundary censors `sk-`, `ghp_`, `PRIVATE KEY`, `AKIA` — rejects on match, redacts on store.
- **Injection scanner**: flags instruction-override / role-spoof / `[PALATE:` / zero-width → `trust_tier=IMPORTED` quarantine + banner.
- **MCP auth guard**: `PALATE_MCP_TOKEN` + loopback allowlist (`127.0.0.1, localhost, ::1, ip6-localhost`) + `hmac.compare_digest` + refusal to bind `0.0.0.0` without token.
- **Permissions**: `chmod 0700 .palate && 0600 .palate/*.db` on `palate init`; `umask 077` at runtime.
- **Trust tiers**: `STATED` / `IMPORTED` / `EXTERNAL_WRITE` on every preference; prewarm ranks with tier awareness.
- **Reimport quarantine**: `TASTE.md` reimport → `IMPORTED w≤0.4`, promotable only via `palate promote`.

## What is not wired (honest)

- E2EE sync — post-MVP (Phase 10).
- Full harness matrix — MVP is Claude Code only.

## Key handling

Key never leaves the machine: OS keychain or `file:<path>`; no plaintext in repo.
