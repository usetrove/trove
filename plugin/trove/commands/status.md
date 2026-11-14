---
description: Show which Trove features are on, in shadow mode, or held back
disable-model-invocation: true
---

Run these commands with the Bash tool, one at a time, and show each output to the user as printed:

```
sh "${CLAUDE_PLUGIN_ROOT}/bin/trove" optimize status
sh "${CLAUDE_PLUGIN_ROOT}/bin/trove" plugin status
sh "${CLAUDE_PLUGIN_ROOT}/bin/trove" hooks status
sh "${CLAUDE_PLUGIN_ROOT}/bin/trove" autopilot
```

Then say in two or three sentences what is on, what is only counting (shadow), and what is held back and why, using only what the output says. If Autopilot is on, say what it turned on, rolled back or is still collecting evidence for. If something is "on trial", say that it is running for about half of new sessions, chosen at random, so Trove can compare, and how far along the output says it is; say whether trials are on or off. Never turn trials on yourself: they are a separate yes the user gives with `trove autopilot trials on`. Do not change any setting.
