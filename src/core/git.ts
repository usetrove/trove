import { execFile } from "node:child_process";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

export async function runGit(
  cwd: string,
  args: string[],
): Promise<{ stdout: string; stderr: string; ok: boolean }> {
  try {
    const { stdout, stderr } = await execFileAsync("git", args, {
      cwd,
      encoding: "utf8",
      maxBuffer: 10 * 1024 * 1024,
    });
    return { stdout: stdout.trimEnd(), stderr: stderr.trimEnd(), ok: true };
  } catch (error) {
    const err = error as { stdout?: string; stderr?: string };
    return {
      stdout: (err.stdout ?? "").trimEnd(),
      stderr: (err.stderr ?? "").trimEnd(),
      ok: false,
    };
  }
}

export async function requireGitRepository(cwd: string): Promise<string> {
  const inside = await runGit(cwd, ["rev-parse", "--is-inside-work-tree"]);
  if (!inside.ok || inside.stdout.trim() !== "true") {
    throw new Error("Not a Git repository. Run this inside a Git repo.");
  }
  const top = await runGit(cwd, ["rev-parse", "--show-toplevel"]);
  if (!top.ok || !top.stdout.trim()) {
    throw new Error("Could not resolve Git repository root.");
  }
  return top.stdout.trim();
}
