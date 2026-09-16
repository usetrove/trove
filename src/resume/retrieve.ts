import { existsSync } from "node:fs";
import { readdir } from "node:fs/promises";
import path from "node:path";
import { readText } from "../core/fs";
import type { TrovePaths } from "../types/handoff";
import type { ResumeSection } from "../types/resume";
import {
  extractPaths,
  isPlaceholder,
  matchScore,
  sectionBody,
  splitH2Sections,
  stripComments,
  tokenize,
} from "./match";
import { estimateTokens } from "./tokens";

export interface LoadedMemory {
  currentTaskRaw: string | null;
  currentTaskObjective: string | null;
  currentTaskNext: string | null;
  currentTaskFiles: string[];
  latestHandoffPath: string | null;
  decisionsRaw: string | null;
  rejectedRaw: string | null;
  projectRaw: string | null;
  sessionFiles: string[];
}

export async function loadMemory(paths: TrovePaths): Promise<LoadedMemory> {
  const currentTaskRaw = await readOptional(paths.currentTask);
  const decisionsRaw = await readOptional(paths.decisions);
  const rejectedRaw = await readOptional(paths.rejected);
  const projectRaw = await readOptional(paths.project);

  const latestHandoffRel = currentTaskRaw
    ? sectionBody(currentTaskRaw, "Latest handoff").split(/\r?\n/)[0]?.trim() ||
      null
    : null;

  const sessionFiles = existsSync(paths.sessions)
    ? (await readdir(paths.sessions))
        .filter((name) => name.endsWith(".md") && !name.startsWith("."))
        .sort()
        .reverse()
    : [];

  const fileBullets = currentTaskRaw
    ? sectionBody(currentTaskRaw, "Relevant files")
        .split(/\r?\n/)
        .map((l) => l.replace(/^[-*`\s]+|[-*`\s]+$/g, "").trim())
        .filter(Boolean)
    : [];

  return {
    currentTaskRaw,
    currentTaskObjective: cleanValue(
      currentTaskRaw ? sectionBody(currentTaskRaw, "Objective") : "",
    ),
    currentTaskNext: cleanValue(
      currentTaskRaw ? sectionBody(currentTaskRaw, "Next action") : "",
    ),
    currentTaskFiles: fileBullets,
    latestHandoffPath: latestHandoffRel,
    decisionsRaw,
    rejectedRaw,
    projectRaw,
    sessionFiles,
  };
}

/**
 * Deterministic retrieval order:
 * 1. current task
 * 2. matched decisions
 * 3. matched rejected approaches
 * 4. related project notes
 * 5. newest relevant handoff
 */
export async function retrieveSections(
  paths: TrovePaths,
  memory: LoadedMemory,
  query: string,
): Promise<ResumeSection[]> {
  const queryTokens = tokenize(query);
  const pathHints = [
    ...extractPaths(query),
    ...memory.currentTaskFiles.map((f) => f.toLowerCase()),
  ];

  // Expand query with current-task terms so decisions/project match the active work.
  if (memory.currentTaskObjective) {
    queryTokens.push(...tokenize(memory.currentTaskObjective));
  }
  if (memory.currentTaskNext) {
    queryTokens.push(...tokenize(memory.currentTaskNext));
  }
  const uniqueTokens = [...new Set(queryTokens)];

  const sections: ResumeSection[] = [];

  // 1. Current task — always first candidate
  if (memory.currentTaskRaw && !isPlaceholder(memory.currentTaskRaw)) {
    const body = stripComments(memory.currentTaskRaw);
    sections.push({
      id: "current-task",
      kind: "current-task",
      title: "Current task",
      body,
      score: 1_000_000,
      tokens: estimateTokens(body),
      sourcePath: paths.currentTask,
    });
  }

  // 2. Decisions (matched)
  if (memory.decisionsRaw) {
    for (const section of splitH2Sections(memory.decisionsRaw)) {
      const full = `## ${section.title}\n${section.body}`;
      const score = matchScore(full, uniqueTokens, pathHints);
      if (score <= 0) continue;
      sections.push({
        id: `decision:${section.title}`,
        kind: "decision",
        title: section.title,
        body: full,
        score: 500_000 + score,
        tokens: estimateTokens(full),
        sourcePath: paths.decisions,
      });
    }
  }

  // 3. Rejected (matched)
  if (memory.rejectedRaw) {
    for (const section of splitH2Sections(memory.rejectedRaw)) {
      const full = `## ${section.title}\n${section.body}`;
      const score = matchScore(full, uniqueTokens, pathHints);
      if (score <= 0) continue;
      sections.push({
        id: `rejected:${section.title}`,
        kind: "rejected",
        title: section.title,
        body: full,
        score: 400_000 + score,
        tokens: estimateTokens(full),
        sourcePath: paths.rejected,
      });
    }
  }

  // 4. Related project notes
  if (memory.projectRaw) {
    for (const section of splitH2Sections(memory.projectRaw)) {
      const full = `### ${section.title}\n${section.body}`;
      let score = matchScore(full, uniqueTokens, pathHints);
      // Light prior for stable orientation sections when they have real content
      if (/architecture|convention|constraint|purpose|command/i.test(section.title)) {
        score += 3;
      }
      if (score <= 0) continue;
      sections.push({
        id: `project:${section.title}`,
        kind: "project",
        title: section.title,
        body: full,
        score: 300_000 + score,
        tokens: estimateTokens(full),
        sourcePath: paths.project,
      });
    }
  }

  // 5. Newest relevant handoff (one session)
  const session = await pickNewestRelevantSession(
    paths,
    memory,
    uniqueTokens,
    pathHints,
  );
  if (session) sections.push(session);

  // Stable deterministic order: kind priority already baked into score bands,
  // then higher match score, then title.
  return sections.sort((a, b) => {
    if (b.score !== a.score) return b.score - a.score;
    return a.title.localeCompare(b.title);
  });
}

