# Benchmarking

> Every report must print `n`, judge/model, oracle mode, or refuse to print a number.

## How to run

```sh
palate verify capture --pure   # per-signal-type rates from capture_counters
palate verify retrieval --pure # Hit@K/MRR on labeled corpus (when wired)
PALATE_JUDGE=gpt-4o palate verify capture  # records judge identity
```

## Scenarios

- `internal/verify/scenarios.go` v1 (n=6, hash `53d17edeb67dfdca`). Blinded set — do not tune the learner against it.
- Add scenarios by appending to `ScenariosV1` and bumping the hash; CI should hash-lock `scenarios.go`.

## Caveats block (required)

Each `verify` report prints:

```json
{"version":"v1","scenarios_hash":"...","judge":"...","pure":true,"caveats":"n=6; hash=...; --pure: oracles disabled"}
```

Without `PALATE_JUDGE` and `--pure`, the number is not publishable.

## What is not wired

- Behavioral A/B (`verify behavior` against live Claude Code) is not yet automated; publish per-scenario lifts only, never a single headline.
- No single number is shown without the caveats block above.
