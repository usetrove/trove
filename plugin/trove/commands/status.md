---
description: Show which Trove features are on, in shadow mode, or held back
disable-model-invocation: true
---

Run these commands with the Bash tool, one at a time, and show each output to the user as printed:

```
sh "${CLAUDE_PLUGIN_ROOT}/bin/trove" optimize status
sh "${CLAUDE_PLUGIN_ROOT}/bin/trove" plugin status
sh "${CLAUDE_PLUGIN_ROOT}/bin/trove" hooks status
```

Then say in two or three sentences what is on, what is only counting (shadow), and what is held back and why, using only what the output says. Do not change any setting.
