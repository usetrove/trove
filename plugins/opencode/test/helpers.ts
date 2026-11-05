import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
export const repoRoot = resolve(here, "..", "..", "..");

let built: string | undefined;

/**
 * The trove program the tests talk to: $TROVE_BIN if set, else one built from this
 * checkout (once per run). Tests never use a trove from PATH or the real ~/.trove.
 */
export function troveBinary(): string {
  if (built) return built;
  const given = process.env["TROVE_BIN"];
  if (given) return (built = given);
  const dir = mkdtempSync(join(tmpdir(), "trove-opencode-bin-"));
  const out = join(dir, process.platform === "win32" ? "trove.exe" : "trove");
  const r = spawnSync("go", ["build", "-o", out, "./cmd/trove"], { cwd: repoRoot, encoding: "utf8" });
  if (r.status !== 0) throw new Error("go build failed: " + (r.stderr || r.stdout));
  return (built = out);
}

/** A fresh Trove data folder with a config.toml, so no test touches the real ~/.trove. */
export function dataDir(config: string): string {
  const dir = mkdtempSync(join(tmpdir(), "trove-opencode-data-"));
  mkdirSync(dir, { recursive: true });
  writeFileSync(join(dir, "config.toml"), config);
  return dir;
}

/**
 * A config with the given optimizer states. The session-level A/B experiment is
 * on by default and deliberately keeps half of the sessions in shadow, so tests
 * that expect an optimizer to act switch it off (a test of the experiment itself
 * passes experiment = true). "other" is the agent label a harness plugin gets, and
 * the only one besides "sdk" that can be marked custom.
 */
export function configWith(optimizers: Record<string, string>, opts: { experiment?: boolean } = {}): string {
  const lines = Object.entries(optimizers).map(([k, v]) => `${k} = "${v}"`);
  return `[optimizers]\n${lines.join("\n")}\n\n[optimize]\ncustom_agents = ["other"]\nbudget_ms = 1000\n\n[experiment]\nenabled = ${opts.experiment ?? false}\n`;
}

/** Runs `fn` with TROVE_DATA_DIR set for the child processes the bridge spawns. */
export async function withDataDir<T>(dir: string, fn: () => Promise<T>): Promise<T> {
  const old = process.env["TROVE_DATA_DIR"];
  process.env["TROVE_DATA_DIR"] = dir;
  try {
    return await fn();
  } finally {
    if (old === undefined) delete process.env["TROVE_DATA_DIR"];
    else process.env["TROVE_DATA_DIR"] = old;
  }
}

export function fileExists(p: string): boolean {
  return existsSync(p);
}
