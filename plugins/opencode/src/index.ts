import { Bridge } from "./bridge.ts";
import { Core } from "./core.ts";
import { v1Hooks } from "./v1.ts";
import type { V1Hooks } from "./v1.ts";
import { setupV2 } from "./v2.ts";
import type { V2Context } from "./v2.ts";

// Trove for OpenCode. One package, both plugin APIs OpenCode ships (verified
// 2026-11-05): `server` is the V1 entry (`opencode-ai` 1.x, config key `plugin`),
// `setup` the V2 entry (`@opencode/cli` 2.x, config key `plugins`). Both run the
// same engine through the same bridge; neither holds any optimization logic.
//
// Options (plugin options in opencode.json):
//   binary          path of the trove program (default: "trove" on PATH)
//   timeoutMs       longest a tool-level call may take (default 1000)
//   requestTimeoutMs longest a request-level call may take (default 2000)
//   debug           print fixed diagnostics to stderr (never message contents)
//
// Only the default export is public. OpenCode's loader may call every export of a
// V1 module as a plugin, so nothing else is exported from this file.

type Options = Record<string, unknown> | undefined;

function runtime(options: Options, cannot: ("deny" | "rewrite_input" | "rewrite_output" | "rewrite_request")[]): Core {
  const o = options ?? {};
  const num = (v: unknown): number | undefined => (typeof v === "number" && v > 0 ? v : undefined);
  const debug = o["debug"] === true;
  const bridge = new Bridge({
    binary: typeof o["binary"] === "string" && o["binary"] !== "" ? (o["binary"] as string) : undefined,
    timeoutMs: num(o["timeoutMs"]),
    log: debug ? (line) => console.error(line) : undefined,
  });
  const core = new Core(bridge, { cannot, requestTimeoutMs: num(o["requestTimeoutMs"]) });
  void bridge.start(); // warm it up without holding OpenCode's start-up
  return core;
}

const plugin = {
  id: "trove",

  /** V1. No hook there can refuse a call or see the request body. */
  server: async (_input: unknown, options?: Options): Promise<V1Hooks> => v1Hooks(runtime(options, ["deny", "rewrite_request"])),

  /** V2. A tool hook cannot refuse a call either (none is documented to). */
  setup: async (ctx: V2Context & { options?: Options }): Promise<() => void> => setupV2(ctx, runtime(ctx.options, ["deny"])),
};

export default plugin;
