// Render every canvas artboard with Claude Design's runtime and list the
// rendered data-spec values, to check them against spec-manifest.json.
import { chromium } from "playwright";
import { readFileSync } from "node:fs";

const [base, manifestPath] = process.argv.slice(2);
const m = JSON.parse(readFileSync(manifestPath, "utf8"));
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
const render = async (file) => {
  await page.goto(`${base}/${file}`);
  await page.waitForFunction(() => document.querySelectorAll("[data-spec]").length > 0, null, { timeout: 15000 }).catch(() => {});
  await page.waitForTimeout(300);
  return page.$$eval("[data-spec]", (els) => els.map((e) => e.getAttribute("data-spec")));
};
let problems = 0;
for (const [file, specs] of Object.entries(m.sheets)) {
  const got = await render(file);
  const missing = specs.filter((s) => !got.includes(s));
  const extra = [...new Set(got)].filter((s) => !specs.includes(s));
  problems += missing.length;
  console.log(`${file}: rendered ${got.length}, manifest ${specs.length}, missing ${missing.length}${missing.length ? " " + JSON.stringify(missing.slice(0, 6)) : ""}${extra.length ? `, not in manifest ${JSON.stringify(extra.slice(0, 6))}` : ""}`);
}
for (const [file, counts] of Object.entries(m.screens)) {
  const got = await render(file);
  const tally = {};
  for (const s of got) tally[s] = (tally[s] ?? 0) + 1;
  const off = Object.entries(counts).filter(([s, n]) => (tally[s] ?? 0) < n).map(([s, n]) => `${s} ${tally[s] ?? 0}/${n}`);
  problems += off.length;
  console.log(`${file}: rendered ${got.length} instances of ${Object.keys(tally).length} specs; short: ${off.length ? off.slice(0, 8).join(", ") : "none"}`);
}
await browser.close();
console.log(problems ? `\n${problems} problem(s)` : "\nall manifest specs render");
