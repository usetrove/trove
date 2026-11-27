import test from "node:test";
import assert from "node:assert/strict";
import { validate } from "./validate.js";

const good = () => ({
  schema_version: 1, trove_version: "1.2.3", os: "linux", arch: "amd64", period_days: 31,
  requests_bucket: "100-999", agents: ["claude-code", "other"],
  waste_percent: { percent: 12, label: "estimated" },
  optimizers: [{ name: "output_trimmer", effective: "on", rolled_back: false, percent_fewer_measured: 19 }],
  guardrail_trips: 1, install_id: "0123456789abcdef0123456789abcdef",
});

test("accepts what the client sends", () => assert.equal(validate(good()), null));

test("accepts the optional fields being absent", () => {
  const p = good();
  delete p.waste_percent;
  p.optimizers = [{ name: "trim", effective: "shadow", rolled_back: true }];
  assert.equal(validate(p), null);
});

test("rejects unknown fields at every level", () => {
  const top = good(); top.prompt = "x";
  const opt = good(); opt.optimizers[0].path = "/home/me";
  const w = good(); w.waste_percent.extra = 1;
  for (const p of [top, opt, w]) assert.notEqual(validate(p), null);
});

test("rejects free text where a fixed vocabulary is expected", () => {
  const a = good(); a.agents = ["my-private-agent"];
  const b = good(); b.requests_bucket = "342";
  const c = good(); c.optimizers[0].name = "/home/me/project";
  const d = good(); d.trove_version = "1.2.3 /home/me";
  const e = good(); e.install_id = "not-an-id";
  for (const p of [a, b, c, d, e]) assert.notEqual(validate(p), null);
});

test("rejects wrong types, ranges and schema versions", () => {
  const a = good(); a.period_days = 1.5;
  const b = good(); b.schema_version = 3;
  const c = good(); c.guardrail_trips = -1;
  for (const p of [a, b, c, null, [], "x"]) assert.notEqual(validate(p), null);
});

test("the reason never repeats submitted text", () => {
  const p = good(); p.optimizers[0].name = "SECRET-VALUE";
  assert.ok(!String(validate(p)).includes("SECRET"));
});

test("accepts the payload the Go client produces (golden file)", async () => {
  const { readFileSync } = await import("node:fs");
  const p = JSON.parse(readFileSync(new URL("../../internal/contribute/testdata/payload.golden.json", import.meta.url), "utf8"));
  assert.equal(validate(p), null);
});

const limits = () => ({
  plan: "max", claude_code_version: "2.1.90",
  five_hour: { ratios: { output: { ratio: 5, width: 1.2 }, cache_read: { ratio: 0.083, width: 0.051 } }, unexplained_pct: 12 },
  seven_day: { unexplained_pct: 15 },
  version_checks: [
    { version: "2.1.90", metric: "limit_pct_per_1k_output_equivalent", status: "alert", cause: "client" },
    { version: "2.1.89", metric: "unexplained_rebuild_rate", status: "normal" },
  ],
});
const withLimits = () => ({ ...good(), schema_version: 2, limits: limits() });

test("accepts the limits section on schema 2, and schema 1 without it", () => {
  assert.equal(validate(withLimits()), null);
  assert.equal(validate({ ...good(), schema_version: 2 }), null);
  assert.notEqual(validate({ ...withLimits(), schema_version: 1 }), null);
});

test("rejects anything in limits outside the approved fields", () => {
  const cases = [
    (l) => { l.model = "x"; },
    (l) => { l.plan = "Max 20x"; },
    (l) => { l.claude_code_version = "2.1.90 /home/me"; },
    (l) => { l.five_hour.weights = {}; },
    (l) => { l.five_hour.ratios.input = { ratio: 1, width: 0 }; },
    (l) => { l.five_hour.ratios.output.lo = 4; },
    (l) => { l.five_hour.ratios.output.ratio = -1; },
    (l) => { l.five_hour.unexplained_pct = 12.5; },
    (l) => { l.version_checks[0].key = "2026-11-20"; },
    (l) => { l.version_checks[0].cause = undefined; },
    (l) => { l.version_checks[1].cause = "client"; },
    (l) => { l.version_checks[1].status = "too_little_data"; },
    (l) => { l.version_checks[1].metric = "secret"; },
    (l) => { l.version_checks = ["1", "2", "3", "4", "5", "6"].map((v) => ({ version: `2.1.${v}`, metric: "unexplained_rebuild_rate", status: "normal" })); },
  ];
  for (const change of cases) {
    const p = withLimits();
    change(p.limits);
    assert.notEqual(validate(p), null, String(change));
  }
});
