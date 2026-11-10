// Hover latency on the timelines (person and session pages): sweeps the pointer
// across each timeline and reports how long the page takes to respond to it,
// from the Event Timing API (each pointer event's input-to-next-paint time).
// Fails when a page's p95 is over LIMIT_MS (a frame or so above the 16 ms floor).
//   node parity/hover-latency.mjs [ui-url]      (default http://localhost:5173; `make up` or `make remote` first)
import { chromium } from "playwright";

const ui = process.argv[2] ?? "http://localhost:5173";
const LIMIT_MS = 50;
const api = `${ui}/api/v1`;
const people = (await (await fetch(`${api}/people`)).json()).data;
const person = [...people].sort((a, b) => b.sessions - a.sessions)[0]?.personId;
const sessions = person ? (await (await fetch(`${api}/people/${person}/sessions`)).json()).data : [];
const session = [...sessions].sort((a, b) => (b.modelCalls ?? 0) - (a.modelCalls ?? 0))[0]?.sessionId;
const pages = [...(person ? [["Person", `/people/${person}`]] : []), ...(session ? [["Session", `/sessions/${session}`]] : [])];

const browser = await chromium.launch();
const pct = (xs, p) => (xs.length ? xs[Math.min(xs.length - 1, Math.floor((p / 100) * xs.length))] : 0);
let worst = 0;
for (const [name, path] of pages) {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  await page.addInitScript(() => {
    window.__ev = [];
    window.__on = false;
    new PerformanceObserver((l) => l.getEntries().forEach((e) => window.__on && window.__ev.push(e.duration))).observe({
      type: "event",
      durationThreshold: 16,
    });
  });
  await page.goto(`${ui}${path}`);
  await page.waitForLoadState("networkidle", { timeout: 30000 }).catch(() => {});
  await page.waitForSelector(".tl-mark", { timeout: 15000 });
  await page.evaluate(() => (window.__on = true));
  const marks = await page.locator(".tl-mark").count();
  const boxes = await page.locator(".tl-body").evaluateAll((els) => els.map((e) => { const r = e.getBoundingClientRect(); return [r.left, r.top, r.width, r.height]; }));
  const t0 = Date.now();
  let moves = 0;
  for (const [left, top, width, height] of boxes) {
    for (const yf of [0.25, 0.5, 0.75]) {
      for (let x = left + 140; x < left + width - 4; x += 6) {
        await page.mouse.move(x, top + height * yf);
        moves++;
      }
    }
  }
  await page.waitForTimeout(500);
  const evs = await page.evaluate(() => window.__ev);
  const ds = [...(evs ?? [])].sort((a, b) => a - b);
  worst = Math.max(worst, pct(ds, 95));
  console.log(
    `${name.padEnd(8)} ${marks} marks · ${moves} moves in ${((Date.now() - t0) / 1000).toFixed(1)} s · ` +
      `slow events (>16 ms): ${ds.length} · p50 ${pct(ds, 50).toFixed(0)} ms · p95 ${pct(ds, 95).toFixed(0)} ms · max ${(ds.at(-1) ?? 0).toFixed(0)} ms`,
  );
  await page.close();
}
await browser.close();
if (worst > LIMIT_MS) {
  console.log(`✕ p95 ${worst.toFixed(0)} ms is over ${LIMIT_MS} ms`);
  process.exit(1);
}
console.log(`✓ p95 at most ${worst.toFixed(0)} ms (limit ${LIMIT_MS} ms)`);
