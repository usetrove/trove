import { existsSync } from "node:fs";
import path from "node:path";
import { readText } from "../core/fs";
import type { TranscriptMessage, TranscriptRole } from "../types/handoff";

/**
 * Resolve a transcript path for handoff.
 * V1: CLI `--transcript` and `TROVE_TRANSCRIPT` only (no auto-detect).
 */
export function resolveTranscriptPath(cliPath?: string): string | null {
  const fromCli = cliPath?.trim();
  if (fromCli) return path.resolve(fromCli);

  const fromEnv = process.env.TROVE_TRANSCRIPT?.trim();
  if (fromEnv) return path.resolve(fromEnv);

  return null;
}

export async function loadTranscript(
  transcriptPath: string | null,
): Promise<{ messages: TranscriptMessage[]; source: string | null }> {
  if (!transcriptPath) {
    return { messages: [], source: null };
  }

  if (!existsSync(transcriptPath)) {
    throw new Error(`Transcript not found: ${transcriptPath}`);
  }

  const raw = await readText(transcriptPath);
  const lower = transcriptPath.toLowerCase();
  const messages = lower.endsWith(".jsonl")
    ? parseCursorJsonl(raw)
    : parseMarkdownChat(raw);

  return { messages, source: transcriptPath };
}

export function parseCursorJsonl(raw: string): TranscriptMessage[] {
  const messages: TranscriptMessage[] = [];

  for (const line of raw.split(/\r?\n/)) {
    const trimmed = line.trim();
    if (!trimmed) continue;

    let parsed: unknown;
    try {
      parsed = JSON.parse(trimmed);
    } catch {
      continue;
    }

    if (!parsed || typeof parsed !== "object") continue;
    const row = parsed as {
      role?: string;
      message?: { content?: unknown };
      content?: unknown;
      text?: string;
    };

    const role = normalizeRole(row.role);
    const text = extractContentText(row.message?.content ?? row.content ?? row.text);
    if (!text.trim()) continue;

    messages.push({ role, text: cleanTranscriptText(text) });
  }

  return messages;
}

export function parseMarkdownChat(raw: string): TranscriptMessage[] {
  const messages: TranscriptMessage[] = [];
  const parts = raw.split(/^##\s+(User|Assistant|System|Human|AI)\s*$/gim);

  if (parts.length === 1) {
    const text = cleanTranscriptText(raw);
    if (text.trim()) messages.push({ role: "unknown", text });
    return messages;
  }

  // split keeps delimiters in odd indices when using capturing group
  for (let i = 1; i < parts.length; i += 2) {
    const heading = parts[i]?.trim() ?? "";
    const body = parts[i + 1] ?? "";
    const text = cleanTranscriptText(body);
    if (!text.trim()) continue;
    messages.push({ role: normalizeRole(heading), text });
  }

  return messages;
}

function extractContentText(content: unknown): string {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";

  const chunks: string[] = [];
  for (const item of content) {
    if (!item || typeof item !== "object") continue;
    const block = item as { type?: string; text?: string };
    if (block.type === "text" && typeof block.text === "string") {
      chunks.push(block.text);
    }
  }
  return chunks.join("\n");
}

function normalizeRole(role: string | undefined): TranscriptRole {
  const value = (role ?? "").trim().toLowerCase();
  if (value === "user" || value === "human") return "user";
  if (value === "assistant" || value === "ai" || value === "model") return "assistant";
  if (value === "system") return "system";
  return "unknown";
}

/** Strip Cursor wrappers and collapse whitespace for summarization. */
export function cleanTranscriptText(text: string): string {
  let cleaned = text;
  cleaned = cleaned.replace(/<timestamp>[\s\S]*?<\/timestamp>\s*/gi, "");
  cleaned = cleaned.replace(/<\/?user_query>/gi, "");
  cleaned = cleaned.replace(/<user_info>[\s\S]*?<\/user_info>/gi, "");
  cleaned = cleaned.replace(/<open_and_recently_viewed_files>[\s\S]*?<\/open_and_recently_viewed_files>/gi, "");
  cleaned = cleaned.replace(/<agent_transcripts>[\s\S]*?<\/agent_transcripts>/gi, "");
  cleaned = cleaned.replace(/<communication>[\s\S]*?<\/communication>/gi, "");
  // Do not strip arbitrary <...> — placeholders like <id> and <path> appear in docs.
  return cleaned.replace(/\r\n/g, "\n").replace(/[ \t]+\n/g, "\n").trim();
}
