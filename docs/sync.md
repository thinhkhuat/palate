# Sync

Event-log sync with optional client-side E2EE. Server sees metadata only when E2EE is on.

## Engine

- Append-only `events` log; `EventHash(memory_id|op|ts|device|payload|parents|importance)` deterministic.
- `PullChanges` / `PushChanges` cursor pagination `WHERE (created_at, id) > (?,?)` (not bare timestamp).
- Fingerprint dedup on push; `parent_event_ids` ancestors for LWW.

## E2EE

- Key derived via Argon2id (`golang.org/x/crypto/argon2`), 32 bytes.
- Payload encrypted with AES-GCM (nonce + ciphertext, base64).
- Key never leaves the machine (passphrase or keychain).

## What is not wired

- No hosted sync server in MVP; engine is ready for a server that stores opaque cursors only.
- No automatic background sync; explicit `push`/`pull` only.
