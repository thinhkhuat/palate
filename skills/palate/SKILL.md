---
name: palate
description: Apply the user's learned coding taste (style, naming, testing habits, and other preferences Palate has captured) to the current task, and know when a user statement should be written back into Palate instead of just followed once.
---

# Palate — taste contract

Palate observes how you edit, correct, and state preferences, scores each one
with a confidence that moves only on evidence (reinforced when confirmed,
weakened when contradicted), and pushes the current profile into this
session at `SessionStart` (the same lines as `TASTE.md`: a budgeted subset of
what it has learned, so a new, one-off observation may not be in it yet), a
`<palate-learned>…</palate-learned>` block at the first prompt after a
preference enters the profile (apply those lines from then on, the
SessionStart profile predates them), and the `palate_recall` MCP tool. A
per-prompt `<palate-prewarm scope="goal">` block appears only if the owner
turned it on. An empty profile is announced as such — learning is automatic;
nothing to do.

## Two tiers of trust

- **Distilled / learned preferences** (anything already in a prewarm block,
  or returned by `palate_recall`) — these are Palate's own inference from
  observed signals (edits, corrections, an LLM-distilled transcript or git
  history). Apply them by default. If the **current request** conflicts with
  one, the current request wins — a learned preference is a standing
  default, not an override of what the user just asked for.
- **STATED preferences** — anything the user types explicitly in this
  conversation ("I prefer X", "always do Y", "never do Z", a correction).
  These are requirements, not suggestions, for the rest of the session. Write
  them back into Palate (see below) so they outlive this session too.

## When the user states a preference explicitly

Do not edit the Palate home (`~/.claude/palate/`, or a legacy repo
`.palate/`) or its `TASTE.md` yourself. `TASTE.md` is an auto-maintained
render of the profile, not the source of truth. The owner's own edits to it
are read back as stated taste. An edit made with the Edit or Write tool is
held instead (the file is not rewritten until the owner accepts or discards
it, and the next notice says so); an edit made through the shell leaves no
snapshot, so Palate cannot tell it from the owner's and would read it back
as their words. Instead, call the
matching MCP tool: with a gateway configured the model judges the statement
(wording, category, confidence, and whether it is new, the same as, a
refinement of, or a contradiction of a stored row) and the tool reply shows
the verdict; without one the built-in weights apply. Either way it is kept and
reinforced on the next confirmation:

| User says | Call |
|---|---|
| "I prefer X" / states a positive preference | `palate_prefer` |
| Corrects something you did ("no, do X because Y") | `palate_correct` |
| Explicitly rejects an approach | `palate_reject` |
| Explicitly accepts/confirms an approach | `palate_accept` |

`palate_recall` searches everything Palate has stored, not only the profile;
a preference that is also in the profile is marked `[profile]`. Use it when
you need more context than the prewarm block already injected. If the user
asks you to forget a preference, `palate_forget` retires it (it can be
restored with the CLI command `palate restore`; there is no MCP restore
tool). Never invent a preference
the user didn't state or that Palate hasn't actually learned — if you're
unsure whether something is a real preference, ask, don't guess.
