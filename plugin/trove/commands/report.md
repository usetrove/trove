---
description: Show Trove's report on token use, waste and savings
argument-hint: "[--since 7d]"
disable-model-invocation: true
---

Run this command with the Bash tool and show its output to the user exactly as printed, without summarizing or rounding any number. Every figure in it is labeled "measured" or "estimated"; keep those labels.

```
sh "${CLAUDE_PLUGIN_ROOT}/bin/trove" report $ARGUMENTS
```

If it prints an error, show the error and the next step it names. Do not change any setting.
