import type { CaptionItem } from "./components/Caption";
import type { Kind, TermLine } from "./components/Terminal";
import { colors } from "./theme";

// Everything the terminal shows, as data. Frames are relative to the start of
// the terminal scene. The report text is the renderer's golden output
// (internal/report/testdata/normal.txt) with the placeholder model names
// replaced by names Trove's pricing table knows. The fix output follows
// renderFixList and renderFixDetail in internal/cli/fix.go and the strings in
// internal/fixer/mcpprune.go.

class Script {
  lines: TermLine[] = [];
  t = 0;

  at(frame: number) {
    this.t = Math.max(this.t, frame);
    return this;
  }

  pause(frames: number) {
    this.t += frames;
    return this;
  }

  cmd(text: string, cps = 2.5, enter = 12) {
    this.lines.push({ text, at: this.t, kind: "cmd", cps });
    this.t += Math.ceil(text.length / cps) + enter;
    return this;
  }

  out(texts: string | string[], kind: Kind = "out", every = 2): TermLine[] {
    const list = Array.isArray(texts) ? texts : [texts];
    const made: TermLine[] = [];
    for (const text of list) {
      const line: TermLine = { text, at: this.t, kind };
      this.lines.push(line);
      made.push(line);
      this.t += every;
    }
    return made;
  }

  // A fresh, idle prompt after a command finishes.
  prompt() {
    this.lines.push({ text: "", at: this.t + 10, kind: "cmd" });
    return this;
  }
}

// ---- report tables, laid out the way the Go renderer does -------------------

const reportTable = (
  first: string,
  rows: [string, string, string, string][],
): string[] => {
  const w = Math.max(first.length, ...rows.map((r) => r[0].length));
  const line = (r: [string, string, string, string], header: boolean) =>
    "  " +
    r[0].padEnd(w) +
    "  " +
    (header ? r[1] : r[1].padStart(8)) +
    "  " +
    (header ? r[2] : r[2].padStart(17)) +
    "  " +
    (header ? r[3] : r[3].padStart(16));
  return [
    line([first, "Requests", "Tokens (measured)", "Cost (estimated)"], true),
    ...rows.map((r) => line(r, false)),
  ];
};

// Go's text/tabwriter with padding 2: every cell but the last is padded to the
// widest cell in its column plus two spaces.
const tabTable = (rows: string[][]): string[] => {
  const n = rows[0].length;
  const widths = Array.from({ length: n }, (_, c) =>
    Math.max(...rows.map((r) => r[c].length)),
  );
  return rows.map((r) =>
    r
      .map((cell, c) => (c === n - 1 ? cell : cell.padEnd(widths[c] + 2)))
      .join(""),
  );
};

export const PROJECT = "~/code/shop-api";
export const FIX_ID = "mcp-5e7a9c13";

export const SEGMENTS = {
  install: { start: 0, end: 105 },
  report: { start: 105, end: 345 },
  fix: { start: 345, end: 495 },
  apply: { start: 495, end: 675 },
};

export const SESSION_FRAMES = SEGMENTS.apply.end;

