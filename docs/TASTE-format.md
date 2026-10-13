# TASTE.md Format

```md
<!-- auto-maintained by palate after event #N; sha256=<12 hex>; edit freely: … -->
# Taste

## Category

- statement. Confidence: 0.xx
```

- The file holds the **profile**: the budgeted subset of live preferences chosen
  by `learn.SelectProfile`, not the whole ledger. The same body is what
  SessionStart injects. Which rows qualify and why the rest are left out:
  [decisions.md](decisions.md) rows 35-37; `palate taste --cut` lists every live
  row that was left out, with its reason.
- Header: one HTML comment carrying the last event id and `sha256=`, the first
  12 hex characters of the SHA-256 of everything after the `# Taste` line. The
  hash is how the read-back below tells untouched bytes from edited ones. An
  empty profile has the same header over a placeholder line.
- `## <Category>` sections, `- <statement>. Confidence: 0.xx` lines (two decimals,
  `0.00–1.00`).
- Command-code shaped: no `(source, TRUST_TIER)` suffix in the file. What you
  review is exactly what the agent gets. Provenance (source, tier, confidence
  history) stays in the DB — query it with `palate export`.
- Deterministic render: same DB → same bytes (categories alphabetical, rows
  confidence-descending within each category, so the strongest taste reads first).
  Which rows are in the file is not a truncation of this order; it is decided
  by the selection before rendering.
- `palate lint` validates header and confidence ranges; forgiving on spacing.
  Lint accepts the no-suffix shape (and ignores anything after `Confidence:`
  for forward/backward file compatibility).

## Editing the file by hand

> **Assessment period (since 2026-10-13, decisions row 51):** treat this file as
> generated-only. Change taste with `palate prefer`, `palate forget` and
> `palate restore`, not by editing the file. A shell edit made by an assistant
> is read back as your own stated preference. No setting enforces this; if no
> later decision is recorded, the approach was found good.

Edits are read back into the ledger (`profile.read_back_edits`, on by default;
code: `internal/render/readback.go`). The read-back runs at SessionStart, at
UserPromptSubmit, in `palate taste` and before each render, under the render
lock.

- The file is compared with `state/TASTE.base.md`, the body Palate last wrote.
  The confidence number on a line is ignored; the statement text is compared.
- A line you **add or reword** becomes a STATED preference at confidence 0.95,
  tagged `taste_edit`, filed under the `##` heading above it. If a live row
  already has that statement it is reinforced instead of duplicated. Lines and
  rows are compared without their trailing period (TASTE.md drops it; stored
  statements end with one), so a line always finds its own row.
  A STATED row qualifies for the profile at once.
- A line you **delete** is forgotten softly: the row is closed, not erased.
  Restore it with `palate restore <id>`. No command lists retired rows; the id
  is in the `events` log (`taste_edit_forget`).
- A line that trips the secret or injection scanner is refused and counted
  (`taste_edit_refused`), never stored.
- Palate **holds** an edit instead of applying it when: the assistant wrote the
  file (its path is in `state/model_edits.json`); the edit adds more than 10
  lines; it would delete more than half of the base; the base on record does
  not match the header's `sha256=`; the header has no `sha256=` (an older file) while a base is on record and the body differs; the `# Taste` line is gone and lines would be deleted; or the backup taken before applying it failed. The held
  bytes are kept in `state/TASTE.held-<timestamp>.md`, beside the base they
  were compared with (`state/TASTE.held-base-<timestamp>.md`). `TASTE.md` is not
  rewritten while a hold is pending, and the next notice says so. `palate taste
  --accept-edits` applies the held edit against that recorded base, never
  against today's profile (which keeps changing while the hold waits), and
  asks for the same guards again except the one that held the edit: an
  assistant-written or backup-failed hold still obeys the mass-deletion limit
  and refuses with a message that names `palate taste`. Two things you can
  override from the command line, whichever guard held the edit (the 10-line
  cap included): more than 10 added lines, and any added line that is text
  that has left the ledger (a retired or merged row, a merge survivor's or a
  reworded row's old wording, whether the dream pass or the learner reworded
  it). `--accept-edits` prints every such line and applies nothing;
  `palate taste --accept-edits --yes` prints the same list and then applies it.
  A file saved from a profile that has since changed (the `sha256=` / base
  mismatch), or one whose header comment line was removed, applies its added
  lines only; it never deletes anything. The same lists and the same `--yes`
  apply to it. A hold with no base to compare
  with (a missing base, or an older-format file that still has the old header
  but no `sha256=`, such as the full dump an older palate writes during the
  switch-over) can only be discarded: `--accept-edits` refuses it and the notice
  offers `palate taste` alone. Plain `palate taste` discards any hold (`palate taste --cut` and `--path` do not: they leave it alone and print one warning line); the held
  copy is kept as `state/TASTE.discarded-<timestamp>.md` (an unreadable hold
  file as `state/readback.hold.discarded-<timestamp>.json`). If you saved
  `TASTE.md` again after the hold was made, accepting or discarding first copies
  that newer file to `state/TASTE.after-hold-<timestamp>.md` and prints the
  path, then goes ahead. Resolving a hold
  also clears TASTE.md's entry in `state/model_edits.json`, so your next edit is
  not mistaken for the assistant's.
- With `profile.read_back_edits: false` nothing is read back, the header text
  is unchanged, and an edit is overwritten by the next render.

## Contract

- `palate.db` (in the Palate home, or `.palate/palate.db` in the legacy layout)
  and its `events` log are the truth.
- `TASTE.md` is a render of the profile plus your read-back edits — deleting
  the DB loses learning state.
- `palate reimport` parses category+statement, ignores confidence, inserts as `IMPORTED w=0.4` quarantine (content-hash `import-<hex>` IDs, statement-level dedup across categories, idempotent).
- `palate reindex` rebuilds FTS/vector from DB, never from markdown.
- `user_diff` rows (retired path) land at `trust_tier=OBSERVED`, never `STATED`;
  the verbatim `prefers: <added lines>` write path is retired — file diffs are
  covered by the LLM distiller via the transcript. `palate consolidate
  --retire-verbatim [--dry-run]` bulk-retires leftover verbatim dumps.

## What is not wired

- No auto-import of a *different* file on render; `palate reimport` / `palate pull` are explicit. (Edits to the home's own `TASTE.md` are the one read-back, above.)
- No versioned TASTE.md history beyond git.
- No MCP tool to restore a forgotten preference; use `palate restore`.
