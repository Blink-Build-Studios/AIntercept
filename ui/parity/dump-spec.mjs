// Prints canvas specimens as a compact outline (tag, classes, box, text, the styles that
// differ from the parent), from the rendered sheet: what to build when a round adds them.
//   node parity/dump-spec.mjs <Sheet.dc.html> <spec>[ <spec>…]    (a spec ending in * matches a prefix)
import { createServer } from "node:http";
import { readFileSync, statSync } from "node:fs";
import { extname, join, normalize } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const [sheet, ...want] = process.argv.slice(2);
const CANVAS = fileURLToPath(new URL("../../docs/ui/handoff/canvas/", import.meta.url));
const TYPES = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".json": "application/json", ".woff2": "font/woff2" };
const server = createServer((req, res) => {
  const rel = normalize(decodeURIComponent(new URL(req.url, "http://x").pathname)).replace(/^(\.\.[/\\])+/, "");
  for (const root of [join(CANVAS, "final"), join(CANVAS, "runtime")]) {
    const f = join(root, rel);
    if (statSync(f, { throwIfNoEntry: false })?.isFile())
      return res.writeHead(200, { "content-type": TYPES[extname(f)] ?? "application/octet-stream" }).end(readFileSync(f));
  }
  res.writeHead(404).end();
});
await new Promise((ok) => server.listen(0, "127.0.0.1", ok));
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
await page.goto(`http://127.0.0.1:${server.address().port}/${sheet}`);
await page.waitForSelector("[data-spec]", { timeout: 15000 });
await page.evaluate(() => document.fonts.ready);
const out = await page.evaluate((want) => {
  const KEYS = ["display", "position", "width", "height", "font-size", "font-weight", "line-height", "font-family", "color", "background-color", "background-image",
    "border-top", "border-right", "border-bottom", "border-left", "border-radius", "padding", "margin", "gap", "opacity", "cursor", "text-decoration-line", "text-decoration-color", "text-underline-offset", "outline", "box-shadow", "letter-spacing", "text-transform", "white-space", "overflow", "text-overflow", "justify-content", "align-items"];
  const match = (s) => want.some((w) => (w.endsWith("*") ? s.startsWith(w.slice(0, -1)) : s === w));
  const lines = [];
  const walk = (el, depth, parentCs) => {
    if (depth > 9) return;
    const cs = getComputedStyle(el);
    const r = el.getBoundingClientRect();
    if (r.width === 0 && r.height === 0 && el.children.length === 0) return;
    const diff = KEYS.filter((k) => !parentCs || cs.getPropertyValue(k) !== parentCs.getPropertyValue(k) || ["width", "height"].includes(k))
      .filter((k) => !["width", "height"].includes(k))
      .map((k) => `${k}:${cs.getPropertyValue(k).replace(/rgb\((\d+), (\d+), (\d+)\)/g, (_, a, b, c) => "#" + [a, b, c].map((n) => (+n).toString(16).padStart(2, "0")).join(""))}`)
      .filter((s) => !/:(none|normal|0px|auto|visible|start|static|rgba\(0, 0, 0, 0\))$/.test(s))
      .filter((s) => !/^(border-\w+:0px none|outline:.* none|text-decoration-color:)/.test(s));
    const text = [...el.childNodes].filter((n) => n.nodeType === 3).map((n) => n.textContent.trim()).join(" ").trim();
    const cls = el.className && typeof el.className === "string" ? "." + el.className.trim().split(/\s+/).slice(0, 3).join(".") : "";
    lines.push(`${"  ".repeat(depth)}${el.tagName.toLowerCase()}${cls} ${Math.round(r.width)}x${Math.round(r.height)}${text ? ` "${text.slice(0, 60)}"` : ""}  ${diff.join("; ")}`);
    for (const c of el.children) walk(c, depth + 1, cs);
  };
  for (const el of document.querySelectorAll("[data-spec]")) {
    const s = el.getAttribute("data-spec");
    if (!match(s)) continue;
    lines.push(`== ${s}${el.dataset.specIllustrative === "true" ? " (illustrative)" : ""}`);
    walk(el, 0, null);
  }
  // Runs of siblings that differ only in their text collapse to one line and a count.
  const sig = (l) => l.replace(/ "[^"]*"/, "").replace(/ \d+x\d+/, "");
  const out = [];
  for (const l of lines) {
    const last = out[out.length - 1];
    if (last && sig(last.l) === sig(l)) last.n++;
    else out.push({ l, n: 1 });
  }
  return out.map((o) => (o.n > 1 ? `${o.l}   (×${o.n})` : o.l)).join("\n");
}, want);
console.log(out);
await browser.close();
server.close();