const build = () => {
  const s = new Script();

  // ---- install --------------------------------------------------------------
  s.at(10).cmd("brew install usetrove/trove/trove");
  s.out("==> Fetching usetrove/trove/trove", "muted", 8);
  s.pause(10).out("==> Installing trove from usetrove/trove", "muted", 8);
  s.pause(12).out("🍺  /opt/homebrew/Cellar/trove/0.4.0: 4 files, 15.3MB");
  s.prompt();

  // ---- report ---------------------------------------------------------------
  s.at(SEGMENTS.report.start + 10).cmd("trove");
  s.out("Trove report: this month (since Oct 1)", "bold");
  s.out("");
  s.out("Headline", "bold");
  s.out([
    "  Spend        ~$73.50 (estimated at list prices, not an invoice); 1.6M tokens",
    "               (measured)",
  ]);
  const [waste] = s.out([
    "  Waste found  at least ~$5.85, 8% of spend (estimated; overlapping findings",
    "               counted once)",
  ]);
  s.out([
    "  Savings      ~$0.50 so far (estimated)",
    "  Measured     not enough data yet to compare sessions with and without an",
    "               optimizer. The dollar savings above are estimates.",
  ]);
  waste.highlight = { from: s.t + 10, color: colors.amber, label: "estimated" };

  s.at(SEGMENTS.report.start + 110);
  s.out("");
  s.out("Spend by agent and model", "bold");
  s.out(
    reportTable("Agent", [
      ["claude_code", "20", "1M", "~$50.00"],
      ["codex_cli", "10", "520K", "~$23.50"],
    ]),
  );
  s.out("");
  s.out(
    reportTable("Model", [
      ["claude-sonnet-5-5", "10", "520K", "~$25.00"],
      ["claude-haiku-5-5", "10", "520K", "~$25.00"],
      ["gpt-5.5", "10", "520K", "~$23.50"],
    ]),
  );

  s.at(SEGMENTS.report.start + 140);
  s.out("");
  s.out("Biggest leaks (estimated, overlapping, not additive)", "bold");
  const [leak] = s.out([
    "  1. Repeated tool results: ~$4.50 (estimated), 60K tokens (estimated)",
    '     Do this: duplicate_result_collapser = "on"',
    "     Add this line under [optimizers] in config.toml. It applies from your next",
    "     session.",
    "  2. Noisy tool output: ~$3.00 (estimated), 90K tokens (estimated)",
    "     Do this: trove optimize status",
    "     noise_normalizer is on. See what it saved under Optimizers.",
  ]);
  leak.highlight = { from: s.t + 8, color: colors.amber };
  s.prompt();

  // ---- fix list -------------------------------------------------------------
  s.at(SEGMENTS.fix.start + 10).cmd("trove fix");
  s.out(`Fixes for ${PROJECT} (all savings are estimated):`, "bold");
  s.out("");
  const table = tabTable([
    ["ID", "KIND", "FILE", "EST. TOKENS/SESSION", "EST. $/SESSION", "STATUS"],
    [FIX_ID, "mcp_prune", ".claude/settings.local.json", "4200", "$0.0126", "ready"],
    ["ctx-8b21d0f4", "context_slim", "CLAUDE.md", "1350", "$0.0041", "ready"],
    ["cache-3f9a1c2e", "cache_breaker_fix", "CLAUDE.md", "18", "$0.0001", "ready"],
  ]);
  s.out(table[0], "muted");
  const [mcpRow] = s.out(table.slice(1));
  s.out("");
  s.out(
    "See one with `trove fix <id>`; apply it with `trove fix <id> --apply`. A dash means Trove has no honest estimate.",
  );
  mcpRow.highlight = { from: s.t + 14, color: colors.green, label: "ready" };
  s.prompt();

  // ---- apply ----------------------------------------------------------------
  s.at(SEGMENTS.apply.start + 10).cmd(`trove fix ${FIX_ID} --apply`);
  s.out(`${FIX_ID}  mcp_prune`, "bold");
  s.out('Turn off the "notion" MCP server in this project');
  s.out("");
  s.out(
    'Why: "notion" is configured in .mcp.json, but none of this project\'s 14 sessions in the last 30 days called any of its tools. Each session still sends its tool definitions.',
  );
  s.out("");
  s.out("Evidence:");
  s.out([
    "  - 14 recent session(s) in this project read from Claude Code's own history; 0 calls to mcp__notion__*",
    "  - ~4200 tokens of tool definitions per session (estimated from your gateway traffic; may be lower if Claude Code defers tool definitions with tool search)",
  ]);
  s.out("");
  s.out(
    "Estimated saving per session: about 4200 tokens, about $0.0126 (estimated, not measured).",
  );
  s.out(
    [
      "Note: Only you are affected: the change goes in .claude/settings.local.json and .mcp.json is not edited. Add that file to .gitignore if it is not ignored already.",
      "Note: Undo with `trove undo`, or delete the name from disabledMcpjsonServers to turn the server back on.",
    ],
    "muted",
  );
  s.out("");
  s.out("Change to .claude/settings.local.json:", "bold");
  s.out("");
  s.out(["--- .claude/settings.local.json", "+++ .claude/settings.local.json"], "bold");
  s.out("@@ -1,6 +1,7 @@", "hunk");
  s.out([" {", '   "permissions": {"allow": []},', '   "disabledMcpjsonServers": [']);
  s.out('-    "old"', "del");
  const [, notion] = s.out(['+    "old",', '+    "notion"'], "add");
  s.out(["   ]", " }"]);
  notion.highlight = { from: s.t + 6, color: colors.green };
  const [promptLine] = s.out(
    "Apply 1 fix(es)? Originals are backed up and `trove undo` reverses them. [y/N] ",
  );
  promptLine.answer = { text: "y", at: s.t + 36 };
  s.at(promptLine.answer.at + 14);
  s.out(
    `${FIX_ID}: applied. the original is backed up and \`trove undo ${FIX_ID}\` reverses it`,
    "add",
  );
  s.out("");
  s.out(
    "To reverse: `trove undo <id>` (one fix) or `trove undo --all`. Results appear in `trove report` after a few sessions, labeled measured only once there is enough data.",
  );
  s.prompt();

  return s.lines;
};

export const sessionLines: TermLine[] = build();

export const sessionCaptions: CaptionItem[] = [
  { text: "One install. One binary. No account.", from: 12, to: 98 },
  { text: "Run trove. See where the tokens went.", from: 120, to: 208 },
  {
    text: "Every number is labeled measured or estimated. All local.",
    from: 214,
    to: 338,
  },
  { text: "Specific fixes, each with evidence.", from: 372, to: 488 },
  { text: "See the exact change before it happens.", from: 536, to: 598 },
  { text: "One command to undo.", from: 612, to: 672 },
];
