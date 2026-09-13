---
description: Show the user's current Palate taste profile
---

Run `palate export` to get the current taste profile as JSON (every active preference with category, statement, confidence, source and trust tier). `palate recall "<topic>" --json` is for a topical lookup; with an empty query it lists recent memories, not preferences, so do not use it for the profile.

If the DB doesn't exist yet or the result is empty, tell the user Palate hasn't learned anything yet, and suggest `palate init` or `/palate:learn` (if an LLM gateway is configured) to get started.

Otherwise, present the preferences grouped by category, each with its confidence score, in a short readable list — not the raw JSON. Point the user at the auto-maintained file if they want to read it themselves: `palate doctor` prints its path on the `taste:` line (`~/.claude/palate/TASTE.md` by default), and it is already current — no `push` needed. `palate push` only exports a git-shareable copy into the current repo.
