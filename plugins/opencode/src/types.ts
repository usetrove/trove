// The messages of `trove engine serve` (docs/adapters.md, protocol version 1).
// They carry no header, key or token field: the login is a three-valued label the
// plugin derives from non-secret fields (guard.ts).

/** The protocol version this plugin speaks. A different version means do nothing. */
export const PROTOCOL = 1;

export type Login = "api_key" | "copilot" | "subscription_or_unknown";

/** What the harness is unable to do; the engine then counts instead of acting. */
export type Cannot = "deny" | "rewrite_input" | "rewrite_output" | "rewrite_request";

export interface Session {
  key: string;
  harness: string;
  login: Login;
  agent?: string;
  provider?: string;
  model?: string;
  trove_run?: boolean;
  prompts?: number;
  cannot?: Cannot[];
}

export interface ToolCall {
  name: string;
  /** The tool's arguments as JSON, in the engine's argument names. */
  input?: unknown;
}

export interface ToolOutput {
  parts: { text: string }[];
  is_error?: boolean;
}

export interface Decision {
  kind: string;
  message?: string;
  bytes?: number;
  tokens?: number;
}

export interface Report {
  name: string;
  state: string;
  changed: boolean;
  bytes_removed: number;
  tokens_removed: number;
  error?: string;
  overhead_tokens?: number;
  decision?: Decision;
}

export interface Observe {
  kind: "usage" | "outcome";
  in_tokens?: number;
  out_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
  cost_usd?: number;
  detail?: "tool_error" | "retry" | "abort" | "session_end" | "compaction";
}

export interface Message {
  id?: number;
  method: "hello" | "capabilities" | "session" | "tool_input" | "tool_output" | "request" | "observe";
  session?: Session;
  call?: ToolCall;
  output?: ToolOutput;
  /** body is base64, as the engine's JSON encoding of bytes. */
  request?: { provider: string; path: string; content_encoding?: string; body: string };
  observe?: Observe;
}

export interface Reply {
  id?: number;
  ok: boolean;
  error?: string;
  protocol?: number;
  version?: string;
  recoverable?: boolean;
  states?: Record<string, string>;
  call?: ToolCall;
  output?: ToolOutput;
  request?: { body: string; changed: boolean; skipped?: string };
  reports?: Report[];
}
