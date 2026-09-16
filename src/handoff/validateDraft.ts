import { confirm } from "../core/prompt";
import type { HandoffInput } from "../types/handoff";

export interface ValidationResult {
  errors: string[];
  warnings: string[];
}

export function validateHandoffInput(input: HandoffInput): ValidationResult {
  const errors: string[] = [];
  const warnings: string[] = [];

  if (!input.nextAction.trim()) {
    errors.push("Next concrete action is required.");
  }

  if (!input.objective.trim()) {
    warnings.push("Objective is empty; consider setting one in current-task.md.");
  }

  if (!input.verification.trim() || /no test result captured/i.test(input.verification)) {
    warnings.push("No tests/commands verified. Treat completions as unverified.");
  }

  if (
    isVague(input.nextAction, [
      "finish it",
      "continue",
      "continue debugging",
      "keep going",
      "fix bugs",
      "continue work on files changed since the previous checkpoint.",
      "confirm the next concrete action for this task.",
    ])
  ) {
    warnings.push(
      'Next action looks generic. Prefer: "Run X, inspect Y, then change Z if condition A is true."',
    );
  }

  return { errors, warnings };
}

export async function confirmWarnings(warnings: string[]): Promise<boolean> {
  if (warnings.length === 0) return true;
  console.log("");
  for (const warning of warnings) {
    console.log(`Warning: ${warning}`);
  }
  console.log("");
  return confirm("Continue anyway?", true);
}

function isVague(text: string, phrases: string[]): boolean {
  const normalized = text.trim().toLowerCase();
  if (!normalized) return false;
  return phrases.some((p) => normalized === p || normalized.startsWith(`${p} `));
}
