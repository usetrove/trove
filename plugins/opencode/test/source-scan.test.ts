import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

// The plugin never reads, stores or forwards a credential (CLAUDE.md rules 2, 3
// and 10), makes no network call of its own (rule 1), and holds no optimization
// logic. These are checks on the source text, with comments removed, because a
// comment may say what the code does not do.

const src = join(dirname(fileURLToPath(import.meta.url)), "..", "src");

function strip(text: string): string {
  return text.replace(/\/\*[\s\S]*?\*\//g, "").replace(/(^|[^:"'`])\/\/.*$/gm, "$1");
}

function code(file: string): string {
  return strip(readFileSync(join(src, file), "utf8"));
}

test("the scanner keeps code and drops comments (so a pass means something)", () => {
  const sample = ["const a = process.env.X; // headers", "/* Authorization */ const b = 1;", 'const u = "http://x";'].join("\n");
  const out = strip(sample);
  assert.match(out, /process\.env/);
  assert.doesNotMatch(out, /headers|Authorization/);
  assert.match(out, /http:\/\/x/);
});

const files = readdirSync(src).filter((f) => f.endsWith(".ts"));

test("there are source files to scan", () => {
  assert.ok(files.length >= 6, files.join(","));
});

const credentialPatterns: [string, RegExp][] = [
  ["environment variables", /\bprocess\.env\b/],
  ["request or response headers", /\.headers\b|\bnew Headers\b/],
  ["an Authorization value", /authorization/i],
  ["a provider's key", /\.apiKey\b|\bprovider\.key\b|\bmodel\.key\b|\.api_key\b/],
  ["OAuth tokens", /\.access\b|\.refresh\b|\.token\b|\bsecret\b|\bbearer\b/i],
  ["the auth hook", /\bauth\s*[:(=]|["']auth["']/],
  ["V2 credential resolution", /connection\.resolve|\.credential\b|\bresolve\(\s*conn/],
  ["the harness client SDK", /\bclient\.(provider|config|auth)\b/],
  ["the shell helper", /\$`|\bBun\.\$/],
];

for (const [what, re] of credentialPatterns) {
  test(`the plugin source never touches ${what}`, () => {
    for (const f of files) {
      const hit = code(f).split("\n").findIndex((l) => re.test(l));
      assert.equal(hit, -1, `${f}:${hit + 1} matches ${re}`);
    }
  });
}

test("the plugin makes no network call of its own", () => {
  for (const f of files) {
    const c = code(f);
    assert.doesNotMatch(c, /\bfetch\s*\(/, f);
    assert.doesNotMatch(c, /node:(http|https|net|dgram|tls|dns)\b/, f);
    assert.doesNotMatch(c, /\bWebSocket\b|\bXMLHttpRequest\b/, f);
  }
});

test("the only program the plugin starts is trove", () => {
  const hits = files.filter((f) => /spawn|exec\(|execSync|child_process/.test(code(f)));
  assert.deepEqual(hits, ["bridge.ts"]);
});

test("the plugin logs only fixed text and codes, never a message", () => {
  const c = code("bridge.ts");
  for (const m of c.matchAll(/this\.#log\(([^)]*)\)/g)) {
    assert.doesNotMatch(m[1] ?? "", /\bmsg\b|\bline\b|\bchunk\b|\bbody\b|\bargs\b/, m[0]);
  }
});

// Rule 13: no provider or product brand in any name Trove creates (the package,
// its files and folders). "works with OpenCode" is fine; OpenCode is not on the list.
test("no file, folder or package name carries a provider brand", () => {
  const brands = ["claude", "anthropic", "codex", "openai"];
  const root = join(src, "..");
  const names: string[] = [];
  const walk = (dir: string) => {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      if (e.name === "node_modules") continue;
      names.push(e.name);
      if (e.isDirectory()) walk(join(dir, e.name));
    }
  };
  walk(root);
  names.push(JSON.parse(readFileSync(join(root, "package.json"), "utf8")).name as string);
  for (const n of names) for (const b of brands) assert.ok(!n.toLowerCase().includes(b), `${n} carries the brand ${b}`);
});
