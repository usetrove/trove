import { Core } from "./core.ts";
import type { SessionCtx } from "./core.ts";
import { classifyV1, cleanId } from "./guard.ts";

// OpenCode V1 plugin glue (`opencode-ai` / `@opencode-ai/plugin` 1.19.x; hook
// shapes read from the package types at tag v1.19.5 on 2026-11-05).
//
// What V1 lets a plugin do, and so what this does:
//   - tool.execute.before   change a tool's arguments           -> rewrite_input
//   - tool.execute.after    change the tool's output text       -> rewrite_output
//   - chat.params           learn the session's provider and where its key came from
//   - chat.message          count the user's prompts
//   - event                 read what the harness reports (usage, tool errors)
// V1 has no hook for the serialized request body, so there is no request-level
// transform here; that needs V2. And no hook is documented to refuse a tool call,
// so "deny" is declared as something this harness cannot do and the engine only
// counts what it would have blocked.
//
// Not verified until run on a real OpenCode (docs/opencode-verification.md): that
// tool.execute.after runs before OpenCode's own output truncation, and that a
// changed `output.args` is what the tool then runs with.

interface Sess {
  login: SessionCtx["login"];
  provider?: string;
  model?: string;
  prompts: number;
}

const MAX_SESSIONS = 256;
const MAX_SEEN = 4000;

// The slice of V1's hook types this file uses. Only the fields named here are read.
export interface V1Hooks {
  "chat.message"?: (input: { sessionID?: string; model?: { providerID?: string; modelID?: string } }, output: unknown) => Promise<void>;
  "chat.params"?: (
    input: { sessionID: string; model?: { providerID?: string; id?: string }; provider?: { source?: string } },
    output: unknown,
  ) => Promise<void>;
  "tool.execute.before"?: (input: { tool: string; sessionID: string; callID: string }, output: { args: any }) => Promise<void>;
  "tool.execute.after"?: (
    input: { tool: string; sessionID: string; callID: string; args: any },
    output: { title?: string; output: string; metadata?: unknown },
  ) => Promise<void>;
  event?: (input: { event: { type: string; properties?: any } }) => Promise<void>;
  dispose?: () => Promise<void>;
}

export function v1Hooks(core: Core): V1Hooks {
  const sessions = new Map<string, Sess>();
  const seen = new Set<string>();
  const once = (k: string): boolean => {
    if (seen.has(k)) return false;
    if (seen.size >= MAX_SEEN) seen.clear();
    seen.add(k);
    return true;
  };
  const sess = (id: string): Sess => {
    let s = sessions.get(id);
    if (!s) {
      if (sessions.size >= MAX_SESSIONS) sessions.delete(sessions.keys().next().value as string);
      s = { login: "subscription_or_unknown", prompts: 0 };
      sessions.set(id, s);
    }
    return s;
  };
  const ctx = (id: string): SessionCtx => {
    const s = sess(id);
    return { id, login: s.login, provider: s.provider, model: s.model, prompts: s.prompts };
  };

  return {
    "chat.message": async (input) => {
      if (!input.sessionID) return;
      const s = sess(input.sessionID);
      s.prompts++;
      s.provider = cleanId(input.model?.providerID) ?? s.provider;
      s.model = cleanId(input.model?.modelID) ?? s.model;
    },

    // Runs before every model call, so the login is known before any tool call of that turn.
    "chat.params": async (input) => {
      const s = sess(input.sessionID);
      s.provider = cleanId(input.model?.providerID) ?? s.provider;
      s.model = cleanId(input.model?.id) ?? s.model;
      s.login = classifyV1(input.model?.providerID, input.provider?.source);
    },

    "tool.execute.before": async (input, output) => {
      try {
        if (output.args && typeof output.args === "object") await core.toolBefore(ctx(input.sessionID), input.tool, input.callID, output.args);
      } catch {
        // fail open: the call runs as the model wrote it
      }
    },

    "tool.execute.after": async (input, output) => {
      try {
        if (typeof output.output === "string") output.output = await core.toolAfter(ctx(input.sessionID), input.tool, input.callID, input.args, output.output);
      } catch {
        // fail open: the model sees the output as the tool produced it
      }
    },

    event: async ({ event }) => {
      try {
        const p = event.properties;
        if (event.type === "message.updated") {
          const info = p?.info;
          // An assistant message's usage is final once it is completed.
          if (info?.role !== "assistant" || !info.time?.completed || typeof info.id !== "string" || !once("m:" + info.id)) return;
          const t = info.tokens;
          if (!t || typeof info.sessionID !== "string") return;
          const c = ctx(info.sessionID);
          c.provider = cleanId(info.providerID) ?? c.provider;
          c.model = cleanId(info.modelID) ?? c.model;
          core.observe(c, {
            kind: "usage",
            in_tokens: num(t.input),
            out_tokens: num(t.output),
            cache_read_tokens: num(t.cache?.read),
            cache_write_tokens: num(t.cache?.write),
            // Only a pay-per-token login has a cost worth recording. A subscription
            // login reports 0, which would read as "free" rather than "unknown".
            cost_usd: c.login === "api_key" ? num(info.cost) : undefined,
          });
        } else if (event.type === "message.part.updated") {
          const part = p?.part;
          if (part?.type === "tool" && part.state?.status === "error" && typeof part.id === "string" && typeof part.sessionID === "string" && once("p:" + part.id)) {
            core.observe(ctx(part.sessionID), { kind: "outcome", detail: "tool_error" });
          }
        }
      } catch {
        // observing is best effort
      }
    },

    dispose: async () => {
      await core.close();
    },
  };
}

function num(v: unknown): number | undefined {
  return typeof v === "number" && Number.isFinite(v) && v >= 0 ? v : undefined;
}
