// @vitest-environment node
// Checks the UI against docs/ui/requirements.json (the definition of done, from
// the handoff). A requirement applies once its file exists: a component's story
// states, a screen's stories, a test once every component it needs exists.
// Stories are read statically (named exports); Storybook itself doesn't run.
import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const UI = fileURLToPath(new URL("..", import.meta.url));
const req = JSON.parse(readFileSync(join(UI, "..", "docs", "ui", "requirements.json"), "utf8"));
const norm = (s) => s.toLowerCase().replace(/[^a-z0-9]/g, "");

const componentPath = (c) => c.path ?? `src/components/${c.name}/${c.name}.tsx`;
const screenPath = (s) => `src/screens/${s.name}/${s.name}.tsx`;
const pagePath = (p) => `src/pages/${p.name}/${p.name}.tsx`;
const exists = (rel) => existsSync(join(UI, rel));
const storyExports = (rel) => {
  const story = join(UI, rel.replace(/\.tsx$/, ".stories.tsx"));
  if (!existsSync(story)) return null;
  return new Set([...readFileSync(story, "utf8").matchAll(/^export const (\w+)/gm)].map((m) => norm(m[1])));
};

function files(dir, re) {
  const out = [];
  const walk = (p) => {
    if (!existsSync(p)) return;
    if (statSync(p).isDirectory()) for (const n of readdirSync(p)) walk(join(p, n));
    else if (re.test(p)) out.push(relative(UI, p));
  };
  walk(join(UI, dir));
  return out;
}

const built = req.components.filter((c) => exists(componentPath(c)));

describe("requirements: components", () => {
  it("every component file is listed in requirements.json", () => {
    const listed = new Set([...req.components.map(componentPath), ...req.screens.map(screenPath), ...req.pages.map(pagePath)]);
    const unlisted = [...files("src/components", /\.tsx$/), ...files("src/screens", /\.tsx$/), ...files("src/pages", /\.tsx$/)].filter(
      (f) => !/\.(stories|test)\.tsx$/.test(f) && !listed.has(f),
    );
    expect(unlisted, "add these to docs/ui/requirements.json (or move them out of components/)").toEqual([]);
  });

  it("every component folder is exported from components/index.ts (handoff §12)", () => {
    const index = readFileSync(join(UI, "src", "components", "index.ts"), "utf8");
    const folders = readdirSync(join(UI, "src", "components")).filter((d) => existsSync(join(UI, "src", "components", d, `${d}.tsx`)));
    const missing = folders.filter((d) => !index.includes(`export * from "./${d}/${d}";`));
    expect(missing, "add these to src/components/index.ts").toEqual([]);
  });

  it.each(built.map((c) => [c.name, c]))("%s has a story for every required state", (_, c) => {
    const exportsFound = storyExports(componentPath(c));
    expect(exportsFound, `${componentPath(c).replace(/\.tsx$/, ".stories.tsx")} is missing`).not.toBeNull();
    const missing = c.states.filter((s) => !exportsFound.has(norm(s)));
    expect(missing, `stories missing for ${c.name} states`).toEqual([]);
  });
});

describe("requirements: screens", () => {
  const screens = req.screens.filter((s) => exists(screenPath(s)));
  it.each(screens.length ? screens.map((s) => [s.name, s]) : [["(none built yet)", null]])("%s has every screen story", (_, s) => {
    if (!s) return;
    const found = storyExports(screenPath(s)) ?? new Set();
    expect(s.states.filter((st) => !found.has(norm(st)))).toEqual([]);
  });
});

describe("requirements: pages", () => {
  const pages = req.pages.filter((p) => exists(pagePath(p)));
  it.each(pages.length ? pages.map((p) => [p.name, p]) : [["(none built yet)", null]])("%s renders its screen", (_, p) => {
    if (!p) return;
    expect(readFileSync(join(UI, pagePath(p)), "utf8")).toMatch(new RegExp(`<${p.screen}[\\s/>]`));
  });
});

describe("requirements: tests", () => {
  const testSources = files("src", /\.test\.tsx?$/).map((f) => readFileSync(join(UI, f), "utf8")).join("\n");
  const builtNames = new Set(built.map((c) => c.name));
  const due = req.tests.filter((t) => t.requires.every((n) => builtNames.has(n)));
  it.each(due.length ? due.map((t) => [t.id, t]) : [["(none due yet)", null]])("%s exists", (_, t) => {
    if (!t) return;
    expect(testSources.includes(`[${t.id}]`), `add a test named "[${t.id}] ${t.title}"`).toBe(true);
  });
});

describe("requirements: manifest", () => {
  it("names are unique and every test's components exist in the manifest", () => {
    const names = req.components.map((c) => c.name);
    expect(new Set(names).size).toBe(names.length);
    for (const t of req.tests) for (const n of t.requires) expect(names, `test ${t.id}`).toContain(n);
  });
});
