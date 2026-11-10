// Accessibility pass (handoff §12: "Storybook a11y: zero violations"): every story,
// in both themes, through axe-core (WCAG 2 A and AA), from a built Storybook.
// The Canvas/ stories are parity sheets (specimens side by side), not UI, so they're skipped.
//   node parity/a11y.mjs <storybook-dir> [story-title-substring…]
// Writes parity/a11y-report.md; exits 1 on any violation.
import { readFileSync, statSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { createRequire } from "node:module";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const [storybookDir, ...only] = process.argv.slice(2);
if (!storybookDir) {
  console.error("usage: node parity/a11y.mjs <storybook-dir> [title filter…]");
  process.exit(2);
}
const here = (p) => fileURLToPath(new URL(p, import.meta.url));
// axe-core comes with @storybook/addon-a11y (a dev dependency); no package of its own.
const requireFromAddon = createRequire(createRequire(import.meta.url).resolve("@storybook/addon-a11y/package.json"));
const AXE = requireFromAddon.resolve("axe-core/axe.min.js");
const EXCEPTIONS = JSON.parse(readFileSync(here("../../docs/ui/a11y-exceptions.json"), "utf8")).exceptions;

const TYPES = { ".html": "text/html", ".js": "text/javascript", ".mjs": "text/javascript", ".css": "text/css", ".json": "application/json", ".svg": "image/svg+xml", ".woff2": "font/woff2" };
const server = createServer((req, res) => {
  const rel = normalize(decodeURIComponent(new URL(req.url, "http://x").pathname)).replace(/^(\.\.[/\\])+/, "");
  const file = join(storybookDir, rel === "/" ? "index.html" : rel);
  if (!statSync(file, { throwIfNoEntry: false })?.isFile()) return res.writeHead(404).end();
  res.writeHead(200, { "content-type": TYPES[extname(file)] ?? "application/octet-stream" }).end(readFileSync(file));
});
await new Promise((ok) => server.listen(0, "127.0.0.1", ok));
const url = `http://127.0.0.1:${server.address().port}`;

const index = JSON.parse(readFileSync(join(storybookDir, "index.json"), "utf8"));
const stories = Object.values(index.entries).filter(
  (e) => e.type === "story" && !e.title.startsWith("Canvas/") && (!only.length || only.some((o) => e.title.includes(o))),
);

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
const found = []; // { story, theme, rule, impact, help, nodes }
const exceptedBy = {}; // "rule within selector (status)" -> stories
let checked = 0;
for (const s of stories) {
  for (const theme of ["dark", "light"]) {
    await page.goto(`${url}/iframe.html?id=${s.id}&viewMode=story&globals=theme:${theme}`);
    await page.waitForSelector("#storybook-root > *", { timeout: 15000 }).catch(() => {});
    await page.waitForTimeout(250);
    await page.addScriptTag({ path: AXE });
    const result = await page.evaluate(async (exceptions) => {
      // eslint-disable-next-line no-undef
      const r = await axe.run(document.getElementById("storybook-root"), { runOnly: { type: "tag", values: ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"] } });
      // A node is excepted when it sits inside an exception's `within` for its rule.
      const excepted = (rule, target) => {
        const el = document.querySelector(target.join(" "));
        return exceptions.find((x) => x.rule === rule && el?.closest(x.within));
      };
      const violations = [];
      const kept = [];
      for (const v of r.violations) {
        const open = v.nodes.filter((n) => !excepted(v.id, n.target));
        for (const n of v.nodes) {
          const x = excepted(v.id, n.target);
          if (x) kept.push({ rule: v.id, within: x.within, status: x.status });
        }
        if (open.length) violations.push({ ...v, nodes: open });
      }
      return { violations, kept };
    }, EXCEPTIONS);
    for (const k of result.kept) (exceptedBy[`${k.rule} within ${k.within} (${k.status})`] ??= new Set()).add(`${s.title} › ${s.name}`);
    for (const v of result.violations) {
      found.push({
        story: `${s.title} › ${s.name}`,
        theme,
        rule: v.id,
        impact: v.impact,
        help: v.help,
        nodes: v.nodes.slice(0, 3).map((n) => n.target.join(" ")),
        detail: v.nodes[0] && `${v.nodes[0].html.slice(0, 220)} :: ${(v.nodes[0].failureSummary ?? "").replace(/\s+/g, " ").slice(0, 260)}`,
      });
    }
    checked++;
  }
}
await browser.close();
server.close();

const byRule = {};
for (const f of found) (byRule[f.rule] ??= []).push(f);
const lines = [`# Accessibility report`, "", `${stories.length} stories × 2 themes = ${checked} checks · ${found.length} violations`, ""];
if (Object.keys(exceptedBy).length) {
  lines.push("## Excepted (docs/ui/a11y-exceptions.json)", "");
  for (const [k, set] of Object.entries(exceptedBy)) lines.push(`- ${k}: ${set.size} stories`);
  lines.push("");
}
for (const [rule, fs] of Object.entries(byRule).sort((a, b) => b[1].length - a[1].length)) {
  lines.push(`## ${rule} (${fs.length}): ${fs[0].help}`, "", `First: \`${fs[0].detail}\``, "");
  for (const f of fs.slice(0, 40)) lines.push(`- ${f.story} (${f.theme}, ${f.impact}): ${f.nodes.join(" · ")}`);
  if (fs.length > 40) lines.push(`- … and ${fs.length - 40} more`);
  lines.push("");
}
writeFileSync(here("./a11y-report.md"), lines.join("\n"));
console.log(lines.slice(0, 3).join("\n"));
for (const [rule, fs] of Object.entries(byRule)) console.log(`  ${rule}: ${fs.length}`);
process.exit(found.length ? 1 : 0);
