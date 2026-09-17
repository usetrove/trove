import type {
  EvidenceItem,
  SessionContext,
  TranscriptMessage,
} from "../types/handoff";

const FINDING_CUES =
  /\b(found that|key finding|it turns out|entry point|no .+ exists today|currently has|important:|discovered that|note that|the command is|writes to|lands in|there is currently no)\b/i;
const DECISION_CUES =
  /\b(decided to|decision:|we'll|we will|prefer|focus on|prioritize|v1 uses|recommendation:|keep writing|insert session)\b/i;
const NEXT_CUES =
  /\b(next step|next:|todo:|still need|open question|follow-?up:|implement |wire |remaining:|what to do next)\b/i;
const REJECT_CUES =
  /\b(rejected:|ruled out|won't |will not |instead of|avoid |do not |don't |not \.trove\/handoff\.md|llm-required|replacing git)\b/i;

/** Prefer real repo paths; avoid bare example filenames. */
const PATH_RE =
  /(?:^|[\s`"'(])((?:src|lib|app|packages|scripts|\.trove)\/[\w./@<>-]+\.\w{1,8})(?=[\s`"''),:;*]|$)/gim;

const NOISE_LINE =
  /^(files:|findings:|draft:|anything important|\[s\]|#{1,3}\s|---+|```|\d+\.\s|\|)/i;

const MAX_FILES = 15;
const MAX_ITEMS = 6;
const MAX_ITEM_CHARS = 220;

export function summarizeSession(
  messages: TranscriptMessage[],
  gitFiles: string[],
  source: string,
): SessionContext | null {
  if (messages.length === 0) return null;

  const userTexts = messages
    .filter((m) => m.role === "user")
    .map((m) => m.text)
    .filter((t) => t.trim().length > 0);
  const assistantTexts = messages
    .filter((m) => m.role === "assistant")
    .map((m) => m.text);
  const allText = [...userTexts, ...assistantTexts].join("\n");

  const taskSummary = buildTaskSummary(userTexts);
  const filesExplored = unique([
    ...extractPaths(allText),
    ...gitFiles.map(normalizePath),
  ])
    .filter(looksLikeSourceFile)
    .slice(0, MAX_FILES);

  return {
    taskSummary,
    filesExplored,
    keyFindings: classifyLines(allText, FINDING_CUES, "transcript-finding"),
    decisions: classifyLines(allText, DECISION_CUES, "transcript-decision"),
    openQuestions: classifyLines(allText, NEXT_CUES, "transcript-next"),
    rejectedApproaches: classifyLines(allText, REJECT_CUES, "transcript-rejected"),
    source,
  };
}

function buildTaskSummary(userTexts: string[]): EvidenceItem {
  const first = userTexts[0] ?? "";
  const goalSection = extractLabeledSection(first, "Goal");
  const problemSection = extractLabeledSection(first, "Current problem");

  let text = "";
  if (goalSection) {
    text = clipSentences(goalSection, 3);
  } else {
    const lastGoal = [...userTexts].reverse().find((t) => looksLikeGoal(t));
    text =
      lastGoal && lastGoal !== first
        ? `${clipSentences(first, 2)} ${clipSentences(lastGoal, 1)}`.trim()
        : clipSentences(first, 3);
  }

  if (!text && problemSection) {
    text = clipSentences(problemSection, 2);
  }

  return {
    text: text || "Continue current work (task unclear from transcript).",
    confidence: text ? "inferred" : "needs_confirmation",
    source: ["transcript"],
  };
}

function extractLabeledSection(text: string, label: string): string | null {
  const re = new RegExp(
    `${escapeRegExp(label)}:\\s*([\\s\\S]*?)(?=\\n\\s*\\n[A-Z][\\w ]+:|\\nDeliverables:|\\nRequirements:|\\nImplementation|$)`,
    "i",
  );
  const match = text.match(re)?.[1]?.trim();
  return match || null;
}

function looksLikeGoal(text: string): boolean {
  return /\b(goal|implement|update|fix|add|build|make|improve|wire|create)\b/i.test(
    text,
  );
}

function classifyLines(
  text: string,
  cue: RegExp,
  sourceTag: string,
): EvidenceItem[] {
  const items: EvidenceItem[] = [];
  const seen = new Set<string>();

  for (const line of text.split(/\r?\n/)) {
    const trimmed = line.replace(/^[-*#>\s]+/, "").trim();
    if (trimmed.length < 28 || trimmed.length > 400) continue;
    if (NOISE_LINE.test(trimmed)) continue;
    if (
      /\bcues\s*\(|line classifiers|option [ab]:|pseudocode|deliverables:|what the developer\b/i.test(
        trimmed,
      ) ||
      /^[A-Z][^:]{2,40}:\s+[a-z]/.test(trimmed)
    ) {
      continue;
    }
    if (!cue.test(trimmed)) continue;

    const normalized = trimmed.toLowerCase();
    if (seen.has(normalized)) continue;
    seen.add(normalized);

    items.push({
      text: clipChars(trimmed, MAX_ITEM_CHARS),
      confidence: "inferred",
      source: [sourceTag],
    });

    if (items.length >= MAX_ITEMS) break;
  }

  return items;
}

function extractPaths(text: string): string[] {
  const found: string[] = [];
  for (const match of text.matchAll(PATH_RE)) {
    const candidate = match[1]?.replace(/\\/g, "/");
    if (!candidate) continue;
    // Drop template placeholders like .trove/sessions/<id>.md
    if (/[<>*]/.test(candidate)) continue;
    found.push(normalizePath(candidate));
  }
  return found;
}

function clipSentences(text: string, maxSentences: number): string {
  const cleaned = text
    .replace(/\s+/g, " ")
    .replace(/^You are helping me[^.]*\.\s*/i, "")
    .replace(/^[-*]\s+/g, "")
    .trim();
  if (!cleaned) return "";

  const sentences = cleaned.match(/[^.!?]+[.!?]+|[^.!?]+$/g) ?? [cleaned];
  return sentences
    .map((s) => s.trim().replace(/^[-*]\s+/, ""))
    .filter(Boolean)
    .slice(0, maxSentences)
    .join(" ")
    .trim();
}

function clipChars(text: string, max: number): string {
  if (text.length <= max) return text;
  return `${text.slice(0, max - 3).trimEnd()}...`;
}

function normalizePath(filePath: string): string {
  return filePath.replace(/\\/g, "/").replace(/^\.\//, "");
}

function unique(items: string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const item of items) {
    const key = item.toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(item);
  }
  return out;
}

function looksLikeSourceFile(filePath: string): boolean {
  const normalized = filePath.replace(/\\/g, "/");
  if (!normalized || normalized.endsWith("/")) return false;
  if (normalized.startsWith("node_modules/") || normalized.startsWith("dist/")) {
    return false;
  }
  if (normalized === "package-lock.json") return false;
  if (normalized.startsWith(".trove/sessions/")) {
    return /\.[a-z0-9]+$/i.test(normalized) && !normalized.includes("/.");
  }
  if (normalized.startsWith(".trove/")) return false;
  // Prefer real repo paths over bare example filenames.
  if (!normalized.includes("/")) return false;
  return true;
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
