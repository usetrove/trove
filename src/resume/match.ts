const STOP_WORDS = new Set([
  "a",
  "an",
  "and",
  "or",
  "the",
  "to",
  "of",
  "in",
  "on",
  "for",
  "with",
  "from",
  "into",
  "continue",
  "work",
  "task",
  "resume",
  "trove",
  "this",
  "that",
  "is",
  "are",
  "be",
  "as",
  "at",
  "by",
  "it",
]);

export function tokenize(text: string): string[] {
  return text
    .toLowerCase()
    .split(/[^a-z0-9/_.-]+/)
    .map((t) => t.trim())
    .filter((t) => t.length > 1 && !STOP_WORDS.has(t));
}

export function extractPaths(text: string): string[] {
  const matches = text.match(/[A-Za-z0-9_./\\-]+\.[A-Za-z0-9]+/g) ?? [];
  return [...new Set(matches.map((m) => m.replace(/\\/g, "/").toLowerCase()))];
}

export function stripComments(markdown: string): string {
  return markdown.replace(/<!--[\s\S]*?-->/g, "").trim();
}

export function splitH2Sections(markdown: string): { title: string; body: string }[] {
  const cleaned = stripComments(markdown);
  if (!cleaned) return [];

  const parts = cleaned.split(/^##\s+/m).filter(Boolean);
  const sections: { title: string; body: string }[] = [];

  for (const part of parts) {
    const lines = part.trim().split(/\r?\n/);
    const title = lines[0]?.replace(/^#\s+/, "").trim();
    if (!title) continue;
    // Skip file-level H1 leftovers that look like the doc title
    if (
      /^(decisions|rejected approaches|project|errata|current task)$/i.test(title)
    ) {
      continue;
    }
    const body = lines.slice(1).join("\n").trim();
    if (!body || isPlaceholder(body)) continue;
    sections.push({ title, body });
  }

  return sections;
}

export function sectionBody(markdown: string, heading: string): string {
  const re = new RegExp(
    `##\\s+${escapeRegExp(heading)}\\s*\\n([\\s\\S]*?)(?=\\n##\\s+|$)`,
    "i",
  );
  return markdown.match(re)?.[1]?.trim() ?? "";
}

export function isPlaceholder(text: string): boolean {
  const cleaned = text
    .replace(/```[\s\S]*?```/g, "")
    .replace(/^[-*]\s*/gm, "")
    .replace(/_/g, "")
    .trim()
    .toLowerCase();
  if (!cleaned || cleaned === "-") return true;
  return (
    cleaned.includes("what this repository is for") ||
    cleaned.includes("main subsystems and how they connect") ||
    cleaned === "tbd" ||
    cleaned === "none yet"
  );
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/** Keyword + path overlap score. Higher = better match. */
export function matchScore(
  text: string,
  queryTokens: string[],
  pathHints: string[],
): number {
  const haystack = text.toLowerCase();
  const tokens = new Set(tokenize(haystack));
  let score = 0;

  for (const token of queryTokens) {
    if (tokens.has(token) || haystack.includes(token)) score += 10;
  }
  for (const hint of pathHints) {
    if (haystack.includes(hint)) score += 18;
  }
  return score;
}
