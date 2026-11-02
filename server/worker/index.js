// Receiver for the opt-in anonymous summary from `trove contribute enable`.
//
//   POST   /v1/contribute          store one validated summary
//   DELETE /v1/contribute/<id>     delete every row stored under that install id
//
// Privacy rules for this file:
//   - It never reads or stores the client IP, headers or cookies, and never logs
//     a request or a body.
//   - It stores the validated JSON and the UTC day it arrived, nothing else.
//   - Rows older than RETENTION_DAYS are deleted by the daily cron trigger.
import { validate } from "./validate.js";

const MAX_BODY = 8192;
const RETENTION_DAYS = 365;
const ID = /^[0-9a-f]{32}$/;

const reply = (status, text) => new Response(text + "\n", { status, headers: { "content-type": "text/plain" } });
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
      // One row per install per day: a repeat the same day replaces nothing.
      await env.DB.prepare(
        "INSERT OR IGNORE INTO submissions (received_day, install_id, payload) VALUES (?, ?, ?)",
      ).bind(today(), p.install_id, JSON.stringify(p)).run();
      return reply(204, "");
    }

    if (request.method === "DELETE" && del) {
      if (!ID.test(del[1])) return reply(400, "bad install id");
      await env.DB.prepare("DELETE FROM submissions WHERE install_id = ?").bind(del[1]).run();
      return reply(204, "");
    }

    return reply(404, "not found");
  },

  async scheduled(_event, env) {
    const cutoff = new Date(Date.now() - RETENTION_DAYS * 86400000).toISOString().slice(0, 10);
    await env.DB.prepare("DELETE FROM submissions WHERE received_day < ?").bind(cutoff).run();
  },
};
