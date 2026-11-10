// Enforces the Wire-only styling rule (docs/ui/BUILD_PLAN.md §2). Fails on:
//   - color literals outside src/design/tokens.css: hex, any CSS color function
//     (any case), and CSS named colors in color-bearing properties, attributes
//     and style objects (custom properties included);
//   - Tailwind escapes from the theme: arbitrary values (`p-[13px]`, `min-[900px]:`),
//     arbitrary properties (`[color:red]`, `hover:[color:red]`), custom-property
//     values (`bg-(--x)`), opacity modifiers on colors (`bg-ink/50`, `/(--x)`), and
//     the bare-number utilities the theme can't remove (`duration-200`,
//     `leading-6`; use duration-fast|base|slow and the type-* styles).
// Comments are ignored (found with strings taken into account). In JS/TS/MDX,
// Tailwind and hex checks only look inside string literals, where classes and
// colors live. Allowed: data-/aria-/group-/peer- attribute variants
// (`data-[state=open]:`). A line containing `lint-ui-ignore` is skipped
// (for the rare false positive, e.g. a hash route "#abc").
// Generated files (spec.ts, samples) are skipped. Tests: scripts/lint-ui.test.mjs.
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

// Color channels with one meaning each (handoff §3.2): only these components may
// use them. Everything else shows liveness or danger by rendering a Marker.
const C = "src/components/";
const RESERVED = [
  {
    re: /(?<![\w-])(?:[a-z-]+:)*(?:bg|text|border|fill|stroke|outline|ring|decoration)-live\b|var\(--live\)/g,
    owners: ["src/design/", `${C}LiveBadge/`, `${C}Marker/`, `${C}Timeline/`, `${C}Tooltip/`, `${C}WaitingChecklist/`, `${C}PersonRow/`],
    msg: "`live` is reserved for the live dot/badge (LiveBadge, Marker, Timeline now-line, the active setup step, a person seen now)",
  },
  {
    re: /(?<![\w-])(?:[a-z-]+:)*(?:bg|text|border|fill|stroke|outline|ring|decoration)-danger\b|var\(--danger\)/g,
    owners: ["src/design/", `${C}Marker/`, `${C}CoverageLine/`, `${C}StatTile/`, `${C}HeaderStat/`, `${C}Timeline/`, `${C}Notice/`, `${C}RequestDrawer/`, `${C}Tooltip/`, `${C}FilterChip/`, `${C}AboutThisData/`, `${C}StepRow/`],
    msg: "`danger` is reserved for destructive commands, coverage gaps and a long turn's failed commands (render a Marker)",
  },
  {
    re: /(?<![\w-])(?:[a-z-]+:)*(?:bg|text|border|fill|stroke|outline|ring|decoration)-agent-[a-z-]+|var\(--agent-[a-z-]+\)/g,
    owners: ["src/design/", `${C}AgentChip/`, `${C}AgentCard/`, `${C}Timeline/`, `${C}Sparkline/`],
    msg: "`agent-*` colors are reserved for agent identity (AgentChip, AgentCard, Timeline bands, Sparkline)",
  },
];

// The CSS named colors (CSS Color 4), minus transparent/currentcolor.
const NAMED_LIST =
  "aliceblue antiquewhite aqua aquamarine azure beige bisque black blanchedalmond blue blueviolet brown burlywood " +
  "cadetblue chartreuse chocolate coral cornflowerblue cornsilk crimson cyan darkblue darkcyan darkgoldenrod darkgray " +
  "darkgreen darkgrey darkkhaki darkmagenta darkolivegreen darkorange darkorchid darkred darksalmon darkseagreen " +
  "darkslateblue darkslategray darkslategrey darkturquoise darkviolet deeppink deepskyblue dimgray dimgrey dodgerblue " +
  "firebrick floralwhite forestgreen fuchsia gainsboro ghostwhite gold goldenrod gray green greenyellow grey honeydew " +
  "hotpink indianred indigo ivory khaki lavender lavenderblush lawngreen lemonchiffon lightblue lightcoral lightcyan " +
  "lightgoldenrodyellow lightgray lightgreen lightgrey lightpink lightsalmon lightseagreen lightskyblue lightslategray " +
  "lightslategrey lightsteelblue lightyellow lime limegreen linen magenta maroon mediumaquamarine mediumblue " +
  "mediumorchid mediumpurple mediumseagreen mediumslateblue mediumspringgreen mediumturquoise mediumvioletred " +
  "midnightblue mintcream mistyrose moccasin navajowhite navy oldlace olive olivedrab orange orangered orchid " +
  "palegoldenrod palegreen paleturquoise palevioletred papayawhip peachpuff peru pink plum powderblue purple " +
  "rebeccapurple red rosybrown royalblue saddlebrown salmon sandybrown seagreen seashell sienna silver skyblue " +
  "slateblue slategray slategrey snow springgreen steelblue tan teal thistle tomato turquoise violet wheat white " +
  "whitesmoke yellow yellowgreen";
