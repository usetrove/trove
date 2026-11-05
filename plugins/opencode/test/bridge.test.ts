import assert from "node:assert/strict";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { Bridge } from "../src/bridge.ts";

const fake = join(dirname(fileURLToPath(import.meta.url)), "fixtures", "fake-engine.mjs");

function bridgeFor(mode: string, opts: { timeoutMs?: number; log?: (l: string) => void } = {}): Bridge {
  process.env["MODE"] = mode; // the child inherits it
  return new Bridge({ binary: process.execPath, args: [fake], timeoutMs: opts.timeoutMs ?? 300, startTimeoutMs: 2000, log: opts.log });
}

test("a working engine answers and the bridge learns what it can do", async () => {
  const b = bridgeFor("echo");
  const rep = await b.call({ method: "capabilities" });
  assert.equal(rep?.ok, true);
  assert.equal(b.available, true);
  assert.equal(b.recoverable, true);
  await b.close();
});

test("a missing program turns Trove off without throwing", async () => {
  const lines: string[] = [];
  const b = new Bridge({ binary: "trove-does-not-exist-xyz", log: (l) => lines.push(l) });
  assert.equal(await b.call({ method: "capabilities" }), null);
  assert.equal(await b.call({ method: "capabilities" }), null);
  assert.equal(b.available, false);
  assert.equal(lines.filter((l) => l.includes("not found")).length, 1, "tell the user once, not on every call");
});

test("a protocol this plugin does not speak turns Trove off", async () => {
  const lines: string[] = [];
  const b = bridgeFor("badproto", { log: (l) => lines.push(l) });
  assert.equal(await b.call({ method: "capabilities" }), null);
  assert.equal(b.available, false);
  assert.ok(lines.some((l) => l.includes("different protocol")));
  await b.close();
});

test("a reply that is too slow is dropped and the call yields null at the deadline", async () => {
  const b = bridgeFor("slow", { timeoutMs: 100 });
  const t0 = Date.now();
  assert.equal(await b.call({ method: "capabilities" }), null);
  assert.ok(Date.now() - t0 < 350, "waited far past the timeout");
  // The late reply for the dropped call must not be taken for the next call's.
  const rep = await b.call({ method: "capabilities" }, 1000);
  assert.equal(rep?.ok, true);
  await b.close();
});

test("a crash gives null for the call and the engine is started again for the next", async () => {
  const b = bridgeFor("crash");
  assert.equal(await b.call({ method: "capabilities" }), null);
  process.env["MODE"] = "echo"; // the restarted child inherits the new mode
  const rep = await b.call({ method: "capabilities" });
  assert.equal(rep?.ok, true);
  await b.close();
});

test("an engine that keeps crashing is given up on instead of restarted forever", async () => {
  const b = bridgeFor("crash");
  for (let i = 0; i < 6; i++) assert.equal(await b.call({ method: "capabilities" }), null);
  assert.equal(b.available, false);
  await b.close();
});

test("output that is not JSON, or an error line with no id, never hangs the caller", async () => {
  for (const mode of ["garbage", "noid"]) {
    const b = bridgeFor(mode, { timeoutMs: 2000 });
    const t0 = Date.now();
    assert.equal(await b.call({ method: "capabilities" }), null, mode);
    assert.ok(Date.now() - t0 < 1500, `${mode}: the caller waited for the full timeout`);
    await b.close();
  }
});

test("a silent engine ends the start-up handshake at its deadline", async () => {
  process.env["MODE"] = "silent";
  const b = new Bridge({ binary: process.execPath, args: [fake], startTimeoutMs: 150 });
  const t0 = Date.now();
  assert.equal(await b.call({ method: "capabilities" }), null);
  assert.ok(Date.now() - t0 < 1000);
  await b.close();
});

test("calls after close() return null", async () => {
  const b = bridgeFor("echo");
  assert.equal((await b.call({ method: "capabilities" }))?.ok, true);
  await b.close();
  assert.equal(await b.call({ method: "capabilities" }), null);
});
