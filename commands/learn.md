---
description: Run the Palate LLM distiller over this project's recent sessions and summarize what it learned
---

Run `palate learn sessions --project "$PWD"` in the current project directory.

If it prints `palate: no LLM configured (PALATE_LLM_ENDPOINT/PALATE_LLM_MODEL) — nothing learned`, tell the user Palate needs an LLM gateway configured before it can distill sessions, and stop — do not attempt anything else.

Then run `palate consolidate` (alias `palate dream`) so the model runs the dream pass: it reviews the profile and the new candidate rows in batches, merging duplicates, shortening vague lines, rescoring and retiring stale ones. A retire, project tag, lower score or lowering merge on one of the user's own `STATED` lines is never applied by the pass: it is filed as a proposal, so name `palate promote <id>` for those even in `auto` mode. In `distill.mode: review` it files proposals instead — say so and name `palate promote <id>`. If it prints that another learn or consolidate pass holds the lock, say the pass was skipped and will retry next session.

Summarize what changed for the user: how many preferences were applied vs. dropped per reader, what the dream pass did (actions applied or proposed), and, if any new preferences look notable (a strong or surprising style/testing/naming habit), call them out by name. A new preference stays a candidate until it is stated explicitly or seen again in a later session, so it may not appear in the profile yet; `palate taste --cut` shows why. Keep the summary short — a few lines, not a full dump of every preference.
