# Demo script (30 to 45 seconds)

Record in a clean terminal with a large font, using a real history (not staged numbers). Keep the on-screen number as whatever the tool actually prints; the caption quotes only what is on screen. Aim for about 40 seconds.

| Time | On screen | Voiceover or caption |
|---|---|---|
| 0:00 to 0:05 | Terminal. `brew install usetrove/trove/trove` finishing | "One install." |
| 0:05 to 0:15 | `trove` prints the report from local history. Pause on the headline number. | "Run trove. This is where my tokens went. All local." |
| 0:15 to 0:22 | `trove fix` lists fixes with estimated savings, labeled "estimated". | "Specific fixes, each with the evidence." |
| 0:22 to 0:30 | `trove fix <id> --apply` shows the exact diff and asks. Press yes. | "I see the exact change first. One command to undo." |
| 0:30 to 0:40 | Claude Code with the status line showing spend and "saved". Work a few turns; the saved figure moves. | "And the status line keeps count. Measured, not guessed." |
| 0:40 to 0:45 | Title card: "Trove. Runs locally. Sends nothing by default. Open source." plus the repo URL | |

## Rules for the recording

- No figure may be added in editing. Every number on screen is real output from that take.
- If the saved figure is labeled estimated by the tool, the caption must not say "measured".
- Record the install on a clean machine, or cut it and say "install" without showing a timing.
- Do not claim a time to first output in the video unless it was measured for that take; if you do, cite it in the description.
- Save the take's report as `docs/launch/evidence/<date>/demo-report.json` (`trove --json`) so the on-screen numbers have a source.
