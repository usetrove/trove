import { existsSync } from "node:fs";
import { readdir } from "node:fs/promises";
import path from "node:path";
import { readText } from "../core/fs";
import type {
  CurrentTaskSnapshot,
  HandoffEvidence,
  PriorHandoffSnapshot,
  TrovePaths,
} from "../types/handoff";
import { collectGitContext } from "./collectGitContext";

export async function collectHandoffEvidence(
  paths: TrovePaths,
): Promise<HandoffEvidence> {
  const git = await collectGitContext(paths.root);
  const currentTask = await readCurrentTask(paths.currentTask);
  const priorHandoff = await readLatestSession(paths.sessions);
  const relatedDecisions = await readRelatedSections(
    paths.decisions,
    git.changedFiles,
  );
  const relatedRejected = await readRelatedSections(
    paths.rejected,
    git.changedFiles,
  );

  return {
    git,
    currentTask,
    priorHandoff,
    relatedDecisions,
    relatedRejected,
  };
}

async function readCurrentTask(filePath: string): Promise<CurrentTaskSnapshot> {
  if (!existsSync(filePath)) {
    return {
      objective: null,
      nextAction: null,
      relevantFiles: [],
      latestHandoff: null,
      raw: null,
    };
  }

  const raw = await readText(filePath);
  return {
    objective: sectionValue(raw, "Objective"),
    nextAction: sectionValue(raw, "Next action"),
    relevantFiles: bulletLines(sectionBody(raw, "Relevant files")),
    latestHandoff: sectionValue(raw, "Latest handoff"),
    raw,
  };
}

async function readLatestSession(
  sessionsDir: string,
): Promise<PriorHandoffSnapshot | null> {
  if (!existsSync(sessionsDir)) return null;
  const entries = (await readdir(sessionsDir))
    .filter((name) => name.endsWith(".md") && !name.startsWith("."))
    .sort()
    .reverse();
  const latest = entries[0];
  if (!latest) return null;

  const fullPath = path.join(sessionsDir, latest);
  const raw = await readText(fullPath);
  return {
    path: fullPath,
    id: raw.match(/^id:\s*(.+)$/m)?.[1]?.trim() ?? latest.replace(/\.md$/, ""),
    objective:
      sectionValue(raw, "Objective") ||
      raw.match(/^#\s+Handoff:\s*(.+)$/m)?.[1]?.trim() ||
      null,
    nextAction: sectionValue(raw, "Exact next action"),
    excerpt: raw.slice(0, 1200),
  };
}

async function readRelatedSections(
  filePath: string,
  changedFiles: string[],
): Promise<string[]> {
  if (!existsSync(filePath)) return [];
  const raw = await readText(filePath);
  const sections = raw.split(/^##\s+/m).slice(1);
  const related: string[] = [];

  for (const section of sections) {
    const lines = section.trim().split(/\r?\n/);
    const title = lines[0]?.trim();
    if (!title) continue;
    const body = lines.slice(1).join("\n");
    const haystack = `${title}\n${body}`.toLowerCase();

    const pathHit = changedFiles.some((file) =>
      haystack.includes(file.toLowerCase().replace(/\\/g, "/")),
    );
    // Keep a few recent/active decisions even without path match
    if (pathHit || /status:\s*active/i.test(body)) {
      related.push(`${title}\n${body.trim()}`.trim());
    }
  }

  return related.slice(0, 5);
}

function sectionBody(markdown: string, heading: string): string {
  const re = new RegExp(`##\\s+${escapeRegExp(heading)}\\s*\\n([\\s\\S]*?)(?=\\n##\\s+|$)`, "i");
  return markdown.match(re)?.[1]?.trim() ?? "";
}

function sectionValue(markdown: string, heading: string): string | null {
  const body = sectionBody(markdown, heading);
  if (!body) return null;
  const cleaned = body
    .replace(/^[-*]\s*/gm, "")
    .replace(/_TBD_/gi, "")
    .replace(/_None yet_/gi, "")
    .trim();
  if (!cleaned || cleaned === "-" || cleaned === "_") return null;
  return cleaned.split(/\r?\n/).map((l) => l.trim()).filter(Boolean).join("; ");
}

function bulletLines(body: string): string[] {
  return body
    .split(/\r?\n/)
    .map((line) => line.replace(/^[-*]\s*/, "").trim())
    .filter((line) => line && line !== "_" && !/^_TBD_$/i.test(line));
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
