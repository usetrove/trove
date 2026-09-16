import path from "node:path";
import { DEFAULT_CONFIG } from "../core/config";
import { ensureDir, writeIfMissing } from "../core/fs";
import { requireGitRepository } from "../core/git";
import { getTrovePaths } from "../core/paths";
import { INIT_FILES } from "../templates";

export async function runInit(cwd = process.cwd()): Promise<void> {
  let repoRoot: string;
  try {
    repoRoot = await requireGitRepository(cwd);
  } catch {
    console.error("Not a Git repository. Initialize Git first, then run `trove init`.");
    process.exitCode = 1;
    return;
  }

  const paths = getTrovePaths(repoRoot);
  await ensureDir(paths.troveDir);
  await ensureDir(paths.sessions);
  await ensureDir(paths.archive);

  for (const [name, contents] of Object.entries(INIT_FILES)) {
    if (name === "config.json") continue;
    const fullPath = path.join(paths.troveDir, name);
    const wrote = await writeIfMissing(fullPath, contents);
    console.log(wrote ? `✓ Created .trove/${name}` : `· skipped .trove/${name} (exists)`);
  }

  const wroteConfig = await writeIfMissing(
    paths.config,
    `${JSON.stringify(DEFAULT_CONFIG, null, 2)}\n`,
  );
  console.log(
    wroteConfig ? "✓ Created .trove/config.json" : "· skipped .trove/config.json (exists)",
  );

  await writeIfMissing(path.join(paths.sessions, ".gitkeep"), "");
  await writeIfMissing(path.join(paths.archive, ".gitkeep"), "");
  await writeIfMissing(
    path.join(paths.troveDir, ".gitignore"),
    "sessions/.draft-*.md\n",
  );

  console.log("");
  console.log("✓ Trove initialized");
  console.log(`Configured default context budget: ${DEFAULT_CONFIG.defaultContextBudgetTokens} tokens`);
  console.log("");
  console.log("Next:");
  console.log("  1. Edit .trove/project.md with stable project context");
  console.log("  2. trove handoff   # before ending a work session");
}
