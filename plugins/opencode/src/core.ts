import { Bridge } from "./bridge.ts";
import { applyBack, toEngine } from "./tools.ts";
import type { Cannot, Login, Observe, Report, Session } from "./types.ts";

// The hook-independent part of the plugin. Each method turns what a hook knows
// into a protocol message, sends it through the bridge, and applies the answer.
// It makes no optimization decision: what changes, what is blocked, what is only
// counted is the engine's call. Every method returns quietly with the original
// values untouched when the bridge gives no answer (fail open).

export const HARNESS = "opencode";

export interface SessionCtx {
  /** OpenCode's own session id. The engine hashes it before storing anything. */
  id: string;
  login: Login;
  provider?: string;
  model?: string;
  /** User prompts seen so far, for the repeat-read message. */
  prompts?: number;
}

export interface CoreOptions {
  /** What this OpenCode API cannot do; the engine then counts instead of acting. */
  cannot?: Cannot[];
  /** Longest a request-level call may take (large bodies). Default 2000 ms. */
  requestTimeoutMs?: number;
}

const MAX_ADVICE = 512;

export class Core {
  readonly bridge: Bridge;
  readonly #cannot: Cannot[];
  readonly #requestTimeoutMs: number;
  /** Messages the engine wants the model to see, held from a call's start to its end, by call id. */
  readonly #advice = new Map<string, string[]>();
  /** Observations sent but not yet answered, so close() does not drop them. */
  readonly #inflight = new Set<Promise<unknown>>();

  constructor(bridge: Bridge, opts: CoreOptions = {}) {
    this.bridge = bridge;
    this.#cannot = opts.cannot ?? [];
    this.#requestTimeoutMs = opts.requestTimeoutMs ?? 2000;
  }

  #session(s: SessionCtx): Session {
    const out: Session = { key: s.id, harness: HARNESS, login: s.login };
    if (s.provider) out.provider = s.provider;
    if (s.model) out.model = s.model;
    if (s.prompts) out.prompts = s.prompts;
    if (this.#cannot.length) out.cannot = this.#cannot;
    return out;
  }

  /**
   * Before a tool runs. May rewrite `args` in place. Returns the engine's reports
   * (for tests and diagnostics). A deny decision is never turned into a failure
   * here: this OpenCode API has no verified way to refuse a call, so a plugin
   * that cannot deny says so ("deny" in cannot) and the engine only counts it.
   */
  async toolBefore(s: SessionCtx, tool: string, callID: string, args: Record<string, unknown>): Promise<Report[]> {
    const sent = toEngine(tool, args);
    if (!sent) return [];
    const rep = await this.bridge.call({
      method: "tool_input",
      session: this.#session(s),
      call: { name: sent.name, input: sent.input },
    });
    if (!rep?.ok || !rep.call) return [];
    applyBack(tool, args, sent, rep.call.input);
    const notes: string[] = [];
    for (const r of rep.reports ?? []) {
      const d = r.decision;
      if (d?.message && (d.kind === "advised" || d.kind === "rewritten")) notes.push(d.message);
    }
    if (notes.length) {
      if (this.#advice.size >= MAX_ADVICE) this.#advice.delete(this.#advice.keys().next().value as string);
      this.#advice.set(callID, notes);
    }
    return rep.reports ?? [];
  }

  /**
   * After a tool ran. `output` is the tool's text; the returned string is what the
   * model should see (the original when the engine changed nothing). Advice the
   * engine attached at the start of the call is added at the end of the output.
   */
  async toolAfter(s: SessionCtx, tool: string, callID: string, args: unknown, output: string): Promise<string> {
    const notes = this.#advice.get(callID);
    this.#advice.delete(callID);
    let text = output;
    const sent = toEngine(tool, args);
    if (sent) {
      const rep = await this.bridge.call({
        method: "tool_output",
        session: this.#session(s),
        call: { name: sent.name, input: sent.input },
        output: { parts: [{ text: output }] },
      });
      const part = rep?.ok ? rep.output?.parts : undefined;
      if (part && part.length === 1 && typeof part[0]?.text === "string") text = part[0].text;
    }
    if (notes?.length) text += "\n\n" + notes.join("\n");
    return text;
  }

  /**
   * Several output parts at once (a tool that returns more than one text part).
   * Returns the parts in the same order, unchanged when the engine says nothing.
   */
  async toolAfterParts(s: SessionCtx, tool: string, callID: string, args: unknown, parts: string[]): Promise<string[]> {
    const notes = this.#advice.get(callID);
    this.#advice.delete(callID);
    let out = parts;
    const sent = toEngine(tool, args);
    if (sent && parts.length > 0) {
      const rep = await this.bridge.call({
        method: "tool_output",
        session: this.#session(s),
        call: { name: sent.name, input: sent.input },
        output: { parts: parts.map((text) => ({ text })) },
      });
      const got = rep?.ok ? rep.output?.parts : undefined;
      if (got && got.length === parts.length && got.every((p) => typeof p.text === "string")) out = got.map((p) => p.text);
    }
    if (notes?.length && out.length) out = [...out.slice(0, -1), out[out.length - 1] + "\n\n" + notes.join("\n")];
    return out;
  }

  /**
   * A whole provider request body. Returns the engine's replacement, or null to
   * send the original (nothing changed, the login may not be changed, or no answer).
   */
  async request(s: SessionCtx, provider: string, path: string, body: Uint8Array): Promise<Uint8Array<ArrayBuffer> | null> {
    const rep = await this.bridge.call(
      {
        method: "request",
        session: this.#session(s),
        request: { provider, path, body: Buffer.from(body).toString("base64") },
      },
      this.#requestTimeoutMs,
    );
    if (!rep?.ok || !rep.request?.changed) return null;
    return new Uint8Array(Buffer.from(rep.request.body, "base64"));
  }

  /** Tells the engine what the harness reported. Never waits and never throws. */
  observe(s: SessionCtx, o: Observe): void {
    const p = this.bridge
      .call({ method: "observe", session: this.#session(s), observe: o })
      .catch(() => null)
      .finally(() => this.#inflight.delete(p));
    this.#inflight.add(p);
  }

  /** Waits for observations already sent, then stops the engine. */
  async close(): Promise<void> {
    await Promise.allSettled([...this.#inflight]);
    await this.bridge.close();
  }
}
