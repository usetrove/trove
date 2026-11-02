import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import worker from "./index.js";

const golden = readFileSync(new URL("../../internal/contribute/testdata/payload.golden.json", import.meta.url), "utf8");

// A stand-in for the D1 binding that records what was run.
function fakeEnv() {
  const calls = [];
  return {
    calls,
    DB: {
      prepare: (sql) => ({
        bind: (...args) => ({ run: async () => { calls.push({ sql, args }); } }),
      }),
    },
  };
}

const post = (body) => new Request("https://x.test/v1/contribute", { method: "POST", body });

test("a valid summary is stored and answered with an empty 204", async () => {
  const env = fakeEnv();
  const res = await worker.fetch(post(golden), env);
  assert.equal(res.status, 204);
  assert.equal(await res.text(), "");
  assert.equal(env.calls.length, 1);
  assert.match(env.calls[0].sql, /INSERT/);
  assert.equal(env.calls[0].args[1], "0123456789abcdef0123456789abcdef");
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

test("delete removes the rows for one install id and answers 204", async () => {
  const env = fakeEnv();
  const res = await worker.fetch(new Request("https://x.test/v1/contribute/0123456789abcdef0123456789abcdef", { method: "DELETE" }), env);
  assert.equal(res.status, 204);
  assert.match(env.calls[0].sql, /DELETE/);
  const bad = await worker.fetch(new Request("https://x.test/v1/contribute/not-an-id", { method: "DELETE" }), env);
  assert.equal(bad.status, 400);
});

test("other routes are 404", async () => {
  assert.equal((await worker.fetch(new Request("https://x.test/"), fakeEnv())).status, 404);
});
