import type {
  AutoDraft,
  DurableRecordProposal,
  EvidenceItem,
  HandoffDocument,
} from "../types/handoff";

function bullets(text: string, empty = "- _None reported._"): string {
  const lines = text
    .split(/\r?\n|;/)
    .map((line) => line.replace(/^[-*]\s*/, "").trim())
    .filter(Boolean);
  if (lines.length === 0) return empty;
  return lines.map((line) => `- ${line}`).join("\n");
}

function paragraph(text: string, empty = "_None reported._"): string {
  const trimmed = text.trim();
  return trimmed || empty;
}

function groupByConfidence(items: EvidenceItem[]): {
  verified: EvidenceItem[];
  inferred: EvidenceItem[];
  needs: EvidenceItem[];
} {
  return {
    verified: items.filter((i) => i.confidence === "verified"),
    inferred: items.filter((i) => i.confidence === "inferred"),
    needs: items.filter((i) => i.confidence === "needs_confirmation"),
  };
}

function renderConfidenceBlock(title: string, items: EvidenceItem[]): string {
  if (items.length === 0) return "";
  return `### ${title}\n${items.map((i) => `- ${i.text}`).join("\n")}\n`;
}

export function renderHandoffMarkdown(doc: HandoffDocument): string {
  const { git, input, relevantFiles, id, createdAt, autoDraft } = doc;
  const session = autoDraft.session;
  const files =
    relevantFiles.length > 0
      ? relevantFiles.map((f) => `- \`${f}\``).join("\n")
      : "- _None listed._";

  const frontmatterFiles = relevantFiles.map((f) => `  - ${f}`).join("\n");
  const workingTree = git.hasUncommittedChanges
    ? "modified files present"
    : "clean";
  const transcriptSource = session
    ? pathBasename(session.source)
    : "none";

  const evidenceItems = [
    ...autoDraft.completed,
    ...autoDraft.verification,
    {
      text: `Branch \`${git.branch ?? "unknown"}\`, commit \`${git.commitSha ?? "unknown"}\``,
      confidence: "verified" as const,
      source: ["git"],
    },
  ];
  const grouped = groupByConfidence(evidenceItems);

  const decisionsSection = input.decisions.trim()
    ? `## Decisions to preserve
### ${firstLineTitle(input.decisions)}
- Decision: ${paragraph(input.decisions)}
- Why: _Developer-confirmed during review._
- Scope: ${(relevantFiles[0]) ? `\`${relevantFiles[0]}\`` : "_unspecified_"}
- Status: active
- Confidence: developer_confirmed
`
    : `## Decisions to preserve
_None promoted in this handoff._
`;

  const rejectedSection = input.rejectedApproaches.trim()
    ? `## Rejected approaches
### ${firstLineTitle(input.rejectedApproaches)}
- Attempted/rejected approach: ${paragraph(input.rejectedApproaches)}
- Why rejected: _Developer-confirmed during review._
- Evidence: Developer review during handoff.
- Confidence: developer_confirmed
`
    : `## Rejected approaches
_None promoted in this handoff._
`;

  const relatedDecisions =
    autoDraft.relatedDecisions.length > 0
      ? `## Existing decisions that may apply
${autoDraft.relatedDecisions.map((d) => `- ${d.text} _(inferred)_`).join("\n")}
`
      : "";

  return `---
id: ${id}
created_at: ${createdAt}
branch: ${git.branch ?? "unknown"}
commit: ${git.commitSha ?? "unknown"}
status: active
relevant_files:${frontmatterFiles ? `\n${frontmatterFiles}` : " []"}
transcript_source: ${transcriptSource}
---

# Handoff: ${input.objective.trim() || "Untitled"}

## Objective
${paragraph(input.objective)}
_(${autoDraft.objective.confidence}; source: ${autoDraft.objective.source.join(", ")})_

${renderSessionContext(doc)}
## Detected work
${bullets(input.completed)}
${git.hasUncommittedChanges ? "- The working tree contains uncommitted changes. _(verified)_" : "- Working tree is clean. _(verified)_"}

## Evidence by confidence
${renderConfidenceBlock("Verified", grouped.verified)}${renderConfidenceBlock("Inferred", grouped.inferred)}${renderConfidenceBlock("Needs confirmation", grouped.needs) || "### Needs confirmation\n- Confirm semantic conclusions before treating them as durable truth.\n"}

## Files changed or relevant
${files}

## Remaining / open
${bullets(input.remainingOrBlocked)}
${input.correctionNotes.trim() ? `\n## Corrections from review\n${paragraph(input.correctionNotes)}\n` : ""}
${relatedDecisions}
${decisionsSection}
${rejectedSection}
## Exact next action
${paragraph(input.nextAction)}
_(developer_confirmed)_

