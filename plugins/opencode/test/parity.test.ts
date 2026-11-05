import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { Bridge } from "../src/bridge.ts";
import { Core } from "../src/core.ts";
import type { SessionCtx } from "../src/core.ts";
import { configWith, dataDir, troveBinary, withDataDir } from "./helpers.ts";

// Parity (Prompt 16): the plugin's glue must add no logic of its own. The Go test
// internal/gateway/parity_plugin_test.go computes, for every optimizer the plugin
// adapter reaches, what the gateway and the hooks adapter produce, and writes it
// to fixtures/parity.json. Here the same inputs go through the plugin's Core and
// the REAL `trove engine serve`, and the answers must be byte for byte the same.

interface Case {
  optimizer: string;
  text?: string;
  tool_output?: string;
  request: string; // base64
  request_out: string; // base64
}

const here = dirname(fileURLToPath(import.meta.url));
const fixtures = JSON.parse(readFileSync(join(here, "fixtures", "parity.json"), "utf8")) as { cases: Case[] };

// The output trimmer is "on" only when trimmed text can be fetched back (the
// recovery store and the MCP server); the Go parity test covers it with a store.
const NEEDS_RECOVERY = new Set(["output_trimmer"]);

const s: SessionCtx = { id: "parity-session", login: "api_key", provider: "anthropic", model: "claude-opus-5-5" };

test("fixtures exist for every request-level and tool-level optimizer", () => {
  const names = fixtures.cases.map((c) => c.optimizer).sort();
  assert.deepEqual(names, ["cache_breakpoints", "duplicate_result_collapser", "noise_normalizer", "output_trimmer", "volatile_prefix_mover"]);
});

for (const c of fixtures.cases) {
  test(`${c.optimizer}: the plugin returns what the gateway forwards`, async () => {
    const dir = dataDir(configWith({ [c.optimizer]: "on" }));
    await withDataDir(dir, async () => {
      const core = new Core(new Bridge({ binary: troveBinary(), timeoutMs: 5000 }), { cannot: ["deny"], requestTimeoutMs: 5000 });
      try {
        if (NEEDS_RECOVERY.has(c.optimizer)) {
          // Without the recovery store the trimmer stays in shadow, so nothing may change.
          const body = Buffer.from(c.request, "base64");
          assert.equal(await core.request(s, "anthropic", "/v1/messages", new Uint8Array(body)), null);
          assert.equal(await core.toolAfter(s, "bash", "call-0", { command: "go test ./..." }, c.text ?? ""), c.text ?? "");
          return;
        }

        // Request level.
        const body = Buffer.from(c.request, "base64");
        const out = await core.request(s, "anthropic", "/v1/messages", new Uint8Array(body));
        assert.ok(out, `${c.optimizer}: the engine returned no replacement`);
        assert.equal(Buffer.from(out).toString("utf8"), Buffer.from(c.request_out, "base64").toString("utf8"));

        // Tool level, for the optimizers that act on a tool's output.
        if (c.text && c.tool_output) {
          const got = await core.toolAfter(s, "bash", "call-1", { command: "go test ./..." }, c.text);
          assert.equal(got, c.tool_output, `${c.optimizer}: tool output differs from the gateway's`);
          assert.notEqual(got, c.text, "the fixture must make the optimizer act");
        }
      } finally {
        await core.close();
      }
    });
  });
}

test("an optimizer that is off changes nothing, whatever the input", async () => {
  const dir = dataDir(configWith({}));
  await withDataDir(dir, async () => {
    const core = new Core(new Bridge({ binary: troveBinary(), timeoutMs: 5000 }), { cannot: ["deny"] });
    try {
      const noisy = "\u001b[31merr\u001b[0m\n" + "retrying\n".repeat(30);
      assert.equal(await core.toolAfter(s, "bash", "c", { command: "x" }, noisy), noisy);
    } finally {
      await core.close();
    }
  });
});
