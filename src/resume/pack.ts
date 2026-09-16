import type { ResumePacket, ResumeSection } from "../types/resume";
import { RESUME_TOKEN_BUDGET, estimateTokens, trimToTokenBudget } from "./tokens";
import { sectionBody } from "./match";

const KIND_ORDER: ResumeSection["kind"][] = [
  "current-task",
  "decision",
  "rejected",
  "project",
  "session",
];

/**
 * Pack in deterministic priority order into a fixed ~800-token markdown brief.
 * Always attempts current-task first.
 */
export function packSections(
  sections: ResumeSection[],
  budget: number = RESUME_TOKEN_BUDGET,
  query: string,
): ResumePacket {
  const hardBudget = budget > 0 ? budget : RESUME_TOKEN_BUDGET;
  const safetyMargin = Math.max(24, Math.floor(hardBudget * 0.05));
  const usable = Math.max(1, hardBudget - safetyMargin);

  const ordered = [...sections].sort((a, b) => {
    const ai = KIND_ORDER.indexOf(a.kind);
    const bi = KIND_ORDER.indexOf(b.kind);
    if (ai !== bi) return ai - bi;
    return b.score - a.score;
  });

  const headerStub = renderBriefHeader(0, hardBudget, query);
  let used = estimateTokens(headerStub);
  const included: ResumeSection[] = [];
  const omitted: ResumeSection[] = [];

  for (const section of ordered) {
    const block = renderBriefSection(section);
    const blockTokens = estimateTokens(block) + 2;
    if (used + blockTokens > usable) {
      omitted.push(section);
      continue;
    }
    included.push(section);
    used += blockTokens;
  }

  let markdown = renderBriefMarkdown(included, omitted, hardBudget, query, used);

  // Hard cap: never exceed the stated approximate budget.
  if (estimateTokens(markdown) > hardBudget) {
    markdown = trimToTokenBudget(markdown, hardBudget);
  }

  return {
    query,
    budget: hardBudget,
    usedTokens: estimateTokens(markdown),
    included,
    omitted,
    markdown: markdown.endsWith("\n") ? markdown : `${markdown}\n`,
  };
}

function renderBriefHeader(used: number, budget: number, query: string): string {
  return [
    "# Trove Resume Brief",
    `Budget: ~${used} / ${budget} tokens (approx)`,
    query ? `Focus: ${query}` : null,
  ]
    .filter(Boolean)
    .join("\n");
}

function renderBriefMarkdown(
  included: ResumeSection[],
  omitted: ResumeSection[],
  budget: number,
  query: string,
  usedEstimate: number,
): string {
  const parts: string[] = [
    renderBriefHeader(usedEstimate, budget, query),
    "",
  ];

  if (included.length === 0) {
    parts.push(
      "_No durable memory found yet. Run `trove handoff` or edit `.trove/current-task.md`._",
    );
  } else {
    for (const section of included) {
      parts.push(renderBriefSection(section));
      parts.push("");
    }
  }

  if (omitted.length > 0) {
    parts.push("## Omitted");
    parts.push(
      omitted
        .slice(0, 6)
        .map((s) => `- ${s.title} (${s.kind})`)
        .join("\n"),
    );
    if (omitted.length > 6) {
      parts.push(`- …and ${omitted.length - 6} more`);
    }
    parts.push("");
  }

  return parts.join("\n").trimEnd() + "\n";
}

function renderBriefSection(section: ResumeSection): string {
  switch (section.kind) {
    case "current-task":
      return formatCurrentTaskBrief(section.body);
    case "decision":
      return `## Decision\n${compactDecision(section.body)}`;
    case "rejected":
      return `## Do not repeat\n${compactRejected(section.body)}`;
    case "project":
      return `## Project\n${truncateLines(section.body, 8)}`;
    case "session":
      return `## Latest handoff\n${formatSessionBrief(section.body)}`;
    default:
      return truncateLines(section.body, 12);
  }
}

function formatCurrentTaskBrief(body: string): string {
  const objective = clean(sectionBody(body, "Objective")) || "_unknown_";
  const next = clean(sectionBody(body, "Next action"));
  const files = sectionBody(body, "Relevant files");
  const status = clean(sectionBody(body, "Status"));

  const lines = [`## Current task`, objective];
  if (status) lines.push("", `Status: ${status}`);
  if (next) lines.push("", `### Next action`, next);
  if (files && files !== "-") {
    const fileLines = files
      .split(/\r?\n/)
      .map((l) => l.trim())
      .filter(Boolean)
      .slice(0, 8);
    if (fileLines.length) {
      lines.push("", "### Relevant files", ...fileLines);
    }
  }
  return lines.join("\n");
}

function formatSessionBrief(body: string): string {
  const objective =
    clean(sectionBody(body, "Objective")) ||
    body.match(/^#\s+Handoff:\s*(.+)$/m)?.[1]?.trim() ||
    null;
  const next = clean(sectionBody(body, "Exact next action"));
  const completed = bulletSummary(
    sectionBody(body, "Detected work") || sectionBody(body, "Current state"),
    4,
  );
  const verification = bulletSummary(sectionBody(body, "Verified evidence"), 3);
  const id = body.match(/^id:\s*(.+)$/m)?.[1]?.trim();

  const lines: string[] = [];
  if (id) lines.push(`Session: \`${id}\``);
  if (objective) lines.push(`Objective: ${objective}`);
  if (completed) {
    lines.push("Completed:");
    lines.push(completed);
  }
  if (verification) {
    lines.push("Verified:");
    lines.push(verification);
  }
  if (next) lines.push(`Next from handoff: ${next}`);
  if (lines.length === 0) return truncateLines(body, 10);
  return lines.join("\n");
}

function compactDecision(body: string): string {
  const title = body.match(/^##\s+(.+)$/m)?.[1]?.trim();
  const decision =
    body.match(/-\s*Decision:\s*(.+)$/im)?.[1]?.trim() ||
    clean(body.replace(/^##\s+.+$/m, ""));
  return title ? `**${title}** — ${decision}` : decision || truncateLines(body, 6);
}

function compactRejected(body: string): string {
  const title = body.match(/^##\s+(.+)$/m)?.[1]?.trim();
  const reason =
    body.match(/-\s*Reason:\s*(.+)$/im)?.[1]?.trim() ||
    body.match(/-\s*Attempted\/rejected approach:\s*(.+)$/im)?.[1]?.trim() ||
    clean(body.replace(/^##\s+.+$/m, ""));
  return title ? `**${title}** — ${reason}` : reason || truncateLines(body, 6);
}

function bulletSummary(text: string, max: number): string {
  const lines = text
    .split(/\r?\n/)
    .map((l) =>
      l
        .replace(/^[-*]\s*/, "")
        .replace(/\s*_\([^)]*\)_/g, "")
        .trim(),
    )
    .filter((l) => l && !l.startsWith("_None") && !/^_/.test(l));
  if (lines.length === 0) return "";
  return lines
    .slice(0, max)
    .map((l) => `- ${l}`)
    .join("\n");
}

function truncateLines(text: string, maxLines: number): string {
  const lines = text.trim().split(/\r?\n/);
  if (lines.length <= maxLines) return text.trim();
  return `${lines.slice(0, maxLines).join("\n")}\n…`;
}

function clean(text: string): string {
  return text
    .replace(/^[-*]\s*/gm, "")
    .replace(/_\([^)]*\)_/g, "")
    .replace(/_TBD_/gi, "")
    .trim();
}
