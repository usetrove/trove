import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { DatabaseSync } from "node:sqlite";
import { Bridge } from "../src/bridge.ts";
import { Core } from "../src/core.ts";
import { v1Hooks } from "../src/v1.ts";
import { setupV2 } from "../src/v2.ts";
import type { V2Context } from "../src/v2.ts";
import { configWith, dataDir, troveBinary, withDataDir } from "./helpers.ts";

// The OpenCode glue against the REAL engine. Each test gets its own Trove data
// folder (never the real ~/.trove) and reads back the rows the engine recorded.

const ON = { noise_normalizer: "on", hook_quiet_commands: "on", hook_read_limit: "on", hook_repeat_read: "on" };
const NOISY = "\u001b[31merr\u001b[0m start\n" + "retrying the cache server connection\n".repeat(30) + "done\n";

interface Event {
  harness: string;
  login: string;
  kind: string;
  optimizer: string;
  state: string;
  provider: string;
  model: string;
  session_key: string;
  cost_usd: number | null;
  in_tokens: number | null;
}

async function withCore(cfg: string, cannot: ("deny" | "rewrite_input" | "rewrite_output" | "rewrite_request")[], fn: (core: Core) => Promise<void>): Promise<Event[]> {
  const dir = dataDir(cfg);
  await withDataDir(dir, async () => {
    const core = new Core(new Bridge({ binary: troveBinary(), timeoutMs: 5000 }), { cannot, requestTimeoutMs: 5000 });
    try {
      assert.equal(await core.bridge.start(), true, "the engine must start");
      await fn(core);
    } finally {
      await core.close(); // lets the engine write what it recorded
    }
  });
  const db = new DatabaseSync(join(dir, "trove.db"), { readOnly: true });
  try {
    return db.prepare("SELECT harness, login, kind, optimizer, state, provider, model, session_key, cost_usd, in_tokens FROM adapter_events ORDER BY id").all() as unknown as Event[];
  } finally {
    db.close();
  }
}

// --- V1 -----------------------------------------------------------------------

async function v1Turn(h: ReturnType<typeof v1Hooks>, sessionID: string, providerID: string, source: string): Promise<void> {
  await h["chat.message"]?.({ sessionID, model: { providerID, modelID: "m-1" } }, {});
  await h["chat.params"]?.({ sessionID, model: { providerID, id: "m-1" }, provider: { source } }, {});
}

