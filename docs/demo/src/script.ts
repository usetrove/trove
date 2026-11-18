import type { CaptionItem } from "./components/Caption";
import type { Kind, TermLine } from "./components/Terminal";
import { colors } from "./theme";

// Everything the terminals show, as data. Frames are relative to the start of
// each terminal scene. The report text follows the renderer's golden output
// (internal/report/testdata/normal.txt and small.txt). The Autopilot text is
// what `trove autopilot on` and `trove autopilot` print (internal/cli/
// autopilot.go, internal/autopilot/summary.go) for the lossless-v1 policy.
// The report's tables and leak list are not typed out here: the Report scene
// draws them as a graphic from the same golden numbers (REPORT below).

const PROMPT_LEAD = 10;

// Typing speed in characters per frame (2.5 is 75 characters a second) and the
// gap between output lines. Both are the demo's pace; slow them to slow it.
export const CPS = 2.5;
const EVERY = 3;

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

  cmd(text: string, cps = CPS, enter = 12) {
    this.lines.push({ text, at: this.t, kind: "cmd", cps });
    this.t += Math.ceil(text.length / cps) + enter;
    return this;
  }

  out(texts: string | string[], kind: Kind = "out", every = EVERY): TermLine[] {
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

  // Wipe the screen; the next command's prompt appears at once.
  clear() {
    this.lines.push({ text: "", at: this.t, kind: "clear" });
    this.t += PROMPT_LEAD;
    return this;
  }

  // A fresh, idle prompt after a command finishes.
  prompt() {
    this.lines.push({ text: "", at: this.t + 10, kind: "cmd" });
    return this;
  }
}

// `trove autopilot` prints each optimizer as "  %-28s %s" plus "  (reason)".
const apRow = (name: string, state: string, reason: string) =>
  `  ${name.padEnd(28)} ${state}  (${reason})`;

const AP_THRESHOLDS =
  "It turns an optimizer on after at least 10 shadow sessions and 2000 estimated net tokens in 30 days (placeholder values, not yet validated by a benchmark). Savings shown are estimated until measured.";
const AP_TRIALS =
  "Trials (changes that are not lossless, tried on half of your sessions to compare) are off. See them with `trove autopilot trials on`.";

export type Part = { lines: TermLine[]; captions: CaptionItem[]; frames: number };

// ---- the report's numbers, from internal/report/testdata/normal.txt ---------

export const REPORT = {
  period: "this month (since Oct 1)",
  spend: "~$73.50",
  tokens: "1.6M",
  waste: "~$5.85",
  wasteShare: "8% of spend",
  // "Biggest leaks (estimated, overlapping, not additive)", largest first.
  leaks: [
    { name: "Repeated tool results", dollars: 4.5, label: "~$4.50", tokens: "60K tokens" },
    { name: "Noisy tool output", dollars: 3.0, label: "~$3.00", tokens: "90K tokens" },
  ],
};

// ---- part one: install, then the report's headline -------------------------

const buildInstall = (): Part => {
  const s = new Script();
  const captions: CaptionItem[] = [];

  s.at(12).cmd("brew install usetrove/trove/trove");
  s.out("==> Fetching usetrove/trove/trove", "muted", 9);
  s.pause(8).out("==> Installing trove from usetrove/trove", "muted", 9);
  s.pause(10).out("🍺  /opt/homebrew/Cellar/trove/0.4.0: 4 files, 15.3MB");
  captions.push({ text: "One install. One binary. *No account.*", from: 8, to: s.t + 44 });
  s.pause(52);

  const reportAt = s.clear().t;
  s.cmd("trove");
  s.out(`Trove report: ${REPORT.period}`, "bold");
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
    "  Savings      none yet; no optimizer has been on (estimated)",
    "  Measured     not enough data yet to compare sessions with and without an",
    "               optimizer. The dollar savings above are estimates.",
  ]);
  waste.highlight = { from: s.t + 12, color: colors.muted, label: "estimated" };
  captions.push({ text: "Run *trove*. See where they leak.", from: reportAt, to: s.t + 84 });
  s.pause(92);

  return { lines: s.lines, captions, frames: s.t };
};

// ---- part two: say yes to Autopilot ----------------------------------------

