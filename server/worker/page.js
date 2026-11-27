// The weekly limit report page: plain HTML built from one approved aggregate, with no scripts, cookies,
// external resources or analytics. Everything printed comes from aggregate(), so nothing on it can describe
// fewer than MIN_GROUP installs.
import { MIN_GROUP, MIN_ALERTING, FLAG_MULTIPLE, BASELINE_VERSIONS } from "./aggregate.js";

const esc = (v) => String(v).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);

const METER_NAMES = { five_hour: "5-hour limit", seven_day: "Weekly limit" };
const TYPE_NAMES = { output: "Output", cache_read: "Cache read", cache_write_5m: "Cache write (5 min)", cache_write_1h: "Cache write (1 hour)" };
const PLAN_NAMES = { all: "All plans", pro: "Pro", max: "Max", ultra: "Ultra", team: "Team", enterprise: "Enterprise", unspecified: "Plan not given" };
const METRIC_NAMES = {
  limit_pct_per_1k_output_equivalent: "5-hour limit used per 1k output-equivalent tokens",
  unexplained_rebuild_rate: "Unexplained cache rebuilds",
  thinking_tokens_per_request: "Thinking tokens per request",
  thinking_request_share: "Share of requests with thinking",
};
const VERDICTS = {
  flagged: "Drains faster",
  no_signal: "No signal",
  no_baseline: "No earlier versions to compare",
  too_few_installs: `Fewer than ${MIN_GROUP} installs`,
};

const range = (s) => `${esc(s.median)} <span class="r">(${esc(s.p25)} to ${esc(s.p75)})</span>`;

function meterSection(meter, cells) {
  let h = `<h2>${esc(METER_NAMES[meter])}</h2>`;
  if (!cells.length) return h + `<p>Not enough installs yet. A group needs at least ${MIN_GROUP}.</p>`;
  h += `<table><tr><th>Group</th>${Object.values(TYPE_NAMES).map((n) => `<th>${esc(n)}</th>`).join("")}<th>Not explained</th></tr>`;
  for (const c of cells) {
    h += `<tr><td>${esc(PLAN_NAMES[c.plan] || c.plan)}</td>`;
    for (const t of Object.keys(TYPE_NAMES)) {
      const s = c.ratios[t];
      h += s ? `<td>${range(s)}<br><span class="r">typical ±${esc(s.median_width / 2)}, ${esc(s.installs)} installs</span></td>` : "<td>–</td>";
    }
    const u = c.unexplained_pct;
    h += u ? `<td>${esc(u.median)}% <span class="r">(${esc(u.p25)} to ${esc(u.p75)}%)</span></td>` : "<td>–</td>";
    h += "</tr>";
  }
  return h + "</table>";
}

function versionSection(versions) {
  let h = "<h2>Claude Code versions</h2>";
  h += `<p>A version is marked "drains faster" when at least ${MIN_GROUP} installs reported on it, at least ${MIN_ALERTING} of them saw ` +
    `their own regression watch alert on it with the cause put down to the client version, and that share is at least ` +
    `${FLAG_MULTIPLE} times the median share of the ${BASELINE_VERSIONS} versions before it.</p>`;
  for (const [metric, rows] of Object.entries(versions)) {
    if (!rows.length) continue;
    h += `<h3>${esc(METRIC_NAMES[metric] || metric)}</h3><table><tr><th>Version</th><th>Verdict</th><th>Installs that alerted</th></tr>`;
    for (const r of rows) {
      const share = r.alerting_pct === undefined ? "–" : `${esc(r.alerting_pct)}% of ${esc(r.installs)}`;
      h += `<tr><td>${esc(r.version)}</td><td>${esc(VERDICTS[r.verdict] || r.verdict)}</td><td>${share}</td></tr>`;
    }
    h += "</table>";
  }
  return h;
}

export function renderPage(report) {
  const installs = report.installs === null ? `fewer than ${MIN_GROUP}` : String(report.installs);
  return `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>Trove limit report, ${esc(report.week)}</title>
<style>
body{font:15px/1.5 system-ui,sans-serif;max-width:60rem;margin:2rem auto;padding:0 1rem;color:#1d1d1b;background:#fbfbf9}
table{border-collapse:collapse;margin:0 0 1.5rem;width:100%}th,td{border-bottom:1px solid #ddd;padding:.4rem .6rem;text-align:left;vertical-align:top}
th{font-weight:600}.r{color:#666;font-size:.85em}
@media (prefers-color-scheme:dark){body{background:#1b1b1a;color:#e8e8e4}th,td{border-color:#3a3a38}.r{color:#a3a39e}}
</style></head><body>
<h1>Limit report, week ${esc(report.week)}</h1>
<p><strong>Estimated</strong> from ${esc(installs)} Trove installs whose users chose to share their own plan-limit estimate.
They chose themselves, so they may not be typical. These are not official weightings, and your own account may differ.</p>
<p>Each number is how much one token of that kind seemed to count toward the limit compared with one input token.
The first number is the median across installs and the range in brackets covers the middle half of them. "Typical ±" is half the
median width of each install's own 90% interval. "Not explained" is the share of limit use the tokens on each machine
could not account for: claude.ai, other devices, and calls Claude Code does not log.</p>
<p>Groups with fewer than ${MIN_GROUP} installs are never shown, and no install's own numbers are published.</p>
${Object.entries(report.meters).map(([m, cells]) => meterSection(m, cells)).join("\n")}
${versionSection(report.versions)}
<p class="r">Method: docs/decisions/limit-report.md and docs/limit-fit.md in the Trove repository.</p>
</body></html>
`;
}
