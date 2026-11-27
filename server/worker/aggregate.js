// The weekly limit report (docs/decisions/limit-report.md, approved 2026-11-26).
//
// aggregate() turns one ISO week's stored summaries into the only thing that is ever published: medians and
// interquartile ranges over groups of at least MIN_GROUP installs, and per-version flags. It never outputs a
// single install's value, a minimum or maximum, an install id, or the size of a group under MIN_GROUP.
// It is a pure function, so the same rows always give the same report.
import { METERS, TYPES, METRICS, PLANS, VERSION } from "./validate.js";

export const MIN_GROUP = 20;
// A version is flagged only when at least this many installs on it alerted with a client cause...
export const MIN_ALERTING = 5;
// ...and its alerting share is at least this multiple of the median share of the versions before it.
export const FLAG_MULTIPLE = 2;
export const BASELINE_VERSIONS = 5;

// quantile is the linear-interpolation quantile of sorted xs.
function quantile(xs, q) {
  const pos = q * (xs.length - 1);
  const lo = Math.floor(pos);
  const hi = Math.min(lo + 1, xs.length - 1);
  return xs[lo] + (xs[hi] - xs[lo]) * (pos - lo);
}

// roundRatio mirrors contribute.RoundRatio: two significant figures below 1, one decimal below 10, whole above.
export function roundRatio(v) {
  if (!(v > 0)) return 0;
  if (v >= 10) return Math.round(v);
  if (v >= 1) return Math.round(v * 10) / 10;
  return Number(v.toPrecision(2));
}

// summary is the published shape of a group's values: never min, max or the values themselves.
function summary(values, round) {
  const xs = [...values].sort((a, b) => a - b);
  return { median: round(quantile(xs, 0.5)), p25: round(quantile(xs, 0.25)), p75: round(quantile(xs, 0.75)) };
}

export function compareVersions(a, b) {
  const pa = a.split(".").map(Number);
  const pb = b.split(".").map(Number);
  for (let i = 0; i < 3; i++) if (pa[i] !== pb[i]) return pa[i] - pb[i];
  return 0;
}

// isoWeek gives the ISO week ("2026-W48") of a YYYY-MM-DD day, and weekDays its seven days.
export function isoWeek(day) {
  const d = new Date(day + "T00:00:00Z");
  const dow = (d.getUTCDay() + 6) % 7; // Monday = 0
  d.setUTCDate(d.getUTCDate() - dow + 3); // the Thursday decides the year
  const year = d.getUTCFullYear();
  const jan4 = new Date(Date.UTC(year, 0, 4));
  const week = 1 + Math.round(((d - jan4) / 86400000 - 3 + ((jan4.getUTCDay() + 6) % 7)) / 7);
  return `${year}-W${String(week).padStart(2, "0")}`;
}

export function weekDays(day) {
  const d = new Date(day + "T00:00:00Z");
  d.setUTCDate(d.getUTCDate() - ((d.getUTCDay() + 6) % 7));
  return Array.from({ length: 7 }, (_, i) => new Date(d.getTime() + i * 86400000).toISOString().slice(0, 10));
}

// latestPerInstall keeps each install's newest limits section from rows {received_day, install_id, payload}.
function latestPerInstall(rows) {
  const by = new Map();
  for (const r of rows) {
    let p;
    try {
      p = typeof r.payload === "string" ? JSON.parse(r.payload) : r.payload;
    } catch {
      continue;
    }
    if (!p || !p.limits || !PLANS.has(p.limits.plan)) continue;
    const prev = by.get(r.install_id);
    if (!prev || r.received_day > prev.day) by.set(r.install_id, { day: r.received_day, limits: p.limits });
  }
  return [...by.values()].map((v) => v.limits);
}

// meterCell summarises one meter over a group of installs, keeping only parts with at least MIN_GROUP values.
function meterCell(group, meter) {
  const out = { ratios: {}, unexplained_pct: null };
  for (const t of TYPES) {
    const rs = group.map((l) => l[meter]?.ratios?.[t]).filter(Boolean);
    if (rs.length < MIN_GROUP) continue;
    out.ratios[t] = {
      ...summary(rs.map((r) => r.ratio), roundRatio),
      median_width: roundRatio(quantile(rs.map((r) => r.width).sort((a, b) => a - b), 0.5)),
      installs: rs.length,
    };
  }
  const us = group.map((l) => l[meter]?.unexplained_pct).filter((v) => Number.isInteger(v));
  if (us.length >= MIN_GROUP) out.unexplained_pct = { ...summary(us, Math.round), installs: us.length };
  return Object.keys(out.ratios).length || out.unexplained_pct ? out : null;
}

