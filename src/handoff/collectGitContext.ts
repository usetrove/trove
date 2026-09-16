import { runGit } from "../core/git";
import type { GitContext } from "../types/handoff";

export async function collectGitContext(repoRoot: string): Promise<GitContext> {
  const commit = await runGit(repoRoot, ["rev-parse", "--short", "HEAD"]);
  const [branch, status, nameOnly, stagedOnly, diffStat, numstat, log] =
    await Promise.all([
      runGit(repoRoot, ["branch", "--show-current"]),
      runGit(repoRoot, ["status", "--short"]),
      runGit(repoRoot, ["diff", "--name-only"]),
      runGit(repoRoot, ["diff", "--cached", "--name-only"]),
      runGit(repoRoot, ["diff", "--stat"]),
      runGit(repoRoot, ["diff", "--numstat"]),
      runGit(repoRoot, ["log", "-5", "--oneline"]),
    ]);

  const statusFiles = parseStatusFiles(status.stdout);
  const unstagedFiles = lines(nameOnly.stdout);
  const stagedFiles = lines(stagedOnly.stdout);
  const changedFiles = unique(
    [...unstagedFiles, ...stagedFiles, ...statusFiles].filter(isUsefulPath),
  );

  return {
    repoRoot,
    branch: branch.ok ? branch.stdout.trim() || null : null,
    commitSha: commit.ok ? commit.stdout.trim() || null : null,
    statusShort: status.stdout,
    changedFiles,
    stagedFiles: stagedFiles.filter(isUsefulPath),
    unstagedFiles: unstagedFiles.filter(isUsefulPath),
    diffStat: diffStat.stdout,
    numstat: numstat.stdout,
    recentCommits: log.ok ? lines(log.stdout) : [],
    hasUncommittedChanges: Boolean(status.stdout.trim()),
  };
}

function isUsefulPath(filePath: string): boolean {
  const normalized = filePath.replace(/\\/g, "/");
  if (!normalized || normalized.endsWith("/")) return false;
  if (normalized === ".trove" || normalized.startsWith(".trove/")) return false;
  if (normalized.startsWith("node_modules/") || normalized === "node_modules") return false;
  if (normalized.startsWith("dist/") || normalized === "dist") return false;
  if (normalized === "package-lock.json") return false;
  return true;
}

export function displayFoundSummary(
  git: GitContext,
  activeTask: string | null,
): void {
  console.log("");
  console.log("Trove found:");
  console.log(`  Branch: ${git.branch ?? "unknown"}`);
  console.log(`  Commit: ${git.commitSha ?? "unknown"}`);
  console.log(`  Changed files: ${git.changedFiles.length}`);
  console.log(`  Recent commits: ${git.recentCommits.length}`);
  if (activeTask) {
    console.log(`  Active task: ${activeTask}`);
  } else {
    console.log("  Active task: (none yet)");
  }
  console.log("");
}

function parseStatusFiles(statusShort: string): string[] {
  return statusShort
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean)
    .map((line) => line.replace(/^[?\sMARCDU]{1,2}\s+/, "").replace(/^.* -> /, ""));
}

function lines(text: string): string[] {
  return text
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean);
}

function unique(items: string[]): string[] {
  return [...new Set(items)];
}
