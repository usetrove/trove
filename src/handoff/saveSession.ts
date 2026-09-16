import path from "node:path";
import { writeText } from "../core/fs";

export async function saveSessionHandoff(
  sessionsDir: string,
  id: string,
  markdown: string,
): Promise<string> {
  const filePath = path.join(sessionsDir, `${id}.md`);
  await writeText(filePath, markdown.endsWith("\n") ? markdown : `${markdown}\n`);
  return filePath;
}

export async function writeDraftHandoff(
  sessionsDir: string,
  id: string,
  markdown: string,
): Promise<string> {
  const filePath = path.join(sessionsDir, `.draft-${id}.md`);
  await writeText(filePath, markdown.endsWith("\n") ? markdown : `${markdown}\n`);
  return filePath;
}
