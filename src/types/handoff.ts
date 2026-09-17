export type Confidence = "verified" | "inferred" | "needs_confirmation";

export interface GitContext {
  repoRoot: string;
  branch: string | null;
  commitSha: string | null;
  statusShort: string;
  changedFiles: string[];
  stagedFiles: string[];
  unstagedFiles: string[];
  diffStat: string;
  numstat: string;
  recentCommits: string[];
  hasUncommittedChanges: boolean;
}

export interface EvidenceItem {
  text: string;
  confidence: Confidence;
  source: string[];
}

export interface CurrentTaskSnapshot {
  objective: string | null;
  nextAction: string | null;
  relevantFiles: string[];
  latestHandoff: string | null;
  raw: string | null;
}

export interface PriorHandoffSnapshot {
  path: string;
  id: string | null;
  objective: string | null;
  nextAction: string | null;
  excerpt: string;
}

export type TranscriptRole = "user" | "assistant" | "system" | "unknown";

export interface TranscriptMessage {
  role: TranscriptRole;
  text: string;
  ts?: string;
}

export interface SessionContext {
  taskSummary: EvidenceItem;
  filesExplored: string[];
  keyFindings: EvidenceItem[];
  decisions: EvidenceItem[];
  openQuestions: EvidenceItem[];
  rejectedApproaches: EvidenceItem[];
  /** Absolute path or label such as "none". */
  source: string;
}

export interface HandoffEvidence {
  git: GitContext;
  currentTask: CurrentTaskSnapshot;
  priorHandoff: PriorHandoffSnapshot | null;
  relatedDecisions: string[];
  relatedRejected: string[];
  session: SessionContext | null;
}

export interface AutoDraft {
  objective: EvidenceItem;
  completed: EvidenceItem[];
  verification: EvidenceItem[];
  remaining: EvidenceItem[];
  relevantFiles: string[];
  relatedDecisions: EvidenceItem[];
  relatedRejected: EvidenceItem[];
  suggestedNextAction: EvidenceItem;
  notes: string;
  session: SessionContext | null;
}

export interface HandoffInput {
  objective: string;
  completed: string;
  remainingOrBlocked: string;
  verification: string;
  decisions: string;
  rejectedApproaches: string;
  nextAction: string;
  correctionNotes: string;
}

export interface DurableRecordProposal {
  type: "decision" | "rejected_approach";
  title: string;
  content: string;
  confidence: "developer_confirmed";
}

export interface HandoffDocument {
  id: string;
  createdAt: string;
  git: GitContext;
  input: HandoffInput;
  relevantFiles: string[];
  durableProposals: DurableRecordProposal[];
  autoDraft: AutoDraft;
}

export interface TroveConfig {
  version: number;
  defaultContextBudgetTokens: number;
  includeGitDiffInHandoff: boolean;
  includeRecentCommits: boolean;
  maxDiffCharacters: number;
  defaultEditor: string;
}

export interface TrovePaths {
  root: string;
  troveDir: string;
  config: string;
  project: string;
  currentTask: string;
  decisions: string;
  rejected: string;
  errata: string;
  sessions: string;
  archive: string;
}
