import { Core } from "./core.ts";
import type { SessionCtx } from "./core.ts";
import { classifyV2, cleanId } from "./guard.ts";
import type { Login } from "./types.ts";

// OpenCode V2 plugin glue (`@opencode/plugin` 2.2.x). The registration syntax and
// hook types were read verbatim from anomalyco/opencode at commit e23d54ca on
// 2026-11-05: hooks are registered with `await ctx.<domain>.hook(name, callback)`,
// the callback gets one mutable event and its return value is ignored.
//
// What V2 lets a plugin do, and so what this does:
//   - session "http.request"  read and replace the outgoing request  -> the only
//     request-level transform. Only the BODY of an Anthropic Messages request is
//     read and replaced; the request's headers (which carry the credential) are
//     never read, and `new Request(request, { body })` carries them over inside the
//     runtime without this code touching them.
//   - tool "execute.before/after"  change a tool's input or result
//   - session "context"            learn the session's provider (no changes made)
//   - ctx.event.subscribe          read usage and tool errors the harness reports
//
// Not verified until run on a real OpenCode (docs/opencode-verification.md):
// that assigning a new Request to event.request is honoured (the docs only show
// header mutation); the built-in tools' V2 argument names; that a provider id is
// the integration id connection.active() expects; the event envelope of
// ctx.event.subscribe.

// The slice of V2's context this file uses. Only these members are touched.
type Hook = (event: any) => Promise<void> | void;
export interface V2Context {
  session: { hook: (name: string, cb: Hook, options?: { providerID?: string }) => Promise<unknown> };
  tool: { hook: (name: string, cb: Hook) => Promise<unknown> };
  integration?: { connection?: { active?: (integrationID: string) => Promise<{ type?: string; method?: string } | undefined> } };
  event?: { subscribe?: (options: { signal?: AbortSignal }) => AsyncIterable<any> };
}

interface Sess {
  login: Login;
  provider?: string;
  model?: string;
}

const MAX_SESSIONS = 256;
const LOGIN_TTL_MS = 30_000;

