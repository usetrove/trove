import test from "node:test";
import assert from "node:assert/strict";
import { aggregate, heldForReview, isoWeek, weekDays, roundRatio, MIN_GROUP, REVIEWED_WEEKS } from "./aggregate.js";
import { renderPage } from "./page.js";
import worker, { publishWeek, WEEKLY_CRON } from "./index.js";

const id = (i) => i.toString(16).padStart(32, "0");

// row makes one stored summary. checks are [version, metric, status, cause].
function row(i, { plan = "max", output = 5, unexplained = 10, checks = [], day = "2026-11-24" } = {}) {
  return {
    received_day: day,
    install_id: id(i),
    payload: JSON.stringify({
      schema_version: 2,
      install_id: id(i),
      limits: {
        plan,
        five_hour: { ratios: { output: { ratio: output, width: 1 } }, unexplained_pct: unexplained },
        version_checks: checks.map(([version, metric, status, cause]) => ({ version, metric, status, ...(cause ? { cause } : {}) })),
      },
    }),
  };
}

const range = (n, f) => Array.from({ length: n }, (_, i) => f(i));

test("nothing is published for a group under the minimum", () => {
  const r = aggregate(range(MIN_GROUP - 1, (i) => row(i)), "2026-W48");
  assert.equal(r.installs, null);
  assert.deepEqual(r.meters.five_hour, []);
  assert.ok(!JSON.stringify(r).includes(String(MIN_GROUP - 1)));
});

test("a big enough group gets the median and middle half, never the extremes", () => {
  const rows = range(MIN_GROUP, (i) => row(i, { output: i === 0 ? 0.5 : i === 1 ? 900 : 4 + i / 10 }));
  const r = aggregate(rows, "2026-W48");
  const cell = r.meters.five_hour.find((c) => c.plan === "all");
  assert.ok(cell);
  const s = cell.ratios.output;
  assert.ok(s.median > 4 && s.median < 7, JSON.stringify(s));
  assert.ok(s.p25 > 4 && s.p75 < 7, JSON.stringify(s));
  const text = JSON.stringify(r);
  assert.ok(!text.includes("900") && !text.includes("0.5,"), text);
  assert.ok(!text.includes(id(3)), "install ids must never appear");
});

test("one install counts once a week, with its newest summary", () => {
  const rows = range(MIN_GROUP, (i) => row(i, { output: 5 }));
  rows.push(row(0, { output: 50, day: "2026-11-23" })); // older and different
  const r = aggregate(rows, "2026-W48");
  const s = r.meters.five_hour[0].ratios.output;
  assert.equal(s.installs, MIN_GROUP);
  assert.equal(s.p75, 5);
});

test("per-plan groups are shown only when the rest is empty or big enough", () => {
  const maxes = range(MIN_GROUP, (i) => row(i, { plan: "max" }));
  const few = range(3, (i) => row(100 + i, { plan: "pro" }));
  let r = aggregate([...maxes, ...few], "w");
  assert.deepEqual(r.meters.five_hour.map((c) => c.plan), ["all"], "all minus max would describe 3 pro installs");
  const pros = range(MIN_GROUP, (i) => row(200 + i, { plan: "pro" }));
  r = aggregate([...maxes, ...pros], "w");
  assert.deepEqual(r.meters.five_hour.map((c) => c.plan), ["all", "max", "pro"]);
});

test("version flags need enough installs, enough alerts and a jump over earlier versions", () => {
  const M = "limit_pct_per_1k_output_equivalent";
  const rows = range(MIN_GROUP * 2, (i) => row(i, {
    checks: [
      ["2.1.88", M, i < 2 ? "alert" : "normal", i < 2 ? "client" : undefined],
      ["2.1.89", M, i < 2 ? "alert" : "normal", i < 2 ? "client" : undefined],
      ["2.1.90", M, i < 12 ? "alert" : "normal", i < 12 ? "client" : undefined],
      ...(i < 3 ? [["2.1.91", M, "alert", "client"]] : []),
      ["2.1.92", M, i < 12 ? "alert" : "normal", i < 12 ? "server" : undefined],
    ],
  }));
  const v = Object.fromEntries(aggregate(rows, "w").versions[M].map((r) => [r.version, r]));
  assert.equal(v["2.1.88"].verdict, "no_baseline");
  assert.equal(v["2.1.89"].verdict, "no_signal");
  assert.equal(v["2.1.90"].verdict, "flagged");
  assert.equal(v["2.1.90"].alerting_pct, 30);
  assert.deepEqual(v["2.1.91"], { version: "2.1.91", verdict: "too_few_installs" }, "no numbers for a small version");
  assert.equal(v["2.1.92"].verdict, "no_signal", "a server-side cause is not the version's fault");
});

