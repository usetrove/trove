import { spawn } from "node:child_process";
import { unlink } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { readText, writeText } from "../core/fs";
import { chooseAction } from "../core/prompt";

export async function previewAndChooseAction(
  markdown: string,
  draftPath: string,
): Promise<"save" | "edit" | "cancel"> {
  console.log("");
  console.log("── Draft preview ──");
  console.log(markdown.trimEnd());
  console.log("");
  console.log(`Draft created:\n${draftPath}`);
  return chooseAction();
}

export async function openAndReadEditedDraft(
  markdown: string,
  preferredPath?: string,
): Promise<string> {
  const filePath =
    preferredPath ??
    path.join(os.tmpdir(), `trove-handoff-edit-${Date.now()}.md`);

  await writeText(filePath, markdown);

  const editor =
    process.env.VISUAL ||
    process.env.EDITOR ||
    (process.platform === "win32" ? "notepad" : "vi");

  console.log(`Opening editor: ${editor}`);
  await runEditor(editor, filePath);

  const edited = await readText(filePath);

  if (!preferredPath) {
    await unlink(filePath).catch(() => undefined);
  }

  return edited;
}

function runEditor(editor: string, filePath: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const child = spawn(editor, [filePath], {
      stdio: "inherit",
      shell: process.platform === "win32",
    });
    child.on("error", reject);
    child.on("close", (code) => {
      if (code === 0) resolve();
      else reject(new Error(`Editor exited with code ${code}`));
    });
  });
}
