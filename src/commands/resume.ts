import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { isTroveInitialized, readTroveConfig } from "../core/config";
import { requireGitRepository } from "../core/git";
import { getTrovePaths } from "../core/paths";
import { packSections } from "../resume/pack";
import { loadMemory, retrieveSections } from "../resume/retrieve";
import { RESUME_TOKEN_BUDGET, estimateTokens } from "../resume/tokens";

export interface ResumeCliOptions {
  budget?: string;
  output?: string;
  verbose?: boolean;
  pathOnly?: boolean;
}

export async function runResume(
  queryArg: string | undefined,
  options: ResumeCliOptions = {},
  cwd = process.cwd(),
): Promise<void> {
  const repoRoot = await requireGitRepository(cwd);
  const paths = getTrovePaths(repoRoot);

  if (!isTroveInitialized(paths)) {
    console.error("Trove has not been initialized in this repository.");
    console.error("");
    console.error("Run:");
    console.error("  trove init");
    process.exitCode = 1;
    return;
  }

  await readTroveConfig(paths.config);
  const memory = await loadMemory(paths);

  if (options.pathOnly) {
    if (!memory.latestHandoffPath) {
      console.error("No latest handoff recorded in current-task.md.");
      process.exitCode = 1;
      return;
    }
    console.log(memory.latestHandoffPath.replace(/\\/g, "/"));
    return;
  }

  const query =
    queryArg?.trim() ||
    memory.currentTaskObjective ||
    memory.currentTaskNext ||
    "continue current task";

  // Fixed ~800 token budget for V1 briefs; --budget remains an escape hatch.
  const budget = options.budget
    ? parseBudget(options.budget)
    : RESUME_TOKEN_BUDGET;

  const candidates = await retrieveSections(paths, memory, query);
  const packet = packSections(candidates, budget, query);

  if (options.verbose) {
    console.error(`resume brief ~${packet.usedTokens}/${packet.budget} tokens`);
    console.error(`included: ${packet.included.length}`);
    for (const section of packet.included) {
      console.error(`  + ${section.kind} · ${section.title}`);
    }
    if (packet.omitted.length) {
      console.error(`omitted: ${packet.omitted.length}`);
    }
    console.error("");
  }

  if (options.output) {
    const outPath = path.resolve(cwd, options.output);
    await mkdir(path.dirname(outPath), { recursive: true });
    await writeFile(outPath, packet.markdown, "utf8");
    console.error(
      `✓ Wrote ${path.relative(cwd, outPath) || outPath} (~${estimateTokens(packet.markdown)} tokens)`,
    );
    return;
  }

  console.log(packet.markdown.trimEnd());
}

function parseBudget(raw: string): number {
  const n = Number(raw);
  if (!Number.isFinite(n) || n < 100) {
    throw new Error(`Invalid budget "${raw}". Use a number >= 100.`);
  }
  return Math.floor(n);
}
