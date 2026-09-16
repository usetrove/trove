export type ResumeKind =
  | "current-task"
  | "decision"
  | "rejected"
  | "project"
  | "session";

export interface ResumeSection {
  id: string;
  kind: ResumeKind;
  title: string;
  body: string;
  /** Deterministic match score; current-task is always packed first. */
  score: number;
  tokens: number;
  sourcePath: string;
}

export interface ResumePacket {
  query: string;
  budget: number;
  usedTokens: number;
  included: ResumeSection[];
  omitted: ResumeSection[];
  markdown: string;
}
