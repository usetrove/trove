// Validation for the anonymous summary that `trove contribute` sends.
//
// This mirrors internal/contribute/payload.go. It accepts exactly the fields
// that file defines, with fixed vocabularies and ranges, and rejects everything
// else, so the server cannot end up holding anything the client does not show.
// Keep the two in step: SCHEMA_VERSION changes in both places together.

export const SCHEMA_VERSION = 2;
// Older clients still send version 1, which is the same minus the limits section.
const ACCEPTED = new Set([1, 2]);

const TOP = new Set([
  "schema_version", "trove_version", "os", "arch", "period_days", "requests_bucket",
  "agents", "waste_percent", "optimizers", "guardrail_trips", "install_id", "limits",
]);

// The limits section (docs/decisions/limit-report.md, approved 2026-11-26).
export const PLANS = new Set(["pro", "max", "ultra", "team", "enterprise", "unspecified"]);
export const METERS = ["five_hour", "seven_day"];
export const TYPES = ["output", "cache_read", "cache_write_5m", "cache_write_1h"];
export const METRICS = new Set([
  "unexplained_rebuild_rate", "thinking_tokens_per_request", "thinking_request_share",
  "limit_pct_per_1k_output_equivalent",
]);
const CAUSES = new Set(["client", "server", "unclear"]);
export const VERSION = /^\d{1,3}\.\d{1,3}\.\d{1,4}$/;
const MAX_VERSIONS = 5;
const BUCKETS = new Set(["0", "1-19", "20-99", "100-999", "1000+"]);
const AGENTS = new Set(["claude-code", "codex", "other"]);
const LABELS = new Set(["measured", "estimated"]);
const STATES = new Set(["on", "shadow", "off"]);

const isInt = (v, lo, hi) => Number.isInteger(v) && v >= lo && v <= hi;
const isObj = (v) => v !== null && typeof v === "object" && !Array.isArray(v);
const noExtra = (obj, allowed) => Object.keys(obj).every((k) => allowed.has(k));

// validate returns null when the payload is acceptable, else a short reason
// that never repeats any of the submitted text.
export function validate(p) {
  if (!isObj(p)) return "not an object";
  if (!noExtra(p, TOP)) return "unknown field";
  if (!ACCEPTED.has(p.schema_version)) return "unsupported schema_version";
  if (typeof p.trove_version !== "string" || !/^[A-Za-z0-9.+-]{1,32}$/.test(p.trove_version)) return "bad trove_version";
  if (typeof p.os !== "string" || !/^[a-z0-9]{2,16}$/.test(p.os)) return "bad os";
  if (typeof p.arch !== "string" || !/^[a-z0-9]{2,16}$/.test(p.arch)) return "bad arch";
  if (!isInt(p.period_days, 0, 366)) return "bad period_days";
  if (!BUCKETS.has(p.requests_bucket)) return "bad requests_bucket";
  if (!Array.isArray(p.agents) || p.agents.length > 3 || !p.agents.every((a) => AGENTS.has(a))) return "bad agents";
  if (p.waste_percent !== undefined) {
    const w = p.waste_percent;
    if (!isObj(w) || !noExtra(w, new Set(["percent", "label"]))) return "bad waste_percent";
    if (!isInt(w.percent, -1000, 1000) || !LABELS.has(w.label)) return "bad waste_percent";
  }
  if (!Array.isArray(p.optimizers) || p.optimizers.length > 40) return "bad optimizers";
  for (const o of p.optimizers) {
    if (!isObj(o) || !noExtra(o, new Set(["name", "effective", "rolled_back", "percent_fewer_measured"]))) return "bad optimizer";
    if (typeof o.name !== "string" || !/^[a-z][a-z0-9_-]{0,40}$/.test(o.name)) return "bad optimizer name";
    if (!STATES.has(o.effective) || typeof o.rolled_back !== "boolean") return "bad optimizer";
    if (o.percent_fewer_measured !== undefined && !isInt(o.percent_fewer_measured, -1000, 100)) return "bad optimizer";
  }
  if (!isInt(p.guardrail_trips, 0, 100000)) return "bad guardrail_trips";
  if (typeof p.install_id !== "string" || !/^[0-9a-f]{32}$/.test(p.install_id)) return "bad install_id";
  if (p.limits !== undefined) {
    if (p.schema_version < 2) return "bad limits";
    const problem = validateLimits(p.limits);
    if (problem) return problem;
  }
  return null;
}

// A ratio as the client rounds it: positive and at most 1000.
const isRatio = (v) => typeof v === "number" && Number.isFinite(v) && v >= 0 && v <= 1000;

function validateLimits(l) {
  if (!isObj(l) || !noExtra(l, new Set(["plan", "claude_code_version", "five_hour", "seven_day", "version_checks"]))) return "bad limits";
  if (!PLANS.has(l.plan)) return "bad plan";
  if (l.claude_code_version !== undefined && (typeof l.claude_code_version !== "string" || !VERSION.test(l.claude_code_version))) return "bad claude_code_version";
  for (const m of METERS) {
    const meter = l[m];
    if (meter === undefined) continue;
    if (!isObj(meter) || !noExtra(meter, new Set(["ratios", "unexplained_pct"]))) return "bad limit meter";
    if (meter.unexplained_pct !== undefined && !isInt(meter.unexplained_pct, 0, 100)) return "bad unexplained_pct";
    if (meter.ratios !== undefined) {
      if (!isObj(meter.ratios) || !noExtra(meter.ratios, new Set(TYPES))) return "bad ratios";
      for (const t of Object.keys(meter.ratios)) {
        const r = meter.ratios[t];
        if (!isObj(r) || !noExtra(r, new Set(["ratio", "width"])) || !isRatio(r.ratio) || !isRatio(r.width)) return "bad ratio";
      }
    }
  }
  if (!Array.isArray(l.version_checks) || l.version_checks.length > MAX_VERSIONS * METRICS.size) return "bad version_checks";
  const versions = new Set();
  for (const c of l.version_checks) {
    if (!isObj(c) || !noExtra(c, new Set(["version", "metric", "status", "cause"]))) return "bad version check";
    if (typeof c.version !== "string" || !VERSION.test(c.version) || !METRICS.has(c.metric)) return "bad version check";
    if (c.status === "alert") {
      if (!CAUSES.has(c.cause)) return "bad version check";
    } else if (c.status !== "normal" || c.cause !== undefined) return "bad version check";
    versions.add(c.version);
  }
  if (versions.size > MAX_VERSIONS) return "bad version_checks";
  return null;
}
