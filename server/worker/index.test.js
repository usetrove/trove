import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import worker, { MAX_PER_DAY } from "./index.js";

const golden = readFileSync(new URL("../../internal/contribute/testdata/payload.golden.json", import.meta.url), "utf8");

// A stand-in for the D1 binding that records what was run. `used` is how many
// summaries it says are already stored for today.
function fakeEnv(used = 0) {
  const calls = [];
  return {
    calls,
    DB: {
      prepare: (sql) => ({
        bind: (...args) => ({
          run: async () => { calls.push({ sql, args }); },
          first: async () => { calls.push({ sql, args }); return { n: used }; },
        }),
      }),
    },
  };
}

const post = (body) => new Request("https://x.test/v1/contribute", { method: "POST", body });
const inserts = (env) => env.calls.filter((c) => /INSERT/.test(c.sql));

test("a valid summary is stored and answered with an empty 204", async () => {
  const env = fakeEnv();
  const res = await worker.fetch(post(golden), env);
  assert.equal(res.status, 204);
  assert.equal(await res.text(), "");
  assert.equal(inserts(env).length, 1);
  assert.equal(inserts(env)[0].args[1], "0123456789abcdef0123456789abcdef");
  assert.match(inserts(env)[0].args[0], /^\d{4}-\d{2}-\d{2}$/); // a day, no time of day
});

test("an invalid summary is rejected and nothing is stored", async () => {
  const env = fakeEnv();
  const res = await worker.fetch(post('{"x":1}'), env);
  assert.equal(res.status, 400);
  assert.equal(env.calls.length, 0);
});

test("not JSON and oversized bodies are rejected", async () => {
  const env = fakeEnv();
  assert.equal((await worker.fetch(post("nope"), env)).status, 400);
  assert.equal((await worker.fetch(post("x".repeat(9000)), env)).status, 413);
  assert.equal(env.calls.length, 0);
});

test("once the daily cap is reached nothing more is stored", async () => {
  const full = fakeEnv(MAX_PER_DAY);
  const res = await worker.fetch(post(golden), full);
  assert.equal(res.status, 429);
  assert.equal(inserts(full).length, 0);
  const almost = fakeEnv(MAX_PER_DAY - 1);
  assert.equal((await worker.fetch(post(golden), almost)).status, 204);
  assert.equal(inserts(almost).length, 1);
});

test("a flood of invalid bodies never reaches the database", async () => {
  const env = fakeEnv();
  for (let i = 0; i < 50; i++) await worker.fetch(post(JSON.stringify({ schema_version: 1, junk: i })), env);
  assert.equal(env.calls.length, 0);
});

test("delete removes the rows for one install id and answers 204", async () => {
  const env = fakeEnv();
  const res = await worker.fetch(new Request("https://x.test/v1/contribute/0123456789abcdef0123456789abcdef", { method: "DELETE" }), env);
  assert.equal(res.status, 204);
  assert.match(env.calls[0].sql, /DELETE/);
  assert.equal(env.calls[0].args[0], "0123456789abcdef0123456789abcdef");
  const bad = await worker.fetch(new Request("https://x.test/v1/contribute/not-an-id", { method: "DELETE" }), env);
  assert.equal(bad.status, 400);
});

test("other routes and methods are 404", async () => {
  assert.equal((await worker.fetch(new Request("https://x.test/"), fakeEnv())).status, 404);
  assert.equal((await worker.fetch(new Request("https://x.test/v1/contribute"), fakeEnv())).status, 404);
});

test("the daily clean-up deletes rows older than 365 days and nothing newer", async () => {
  const env = fakeEnv();
  const before = Date.now();
  await worker.scheduled({}, env);
  assert.equal(env.calls.length, 1);
  assert.match(env.calls[0].sql, /DELETE FROM submissions WHERE received_day < \?/);
  const cutoff = new Date(env.calls[0].args[0] + "T00:00:00Z").getTime();
  const days = (before - cutoff) / 86400000;
  assert.ok(days >= 365 && days < 367, `cutoff is ${days.toFixed(1)} days ago`);
});

test("the worker never reads the client address or headers", () => {
  const src = readFileSync(new URL("./index.js", import.meta.url), "utf8");
  for (const forbidden of ["cf-connecting-ip", "x-forwarded-for", "request.cf", "console.log", "console.info", "console.error"]) {
    assert.ok(!src.toLowerCase().includes(forbidden), `index.js mentions ${forbidden}`);
  }
  assert.ok(!/headers\.get\((?!"content-length")/.test(src), "index.js reads a header other than content-length");
});
