# TASTE.md Format

```md
# Taste

## Category

- statement. Confidence: 0.xx
```

- `## <Category>` sections, `- <statement>. Confidence: 0.xx` lines (two decimals, `0.00–1.00`).
- Deterministic render: same DB → same bytes (sorted categories, stable order).
- `palate lint` validates header and confidence ranges; forgiving on spacing.

## Contract

- `.palate/palate.db` + `events` log is truth.
- `TASTE.md` is a lossy render artifact — deleting the DB loses learning state.
- `palate reimport` parses category+statement, ignores confidence, inserts as `IMPORTED w=0.4` quarantine (content-hash `import-<hex>` IDs, statement-level dedup across categories, idempotent).
- `palate reindex` rebuilds FTS/vector from DB, never from markdown.

## What is not wired

- No auto-import on render; explicit `palate reimport` required.
- No versioned TASTE.md history beyond git.
