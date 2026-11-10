// make parity: compare our components with the Claude Design canvas, numerically.
//
//   node parity/parity.mjs <storybook-static-dir> [component ...]
//
// Serves the built Storybook and the canvas sheets (with the Claude Design
// runtime, docs/ui/handoff/canvas/runtime/) itself, on ephemeral local ports.
// For every data-spec on the canvas sheets (docs/ui/handoff/canvas/final/
// spec-manifest.json) whose component has a parity story here (a story
// titled "Canvas/<group>/<Component>"; one story may hold several components'
// specimens, each in a [data-parity="<spec>"] wrapper), render both and compare the root box and computed
// styles, then text elements paired by text and painted marks paired by order.
// Differences listed in docs/ui/deviations.json are accepted; anything else
// fails. Writes parity/report.md and parity/report.json.
import { readFileSync, statSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { describeElement } from "./measure.mjs";

const here = (p) => fileURLToPath(new URL(p, import.meta.url));
const [storybookDir, ...only] = process.argv.slice(2);
const CANVAS = here("../../docs/ui/handoff/canvas/");

const TYPES = { ".html": "text/html", ".js": "text/javascript", ".mjs": "text/javascript", ".css": "text/css", ".json": "application/json", ".svg": "image/svg+xml", ".woff2": "font/woff2", ".png": "image/png" };
/** A static server over the first of `roots` that has the requested file. */
async function serve(roots) {
  const server = createServer((req, res) => {
    const rel = normalize(decodeURIComponent(new URL(req.url, "http://x").pathname)).replace(/^(\.\.[/\\])+/, "");
    const file = roots.map((r) => join(r, rel === "/" ? "index.html" : rel)).find((f) => statSync(f, { throwIfNoEntry: false })?.isFile());
    if (!file) return res.writeHead(404).end();
    res.writeHead(200, { "content-type": TYPES[extname(file)] ?? "application/octet-stream" }).end(readFileSync(file));
  });
  await new Promise((ok) => server.listen(0, "127.0.0.1", ok));
  return { server, url: `http://127.0.0.1:${server.address().port}` };
}
const canvasHost = await serve([join(CANVAS, "final"), join(CANVAS, "runtime")]);
const storybookHost = await serve([storybookDir]);
const canvasUrl = canvasHost.url;
const sbUrl = storybookHost.url;
const manifest = JSON.parse(readFileSync(here("../../docs/ui/handoff/canvas/final/spec-manifest.json"), "utf8"));
// Specimens the manifest marks illustrative (sketches, or superseded by a later sheet): not measured.
const illustrativeIn = (sheet, spec) => (manifest.illustrative?.[sheet] ?? []).includes(spec);
const deviations = JSON.parse(readFileSync(here("../../docs/ui/deviations.json"), "utf8")).deviations;

const PX_TOLERANCE = 0.5;
const num = (v) => (typeof v === "string" && /^-?[\d.]+px$/.test(v) ? parseFloat(v) : null);

// Properties that paint nothing on both sides: a zero-width border side's
// style/color, an outline that is off. Tailwind's reset (`border: 0 solid`)
// differs from the canvas's (`none`) there without any visible effect.
const invisible = (k, a, b) => {
  const side = /^border-(top|right|bottom|left)-(style|color)$/.exec(k)?.[1];
  if (side) return [a, b].every((s) => parseFloat(s[`border-${side}-width`] ?? "0") === 0);
  if (/^outline-(color|offset)$/.test(k)) return [a, b].every((s) => s["outline-style"] === "none");
  return false;
};

// Values that compute differently but render the same: both sheets and stories are
// left-to-right, so `text-align: start` is `left`.
const same = (k, va, vb) => k === "text-align" && [va, vb].every((v) => v === "left" || v === "start");

function compareStyles(path, a, b, out) {
  for (const [k, va] of Object.entries(a)) {
    if (invisible(k, a, b)) continue;
    const vb = b[k];
    if (same(k, va, vb)) continue;
    const na = num(va);
    const nb = num(vb);
    if (na !== null && nb !== null ? Math.abs(na - nb) > PX_TOLERANCE : va !== vb) out.push({ path, prop: k, canvas: va, ours: vb });
  }
}
function compareBox(path, a, b, out, keys = ["w", "h", "x", "y"]) {
  for (const k of keys) if (Math.abs(a[k] - b[k]) > PX_TOLERANCE) out.push({ path, prop: `box.${k}`, canvas: a[k], ours: b[k] });
}

function compare(canvas, ours) {
  const diffs = [];
  compareBox("root", canvas.box, ours.box, diffs, ["w", "h"]);
  compareStyles("root", canvas.style, ours.style, diffs);
  if (canvas.text !== ours.text) diffs.push({ path: "root", prop: "text", canvas: canvas.text, ours: ours.text });
  const pool = [...ours.texts];
  for (const t of canvas.texts) {
    const i = pool.findIndex((o) => o.text === t.text);
    if (i < 0) {
      diffs.push({ path: `text "${t.text}"`, prop: "missing", canvas: t.text, ours: null });
      continue;
    }
    const [o] = pool.splice(i, 1);
    compareBox(`text "${t.text}"`, t.box, o.box, diffs);
    compareStyles(`text "${t.text}"`, t.style, o.style, diffs);
  }
  for (const o of pool) diffs.push({ path: `text "${o.text}"`, prop: "extra", canvas: null, ours: o.text });
  canvas.marks.forEach((m, i) => {
    const o = ours.marks[i];
    if (!o) return diffs.push({ path: `mark[${i}]`, prop: "missing", canvas: `${m.box.w}x${m.box.h}`, ours: null });
    compareBox(`mark[${i}]`, m.box, o.box, diffs);
    compareStyles(`mark[${i}]`, m.style, o.style, diffs);
  });
  for (let i = canvas.marks.length; i < ours.marks.length; i++) diffs.push({ path: `mark[${i}]`, prop: "extra", canvas: null, ours: "mark" });
  return diffs;
}

// "*" in a path or prop matches any run of characters ("border-*-color").
const glob = (pattern = "*", value) =>
  new RegExp(`^${pattern.split("*").map((p) => p.replace(/[.+?^${}()|[\]\\]/g, "\\$&")).join(".*")}$`).test(value);
// The deviation rule covering a diff, if any. A rule's status is "approved"
// (with a source) or "pending" (awaiting a design decision: reported as ⚠, not ✓).
const ruleFor = (spec, d) =>
  [spec, `${spec.split("/")[0]}/*`].flatMap((key) => deviations[key] ?? []).find((r) => glob(r.path, d.path) && glob(r.prop, d.prop));

const browser = await chromium.launch();
// One time zone for both sides: our stories build their times in the browser's zone and the
// canvas states its labels in the viewer's, so UTC keeps a DST change (Nov 1 in US zones)
// out of the comparison, whoever runs it.
const page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, timezoneId: "UTC" });
await page.addInitScript({ content: `window.__describe = ${describeElement.toString()};` });
const results = [];
// Measure the resting frame: no animation or transition mid-flight (spinners, heartbeats).
const settle = async () => {
  await page.addStyleTag({ content: "*,*::before,*::after{animation:none!important;transition:none!important}" });
  await page.evaluate(() => document.fonts.ready);
};
// A specimen name can recur on several sheets (the canvas reuses some). Each
// occurrence is measured; a story tells them apart with "<spec>@<Sheet>"
// (e.g. "Button/disabledWithReason@CompTooltip"), else one specimen serves all.
const sheetsOf = {};
for (const [f, specs] of Object.entries(manifest.sheets)) for (const s of specs) (sheetsOf[s] ??= []).push(f);
const sheetName = (f) => f.replace(/\.dc\.html$/, "");

