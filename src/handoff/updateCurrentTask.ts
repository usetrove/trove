import path from "node:path";
import { writeText } from "../core/fs";
import { renderCurrentTaskMarkdown } from "../templates/handoff";
import type { HandoffDocument } from "../types/handoff";

export async function updateCurrentTask(
  currentTaskPath: string,
  doc: HandoffDocument,
  savedSessionPath: string,
  repoRoot: string,
): Promise<void> {
  const relative = path.relative(repoRoot, savedSessionPath).replace(/\\/g, "/");
  const markdown = renderCurrentTaskMarkdown(doc, relative);
  await writeText(currentTaskPath, markdown);
}
