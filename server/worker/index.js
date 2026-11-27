// Receiver for the opt-in anonymous summary from `trove contribute enable`.
//
//   POST   /v1/contribute          store one validated summary
//   DELETE /v1/contribute/<id>     delete every row stored under that install id
//   GET    /v1/limit-report        the latest approved weekly limit report, as HTML
//   GET    /v1/limit-report.json   the same, as JSON
//
// Privacy rules for this file:
//   - It never reads or stores the client IP, headers or cookies, and never logs
//     a request or a body.
//   - It stores the validated JSON and the UTC day it arrived, nothing else.
//   - Rows older than RETENTION_DAYS are deleted by the daily cron trigger.
//   - The weekly cron publishes only aggregate.js output, and only once a maintainer approved it
//     (or, after REVIEWED_WEEKS approved weeks, when heldForReview finds nothing to look at).
import { validate } from "./validate.js";
import { aggregate, heldForReview, isoWeek, weekDays } from "./aggregate.js";
import { renderPage } from "./page.js";

// Must match the weekly entry in wrangler.toml.
export const WEEKLY_CRON = "41 4 * * 1";

// Must stay above contribute.MaxBody in internal/contribute/limits.go (the largest payload the client can build).
const MAX_BODY = 16384;
const RETENTION_DAYS = 365;
// The most summaries stored in one UTC day. Install ids are random and free to make,
// so this is what bounds storage if someone floods the endpoint. A real install sends
// one a week; Trove treats the 429 as "try again later".
export const MAX_PER_DAY = 5000;
const ID = /^[0-9a-f]{32}$/;

// A 204 must not have a body: Response throws if it does, which surfaces as error 1101.
const reply = (status, text) =>
  status === 204
    ? new Response(null, { status })
    : new Response(text + "\n", { status, headers: { "content-type": "text/plain" } });
const today = () => new Date().toISOString().slice(0, 10);

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const del = url.pathname.match(/^\/v1\/contribute\/([^/]+)$/);

    if (request.method === "POST" && url.pathname === "/v1/contribute") {
      const len = Number(request.headers.get("content-length") || 0);
      if (len > MAX_BODY) return reply(413, "too large");
      const raw = await request.text();
      if (raw.length > MAX_BODY) return reply(413, "too large");
      let p;
      try {
        p = JSON.parse(raw);
      } catch {
        return reply(400, "not JSON");
      }
      const problem = validate(p);
      if (problem) return reply(400, problem);
      const day = today();
      const used = await env.DB.prepare("SELECT COUNT(*) AS n FROM submissions WHERE received_day = ?").bind(day).first();
      if (used && used.n >= MAX_PER_DAY) return reply(429, "too many today, try again later");
      // One row per install per day: a repeat the same day replaces nothing.
      await env.DB.prepare(
        "INSERT OR IGNORE INTO submissions (received_day, install_id, payload) VALUES (?, ?, ?)",
      ).bind(day, p.install_id, JSON.stringify(p)).run();
      return reply(204, "");
    }

    if (request.method === "DELETE" && del) {
      if (!ID.test(del[1])) return reply(400, "bad install id");
      await env.DB.prepare("DELETE FROM submissions WHERE install_id = ?").bind(del[1]).run();
      return reply(204, "");
    }

    if (request.method === "GET" && (url.pathname === "/v1/limit-report" || url.pathname === "/v1/limit-report.json")) {
      const row = await env.DB.prepare("SELECT report FROM limit_reports WHERE approved = 1 ORDER BY week DESC LIMIT 1").bind().first();
      if (!row) return reply(404, "no limit report has been published yet");
      const headers = { "cache-control": "public, max-age=3600", "x-content-type-options": "nosniff" };
      if (url.pathname.endsWith(".json")) {
        return new Response(row.report, { status: 200, headers: { ...headers, "content-type": "application/json" } });
      }
      return new Response(renderPage(JSON.parse(row.report)), {
        status: 200,
        headers: { ...headers, "content-type": "text/html; charset=utf-8", "content-security-policy": "default-src 'none'; style-src 'unsafe-inline'" },
      });
    }

    return reply(404, "not found");
  },

  async scheduled(event, env, now = Date.now()) {
    if (event && event.cron === WEEKLY_CRON) return publishWeek(env, now);
    const cutoff = new Date(now - RETENTION_DAYS * 86400000).toISOString().slice(0, 10);
    await env.DB.prepare("DELETE FROM submissions WHERE received_day < ?").bind(cutoff).run();
  },
};

// publishWeek aggregates the ISO week that ended before now and stores the aggregate, approved only when
// heldForReview finds nothing a maintainer needs to see. A week already stored is left as it is.
export async function publishWeek(env, now) {
  const lastDay = new Date(now - 86400000).toISOString().slice(0, 10);
  const days = weekDays(lastDay);
  const week = isoWeek(lastDay);
  const { results } = await env.DB.prepare(
    "SELECT received_day, install_id, payload FROM submissions WHERE received_day BETWEEN ? AND ?",
  ).bind(days[0], days[6]).all();
  const report = aggregate(results || [], week);
  const prev = await env.DB.prepare("SELECT report FROM limit_reports WHERE approved = 1 ORDER BY week DESC LIMIT 1").bind().first();
  const count = await env.DB.prepare("SELECT COUNT(*) AS n FROM limit_reports WHERE approved = 1").bind().first();
  const held = heldForReview(report, prev ? JSON.parse(prev.report) : null, count ? count.n : 0);
  await env.DB.prepare("INSERT OR IGNORE INTO limit_reports (week, report, approved) VALUES (?, ?, ?)")
    .bind(week, JSON.stringify(report), held ? 0 : 1).run();
  return { week, held };
}