const buildConsent = (): Part => {
  const s = new Script();
  const captions: CaptionItem[] = [];

  s.at(12).cmd("trove autopilot on");
  s.out(
    [
      "Autopilot lets Trove watch your usage, test savings in the background without",
      "changing anything, and turn on only the ones that prove safe for your sessions.",
      "",
      "  - It starts in shadow mode: nothing is changed until there is enough evidence.",
      "  - Only lossless optimizers that leave your cached prompt alone are eligible.",
      "  - Changes reach only sessions that start afterwards, never one in progress.",
      "  - Anything you set yourself in config.toml is left exactly as you set it.",
      "  - A guardrail turns an optimizer back to shadow if it stops paying off.",
      "  - It never touches your login or your traffic beyond what you already set up.",
    ],
    "out",
    2,
  );
  const [local] = s.out("  - Nothing leaves your machine.", "out", 2);
  s.out(
    ["", "Undo it any time with `trove autopilot off` (or `trove undo` for everything).", ""],
    "out",
    2,
  );
  local.highlight = { from: s.t + 10, color: colors.gold, label: "local" };
  captions.push({ text: "Say *yes once*. Autopilot does the rest.", from: 4, to: s.t + 96 });
  const [ask] = s.out("Turn on Autopilot? [y/N] ");
  ask.answer = { text: "y", at: s.t + 62 };
  s.at(ask.answer.at + 18);
  s.out("Autopilot is on. It starts in shadow mode and changes nothing yet.", "bold");
  s.out("");
  s.out("Autopilot is on.", "bold");
  const [, noise] = s.out([
    apRow("duplicate_result_collapser", "shadow", "collecting shadow estimates"),
    apRow("noise_normalizer", "shadow", "collecting shadow estimates"),
  ]);
  s.out("");
  s.out(AP_THRESHOLDS, "muted");
  s.out("");
  s.out(AP_TRIALS, "muted");
  s.out("");
  s.out("  Changes in the last 7 days:");
  s.out([
    "    2026-10-14  noise_normalizer -> shadow  (Autopilot turned on)",
    "    2026-10-14  duplicate_result_collapser -> shadow  (Autopilot turned on)",
  ]);
  noise.highlight = { from: s.t + 10, color: colors.muted, label: "estimates only" };
  captions.push({
    text: "It starts in *shadow*: it estimates, and changes nothing.",
    from: s.t + 14,
    to: s.t + 118,
  });
  s.pause(124);

  return { lines: s.lines, captions, frames: s.t };
};

// ---- part three: Autopilot has turned one on by itself ----------------------

export const TOAST = {
  title: "Trove Autopilot",
  body: "Turned on noise_normalizer from shadow evidence (estimated). Undo with `trove autopilot off`.",
  from: 6,
  to: 150,
};

const buildStatus = (): Part => {
  const s = new Script();
  const captions: CaptionItem[] = [];

  s.at(70).cmd("trove autopilot");
  s.out("Autopilot is on.", "bold");
  const [, noiseOn] = s.out([
    apRow("duplicate_result_collapser", "candidate", "evidence looks good; confirming on the next check"),
    apRow("noise_normalizer", "on", "turned on from shadow evidence (estimated)"),
  ]);
  s.out("");
  s.out(AP_THRESHOLDS, "muted");
  s.out("");
  s.out(AP_TRIALS, "muted");
  s.out("");
  s.out("  Changes in the last 7 days:");
  s.out([
    "    2026-10-27  noise_normalizer -> candidate  (evidence looks good; confirming on the next check)",
    "    2026-10-28  noise_normalizer -> on  (turned on from shadow evidence (estimated))",
    "    2026-10-28  duplicate_result_collapser -> candidate  (evidence looks good; confirming on the next check)",
  ]);
  noiseOn.highlight = {
    from: s.t + 10,
    color: colors.green,
    label: "turned on by itself",
    labelColor: colors.gold,
  };
  captions.push({
    text: "Proven on your own sessions? Trove turns it on *by itself*.",
    from: 14,
    to: s.t + 96,
  });
  s.prompt();
  s.pause(108);

  return { lines: s.lines, captions, frames: s.t };
};

export const partInstall: Part = buildInstall();
export const partConsent: Part = buildConsent();
export const partStatus: Part = buildStatus();
