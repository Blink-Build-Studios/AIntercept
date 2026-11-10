// Pins the token lint against every bypass and false positive found in review
// (two rounds). Add a case here whenever the lint is changed.
import { checkSource } from "./lint-ui.mjs";

const flagged = (src, file = "src/x.tsx") => checkSource(src, file).map((v) => v.match);

describe("lint-ui catches", () => {
  it.each([
    // color literals
    ["hex in a string", `const c = "#ff0000";`],
    ["hex in css", `.a { color: #f00; }`, "src/a.css"],
    ["rgb any case", `.a { color: RGB(1, 2, 3); }`, "src/a.css"],
    ["modern color functions", `.a { color: oklab(50% 0.1 0.1); background: color-mix(in srgb, red, blue); }`, "src/a.css"],
    ["hwb/lab/lch", `.a { color: hwb(1 2% 3%); fill: lab(50% 40 60); stroke: lch(50% 40 60); }`, "src/a.css"],
    ["named color", `.a { color: red; }`, "src/a.css"],
    ["named color in border shorthand", `.a { border: 1px solid white; }`, "src/a.css"],
    ["named color in a custom property", `:root { --accent: red; }`, "src/a.css"],
    ["named color in text-shadow", `.a { text-shadow: 0 0 2px hotpink; }`, "src/a.css"],
    ["named color in a gradient", `.a { background-image: linear-gradient(white, black); }`, "src/a.css"],
    ["stop-color", `.a { stop-color: navy; }`, "src/a.css"],
    ["html style attribute", `<p style="color: rebeccapurple">x</p>`, "index.html"],
    ["inline style color", `<p style={{ color: "red" }} />`],
    ["inline style borderLeft", `<p style={{ borderLeft: "2px solid tomato" }} />`],
    ["inline backgroundImage", `<p style={{ backgroundImage: "linear-gradient(red, blue)" }} />`],
    ["SVG fill/stroke attributes", `<path fill="red" stroke="black" />`],
    // Tailwind escapes
    ["arbitrary value", `<p className="p-[13px]" />`],
    ["arbitrary color", `<p className="text-[#fff]" />`],
    ["arbitrary breakpoint variant", `<p className="min-[900px]:p-2 max-[13px]:hidden" />`],
    ["arbitrary property", `<p className="[color:red] p-2" />`],
    ["arbitrary property behind a variant", `<p className="hover:[color:red]" />`],
    ["important arbitrary property", `<p className="[color:red]!" />`],
    ["custom property value", `<p className="bg-(--hot)" />`],
    ["typed custom value", `<p className="p-(length:13px)" />`],
    ["opacity modifier", `<p className="bg-ink/50" />`],
    ["arbitrary opacity modifier", `<p className="text-ink/[.3]" />`],
    ["custom-property opacity modifier", `<p className="text-ink/(--x)" />`],
    ["bare duration", `<p className="duration-200 delay-75" />`],
    ["bare leading", `<p className="leading-6" />`],
    ["classes in a template literal", "const c = `p-2 ${on ? 'bg-ink/50' : ''}`;"],
  ])("%s", (_, src, file) => expect(flagged(src, file).length).toBeGreaterThan(0));

  it("scans .storybook and .mdx files", () => {
    expect(flagged(`const c = "#abcdef";`, ".storybook/preview.tsx")).toHaveLength(1);
    expect(flagged(`<div className="p-[3px]" />`, "src/Intro.mdx")).toHaveLength(1);
  });

  it('is not blinded by "/*" inside a string', () => {
    expect(flagged(`const glob = "src/*"; const c = "#ff0000"; // */`)).toEqual(["#ff0000"]);
  });
});

