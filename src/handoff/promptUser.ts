import { ask, confirm } from "../core/prompt";
import type { AutoDraft, HandoffInput } from "../types/handoff";
import { renderCompactDraftPreview } from "./autoDraft";

export type ReviewAction = "save" | "durable" | "edit" | "cancel";

export async function reviewDraftInteractively(args: {
  draft: AutoDraft;
  nextFromCli?: string;
}): Promise<{ action: ReviewAction; input: HandoffInput } | { action: "cancel" }> {
  console.log(renderCompactDraftPreview(args.draft));
  console.log("");

  let nextAction =
    args.nextFromCli?.trim() ||
    (await ask(
      "What is the next concrete action?",
      args.draft.suggestedNextAction.text,
    ));

  while (!nextAction.trim()) {
    nextAction = await ask("Next action is required. What should happen next?");
  }

  const missed = await ask(
    "Anything important that Trove missed or got wrong? (optional)",
  );

  const menu = await chooseReviewAction();
  if (menu === "cancel") {
    return { action: "cancel" };
  }

  let decisions = "";
  let rejected = "";

  if (menu === "durable") {
    decisions = await ask("Add a decision to preserve? (optional)");
    rejected = await ask("Add a rejected approach to avoid repeating? (optional)");
  }

  if (missed.trim() && !decisions && !rejected) {
    // Keep corrections visible in remaining/notes
  }

  return {
    action: menu,
    input: {
      objective: args.draft.objective.text,
      completed: args.draft.completed.map((c) => c.text).join("\n"),
      remainingOrBlocked: [
        ...args.draft.remaining.map((r) => r.text),
        missed.trim() ? `Correction/missed: ${missed.trim()}` : "",
      ]
        .filter(Boolean)
        .join("\n"),
      verification: args.draft.verification.map((v) => v.text).join("\n"),
      decisions,
      rejectedApproaches: rejected,
      nextAction: nextAction.trim(),
      correctionNotes: missed.trim(),
    },
  };
}

export async function chooseReviewAction(): Promise<ReviewAction> {
  const rlAnswer = await ask(
    "[S] Save quick handoff  [D] Add decision/rejection  [E] Edit full draft  [C] Cancel",
    "S",
  );
  const answer = rlAnswer.trim().toLowerCase();
  if (!answer || answer === "s" || answer === "save") return "save";
  if (answer === "d" || answer === "durable" || answer === "decision") return "durable";
  if (answer === "e" || answer === "edit") return "edit";
  if (answer === "c" || answer === "cancel") return "cancel";
  console.log("Please choose S, D, E, or C.");
  return chooseReviewAction();
}

export async function confirmSave(): Promise<boolean> {
  return confirm("Save this handoff?", true);
}