async function pickNewestRelevantSession(
  paths: TrovePaths,
  memory: LoadedMemory,
  queryTokens: string[],
  pathHints: string[],
): Promise<ResumeSection | null> {
  if (memory.sessionFiles.length === 0) return null;

  type Candidate = { file: string; fullPath: string; raw: string; score: number };
  const candidates: Candidate[] = [];

  // Prefer the handoff linked from current-task, then scan newest sessions.
  const preferredNames = [
    memory.latestHandoffPath
      ? path.basename(memory.latestHandoffPath)
      : null,
    ...memory.sessionFiles,
  ].filter((v, i, arr): v is string => Boolean(v) && arr.indexOf(v) === i);

  for (const file of preferredNames.slice(0, 8)) {
    const fullPath = path.join(paths.sessions, file);
    if (!existsSync(fullPath) && memory.latestHandoffPath) {
      const alt = path.join(paths.root, memory.latestHandoffPath);
      if (existsSync(alt)) {
        const raw = await readText(alt);
        const score =
          matchScore(raw, queryTokens, pathHints) +
          (file === path.basename(memory.latestHandoffPath || "") ? 50 : 0);
        candidates.push({ file, fullPath: alt, raw, score });
        continue;
      }
    }
    if (!existsSync(fullPath)) continue;
    const raw = await readText(fullPath);
    const recencyBoost = Math.max(0, 40 - candidates.length * 5);
    const linkedBoost =
      memory.latestHandoffPath &&
      path.basename(memory.latestHandoffPath) === file
        ? 50
        : 0;
    const score = matchScore(raw, queryTokens, pathHints) + recencyBoost + linkedBoost;
    candidates.push({ file, fullPath, raw, score });
  }

  if (candidates.length === 0) return null;

  // Newest relevant: among those with score > recency-only floor, pick best;
  // if none match query, still take the newest (first in preferredNames / sessionFiles).
  const matched = candidates.filter((c) => c.score > 40);
  const chosen =
    matched.sort((a, b) => b.score - a.score)[0] ??
    candidates[0];

  if (!chosen) return null;

  const truncated = truncate(chosen.raw, 1800);
  return {
    id: `session:${chosen.file}`,
    kind: "session",
    title: `Session ${chosen.file.replace(/\.md$/, "")}`,
    body: truncated,
    score: 200_000 + chosen.score,
    tokens: estimateTokens(truncated),
    sourcePath: chosen.fullPath,
  };
}

async function readOptional(filePath: string): Promise<string | null> {
  if (!existsSync(filePath)) return null;
  return readText(filePath);
}

function cleanValue(text: string): string | null {
  const cleaned = text
    .replace(/^[-*]\s*/gm, "")
    .replace(/_TBD_/gi, "")
    .trim();
  if (!cleaned || isPlaceholder(cleaned)) return null;
  return cleaned.split(/\r?\n/).map((l) => l.trim()).filter(Boolean).join("; ");
}

function truncate(text: string, maxChars: number): string {
  const cleaned = stripComments(text).trim();
  if (cleaned.length <= maxChars) return cleaned;
  return `${cleaned.slice(0, maxChars).trimEnd()}\n\n_(truncated)_`;
}
