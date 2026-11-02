// Validation for the anonymous summary that `trove contribute` sends.
//
// This mirrors internal/contribute/payload.go. It accepts exactly the fields
// that file defines, with fixed vocabularies and ranges, and rejects everything
// else, so the server cannot end up holding anything the client does not show.
// Keep the two in step: SCHEMA_VERSION changes in both places together.

export const SCHEMA_VERSION = 1;

const TOP = new Set([
  "schema_version", "trove_version", "os", "arch", "period_days", "requests_bucket",
  "agents", "waste_percent", "optimizers", "guardrail_trips", "install_id",
]);
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
  if (p.schema_version !== SCHEMA_VERSION) return "unsupported schema_version";
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
  return null;
}