// Parity stories, from the built Storybook's index: component name -> story id.
const index = JSON.parse(readFileSync(join(storybookDir, "index.json"), "utf8"));
const stories = Object.fromEntries(
  Object.values(index.entries)
    .filter((e) => e.type === "story" && e.title.startsWith("Canvas/"))
    .map((e) => [e.title.split("/").pop(), e.id]),
);

for (const [component, storyId] of Object.entries(stories)) {
  if (only.length && !only.includes(component)) continue;
  const specs = Object.keys(sheetsOf).filter((s) => s.split("/")[0] === component);
  await page.goto(`${sbUrl}/iframe.html?id=${storyId}&viewMode=story&globals=theme:dark`);
  await page.waitForSelector("[data-parity]", { timeout: 15000 });
  await settle();
  // [data-parity] wraps its specimen (the first child); [data-parity-self] is the specimen
  // (set by stories on an element deep inside a component: a hovered band, the axis readout).
  const oursBy = await page.$$eval("[data-parity], [data-parity-self]", (els) =>
    Object.fromEntries(
      els.map((e) =>
        e.hasAttribute("data-parity-self")
          ? [e.getAttribute("data-parity-self"), window.__describe(e)]
          : [e.getAttribute("data-parity"), window.__describe(e.firstElementChild)],
      ),
    ),
  );
  const bySheet = {};
  for (const s of specs) for (const f of sheetsOf[s]) (bySheet[f] ??= []).push(s);
  for (const [sheet, list] of Object.entries(bySheet)) {
    await page.goto(`${canvasUrl}/${sheet}`);
    await page.waitForSelector("[data-spec]", { timeout: 15000 });
    await settle();
    const canvasBy = await page.$$eval("[data-spec]", (els) =>
      Object.fromEntries(
        els.map((e) => [e.getAttribute("data-spec"), e.dataset.specIllustrative === "true" ? "illustrative" : window.__describe(e)]),
      ),
    );
    for (const plain of list) {
      const spec = sheetsOf[plain].length > 1 ? `${plain}@${sheetName(sheet)}` : plain;
      // An intermediate moment or annotation (handoff §11A.10): checked by eye, not measured.
      if (canvasBy[plain] === "illustrative" || illustrativeIn(sheet, plain)) {
        results.push({ spec, status: "illustrative", diffs: [] });
        continue;
      }
      if (!canvasBy[plain]) continue;
      const ours = oursBy[spec] ?? oursBy[plain];
      if (!ours) {
        results.push({ spec, status: "no-story", diffs: [] });
        continue;
      }
      const diffs = compare(canvasBy[plain], ours);
      const rules = diffs.map((d) => ruleFor(spec, d) ?? ruleFor(plain, d));
      const open = diffs.filter((_, i) => !rules[i]);
      const pending = rules.filter((r) => r && r.status !== "approved");
      const status = open.length ? "fail" : pending.length ? "pending" : "pass";
      results.push({ spec, status, diffs: open, accepted: diffs.length - open.length, pending: pending.map((r) => r.reason) });
    }
  }
}
await browser.close();
canvasHost.server.close();
storybookHost.server.close();

