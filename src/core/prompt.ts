import { createInterface } from "node:readline/promises";
import { stdin, stdout } from "node:process";

export function isInteractive(): boolean {
  return Boolean(stdin.isTTY && stdout.isTTY);
}

export async function ask(question: string, defaultValue = ""): Promise<string> {
  const rl = createInterface({ input: stdin, output: stdout });
  try {
    const suffix = defaultValue ? ` (${defaultValue})` : "";
    const answer = await rl.question(`? ${question}${suffix}\n> `);
    const trimmed = answer.trim();
    return trimmed || defaultValue;
  } finally {
    rl.close();
  }
}

export async function confirm(question: string, defaultYes = false): Promise<boolean> {
  const suffix = defaultYes ? "Y/n" : "y/N";
  const rl = createInterface({ input: stdin, output: stdout });
  try {
    const answer = (await rl.question(`? ${question} [${suffix}]\n> `)).trim().toLowerCase();
    if (!answer) return defaultYes;
    return answer === "y" || answer === "yes";
  } finally {
    rl.close();
  }
}

export async function chooseAction(): Promise<"save" | "edit" | "cancel"> {
  const rl = createInterface({ input: stdin, output: stdout });
  try {
    while (true) {
      const answer = (await rl.question("\n[S] Save   [E] Edit   [C] Cancel\n> "))
        .trim()
        .toLowerCase();
      if (!answer || answer === "s" || answer === "save") return "save";
      if (answer === "e" || answer === "edit") return "edit";
      if (answer === "c" || answer === "cancel") return "cancel";
      console.log("Please choose S, E, or C.");
    }
  } finally {
    rl.close();
  }
}
