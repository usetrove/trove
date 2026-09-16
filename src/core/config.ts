import { existsSync } from "node:fs";
import { readFile, writeFile } from "node:fs/promises";
import type { TroveConfig } from "../types/handoff";

export const DEFAULT_CONFIG: TroveConfig = {
  version: 1,
  defaultContextBudgetTokens: 800,
  includeGitDiffInHandoff: true,
  includeRecentCommits: true,
  maxDiffCharacters: 12000,
  defaultEditor: "system",
};

export async function readTroveConfig(configPath: string): Promise<TroveConfig> {
  if (!existsSync(configPath)) {
    return { ...DEFAULT_CONFIG };
  }
  const raw = JSON.parse(await readFile(configPath, "utf8")) as Partial<TroveConfig>;
  return {
    ...DEFAULT_CONFIG,
    ...raw,
  };
}

export async function writeTroveConfig(
  configPath: string,
  config: TroveConfig = DEFAULT_CONFIG,
): Promise<void> {
  await writeFile(configPath, `${JSON.stringify(config, null, 2)}\n`, "utf8");
}

export function isTroveInitialized(paths: {
  troveDir: string;
  currentTask: string;
  sessions: string;
}): boolean {
  return (
    existsSync(paths.troveDir) &&
    existsSync(paths.currentTask) &&
    existsSync(paths.sessions)
  );
}
