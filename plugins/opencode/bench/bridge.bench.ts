// Per-call overhead of the plugin-to-engine bridge.
//
//   node bench/bridge.bench.ts            (TROVE_BIN=<built trove> to use a given binary)
//
// Each case sends the same message two ways:
//   plugin  through the plugin's Core and Bridge, to the real `trove engine serve`
//   direct  the same JSON lines written straight to a second `trove engine serve`
//           with a minimal reader, no Core and no Bridge logic
// The engine does the same work in both, so (plugin - direct) is what the plugin's
// own code costs on top of the pipes; (direct) is the pipe and engine floor. Both
// are wall-clock round trips as the plugin sees them: "measured", on this machine,
// with the spread shown. Nothing here is a model call.

import { spawn } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Bridge } from "../src/bridge.ts";
import { Core } from "../src/core.ts";
import type { SessionCtx } from "../src/core.ts";
import { configWith, troveBinary } from "../test/helpers.ts";

const N = Number(process.env["BENCH_N"] ?? 300);

const dir = mkdtempSync(join(tmpdir(), "trove-opencode-bench-"));
writeFileSync(join(dir, "config.toml"), configWith({ noise_normalizer: "on", hook_quiet_commands: "on", hook_read_limit: "on", volatile_prefix_mover: "on" }));
process.env["TROVE_DATA_DIR"] = dir;

const bin = troveBinary();
const s: SessionCtx = { id: "bench", login: "api_key", provider: "anthropic", model: "claude-opus-5-5" };

const noisy = (n: number) => "\u001b[31merr\u001b[0m\n" + "retrying the cache server connection\n".repeat(n);
const small = noisy(30);
const large = noisy(Math.ceil(100_000 / 37));
const reqBody = (text: string) =>
  JSON.stringify({
    model: "claude-opus-5-5",
    system: "You are a coding agent.\nToday's date: 2026-10-29\nBe brief.\n",
    messages: [
      { role: "user", content: "go" },
      { role: "assistant", content: [{ type: "tool_use", id: "a", name: "Bash", input: {} }] },
      { role: "user", content: [{ type: "tool_result", tool_use_id: "a", content: text }] },
    ],
  });
const bigReq = reqBody(noisy(Math.ceil(190_000 / 37)));

function stats(xs: number[]): { p50: number; p95: number; max: number } {
  const a = [...xs].sort((x, y) => x - y);
  const at = (q: number) => a[Math.min(a.length - 1, Math.floor(q * a.length))] ?? 0;
  return { p50: at(0.5), p95: at(0.95), max: a[a.length - 1] ?? 0 };
}

async function time(n: number, fn: (i: number) => Promise<unknown>): Promise<number[]> {
  for (let i = 0; i < 20; i++) await fn(i); // warm up
  const out: number[] = [];
  for (let i = 0; i < n; i++) {
    const t0 = process.hrtime.bigint();
    await fn(i);
    out.push(Number(process.hrtime.bigint() - t0) / 1e6);
  }
  return out;
}

/** The same protocol with no plugin code at all: lines in, lines out. */
function rawEngine() {
  const child = spawn(bin, ["engine", "serve"], { stdio: ["pipe", "pipe", "ignore"] });
  let buf = "";
  const waiters = new Map<number, () => void>();
  child.stdout.setEncoding("utf8");
  child.stdout.on("data", (c: string) => {
    buf += c;
    let nl;
    while ((nl = buf.indexOf("\n")) >= 0) {
      const line = buf.slice(0, nl);
      buf = buf.slice(nl + 1);
      const id = (JSON.parse(line) as { id: number }).id;
      waiters.get(id)?.();
      waiters.delete(id);
    }
  });
  let next = 1;
  return {
    call: (m: object) =>
      new Promise<void>((resolve) => {
        const id = next++;
        waiters.set(id, resolve);
        child.stdin.write(JSON.stringify({ ...m, id }) + "\n");
      }),
    close: () => child.stdin.end(),
  };
}

const core = new Core(new Bridge({ binary: bin, timeoutMs: 10_000 }), { cannot: ["deny"], requestTimeoutMs: 10_000 });
const coldStart0 = process.hrtime.bigint();
await core.bridge.start();
const coldStartMs = Number(process.hrtime.bigint() - coldStart0) / 1e6;
const raw = rawEngine();
const session = { key: "bench", harness: "opencode", login: "api_key", agent: "other" };
await raw.call({ method: "hello" });

const cases: { name: string; plugin: (i: number) => Promise<unknown>; direct: (i: number) => Promise<unknown> }[] = [
  {
    name: "tool_input (a Bash command)",
    plugin: (i) => core.toolBefore(s, "bash", "c" + i, { command: "npm install" }),
    direct: () => raw.call({ method: "tool_input", session, call: { name: "Bash", input: { command: "npm install" } } }),
  },
  {
    name: "tool_output, 1 KB",
    plugin: (i) => core.toolAfter(s, "bash", "c" + i, { command: "x" }, small),
    direct: () => raw.call({ method: "tool_output", session, call: { name: "Bash", input: { command: "x" } }, output: { parts: [{ text: small }] } }),
  },
  {
    name: "tool_output, 100 KB",
    plugin: (i) => core.toolAfter(s, "bash", "c" + i, { command: "x" }, large),
    direct: () => raw.call({ method: "tool_output", session, call: { name: "Bash", input: { command: "x" } }, output: { parts: [{ text: large }] } }),
  },
  {
    name: "request, 190 KB",
    plugin: () => core.request(s, "anthropic", "/v1/messages", new TextEncoder().encode(bigReq)),
    direct: () => raw.call({ method: "request", session, request: { provider: "anthropic", path: "/v1/messages", body: Buffer.from(bigReq).toString("base64") } }),
  },
];

const rows: string[] = [];
const results: Record<string, unknown> = {};
for (const c of cases) {
  const d = stats(await time(N, c.direct));
  const p = stats(await time(N, c.plugin));
  const over = p.p50 - d.p50;
  rows.push(`${c.name.padEnd(30)} direct p50 ${d.p50.toFixed(2).padStart(7)}  p95 ${d.p95.toFixed(2).padStart(7)}  |  plugin p50 ${p.p50.toFixed(2).padStart(7)}  p95 ${p.p95.toFixed(2).padStart(7)}  |  plugin code adds p50 ${over.toFixed(2)} ms`);
  results[c.name] = { direct: d, plugin: p, added_p50_ms: over };
}

console.log(`bridge overhead, ${N} calls per case, milliseconds, node ${process.version} on ${process.platform}/${process.arch}`);
console.log(`cold start (spawn + hello): ${coldStartMs.toFixed(1)} ms`);
for (const r of rows) console.log(r);
if (process.env["BENCH_JSON"]) console.log(JSON.stringify({ n: N, cold_start_ms: coldStartMs, node: process.version, platform: process.platform, arch: process.arch, results }));

raw.close();
await core.close();
