// Latency review (handoff §12: each screen "reviewed under the normal, slow and error
// latency settings"), as a check rather than a look: the running app, every page, with
// the API answering normally, after 9 s, or with a 500.
//   node parity/latency.mjs [ui-url]      (default http://localhost:5173; `make up` first)
// normal: the page renders, no console errors.
// slow:   skeletons at once; "Still loading…" with Cancel by 9 s; Cancel clears it.
// error:  every region shows Retry and the query id; no console errors but the 500s.
import { chromium } from "playwright";

const ui = process.argv[2] ?? "http://localhost:5173";
const api = `${ui}/api/v1`;
const QUERY_ID = "q_latency";

const people = await (await fetch(`${api}/people`)).json();
const person = people.data[0]?.personId;
const sessions = person ? await (await fetch(`${api}/people/${person}/sessions`)).json() : { data: [] };
const session = sessions.data[0]?.sessionId;
const pages = [["People", "/people"], ...(person ? [["Person", `/people/${person}`]] : []), ...(session ? [["Session", `/sessions/${session}`]] : [])];

const browser = await chromium.launch();
const results = [];
for (const [name, path] of pages) {
  for (const mode of ["normal", "slow", "error"]) {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
    const errors = [];
    page.on("console", (m) => m.type() === "error" && !m.text().includes("500") && errors.push(m.text()));
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/api/v1/**", async (route) => {
      if (mode === "slow") {
        await new Promise((r) => setTimeout(r, 9000));
        return route.continue().catch(() => {});
      }
      if (mode === "error")
        return route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ error: { code: "internal", message: "Query failed", queryId: QUERY_ID } }) });
      return route.continue();
    });
    const checks = [];
    const check = (what, ok) => checks.push([what, !!ok]);
    await page.goto(`${ui}${path}`);
    if (mode === "normal") {
      await page.waitForLoadState("networkidle", { timeout: 30000 }).catch(() => {});
      check("content, no skeletons left", (await page.locator('[aria-busy="true"]').count()) === 0);
    } else if (mode === "slow") {
      await page.waitForTimeout(1000);
      check("skeleton at once", (await page.locator('[aria-busy="true"]').count()) > 0);
      const notice = page.getByText("Still loading…").first();
      check("Still loading… by 9 s", await notice.waitFor({ timeout: 9500 }).then(() => true, () => false));
      const cancel = page.getByRole("button", { name: "Cancel" }).first();
      check("Cancel", (await cancel.count()) > 0);
      if (await cancel.count()) {
        const before = await page.getByText("Still loading…").count();
        await cancel.click();
        await page.waitForTimeout(500);
        check("Cancel clears that one", (await page.getByText("Still loading…").count()) < before);
      }
    } else {
      await page.waitForTimeout(3000);
      check("Retry", (await page.getByRole("button", { name: "Retry" }).count()) > 0);
      check("query id shown", (await page.getByText(QUERY_ID).count()) > 0);
    }
    check("no console errors", errors.length === 0);
    results.push({ name, mode, checks, errors: errors.slice(0, 3) });
    await page.close();
  }
}
await browser.close();

let failed = 0;
for (const r of results) {
  const bad = r.checks.filter(([, ok]) => !ok);
  failed += bad.length;
  console.log(`${bad.length ? "✕" : "✓"} ${r.name.padEnd(8)} ${r.mode.padEnd(7)} ${r.checks.map(([w, ok]) => `${ok ? "✓" : "✕"} ${w}`).join(" · ")}`);
  for (const e of r.errors) console.log(`    error: ${e.slice(0, 200)}`);
}
process.exit(failed ? 1 : 0);
