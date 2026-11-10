import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { RuleTester } from "eslint";
import tseslint from "typescript-eslint";
import local from "./index.mjs";

RuleTester.afterAll = afterAll;
RuleTester.describe = describe;
RuleTester.it = it;
const tester = new RuleTester({
  languageOptions: { parser: tseslint.parser, parserOptions: { ecmaFeatures: { jsx: true } } },
});

// A throwaway tree.
const root = mkdtempSync(join(tmpdir(), "eslint-rules-"));
const at = (...parts) => {
  const d = join(root, "src", ...parts.slice(0, -1));
  mkdirSync(d, { recursive: true });
  return join(d, parts[parts.length - 1]);
};
const good = at("components", "Good", "Good.tsx");
writeFileSync(at("components", "Good", "Good.stories.tsx"), "export default {};");
const bad = at("components", "Bad", "Bad.tsx");
const screen = at("screens", "PersonScreen", "PersonScreen.tsx");
writeFileSync(at("screens", "PersonScreen", "PersonScreen.stories.tsx"), "export default {};");
const bareScreen = at("screens", "BareScreen", "BareScreen.tsx");
const page = at("pages", "PersonPage", "PersonPage.tsx");
const lib = at("lib", "format.ts");

tester.run("story-for-component", local.rules["story-for-component"], {
  valid: [
    { filename: good, code: "export function Good() { return <p />; }" },
    { filename: bad, code: "export const helper = 1; function Internal() { return null; }" },
    { filename: lib, code: "export function Format() {}" },
    { filename: screen, code: "export function PersonScreen() { return <main />; }" },
    { filename: page, code: "export function PersonPage() { return <PersonScreen />; }" }, // pages need no story
  ],
  invalid: [
    { filename: bad, code: "export function Bad() { return <p />; }", errors: [{ messageId: "missing" }] },
    { filename: bad, code: "export const Bad = () => <p />;", errors: [{ messageId: "missing" }] },
    { filename: bareScreen, code: "export function BareScreen() { return <main />; }", errors: [{ messageId: "missing" }] },
  ],
});

tester.run("layer-boundaries", local.rules["layer-boundaries"], {
  valid: [
    { filename: page, code: 'import { useScopedQuery } from "../../api/hooks/useScopedQuery"; import { useParams } from "react-router-dom"; export function PersonPage() { return <PersonScreen />; }' },
    { filename: page, code: "export function PersonPage() { return <><PersonScreen /></>; }" },
    { filename: screen, code: 'import type { Schemas } from "../../api/types"; import { Button } from "../../components/Button/Button"; export function PersonScreen() { return <main />; }' },
    { filename: screen, code: 'import type { Region } from "../../lib/region"; import { RegionState } from "../../components/RegionState/RegionState"; export function PersonScreen() { return null; }' },
    { filename: good, code: 'import type { Schemas } from "../../api/types"; export function Good() { return null; }' },
    { filename: join(root, "src", "components", "Good", "Good.stories.tsx"), code: 'import { useStatus } from "../../api/hooks/useStatus"; export const S = {};' },
  ],
  invalid: [
    { filename: screen, code: 'import { useScopedQuery } from "../../api/hooks/useScopedQuery"; export function PersonScreen() { return null; }', errors: [{ messageId: "impure" }] },
    { filename: good, code: 'import { useQuery } from "@tanstack/react-query"; export function Good() { return null; }', errors: [{ messageId: "impure" }] },
    { filename: good, code: 'import { getJson } from "../../api/client"; export function Good() { return null; }', errors: [{ messageId: "impure" }] },
    { filename: screen, code: 'import { Link } from "react-router-dom"; export function PersonScreen() { return null; }', errors: [{ messageId: "impure" }] },
    { filename: page, code: 'import { Button } from "../../components/Button/Button"; export function PersonPage() { return <PersonScreen />; }', errors: [{ messageId: "pageImport" }] },
    { filename: page, code: "export function PersonPage() { return <div><PersonScreen /></div>; }", errors: [{ messageId: "pageMarkup" }] },
    { filename: screen, code: 'import type { Region } from "../../lib/region"; export function PersonScreen() { return null; }', errors: [{ messageId: "regionWithoutState" }] },
  ],
});