const fails = results.filter((r) => r.status === "fail" || r.status === "no-story");
const pendingR = results.filter((r) => r.status === "pending");
const illustrative = results.filter((r) => r.status === "illustrative");
// Canvas components with no parity story yet (whole run only).
const uncovered = only.length ? [] : [...new Set(Object.keys(sheetsOf).map((s) => s.split("/")[0]))].filter((c) => !stories[c]).sort();
const md = [
  `# Design parity report`,
  ``,
  `${results.length - illustrative.length} specimens · ${results.filter((r) => r.status === "pass").length} pass · ${fails.length} fail` +
    (pendingR.length ? ` · ${pendingR.length} pending decision` : "") +
    (illustrative.length ? ` · ${illustrative.length} illustrative (not measured)` : ""),
  ...(uncovered.length ? [``, `Not yet covered (no Canvas story): ${uncovered.join(", ")}`] : []),
  ``,
  ...results.map((r) =>
    r.status === "illustrative"
      ? `- ◌ ${r.spec} (illustrative, not measured)`
      : r.status === "pending"
      ? `- ⚠ ${r.spec}: pending decision: ${[...new Set(r.pending)].join(" / ")}`
      : r.status === "pass"
      ? `- ✓ ${r.spec}${r.accepted ? ` (${r.accepted} accepted deviation${r.accepted > 1 ? "s" : ""})` : ""}`
      : `- ✕ ${r.spec}: ${r.status === "no-story" ? "no parity specimen" : ""}\n` +
        r.diffs.map((d) => `  - ${d.path} · ${d.prop}: canvas \`${d.canvas}\` · ours \`${d.ours}\``).join("\n"),
  ),
].join("\n");
// PARITY_REPORT=<dir> keeps concurrent runs from sharing a report.
const out = process.env.PARITY_REPORT ?? here(".");
writeFileSync(join(out, "report.md"), md + "\n");
writeFileSync(join(out, "report.json"), JSON.stringify(results, null, 2) + "\n");
console.log(md.split("\n").slice(0, 3).join("\n"));
process.exit(fails.length ? 1 : 0);
