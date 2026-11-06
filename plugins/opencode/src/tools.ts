// OpenCode's tool names and argument names, mapped to the names the engine
// knows (it was built around Claude Code's: Read with file_path, Bash with
// command). This is a data table, not logic. The V1 names were read from
// packages/opencode/src/tool/*.ts at tag v1.19.5 on 2026-11-05 (read: filePath,
// offset, limit; bash: command; grep: pattern, path, include; glob: pattern,
// path). grep and glob ids are inferred from the file names; V2's built-in tool
// schemas were not read, so each argument lists the spellings to accept. A tool
// or argument not listed here is simply not offered to the engine.

export interface ToolMap {
  /** The engine's tool name. */
  name: string;
  /** engine argument name -> the OpenCode spellings, preferred first */
  args: Record<string, string[]>;
}

export const TOOLS: Record<string, ToolMap> = {
  read: { name: "Read", args: { file_path: ["filePath", "file_path", "path"], offset: ["offset"], limit: ["limit"] } },
  bash: { name: "Bash", args: { command: ["command"] } },
  grep: { name: "Grep", args: { pattern: ["pattern"], path: ["path"], glob: ["include"] } },
  glob: { name: "Glob", args: { pattern: ["pattern"], path: ["path"] } },
};

export interface Mapped {
  name: string;
  input: Record<string, unknown>;
  /** engine argument name -> the OpenCode key it came from */
  keys: Record<string, string>;
}

/** The engine's view of a call, or null for a tool the engine has no use for. */
export function toEngine(tool: string, args: unknown): Mapped | null {
  const m = TOOLS[tool];
  if (!m || typeof args !== "object" || args === null) return null;
  const a = args as Record<string, unknown>;
  const input: Record<string, unknown> = {};
  const keys: Record<string, string> = {};
  for (const [ek, spellings] of Object.entries(m.args)) {
    for (const k of spellings) {
      if (a[k] !== undefined) {
        input[ek] = a[k];
        keys[ek] = k;
        break;
      }
    }
  }
  return { name: m.name, input, keys };
}

/**
 * Writes the engine's rewritten arguments back into the OpenCode call, only the
 * ones that changed, under the key OpenCode uses. Returns whether anything changed.
 */
export function applyBack(tool: string, args: Record<string, unknown>, sent: Mapped, rewritten: unknown): boolean {
  const m = TOOLS[tool];
  if (!m || typeof rewritten !== "object" || rewritten === null) return false;
  let changed = false;
  for (const [ek, v] of Object.entries(rewritten as Record<string, unknown>)) {
    if (!(ek in m.args)) continue; // a field we did not send
    if (JSON.stringify(v) === JSON.stringify(sent.input[ek])) continue;
    args[sent.keys[ek] ?? m.args[ek]?.[0] ?? ek] = v;
    changed = true;
  }
  return changed;
}
