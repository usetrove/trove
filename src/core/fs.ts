import { existsSync } from "node:fs";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

export async function ensureDir(dirPath: string): Promise<void> {
  await mkdir(dirPath, { recursive: true });
}

export async function writeText(filePath: string, contents: string): Promise<void> {
  await ensureDir(path.dirname(filePath));
  await writeFile(filePath, contents, "utf8");
}

export async function writeIfMissing(filePath: string, contents: string): Promise<boolean> {
  if (existsSync(filePath)) return false;
  await writeText(filePath, contents);
  return true;
}

export async function readText(filePath: string): Promise<string> {
  return readFile(filePath, "utf8");
}

export async function appendText(filePath: string, contents: string): Promise<void> {
  const existing = existsSync(filePath) ? await readFile(filePath, "utf8") : "";
  const separator = existing.endsWith("\n") || existing.length === 0 ? "" : "\n";
  await writeText(filePath, `${existing}${separator}${contents}`);
}
