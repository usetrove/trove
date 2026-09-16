/** Fixed V1 resume budget. Approximate: ~4 characters per token. */
export const RESUME_TOKEN_BUDGET = 800;

/** Approximate token estimate: ~4 characters per token. Consistent for V1. */
export function estimateTokens(text: string): number {
  const normalized = text.replace(/\s+/g, " ").trim();
  if (!normalized) return 0;
  return Math.max(1, Math.ceil(normalized.length / 4));
}

/** Trim text until it fits an approximate token budget. */
export function trimToTokenBudget(text: string, budget: number): string {
  if (estimateTokens(text) <= budget) return text;
  let lo = 0;
  let hi = text.length;
  let best = "";
  while (lo <= hi) {
    const mid = Math.floor((lo + hi) / 2);
    const candidate = `${text.slice(0, mid).trimEnd()}\n\n_(trimmed to ~${budget} token budget)_`;
    if (estimateTokens(candidate) <= budget) {
      best = candidate;
      lo = mid + 1;
    } else {
      hi = mid - 1;
    }
  }
  return best || text.slice(0, Math.max(0, budget * 3));
}
