---
name: Bug report
about: Something in Trove did not work as described
labels: bug
---

**Easiest path:** run `trove feedback`. It builds this report for you with versions, agent names, counts and error classes, shows you the full text, and opens it only if you say yes. It never includes prompts, code, paths or keys.

If you write it by hand, please include:

- **What happened, and what you expected.**
- **Trove version** (`trove --version`) and how you installed it (Homebrew, install script, plugin, from source).
- **Agent and version** (for example Claude Code 2.1.x, Codex CLI) and your OS.
- **Which part:** report, `trove fix`, hooks, MCP server, gateway, Autopilot, or the plugin.
- **Output of `trove doctor`.**

> [!WARNING]
> Do not paste prompts, code, file paths, API keys or session logs. Trove never needs them to diagnose a problem.
> For a security issue, do not open a public issue: see [SECURITY.md](../../SECURITY.md).
