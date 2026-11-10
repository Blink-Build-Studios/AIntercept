// Project rules for the aintercept UI (docs/ui/BUILD_PLAN.md §2, §11).
//
// Layers (the Page/Screen pattern, as in django-app-template):
//   src/pages/*Page.tsx      API wiring and URL state; renders exactly one *Screen; no story.
//   src/screens/*Screen.tsx  pure: all data via props typed from the API; stories feed samples.
//   src/components/**        pure building blocks.
import { existsSync } from "node:fs";
import { basename, dirname, join } from "node:path";

const layer = (file) =>
  /\/src\/pages\//.test(file) ? "page" : /\/src\/screens\//.test(file) ? "screen" : /\/src\/components\//.test(file) ? "component" : null;
const isSource = (file) => /\.tsx?$/.test(file) && !/\.(stories|test)\.tsx?$/.test(file);
const PASCAL = /^[A-Z][A-Za-z0-9]*$/;

/** Exported PascalCase function/const names in the module (its components). */
function exportedComponents(program) {
  const names = [];
  for (const node of program.body) {
    if (node.type !== "ExportNamedDeclaration" || !node.declaration) continue;
    const d = node.declaration;
    if (d.type === "FunctionDeclaration" && d.id && PASCAL.test(d.id.name)) names.push(d.id.name);
    if (d.type === "VariableDeclaration")
      for (const v of d.declarations) if (v.id.type === "Identifier" && PASCAL.test(v.id.name)) names.push(v.id.name);
  }
  return names;
}

const storyForComponent = {
  meta: {
    type: "problem",
    docs: { description: "Every component and screen file has a sibling <Name>.stories.tsx (pages don't: they only wire data)" },
    schema: [],
    messages: { missing: "{{component}} has no story file: add {{story}} (one story per state in docs/ui/requirements.json)." },
  },
  create(context) {
    const file = context.filename;
    const l = layer(file);
    if ((l !== "component" && l !== "screen") || !/\.tsx$/.test(file) || !isSource(file)) return {};
    return {
      Program(program) {
        const components = exportedComponents(program);
        if (!components.length) return;
        const story = join(dirname(file), basename(file).replace(/\.tsx$/, ".stories.tsx"));
        if (!existsSync(story)) {
          context.report({ node: program, messageId: "missing", data: { component: components.join(", "), story: basename(story) } });
        }
      },
    };
  },
};

// Imports a pure layer may not use: data fetching and routing belong to pages.
const DATA_IMPORT = /(?:^|\/)api\/(?:hooks\/|client(?:$|\.))|^@tanstack\/react-query$|^react-router(?:-dom)?$/;

const layerBoundaries = {
  meta: {
    type: "problem",
    docs: { description: "Screens and components are pure; pages only wire data into one screen" },
    schema: [],
    messages: {
      impure: "{{layer}}s are pure: '{{source}}' (data fetching / routing) belongs in a page (src/pages/*Page.tsx).",
      pageImport: "Pages render a screen only: import UI from src/screens, not '{{source}}'.",
      pageMarkup: "Pages render exactly one *Screen and no markup; '<{{name}}>' belongs in the screen.",
      regionWithoutState: "This screen takes Region props but doesn't import RegionState: render each region through it (handoff §6A.3).",
    },
  },
  create(context) {
    const file = context.filename;
    const l = layer(file);
    if (!l || !isSource(file)) return {};
    let importsRegion = null;
    let importsRegionState = false;
    return {
      ImportDeclaration(node) {
        const source = String(node.source.value);
        if ((l === "screen" || l === "component") && DATA_IMPORT.test(source)) {
          context.report({ node, messageId: "impure", data: { layer: l, source } });
        }
        if (l === "page" && /\/components\//.test(source)) context.report({ node, messageId: "pageImport", data: { source } });
        if (/\/lib\/region$/.test(source) && node.specifiers.some((s) => s.imported?.name === "Region")) importsRegion ??= node;
        if (/\/RegionState(\/|$)/.test(source)) importsRegionState = true;
      },
      JSXOpeningElement(node) {
        if (l !== "page") return;
        const name = node.name.type === "JSXIdentifier" ? node.name.name : node.name.type === "JSXMemberExpression" ? node.name.property.name : "";
        if (!/Screen$/.test(name) && name !== "Fragment") context.report({ node, messageId: "pageMarkup", data: { name } });
      },
      "Program:exit"() {
        if (l === "screen" && importsRegion && !importsRegionState) context.report({ node: importsRegion, messageId: "regionWithoutState" });
      },
    };
  },
};

export default { rules: { "story-for-component": storyForComponent, "layer-boundaries": layerBoundaries } };