test("rows without a limits section, or broken ones, are ignored", () => {
  const rows = [{ received_day: "2026-11-24", install_id: id(1), payload: '{"schema_version":1}' }, { install_id: id(2), payload: "{" }];
  assert.equal(aggregate(rows, "w").installs, null);
});

test("the first weeks, big moves and new flags wait for a maintainer", () => {
  const rows = range(MIN_GROUP, (i) => row(i, { output: 5 }));
  const a = aggregate(rows, "w1");
  assert.equal(heldForReview(a, a, REVIEWED_WEEKS - 1), true);
  assert.equal(heldForReview(a, a, REVIEWED_WEEKS), false);
  const b = aggregate(range(MIN_GROUP, (i) => row(i, { output: 9 })), "w2");
  assert.equal(heldForReview(b, a, REVIEWED_WEEKS), true);
});

test("iso weeks and rounding", () => {
  assert.equal(isoWeek("2026-11-26"), "2026-W48");
  assert.equal(isoWeek("2027-01-01"), "2026-W53");
  assert.equal(isoWeek("2026-01-01"), "2026-W01");
  assert.deepEqual([weekDays("2026-11-26")[0], weekDays("2026-11-26")[6]], ["2026-11-23", "2026-11-29"]);
  for (const [v, want] of [[0.0834, 0.083], [1.27, 1.3], [12.6, 13], [0, 0]]) assert.equal(roundRatio(v), want);
});

test("the page has no scripts or outside resources and escapes what it prints", () => {
  const r = aggregate(range(MIN_GROUP, (i) => row(i)), "<script>");
  const html = renderPage(r);
  assert.ok(!/<script/i.test(html));
  assert.ok(!/(src|href)=/i.test(html));
  assert.ok(html.includes("Estimated") && html.includes("not official"));
});

function fakeDB({ rows = [], approved = [] } = {}) {
  const stored = [...approved.map((r, i) => ({ week: `2026-W${40 + i}`, report: JSON.stringify(r), approved: 1 }))];
  return {
    stored,
    prepare: (sql) => ({
      bind: (...args) => ({
        all: async () => ({ results: rows.filter((r) => r.received_day >= args[0] && r.received_day <= args[1]) }),
        first: async () => {
          const ok = stored.filter((s) => s.approved === 1).sort((a, b) => (a.week < b.week ? 1 : -1));
          if (/COUNT/.test(sql)) return { n: ok.length };
          return ok[0] ? { report: ok[0].report } : null;
        },
        run: async () => { if (/INSERT/.test(sql)) stored.push({ week: args[0], report: args[1], approved: args[2] }); },
      }),
    }),
  };
}

test("the weekly job stores only the aggregate, held for review at first", async () => {
  const rows = range(MIN_GROUP, (i) => row(i));
  const env = { DB: fakeDB({ rows }) };
  const monday = Date.parse("2026-11-30T04:41:00Z");
  await worker.scheduled({ cron: WEEKLY_CRON }, env, monday);
  assert.equal(env.DB.stored.length, 1);
  const s = env.DB.stored[0];
  assert.equal(s.week, "2026-W48");
  assert.equal(s.approved, 0);
  assert.ok(!s.report.includes(id(1)));
  const res = await worker.fetch(new Request("https://x.test/v1/limit-report"), env);
  assert.equal(res.status, 404, "an unapproved week is not served");
});

test("an approved report is served as HTML and JSON", async () => {
  const r = aggregate(range(MIN_GROUP, (i) => row(i)), "2026-W47");
  const env = { DB: fakeDB({ approved: [r] }) };
  const html = await worker.fetch(new Request("https://x.test/v1/limit-report"), env);
  assert.equal(html.status, 200);
  assert.match(html.headers.get("content-security-policy"), /default-src 'none'/);
  assert.match(await html.text(), /Limit report/);
  const json = await worker.fetch(new Request("https://x.test/v1/limit-report.json"), env);
  assert.deepEqual(await json.json(), r);
  const { held } = await publishWeek({ DB: fakeDB({ approved: [r, r, r, r], rows: range(MIN_GROUP, (i) => row(i)) }) }, Date.parse("2026-11-30T05:00:00Z"));
  assert.equal(held, false, "after four approved weeks a steady week publishes itself");
});