// planGroups decides which groups are published: "all" always (when big enough), and each plan with at least
// MIN_GROUP installs, but only if the installs left out of the plan groups are none or at least MIN_GROUP.
// Otherwise "all" minus the published plans would describe a small group.
function planGroups(sections) {
  const groups = [{ plan: "all", members: sections }];
  const byPlan = new Map();
  for (const l of sections) byPlan.set(l.plan, [...(byPlan.get(l.plan) || []), l]);
  const big = [...byPlan.entries()].filter(([, m]) => m.length >= MIN_GROUP).sort(([a], [b]) => (a < b ? -1 : 1));
  const left = sections.length - big.reduce((n, [, m]) => n + m.length, 0);
  if (left === 0 || left >= MIN_GROUP) for (const [plan, members] of big) groups.push({ plan, members });
  return groups;
}

// versionFlags gives, per metric and version, the share of installs on that version whose check alerted with a
// client cause, and a verdict: flagged, no_signal, no_baseline or too_few_installs (with no numbers).
function versionFlags(sections) {
  const out = {};
  for (const metric of METRICS) {
    const cells = new Map();
    for (const l of sections) {
      for (const c of l.version_checks || []) {
        if (c.metric !== metric || !VERSION.test(c.version)) continue;
        const cell = cells.get(c.version) || { installs: 0, alerting: 0 };
        cell.installs++;
        if (c.status === "alert" && c.cause === "client") cell.alerting++;
        cells.set(c.version, cell);
      }
    }
    const versions = [...cells.keys()].sort(compareVersions);
    const rows = [];
    const shares = []; // shares of earlier versions big enough to count, oldest first
    for (const v of versions) {
      const { installs, alerting } = cells.get(v);
      if (installs < MIN_GROUP) {
        rows.push({ version: v, verdict: "too_few_installs" });
        continue;
      }
      const share = alerting / installs;
      const before = shares.slice(-BASELINE_VERSIONS).sort((a, b) => a - b);
      let verdict = "no_baseline";
      if (before.length) {
        const base = quantile(before, 0.5);
        verdict = alerting >= MIN_ALERTING && share >= FLAG_MULTIPLE * base && share > 0 ? "flagged" : "no_signal";
      }
      rows.push({ version: v, verdict, alerting_pct: Math.round(100 * share), installs });
      shares.push(share);
    }
    out[metric] = rows.reverse(); // newest first
  }
  return out;
}

// aggregate builds the week's report from that week's stored rows.
export function aggregate(rows, week) {
  const sections = latestPerInstall(rows);
  const report = { week, label: "estimated", min_group: MIN_GROUP, installs: null, meters: {}, versions: versionFlags(sections) };
  if (sections.length >= MIN_GROUP) report.installs = sections.length;
  for (const meter of METERS) {
    const cells = [];
    for (const g of planGroups(sections)) {
      if (g.members.length < MIN_GROUP) continue;
      const cell = meterCell(g.members, meter);
      if (cell) cells.push({ plan: g.plan, ...cell });
    }
    report.meters[meter] = cells;
  }
  return report;
}

// heldForReview reports whether a new report must wait for a maintainer: always while fewer than
// REVIEWED_WEEKS weeks have been approved, and afterwards whenever a published median moved by more than
// HOLD_CHANGE against the last approved week.
export const REVIEWED_WEEKS = 4;
export const HOLD_CHANGE = 0.5;

export function heldForReview(next, previous, approvedWeeks) {
  if (approvedWeeks < REVIEWED_WEEKS || !previous) return true;
  const medians = (r) => {
    const m = new Map();
    for (const meter of METERS) {
      for (const c of r.meters[meter] || []) {
        for (const [t, s] of Object.entries(c.ratios)) m.set(`${meter}/${c.plan}/${t}`, s.median);
        if (c.unexplained_pct) m.set(`${meter}/${c.plan}/unexplained`, c.unexplained_pct.median);
      }
    }
    return m;
  };
  const a = medians(previous);
  for (const [k, v] of medians(next)) {
    const old = a.get(k);
    if (old > 0 && Math.abs(v - old) / old > HOLD_CHANGE) return true;
  }
  for (const rows of Object.values(next.versions)) if (rows.some((r) => r.verdict === "flagged")) return true;
  return false;
}