const NAMED = new Set(NAMED_LIST.split(" "));
const COLOR_FUNC = /\b(?:rgba?|hsla?|hwb|lab|lch|oklab|oklch|color-mix|color|light-dark)\(/gi;
const HEX = /(?<=^|[\s:("'`,=])#[0-9a-fA-F]{3,8}\b/g;
// Keys/attributes that carry a color: CSS properties and custom properties, JS style keys, SVG attributes.
const COLOR_KEY =
  /(?:^|[\s;{,("'])(--[\w-]+|[a-zA-Z-]*(?:color|Color|background|Background|border|Border|outline|Outline|fill|Fill|stroke|Stroke|shadow|Shadow|gradient|Gradient)[\w-]*)\s*[:=]\s*/g;
const VARIANT_PREFIX = /^(?:(?:group|peer|in)-)*(?:data|aria|supports|has|not|nth|in|group|peer)$/;
// A Tailwind class starts a class list or follows whitespace, a quote (nested
// strings in template literals), a variant colon or `!`.
const CLASS_START = "(?<=^|[\\s:!\"'`])";

const TW = [
  {
    re: new RegExp(CLASS_START + String.raw`([a-z][\w-]*?)-\[[^\]\s]+\]`, "g"),
    allow: (m) => VARIANT_PREFIX.test(m[1]),
    msg: "Tailwind arbitrary value (add a token or a component CSS file)",
  },
  {
    re: /(?<=^|[\s:])!?\[[a-z-]+:[^\]\s]+\](?=$|[\s!])/g,
    msg: "Tailwind arbitrary property (add a token or a component CSS file)",
  },
  { re: new RegExp(CLASS_START + String.raw`[a-z][\w-]*-\((?:--|[a-z-]+:)[^)\s]*\)`, "g"), msg: "Tailwind custom-property value (use a theme utility)" },
  {
    re: new RegExp(CLASS_START + String.raw`[a-z][\w-]*\/(?:\d+|\[[^\]\s]+\]|\([^)\s]+\))(?=$|[\s"'])`, "g"),
    allow: (m) => !/^(?:bg|text|border(?:-[trblxy])?|fill|stroke|outline|ring|divide|decoration|accent|caret|shadow|from|via|to|placeholder)-/.test(m[0]),
    msg: "Tailwind color opacity modifier (mixes a non-Wire color)",
  },
  {
    // The theme keeps only Wire spacing (0, px, 0.5, 1, 2, 3, 4, 6, 8) and clears the
    // container sizes; any other number makes Tailwind emit invalid CSS that silently
    // does nothing. One-off sizes go in the component's CSS file.
    re: new RegExp(
      CLASS_START +
        String.raw`-?(?:p[xytrbl]?|m[xytrbl]?|gap(?:-[xy])?|space-[xy]|(?:min-|max-)?[wh]|size|basis|inset(?:-[xy])?|top|right|bottom|left|translate-[xy]|scroll-[mp][xytrbl]?)-(?!(?:0|px|0\.5|1|2|3|4|6|8)(?![\w.]))(?:\d[\w.]*|xs|sm|md|lg|\dxl|xl|prose)(?![\w/-])`,
      "g",
    ),
    msg: "non-Wire size (only 0, px, 0.5, 1, 2, 3, 4, 6, 8; use component CSS for one-off sizes)",
  },
  {
    re: new RegExp(CLASS_START + String.raw`(?:duration|delay|leading|tracking)-\d[\w.]*`, "g"),
    msg: "bare-number utility (use duration-fast|base|slow or a type-* style)",
  },
];

/**
 * Split source into code with comments blanked, plus the string-literal spans.
 * `lang` is "js" (also TSX/MDX), "css" or "html". Newlines are preserved.
 */
export function tokenize(text, lang) {
  const out = text.split("");
  const strings = [];
  const blank = (a, b) => {
    for (let k = a; k < b; k++) if (out[k] !== "\n") out[k] = " ";
  };
  let i = 0;
  while (i < text.length) {
    const two = text.slice(i, i + 2);
    if (lang === "html" && text.startsWith("<!--", i)) {
      const end = text.indexOf("-->", i + 4);
      const stop = end < 0 ? text.length : end + 3;
      blank(i, stop);
      i = stop;
    } else if (lang !== "html" && two === "/*") {
      const end = text.indexOf("*/", i + 2);
      const stop = end < 0 ? text.length : end + 2;
      blank(i, stop);
      i = stop;
    } else if (lang === "js" && two === "//" && text[i - 1] !== ":") {
      const end = text.indexOf("\n", i);
      const stop = end < 0 ? text.length : end;
      blank(i, stop);
      i = stop;
    } else if (lang === "js" && text[i] === "{" && text.startsWith("{/*", i)) {
      i++; // JSX comment: the /* branch blanks it next
    } else if (text[i] === '"' || text[i] === "'" || text[i] === "`") {
      const q = text[i];
      let j = i + 1;
      while (j < text.length && text[j] !== q && !(q !== "`" && text[j] === "\n")) j += text[j] === "\\" ? 2 : 1;
      strings.push([i + 1, Math.min(j, text.length)]);
      i = j + 1;
    } else {
      i++;
    }
  }
  return { code: out.join(""), strings };
}

const langOf = (rel) => (/\.css$/.test(rel) ? "css" : /\.html$/.test(rel) ? "html" : "js");

/** Violations in one file's text. `rel` is its path relative to ui/. */
export function checkSource(text, rel) {
  const lang = langOf(rel);
  const { code, strings } = tokenize(text, lang);
  const lineStarts = [0];
  for (let k = 0; k < text.length; k++) if (text[k] === "\n") lineStarts.push(k + 1);
  const lineOf = (pos) => {
    let lo = 0;
    let hi = lineStarts.length - 1;
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1;
      if (lineStarts[mid] <= pos) lo = mid;
      else hi = mid - 1;
    }
    return lo + 1;
  };
  const ignored = new Set(text.split("\n").flatMap((l, k) => (l.includes("lint-ui-ignore") ? [k + 1] : [])));
  const out = [];
  const add = (pos, msg, match) => {
    const line = lineOf(pos);
    if (!ignored.has(line)) out.push({ file: rel, line, msg, match: match.trim() });
  };
  const scan = (re, src, offset, msg, allow) => {
    re.lastIndex = 0;
    for (const m of src.matchAll(re)) if (!allow || !allow(m)) add(offset + m.index, msg, m[0]);
  };

  // Where colors and classes can appear: whole file for CSS/HTML, string literals for JS.
  const regions = lang === "js" ? strings.map(([a, b]) => [a, code.slice(a, b)]) : [[0, code]];
  for (const [offset, src] of regions) {
    scan(HEX, src, offset, "color literal (use a Wire token)");
    if (lang !== "css") for (const r of TW) scan(r.re, src, offset, r.msg, r.allow);
  }
  scan(COLOR_FUNC, code, 0, "color function (use a Wire token)");

  // Named colors after a color-bearing key: CSS declarations, style attributes,
  // JSX/SVG attributes and style objects. var()/url() and quoted font names are skipped.
  const inString = (pos) => strings.some(([a, b]) => pos >= a && pos < b);
  COLOR_KEY.lastIndex = 0;
  for (const m of code.matchAll(COLOR_KEY)) {
    // In JS the key must be code (a prop, attribute or object key), not text inside a string.
    if (lang === "js" && inString(m.index + m[0].indexOf(m[1]))) continue;
    const start = m.index + m[0].length;
    const rest = code.slice(start);
    const value = (rest[0] === '"' || rest[0] === "'" || rest[0] === "`"
      ? rest.slice(1, rest.indexOf(rest[0], 1) >>> 0)
      : rest.split(/[;}\n]/)[0]
    ).replace(/\b(?:var|url)\([^)]*\)/g, "");
    for (const w of value.split(/[^a-zA-Z]+/)) {
      if (NAMED.has(w.toLowerCase())) add(start, "named color (use a Wire token)", `${m[1]}: ${w}`);
    }
  }

  // Reserved color channels (handoff §3.2): only the listed components may use them.
  for (const { re, owners, msg } of RESERVED) {
    if (owners.some((o) => rel.startsWith(o))) continue;
    scan(re, code, 0, msg);
  }

  // Component CSS sits in @layer components, below Tailwind's utilities layer;
  // unlayered rules would silently override utilities on the same element.
  if (/^src\/components\/.+\.css$/.test(rel) && !/@layer\s+components\s*\{/.test(code)) {
    add(0, "component CSS must be wrapped in @layer components { … } (utilities must win)", rel);
  }

  // Copy (handoff §3.3): neutral voice and no emoji, in text the viewer can see.
  if (lang !== "css") {
    scan(/\byour\b/gi, code, 0, 'copy says "your": use a neutral voice (handoff §3.3)');
    scan(/\p{Extended_Pictographic}|️/gu, code, 0, "emoji (use the handoff's text glyphs, §3.2)");
  }
  return out;
}

const SKIP = [/^src\/api\/spec\.ts$/, /^src\/api\/samples\//, /^src\/design\/tokens\.css$/, /\.test\.m?[jt]sx?$/];
const ROOTS = ["src", ".storybook", "index.html"];
const SCANNED = /\.(css|[cm]?[jt]sx?|mdx|html)$/;

export function checkTree(uiRoot) {
  const files = [];
  const walk = (p) => {
    if (statSync(p).isDirectory()) for (const n of readdirSync(p)) walk(join(p, n));
    else files.push(p);
  };
  for (const r of ROOTS) {
    try {
      walk(join(uiRoot, r));
    } catch {
      /* missing root */
    }
  }
  return files
    .map((f) => relative(uiRoot, f))
    .filter((rel) => SCANNED.test(rel) && !SKIP.some((s) => s.test(rel)))
    .flatMap((rel) => checkSource(readFileSync(join(uiRoot, rel), "utf8"), rel));
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const found = checkTree(fileURLToPath(new URL("..", import.meta.url)));
  for (const v of found) console.error(`${v.file}:${v.line}: ${v.msg}: ${v.match}`);
  if (found.length) {
    console.error(`\n${found.length} styling violation(s).`);
    process.exit(1);
  }
  console.log("lint-ui: ok");
}