describe("lint-ui allows", () => {
  it.each([
    ["comments with issue numbers", `// see issue #123 and PR #4686\n/* #fff */ const a = 1;`],
    ["JSX comments", `<div>{/* text-[#fff] */}</div>`],
    ["hex-like text outside strings", `<p>Fixed in PR #1234</p>`],
    ["in-page anchors in a path", `<a href="/people#add">x</a>`],
    ["urls containing #", "const u = `http://x/#fff`;"],
    ["data variants", `<p className="data-[state=open]:opacity-50" />`],
    ["aria and group variants", `<p className="aria-[expanded=true]:block group-data-[open]:hidden peer-aria-[x]:p-2" />`],
    ["Wire utilities", `<p className="bg-surface text-ink-muted border-line type-data p-3 duration-fast rounded-0" />`],
    ["var() in inline style", `<p style={{ color: "var(--ink)", left: \`\${x}%\` }} />`],
    ["non-color style values", `<p style={{ border: "none", outline: "0", background: "transparent", color: "currentColor" }} />`],
    ["config objects with color-like keys", `const ch = { color: "model", fill: "solid" };`],
    ["urls with //", `const u = "http://127.0.0.1:8787";`],
    ["array indexing and division", `const v = rows[i]; const w = obj["key"]; const r = a/2;`],
    ["percent widths", `<p className="w-1/2" />`],
  ])("%s", (_, src) => expect(flagged(src)).toEqual([]));

  it("allows var() colors in css", () => {
    expect(flagged(`.a { color: var(--ink); border: 1px solid var(--line); --x: var(--red); }`, "src/a.css")).toEqual([]);
  });

  it("honors lint-ui-ignore", () => {
    expect(flagged(`navigate("#abc"); // lint-ui-ignore: hash route`)).toEqual([]);
  });
});

describe("lint-ui round-3 false positives", () => {
  it.each([
    ["color words inside an attribute string", `<b aria-label="color: red team" />`],
    ["route paths with to-/from- segments", `fetch("/api/v1/to-do/1"); fetch("/from-a/2");`],
    ["chained variant prefixes", `<p className="in-data-[active=true]:p-2 group-aria-[x]:hidden" />`],
  ])("%s", (_, src) => expect(flagged(src)).toEqual([]));
});

describe("lint-ui reserved channels and copy", () => {
  it("allows a channel only in its owners", () => {
    expect(flagged(`<span className="text-live" />`, "src/components/LiveBadge/LiveBadge.tsx")).toEqual([]);
    expect(flagged(`<span className="text-live" />`, "src/components/SessionRow/SessionRow.tsx")).toEqual(["text-live"]);
    expect(flagged(`<span className="hover:border-danger" />`, "src/screens/PersonPage/PersonPage.tsx")).toEqual(["hover:border-danger"]);
    expect(flagged(`<span className="bg-agent-codex" />`, "src/components/AgentChip/AgentChip.tsx")).toEqual([]);
    const css = (decl) => `@layer components {\n  .x { ${decl} }\n}`;
    expect(flagged(css("color: var(--danger);"), "src/components/PromptRow/PromptRow.css")).toEqual(["var(--danger)"]);
    expect(flagged(css("color: var(--danger);"), "src/components/Marker/Marker.css")).toEqual([]);
  });

  it("bans 'your' in copy, any case", () => {
    expect(flagged(`<p>Your sessions</p>`)).toEqual(["Your"]);
    expect(flagged(`const t = "see your data";`)).toEqual(["your"]);
    expect(flagged(`const yourName = 1; // your`)).toEqual([]);
  });

  it("bans emoji but allows the handoff's glyphs", () => {
    expect(flagged(`<p>done 🎉</p>`)).toHaveLength(1);
    expect(flagged(`<p>ok ✔️</p>`).length).toBeGreaterThan(0);
    expect(flagged(`<p>▾ ▸ › ⤷ ● ↯ ✕ ▲ ✓ ○ ⇣ — · ≈</p>`)).toEqual([]);
  });
});

describe("lint-ui Wire sizes", () => {
  it.each([
    ["basis-40"], ["w-64"], ["max-w-md"], ["p-5"], ["gap-10"], ["h-12"], ["-mt-7"], ["md:px-5"], ["size-9"], ["max-w-2xl"],
  ])("flags %s", (cls) => expect(flagged(`<p className="${cls}" />`)).toEqual([cls.replace(/^\w+:/, "")]));
  it.each([["p-3 gap-2 px-4 py-1 m-0 h-px w-full w-1/2 size-2 mt-0.5 basis-full min-w-0 inset-0 -mt-1 h-8"]])(
    "allows Wire values, fractions and keywords",
    (cls) => expect(flagged(`<p className="${cls}" />`)).toEqual([]),
  );
});

describe("lint-ui component CSS layer", () => {
  it("requires @layer components in component CSS", () => {
    expect(checkSource(".x { color: var(--ink); }", "src/components/X/X.css").map((v) => v.msg)).toEqual([
      "component CSS must be wrapped in @layer components { … } (utilities must win)",
    ]);
    expect(checkSource("@layer components {\n  .x { color: var(--ink); }\n}", "src/components/X/X.css")).toEqual([]);
    expect(checkSource(".x { color: var(--ink); }", "src/design/global.css")).toEqual([]);
  });
});
