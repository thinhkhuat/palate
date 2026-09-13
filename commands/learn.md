---
description: Run the Palate LLM distiller over this project's recent sessions and summarize what it learned
---

Run `palate learn sessions --project "$PWD"` in the current project directory.

If it prints `palate: no LLM configured (PALATE_LLM_ENDPOINT/PALATE_LLM_MODEL) — nothing learned`, tell the user Palate needs an LLM gateway configured before it can distill sessions, and stop — do not attempt anything else.

Otherwise, summarize what changed for the user: how many preferences were applied vs. dropped per reader, and, if any new preferences look notable (a strong or surprising style/testing/naming habit), call them out by name. Keep the summary short — a few lines, not a full dump of every preference.
