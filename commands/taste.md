---
description: Show the user's current Palate taste profile
---

Run `palate recall "" --json` in the current project directory to get the current taste profile (preferences and memories, with confidence scores).

If the DB doesn't exist yet or the result is empty, tell the user Palate hasn't learned anything in this project yet, and suggest `palate init` (if `.palate/` is missing) or `/palate:learn` (if an LLM gateway is configured) to get started.

Otherwise, present the preferences grouped by category, each with its confidence score, in a short readable list — not the raw JSON. Note that `TASTE.md` (after `palate push`) is the git-shareable, human-readable render of the same data if the user wants a file to look at.
