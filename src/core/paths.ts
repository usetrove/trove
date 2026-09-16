import path from "node:path";
import type { TrovePaths } from "../types/handoff";

export function getTrovePaths(repoRoot: string): TrovePaths {
  const troveDir = path.join(repoRoot, ".trove");
  return {
    root: repoRoot,
    troveDir,
    config: path.join(troveDir, "config.json"),
    project: path.join(troveDir, "project.md"),
    currentTask: path.join(troveDir, "current-task.md"),
    decisions: path.join(troveDir, "decisions.md"),
    rejected: path.join(troveDir, "rejected.md"),
    errata: path.join(troveDir, "errata.md"),
    sessions: path.join(troveDir, "sessions"),
    archive: path.join(troveDir, "archive"),
  };
}
