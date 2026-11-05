import type { Login } from "./types.ts";

// The login gate. Trove never sees a credential (CLAUDE.md rules 2, 3 and 10), so
// the plugin classifies the login from fields OpenCode shows for every provider:
// the provider id, where its key came from (V1) or the connection's method (V2).
// Only api_key and copilot may have their content changed. Everything else,
// including a ChatGPT login (held until OpenAI's own terms have been read, see
// docs/opencode.md) and anything we cannot classify, is shadow-only: the engine
// counts what it would do and changes nothing.
//
// This file must never touch a key, token, header or the auth hooks. A test
// scans the plugin source for them.

/** Provider ids GitHub's changelog of 2026-01-16 covers: Copilot subscriptions in OpenCode. */
const COPILOT = new Set(["github-copilot", "github-copilot-enterprise"]);

/**
 * V1 hook inputs name the provider and say where its key came from, but not
 * whether a stored login is a key or an OAuth login. So only a key that comes
 * from the environment or from OpenCode's config file counts as an API key; a
 * key stored with `opencode auth login` ("api") could be either and stays shadow-only.
 */
export function classifyV1(providerID: string | undefined, source: string | undefined): Login {
  if (providerID && COPILOT.has(providerID)) return "copilot";
  if (source === "env" || source === "config") return "api_key";
  return "subscription_or_unknown";
}

/**
 * V2 exposes the connection's method without its secret. An environment variable
 * or a stored key is an API key; an OAuth or external login is not.
 */
export function classifyV2(providerID: string | undefined, connection: { type?: string; method?: string } | undefined): Login {
  if (providerID && COPILOT.has(providerID)) return "copilot";
  if (!connection) return "subscription_or_unknown";
  if (connection.type === "env") return "api_key";
  if (connection.type === "credential" && connection.method === "key") return "api_key";
  return "subscription_or_unknown";
}

export function eligible(login: Login): boolean {
  return login === "api_key" || login === "copilot";
}

/** Keeps a provider or model id only if it looks like one; anything else is dropped. */
export function cleanId(s: unknown): string | undefined {
  return typeof s === "string" && /^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,95}$/.test(s) ? s : undefined;
}