export async function setupV2(ctx: V2Context, core: Core): Promise<() => void> {
  const sessions = new Map<string, Sess>();
  const logins = new Map<string, { login: Login; at: number }>();
  const abort = new AbortController();

  // The login for a provider, from the connection's method (no secret is read).
  // Cached briefly: it is asked for on every request.
  const loginFor = async (providerID: string | undefined): Promise<Login> => {
    if (!providerID) return "subscription_or_unknown";
    const hit = logins.get(providerID);
    if (hit && Date.now() - hit.at < LOGIN_TTL_MS) return hit.login;
    let conn: { type?: string; method?: string } | undefined;
    try {
      conn = await ctx.integration?.connection?.active?.(providerID);
    } catch {
      conn = undefined; // unknown stays shadow-only
    }
    const login = classifyV2(providerID, conn);
    logins.set(providerID, { login, at: Date.now() });
    return login;
  };

  const note = async (sessionID: string, providerID: string | undefined, modelID: string | undefined): Promise<Sess> => {
    let s = sessions.get(sessionID);
    if (!s) {
      if (sessions.size >= MAX_SESSIONS) sessions.delete(sessions.keys().next().value as string);
      s = { login: "subscription_or_unknown" };
      sessions.set(sessionID, s);
    }
    s.provider = cleanId(providerID) ?? s.provider;
    s.model = cleanId(modelID) ?? s.model;
    s.login = await loginFor(providerID);
    return s;
  };
  const sc = (id: string): SessionCtx => {
    const s = sessions.get(id);
    return { id, login: s?.login ?? "subscription_or_unknown", provider: s?.provider, model: s?.model };
  };

  await ctx.session.hook("context", async (event) => {
    try {
      await note(String(event.sessionID), event.model?.providerID, event.model?.id);
    } catch {
      // learning the provider is best effort
    }
  });

  // The request-level transform: Anthropic Messages bodies only, primary and
  // compaction requests (the ones that carry the conversation's tool results).
  await ctx.session.hook("http.request", async (event) => {
    try {
      if (event.model?.providerID !== "anthropic" || (event.kind !== "primary" && event.kind !== "compaction")) return;
      const req: Request = event.request;
      if (req.method !== "POST" || !new URL(req.url).pathname.endsWith("/v1/messages")) return;
      const s = await note(String(event.sessionID), event.model.providerID, event.model.id);
      if (s.login !== "api_key" && s.login !== "copilot") return; // the engine would only skip it
      // Bodies are one-shot streams: read a clone, so the original stays usable if we do nothing.
      const body = new Uint8Array(await req.clone().arrayBuffer());
      const out = await core.request(sc(String(event.sessionID)), "anthropic", "/v1/messages", body);
      if (out) event.request = new Request(req, { body: out });
    } catch {
      // fail open: the request goes out as OpenCode built it
    }
  });

  await ctx.tool.hook("execute.before", async (event) => {
    try {
      if (event.input && typeof event.input === "object") await core.toolBefore(sc(String(event.sessionID)), String(event.tool), String(event.id), event.input);
    } catch {
      // fail open
    }
  });

  await ctx.tool.hook("execute.after", async (event) => {
    try {
      const id = sc(String(event.sessionID));
      if (event.status === "error") {
        core.observe(id, { kind: "outcome", detail: "tool_error" });
        return;
      }
      if (event.status !== "completed" || !event.result) return;
      const r = event.result as { content?: unknown; output?: unknown };
      const tool = String(event.tool);
      const callID = String(event.id);
      if (typeof r.content === "string") {
        event.result = { ...r, content: await core.toolAfter(id, tool, callID, event.input, r.content) };
      } else if (Array.isArray(r.content) && r.content.length > 0 && r.content.every((c: any) => c?.type === "text" && typeof c.text === "string")) {
        const texts = await core.toolAfterParts(id, tool, callID, event.input, r.content.map((c: any) => c.text as string));
        event.result = { ...r, content: r.content.map((c: any, i: number) => ({ ...c, text: texts[i] ?? c.text })) };
      } else if (typeof r.output === "string") {
        event.result = { ...r, output: await core.toolAfter(id, tool, callID, event.input, r.output) };
      }
    } catch {
      // fail open
    }
  });

  // Usage and tool errors the harness reports. The envelope of these events is
  // not documented, so every field is read defensively and anything unexpected is
  // ignored.
  const subscribe = ctx.event?.subscribe;
  if (typeof subscribe === "function") {
    const models = new Map<string, { providerID?: string; id?: string }>();
    void (async () => {
      try {
        for await (const ev of subscribe.call(ctx.event, { signal: abort.signal })) {
          try {
            const p = ev?.properties ?? ev?.data ?? ev?.payload ?? ev;
            if (!p || typeof p.sessionID !== "string") continue;
            if (ev.type === "session.step.started" && typeof p.assistantMessageID === "string") {
              if (models.size > 512) models.clear();
              models.set(p.assistantMessageID, p.model ?? {});
            } else if (ev.type === "session.step.ended" && p.tokens) {
              const m = models.get(p.assistantMessageID) ?? {};
              const s = await note(p.sessionID, m.providerID, m.id);
              core.observe(sc(p.sessionID), {
                kind: "usage",
                in_tokens: num(p.tokens.input),
                out_tokens: num(p.tokens.output),
                cache_read_tokens: num(p.tokens.cache?.read),
                cache_write_tokens: num(p.tokens.cache?.write),
                cost_usd: s.login === "api_key" ? num(p.cost) : undefined,
              });
            }
          } catch {
            // one bad event does not stop the rest
          }
        }
      } catch {
        // the stream ended or was aborted
      }
    })();
  }

  return () => {
    abort.abort();
    void core.close();
  };
}

function num(v: unknown): number | undefined {
  return typeof v === "number" && Number.isFinite(v) && v >= 0 ? v : undefined;
}
