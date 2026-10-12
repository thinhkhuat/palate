---
description: Show the user's current Palate taste profile
---

Run `palate taste` — it reads back any hand edit to TASTE.md, re-syncs the auto-maintained file from the store and prints it. What it prints is the profile: the budgeted subset of live preferences that SessionStart also injects, grouped by category with a confidence each. Source and trust tier are not in the file; `palate export` gives every live preference with them as JSON. `palate recall "<topic>" --json` is for a topical lookup and, with an empty query, lists recent memories rather than preferences, so do not use it for the profile.

If the DB doesn't exist yet or the result is empty, tell the user Palate hasn't learned anything yet, and suggest `palate init` or `/palate:learn` (if an LLM gateway is configured) to get started.

Otherwise, present the preferences grouped by category, each with its confidence score, in a short readable list — not the raw output. Point the user at the auto-maintained file if they want to read or edit it themselves: `palate doctor` prints its path on the `taste:` line (`~/.claude/palate/TASTE.md` by default), and it is already current — no `push` needed. `palate push` only exports a git-shareable copy into the current repo.

Two follow-ups the user may ask for:

- "What was left out?" — `palate taste --cut` prints the id and reason of every live preference that is not in the profile (for example one session so far, or over budget). `palate restore <id>` promotes such a row to STATED, or brings back a preference that was forgotten (a row the dream pass forgot as stale also comes back as STATED).
- A held edit. If a notice says a hand edit to TASTE.md is held, do not run plain `palate taste` (`--cut` and `--path` leave it alone and print a warning): it discards the held edit. Tell the user, and run `palate taste --accept-edits` only if they say to apply it. If the notice says the edit cannot be applied safely, it can only be discarded (`palate taste`, which keeps a copy under `state/`); do not try `--accept-edits`. If `--accept-edits` lists retired or reworded lines it would bring back, or says the edit adds more than 10 lines, show the list to the user and add `--yes` only if they say so.