test("V1, API key from the environment: arguments and output are rewritten, and the engine records it", async () => {
  const big = join(mkdtempSync(join(tmpdir(), "oc-read-")), "big.txt");
  writeFileSync(big, "a line of a large file\n".repeat(4000));
  const events = await withCore(configWith(ON), ["deny", "rewrite_request"], async (core) => {
    const h = v1Hooks(core);
    await v1Turn(h, "s1", "anthropic", "env");

    const bash = { command: "npm install" };
    await h["tool.execute.before"]?.({ tool: "bash", sessionID: "s1", callID: "c1" }, { args: bash });
    assert.equal(bash.command, "npm install --quiet");

    const out = { title: "t", output: NOISY, metadata: {} };
    await h["tool.execute.after"]?.({ tool: "bash", sessionID: "s1", callID: "c1", args: bash }, out);
    assert.match(out.output, /\(repeated 30 times\)/);
    assert.doesNotMatch(out.output, /\u001b\[/);

    // OpenCode names the path `filePath`; the limit is written back under OpenCode's own key.
    const read: Record<string, unknown> = { filePath: big };
    await h["tool.execute.before"]?.({ tool: "read", sessionID: "s1", callID: "c2" }, { args: read });
    assert.equal(read["limit"], 400);
    assert.equal(read["filePath"], big);
  });
  const kinds = events.map((e) => `${e.optimizer}:${e.kind}`).sort();
  assert.deepEqual(kinds, ["hook_quiet_commands:rewritten", "hook_read_limit:rewritten", "noise_normalizer:rewritten"]);
  for (const e of events) {
    assert.equal(e.harness, "opencode");
    assert.equal(e.login, "api_key");
    assert.equal(e.state, "on");
    assert.match(e.session_key, /^h-[0-9a-f]{24}$/, "the harness's own session id must not be stored");
    assert.equal(e.provider, "anthropic");
  }
});

test("V1, a stored login (could be OAuth): nothing is changed, and the engine only counts", async () => {
  const events = await withCore(configWith(ON), ["deny", "rewrite_request"], async (core) => {
    const h = v1Hooks(core);
    await v1Turn(h, "s2", "openai", "api"); // a ChatGPT login looks exactly like this
    const bash = { command: "npm install" };
    await h["tool.execute.before"]?.({ tool: "bash", sessionID: "s2", callID: "c1" }, { args: bash });
    assert.equal(bash.command, "npm install");
    const out = { title: "t", output: NOISY, metadata: {} };
    await h["tool.execute.after"]?.({ tool: "bash", sessionID: "s2", callID: "c1", args: bash }, out);
    assert.equal(out.output, NOISY);
  });
  assert.ok(events.length >= 2, JSON.stringify(events));
  for (const e of events) {
    assert.equal(e.login, "subscription_or_unknown");
    assert.equal(e.state, "shadow");
    assert.match(e.kind, /^would_/);
  }
});

test("V1, before the provider is known, the session counts as unclassified and is shadow-only", async () => {
  await withCore(configWith(ON), ["deny", "rewrite_request"], async (core) => {
    const h = v1Hooks(core);
    const bash = { command: "npm install" };
    await h["tool.execute.before"]?.({ tool: "bash", sessionID: "never-seen", callID: "c" }, { args: bash });
    assert.equal(bash.command, "npm install");
  });
});

test("V1, GitHub Copilot: rewritten like an API key, but no cost is recorded for usage", async () => {
  const events = await withCore(configWith(ON), ["deny", "rewrite_request"], async (core) => {
    const h = v1Hooks(core);
    await v1Turn(h, "s3", "github-copilot", "api");
    const bash = { command: "npm install" };
    await h["tool.execute.before"]?.({ tool: "bash", sessionID: "s3", callID: "c1" }, { args: bash });
    assert.equal(bash.command, "npm install --quiet");
    await h.event?.({
      event: {
        type: "message.updated",
        properties: {
          info: { role: "assistant", id: "msg1", sessionID: "s3", providerID: "github-copilot", modelID: "gpt-5.1", time: { completed: 1 }, cost: 0, tokens: { input: 1200, output: 80, reasoning: 0, cache: { read: 1000, write: 0 } } },
        },
      },
    });
    // The same message again must not be counted twice.
    await h.event?.({ event: { type: "message.updated", properties: { info: { role: "assistant", id: "msg1", sessionID: "s3", time: { completed: 2 }, tokens: { input: 1200, output: 80, cache: { read: 0, write: 0 } } } } } });
  });
  const usage = events.filter((e) => e.kind === "usage");
  assert.equal(usage.length, 1);
  assert.equal(usage[0]?.login, "copilot");
  assert.equal(usage[0]?.in_tokens, 1200);
  assert.equal(usage[0]?.cost_usd, null, "a subscription login's 0 cost would read as free, not unknown");
  assert.equal(usage[0]?.model, "gpt-5.1");
});

test("V1, API key: usage carries the harness-reported cost, and a tool error is an outcome", async () => {
  const events = await withCore(configWith({}), ["deny", "rewrite_request"], async (core) => {
    const h = v1Hooks(core);
    await v1Turn(h, "s4", "anthropic", "env");
    await h.event?.({ event: { type: "message.updated", properties: { info: { role: "assistant", id: "m9", sessionID: "s4", providerID: "anthropic", modelID: "claude-sonnet-5-5", time: { completed: 1 }, cost: 0.0123, tokens: { input: 10, output: 5, cache: { read: 0, write: 0 } } } } } });
    await h.event?.({ event: { type: "message.part.updated", properties: { part: { type: "tool", id: "p1", sessionID: "s4", state: { status: "error" } } } } });
    await h.event?.({ event: { type: "message.part.updated", properties: { part: { type: "tool", id: "p1", sessionID: "s4", state: { status: "error" } } } } });
  });
  const usage = events.find((e) => e.kind === "usage");
  assert.ok(usage, "no usage row recorded: " + JSON.stringify(events));
  assert.equal(usage.cost_usd, 0.0123);
  assert.equal(events.filter((e) => e.kind === "outcome").length, 1, "the same failed call must be counted once");
});

test("V1, a trove program that is missing or broken never changes or breaks a call", async () => {
  const core = new Core(new Bridge({ binary: "trove-does-not-exist-xyz" }), { cannot: ["deny"] });
  const h = v1Hooks(core);
  await v1Turn(h, "s5", "anthropic", "env");
  const bash = { command: "npm install" };
  await h["tool.execute.before"]?.({ tool: "bash", sessionID: "s5", callID: "c" }, { args: bash });
  assert.equal(bash.command, "npm install");
  const out = { title: "t", output: NOISY, metadata: {} };
  await h["tool.execute.after"]?.({ tool: "bash", sessionID: "s5", callID: "c", args: bash }, out);
  assert.equal(out.output, NOISY);
  await h.dispose?.();
});

test("V1, a block the harness cannot enforce is counted as would_block, never as blocked", async () => {
  const f = join(mkdtempSync(join(tmpdir(), "oc-rep-")), "small.txt");
  writeFileSync(f, "hello\n");
  const events = await withCore(configWith({ hook_repeat_read: "on" }), ["deny", "rewrite_request"], async (core) => {
    const h = v1Hooks(core);
    await v1Turn(h, "s6", "anthropic", "env");
    const args = { filePath: f };
    await h["tool.execute.before"]?.({ tool: "read", sessionID: "s6", callID: "r1" }, { args });
    await h["tool.execute.after"]?.({ tool: "read", sessionID: "s6", callID: "r1", args }, { output: "hello" });
    const again = { filePath: f };
    await h["tool.execute.before"]?.({ tool: "read", sessionID: "s6", callID: "r2" }, { args: again });
    assert.deepEqual(again, { filePath: f }, "the read must go through");
  });
  assert.ok(events.some((e) => e.kind === "would_block" && e.state === "shadow"), JSON.stringify(events));
  assert.ok(!events.some((e) => e.kind === "blocked"));
});

// --- V2 -----------------------------------------------------------------------

type Handler = (event: any) => Promise<void> | void;

function fakeV2(connection: { type?: string; method?: string } | undefined) {
  const session = new Map<string, Handler>();
  const tool = new Map<string, Handler>();
  const ctx: V2Context = {
    session: { hook: async (name, cb) => void session.set(name, cb) },
    tool: { hook: async (name, cb) => void tool.set(name, cb) },
    integration: { connection: { active: async () => connection } },
  };
  return { ctx, session, tool };
}

const SECRET = "sk-ant-TEST-NOT-A-REAL-KEY";

function anthropicRequest(url: string, tool: string): Request {
  const body = JSON.stringify({
    model: "claude-opus-5-5",
    system: "s",
    messages: [
      { role: "user", content: "go" },
      { role: "assistant", content: [{ type: "tool_use", id: "a", name: "Bash", input: {} }] },
      { role: "user", content: [{ type: "tool_result", tool_use_id: "a", content: tool }] },
    ],
  });
  return new Request(url, { method: "POST", headers: { "x-api-key": SECRET, "content-type": "application/json", "anthropic-version": "2023-06-01" }, body });
}

test("V2, API key: the request body is replaced, the headers go out exactly as they were, and the new body reaches the server", async () => {
  const got: { body: string; headers: Record<string, string | string[] | undefined> }[] = [];
  const server = createServer((req, res) => {
    let b = "";
    req.on("data", (c) => (b += c));
    req.on("end", () => {
      got.push({ body: b, headers: req.headers });
      res.end("{}");
    });
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}/v1/messages`;
  try {
    const { ctx, session } = fakeV2({ type: "credential", method: "key" });
    await withCore(configWith({ noise_normalizer: "on" }), ["deny"], async (core) => {
      await setupV2(ctx, core);
      const req = anthropicRequest(url, NOISY);
      const original = await req.clone().text();
      const event: any = { sessionID: "v2s1", model: { providerID: "anthropic", id: "claude-opus-5-5" }, kind: "primary", request: req };
      await session.get("context")?.(event);
      await session.get("http.request")?.(event);
      const sent: Request = event.request;
      const body = await sent.clone().text();
      assert.notEqual(body, original, "the request body should have been rewritten");
      assert.match(body, /repeated 30 times/);
      assert.ok(body.length < original.length);
      // The credential header is carried over untouched; this test is the only code that reads it.
      assert.equal(sent.headers.get("x-api-key"), SECRET);
      assert.equal(sent.headers.get("anthropic-version"), "2023-06-01");
      assert.equal(sent.method, "POST");

      // What actually goes on the wire: the new body, with a consistent length.
      await fetch(sent);
      assert.equal(got.length, 1);
      assert.equal(got[0]?.body, body);
      assert.equal(got[0]?.headers["x-api-key"], SECRET);
      const len = got[0]?.headers["content-length"];
      assert.ok(len === undefined || Number(len) === Buffer.byteLength(body), `content-length ${String(len)} does not match the body`);
    });
  } finally {
    server.close();
  }
});

test("V2, a login that is not an API key, a non-Anthropic provider and title requests are left alone", async () => {
  const cases: { name: string; conn: { type?: string; method?: string } | undefined; provider: string; kind: string }[] = [
    { name: "oauth", conn: { type: "credential", method: "oauth" }, provider: "anthropic", kind: "primary" },
    { name: "unknown connection", conn: undefined, provider: "anthropic", kind: "primary" },
    { name: "other provider", conn: { type: "credential", method: "key" }, provider: "openai", kind: "primary" },
    { name: "title request", conn: { type: "credential", method: "key" }, provider: "anthropic", kind: "title" },
  ];
  for (const c of cases) {
    const { ctx, session } = fakeV2(c.conn);
    await withCore(configWith({ noise_normalizer: "on" }), ["deny"], async (core) => {
      await setupV2(ctx, core);
      const req = anthropicRequest("http://127.0.0.1:9/v1/messages", NOISY);
      const event: any = { sessionID: "v2s2", model: { providerID: c.provider, id: "m" }, kind: c.kind, request: req };
      await session.get("http.request")?.(event);
      assert.equal(event.request, req, `${c.name}: the request object itself must be untouched`);
    });
  }
});

test("V2, tools: input is rewritten in place and a string or text-part result is cleaned", async () => {
  const { ctx, session, tool } = fakeV2({ type: "credential", method: "key" });
  await withCore(configWith(ON), ["deny"], async (core) => {
    await setupV2(ctx, core);
    await session.get("context")?.({ sessionID: "v2s3", model: { providerID: "anthropic", id: "m" } });

    const before: any = { tool: "bash", sessionID: "v2s3", id: "t1", input: { command: "npm install" } };
    await tool.get("execute.before")?.(before);
    assert.equal(before.input.command, "npm install --quiet");

    const afterStr: any = { tool: "bash", sessionID: "v2s3", id: "t1", input: before.input, status: "completed", result: { content: NOISY, metadata: { m: 1 } } };
    await tool.get("execute.after")?.(afterStr);
    assert.match(afterStr.result.content, /repeated 30 times/);
    assert.deepEqual(afterStr.result.metadata, { m: 1 });

    const afterParts: any = { tool: "bash", sessionID: "v2s3", id: "t2", input: { command: "x" }, status: "completed", result: { content: [{ type: "text", text: NOISY }, { type: "text", text: "tail" }] } };
    await tool.get("execute.after")?.(afterParts);
    assert.match(afterParts.result.content[0].text, /repeated 30 times/);
    assert.equal(afterParts.result.content[1].text, "tail");

    // A result with a file part is not text the plugin understands: left alone.
    const file: any = { tool: "bash", sessionID: "v2s3", id: "t3", input: {}, status: "completed", result: { content: [{ type: "file", uri: "x" }] } };
    await tool.get("execute.after")?.(file);
    assert.deepEqual(file.result.content, [{ type: "file", uri: "x" }]);
  });
});

test("V2, an OAuth login is shadow-only for tools too", async () => {
  const { ctx, session, tool } = fakeV2({ type: "credential", method: "oauth" });
  await withCore(configWith(ON), ["deny"], async (core) => {
    await setupV2(ctx, core);
    await session.get("context")?.({ sessionID: "v2s4", model: { providerID: "openai", id: "m" } });
    const before: any = { tool: "bash", sessionID: "v2s4", id: "t1", input: { command: "npm install" } };
    await tool.get("execute.before")?.(before);
    assert.equal(before.input.command, "npm install");
  });
});

test("the experiment's control arm keeps a session in shadow, as it does on the gateway", async () => {
  // With the A/B experiment on, half of the sessions (balanced in blocks) run the
  // optimizer in shadow. Over a block of 4 sessions, both arms must appear.
  const dir = dataDir(configWith({ noise_normalizer: "on" }, { experiment: true }));
  const states: string[] = [];
  await withDataDir(dir, async () => {
    const core = new Core(new Bridge({ binary: troveBinary(), timeoutMs: 5000 }), { cannot: ["deny"] });
    try {
      for (let i = 0; i < 4; i++) {
        const rep = await core.bridge.call({ method: "session", session: { key: "exp-" + i, harness: "opencode", login: "api_key" } });
        states.push(rep?.states?.["noise_normalizer"] ?? "missing");
      }
    } finally {
      await core.close();
    }
  });
  assert.deepEqual([...states].sort(), ["on", "on", "shadow", "shadow"]);
});
