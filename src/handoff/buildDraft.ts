import { handoffId, isoWithOffset } from "../core/time";
import type {
  AutoDraft,
  DurableRecordProposal,
  GitContext,
  HandoffDocument,
  HandoffInput,
} from "../types/handoff";

export function buildHandoffDocument(args: {
  gitContext: GitContext;
  input: HandoffInput;
  relevantFiles: string[];
  autoDraft: AutoDraft;
  createdAt?: Date;
}): HandoffDocument {
  const createdAt = args.createdAt ?? new Date();
  const id = handoffId(createdAt);

  return {
    id,
    createdAt: isoWithOffset(createdAt),
    git: args.gitContext,
    input: args.input,
    relevantFiles: args.relevantFiles,
    durableProposals: buildDurableRecordProposals(args.input),
    autoDraft: args.autoDraft,
  };
}

export function buildDurableRecordProposals(
  input: HandoffInput,
): DurableRecordProposal[] {
  const proposals: DurableRecordProposal[] = [];

  if (input.decisions.trim()) {
    proposals.push({
      type: "decision",
      title: titleFrom(input.decisions),
      content: input.decisions.trim(),
      confidence: "developer_confirmed",
    });
  }

  if (input.rejectedApproaches.trim()) {
    proposals.push({
      type: "rejected_approach",
      title: titleFrom(input.rejectedApproaches),
      content: input.rejectedApproaches.trim(),
      confidence: "developer_confirmed",
    });
  }

  return proposals;
}

function titleFrom(text: string): string {
  const line = text
    .split(/\r?\n|;/)
    .map((l) => l.replace(/^[-*]\s*/, "").trim())
    .find(Boolean);
  if (!line) return "Untitled";
  return line.length > 72 ? `${line.slice(0, 69)}...` : line;
}
