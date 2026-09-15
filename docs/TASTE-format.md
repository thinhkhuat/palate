# TASTE.md Format

```md
# Taste

## Category

- statement. Confidence: 0.xx
```

- `## <Category>` sections, `- <statement>. Confidence: 0.xx` lines (two decimals, `0.00–1.00`).
- Command-code shaped: no `(source, TRUST_TIER)` suffix in the file. What you
  review is exactly what the agent gets. Provenance (source, tier, confidence
  history) stays in the DB — query it with `palate recall -v` / `palate export`.
- Deterministic render: same DB → same bytes (categories alphabetical, rows
  confidence-descending within each category, so the strongest taste reads
  first and prewarm truncation drops the weakest rows first).
- `palate lint` validates header and confidence ranges; forgiving on spacing.
  Lint accepts the no-suffix shape (and ignores anything after `Confidence:`
  for forward/backward file compatibility).

## Contract

- `.palate/palate.db` + `events` log is truth.
- `TASTE.md` is a lossy render artifact — deleting the DB loses learning state.
- `palate reimport` parses category+statement, ignores confidence, inserts as `IMPORTED w=0.4` quarantine (content-hash `import-<hex>` IDs, statement-level dedup across categories, idempotent).
- `palate reindex` rebuilds FTS/vector from DB, never from markdown.
- `user_diff` rows (retired path) land at `trust_tier=OBSERVED`, never `STATED`;
  the verbatim `prefers: <added lines>` write path is retired — file diffs are
  covered by the LLM distiller via the transcript. `palate consolidate
  --retire-verbatim [--dry-run]` bulk-retires leftover verbatim dumps.

## What is not wired

- No auto-import on render; explicit `palate reimport` required.
- No versioned TASTE.md history beyond git.
