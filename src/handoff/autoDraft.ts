import type {
  AutoDraft,
  EvidenceItem,
  HandoffEvidence,
  HandoffInput,
} from "../types/handoff";

export function buildAutoDraft(evidence: HandoffEvidence): AutoDraft {
  const { git, currentTask, priorHandoff, relatedDecisions, relatedRejected } =
    evidence;

  const objectiveText =
    currentTask.objective ||
    priorHandoff?.objective ||
    inferObjectiveFromBranch(git.branch) ||
    "Continue current work";

  const objective: EvidenceItem = {
    text: objectiveText,
    confidence: currentTask.objective
      ? "verified"
      : priorHandoff?.objective
        ? "inferred"
        : git.branch
          ? "inferred"
          : "needs_confirmation",
    source: currentTask.objective
      ? ["current-task.md"]
      : priorHandoff?.objective
        ? ["prior-handoff"]
        : git.branch
          ? ["git-branch"]
          : ["fallback"],
  };

  const completed: EvidenceItem[] = [];
  if (git.changedFiles.length > 0) {
    const sample = git.changedFiles.slice(0, 5).map(basename).join(", ");
    const more =
      git.changedFiles.length > 5 ? ` (+${git.changedFiles.length - 5} more)` : "";
    completed.push({
      text: `Changes detected in ${sample}${more}.`,
      confidence: "verified",
      source: ["git-status", "git-diff"],
    });
  }
  if (git.stagedFiles.length > 0) {
    completed.push({
      text: `${git.stagedFiles.length} file(s) staged.`,
      confidence: "verified",
      source: ["git-diff-cached"],
    });
  }
  if (!git.hasUncommittedChanges && git.recentCommits.length > 0) {
    completed.push({
      text: `Working tree clean. Latest commit: ${git.recentCommits[0]}.`,
      confidence: "verified",
      source: ["git-status", "git-log"],
    });
  }
  if (completed.length === 0) {
    completed.push({
      text: "No file changes detected; handoff may be investigation/planning only.",
      confidence: "inferred",
      source: ["git-status"],
    });
  }

  const verification: EvidenceItem[] = [
    {
      text: "No test result captured automatically.",
      confidence: "needs_confirmation",
      source: ["none"],
    },
  ];

  const remaining: EvidenceItem[] = [];
  if (currentTask.nextAction) {
    remaining.push({
      text: `Previous next action was: ${currentTask.nextAction}`,
      confidence: "inferred",
      source: ["current-task.md"],
    });
  } else if (priorHandoff?.nextAction) {
    remaining.push({
      text: `Previous handoff next action was: ${priorHandoff.nextAction}`,
      confidence: "inferred",
      source: ["prior-handoff"],
    });
  } else {
    remaining.push({
      text: "Open work is unclear from evidence.",
      confidence: "needs_confirmation",
      source: ["none"],
    });
  }

  const relevantFiles = unique([
    ...currentTask.relevantFiles.filter(looksLikeSourceFile),
    ...git.changedFiles.filter(looksLikeSourceFile),
  ]).slice(0, 20);

  const suggestedNextAction: EvidenceItem = {
    text:
      currentTask.nextAction ||
      priorHandoff?.nextAction ||
      (git.changedFiles.length > 0
        ? "Continue work on files changed since the previous checkpoint."
        : "Confirm the next concrete action for this task."),
    confidence:
      currentTask.nextAction || priorHandoff?.nextAction
        ? "needs_confirmation"
        : "needs_confirmation",
    source: currentTask.nextAction
      ? ["current-task.md"]
      : priorHandoff?.nextAction
        ? ["prior-handoff"]
        : ["suggestion"],
  };

  return {
    objective,
    completed,
    verification,
    remaining,
    relevantFiles,
    relatedDecisions: relatedDecisions.map((text) => ({
      text: firstLine(text),
      confidence: "inferred" as const,
      source: ["decisions.md"],
    })),
    relatedRejected: relatedRejected.map((text) => ({
      text: firstLine(text),
      confidence: "inferred" as const,
      source: ["rejected.md"],
    })),
    suggestedNextAction,
    notes: "",
  };
}

export function autoDraftToInput(
  draft: AutoDraft,
  nextAction: string,
  extras: Partial<HandoffInput> = {},
): HandoffInput {
  return {
    objective: extras.objective?.trim() || draft.objective.text,
    completed:
      extras.completed?.trim() ||
      draft.completed.map((item) => item.text).join("\n"),
    remainingOrBlocked:
      extras.remainingOrBlocked?.trim() ||
      draft.remaining.map((item) => item.text).join("\n"),
    verification:
      extras.verification?.trim() ||
      draft.verification.map((item) => item.text).join("\n"),
    decisions: extras.decisions?.trim() || "",
    rejectedApproaches: extras.rejectedApproaches?.trim() || "",
    nextAction: nextAction.trim(),
    correctionNotes: extras.correctionNotes?.trim() || "",
  };
}

export function renderCompactDraftPreview(draft: AutoDraft): string {
  const lines = [
    "Draft:",
    `  Objective: ${draft.objective.text}  [${draft.objective.confidence}]`,
    `  Completed: ${draft.completed.map((c) => c.text).join(" ")}`,
    `  Verification: ${draft.verification.map((v) => v.text).join(" ")}`,
    `  Files: ${
      draft.relevantFiles.length
        ? draft.relevantFiles.slice(0, 5).join(", ") +
          (draft.relevantFiles.length > 5
            ? ` (+${draft.relevantFiles.length - 5})`
            : "")
        : "(none)"
    }`,
  ];

  if (draft.relatedDecisions.length) {
    lines.push(
      `  Existing decisions that may apply: ${draft.relatedDecisions
        .map((d) => d.text)
        .join("; ")}`,
    );
  }

  lines.push(
    `  Next action: [${draft.suggestedNextAction.confidence}] ${draft.suggestedNextAction.text}`,
  );
  return lines.join("\n");
}

function inferObjectiveFromBranch(branch: string | null): string | null {
  if (!branch || branch === "main" || branch === "master") return null;
  return branch
    .replace(/^(feature|fix|chore|bugfix)\//i, "")
    .replace(/[-_]/g, " ")
    .trim();
}

function basename(filePath: string): string {
  return filePath.replace(/\\/g, "/").split("/").pop() || filePath;
}

function firstLine(text: string): string {
  return text.split(/\r?\n/).map((l) => l.trim()).find(Boolean) || text.trim();
}

function unique(items: string[]): string[] {
  return [...new Set(items)];
}

function looksLikeSourceFile(filePath: string): boolean {
  const normalized = filePath.replace(/\\/g, "/");
  if (!normalized || normalized.endsWith("/")) return false;
  if (normalized.startsWith("node_modules/") || normalized.startsWith("dist/")) return false;
  if (normalized === "package-lock.json") return false;
  if (normalized.startsWith(".trove/sessions/")) return true;
  if (normalized.startsWith(".trove/")) return false;
  return true;
}