## Git checkpoint
- Branch: \`${git.branch ?? "unknown"}\`
- Commit: \`${git.commitSha ?? "unknown"}\`
- Working tree: ${workingTree}
- Staged files: ${git.stagedFiles.length}
- Unstaged/changed files: ${git.changedFiles.length}
${git.recentCommits.length ? `\n### Recent commits\n${git.recentCommits.map((c) => `- ${c}`).join("\n")}` : ""}
${git.diffStat.trim() ? `\n### Diff stat\n\`\`\`\n${git.diffStat.trim()}\n\`\`\`` : ""}
`;
}

function renderSessionContext(doc: HandoffDocument): string {
  const session = doc.autoDraft.session;
  if (!session) {
    return `## Session context
_No transcript provided. Pass \`--transcript <path>\` or set \`TROVE_TRANSCRIPT\` to capture session-level context._

`;
  }

  const files =
    session.filesExplored.length > 0
      ? session.filesExplored.map((f) => `- \`${f}\``).join("\n")
      : "- _None detected._";

  return `## Session context

### Task summary
${paragraph(session.taskSummary.text)}
_(${session.taskSummary.confidence}; source: ${session.taskSummary.source.join(", ")})_

### Files and modules explored
${files}

### Key findings
${evidenceBullets(session.keyFindings)}

### Decisions / focus areas
${evidenceBullets(session.decisions)}

### Open questions / next steps
${evidenceBullets(session.openQuestions)}

### Rejected approaches
${evidenceBullets(session.rejectedApproaches)}

`;
}

function evidenceBullets(items: EvidenceItem[], empty = "- _None detected._"): string {
  if (items.length === 0) return empty;
  return items.map((i) => `- ${i.text}`).join("\n");
}

function pathBasename(filePath: string): string {
  return filePath.replace(/\\/g, "/").split("/").pop() || filePath;
}

export function renderReviewMarkdown(draft: AutoDraft): string {
  const sessionBlock = draft.session
    ? `## Session context (from transcript)
${draft.session.taskSummary.text}
_(${draft.session.taskSummary.confidence})_

Files: ${
        draft.session.filesExplored.length
          ? draft.session.filesExplored.slice(0, 8).join(", ")
          : "_none_"
      }
Findings: ${
        draft.session.keyFindings.length
          ? draft.session.keyFindings.map((f) => f.text).join("; ")
          : "_none_"
      }
`
    : `## Session context
_No transcript provided._
`;

  return `# Handoff Draft

## Current task
${draft.objective.text}
_(${draft.objective.confidence})_

${sessionBlock}
## Detected work
${draft.completed.map((c) => `- ${c.text} _(${c.confidence})_`).join("\n")}

## Files likely relevant
${
  draft.relevantFiles.length
    ? draft.relevantFiles.map((f) => `- ${f}`).join("\n")
    : "- _None detected_"
}

## Existing decisions that may apply
${
  draft.relatedDecisions.length
    ? draft.relatedDecisions.map((d) => `- ${d.text}`).join("\n")
    : "- _None matched_"
}

## Verification evidence
${draft.verification.map((v) => `- ${v.text} _(${v.confidence})_`).join("\n")}

## Suggested next action
${draft.suggestedNextAction.text}
_(${draft.suggestedNextAction.confidence})_
`;
}

export function renderCurrentTaskMarkdown(
  doc: HandoffDocument,
  sessionRelativePath: string,
): string {
  const files =
    (doc.relevantFiles.length > 0 ? doc.relevantFiles : doc.git.changedFiles)
      .map((f) => `- ${f}`)
      .join("\n") || "- _None listed._";

  return `# Current Task

## Objective
${doc.input.objective.trim()}

## Status
Active

## Latest handoff
${sessionRelativePath}

## Next action
${doc.input.nextAction.trim()}

## Relevant files
${files}
`;
}

export function renderDurableDecision(proposal: DurableRecordProposal, date: string): string {
  return `
## ${proposal.title}

- Date: ${date}
- Status: Active
- Decision: ${proposal.content.trim()}
- Rationale: _Developer-confirmed during handoff._
- Confidence: ${proposal.confidence}
`;
}

export function renderDurableRejected(proposal: DurableRecordProposal, date: string): string {
  return `
## ${proposal.title}

- Rejected on: ${date}
- Reason: ${proposal.content.trim()}
- Confidence: ${proposal.confidence}
`;
}

function firstLineTitle(text: string): string {
  const line = text
    .split(/\r?\n|;/)
    .map((l) => l.replace(/^[-*]\s*/, "").trim())
    .find(Boolean);
  if (!line) return "Untitled";
  return line.length > 72 ? `${line.slice(0, 69)}...` : line;
}
