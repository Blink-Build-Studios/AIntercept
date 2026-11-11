// ESLint for the aintercept UI. Project rules are in eslint-rules/; the styling
// and copy rules are in scripts/lint-ui.mjs. Both run in CI (make check).
import js from "@eslint/js";
import jsxA11y from "eslint-plugin-jsx-a11y";
import reactHooks from "eslint-plugin-react-hooks";
import globals from "globals";
import tseslint from "typescript-eslint";
import local from "./eslint-rules/index.mjs";

// Number, date and duration formatting lives in lib/format.ts (handoff §3.3).
const FORMATTING = [
  { property: "toLocaleString", message: "Format numbers and dates with lib/format.ts (handoff §3.3)." },
  { property: "toLocaleDateString", message: "Format dates with lib/format.ts (handoff §3.3)." },
  { property: "toLocaleTimeString", message: "Format times with lib/format.ts (handoff §3.3)." },
  { property: "toFixed", message: "Format numbers with lib/format.ts (handoff §3.3)." },
  { property: "toPrecision", message: "Format numbers with lib/format.ts (handoff §3.3)." },
];

export default tseslint.config(
  { ignores: ["dist/", ".vite/", "storybook-static/", "public/", "src/api/spec.ts", "src/api/samples/", "openapi.json"] },
  js.configs.recommended,
  ...tseslint.configs.strict,
  {
    files: ["**/*.{ts,tsx}"],
    languageOptions: { globals: { ...globals.browser } },
    plugins: { "react-hooks": reactHooks, "jsx-a11y": jsxA11y, local },
    rules: {
      ...reactHooks.configs.recommended.rules,
      ...jsxA11y.flatConfigs.strict.rules,
      "local/story-for-component": "error",
      "local/layer-boundaries": "error",
      "no-restricted-properties": ["error", ...FORMATTING],
      "no-restricted-syntax": [
        "error",
        { selector: "NewExpression[callee.name='Intl']", message: "Format with lib/format.ts (handoff §3.3)." },
        { selector: "MemberExpression[object.name='Intl']", message: "Format with lib/format.ts (handoff §3.3)." },
        // react/forbid-dom-props for `title`, without the plugin (handoff §6B.1).
        {
          selector: "JSXOpeningElement[name.name=/^[a-z]/] > JSXAttribute[name.name='title']",
          message: "No native title: explain with <Tooltip> (handoff §6B.1).",
        },
      ],
      // Network access only through the typed client.
      "no-restricted-globals": ["error", { name: "fetch", message: "Use api/client.ts (getJson) so errors and timeouts are uniform." }],
      // API shapes come from the generated contract, through api/types.ts.
      "no-restricted-imports": [
        "error",
        {
          patterns: [
            { group: ["**/api/spec", "**/spec"], message: "Import API types from api/types.ts (generated from the API)." },
          ],
        },
      ],
      "@typescript-eslint/no-non-null-assertion": "error",
    },
  },
  {
    files: ["src/lib/format.ts"],
    rules: { "no-restricted-properties": "off", "no-restricted-syntax": "off" },
  },
  { files: ["src/api/client.ts"], rules: { "no-restricted-globals": "off" } },
  { files: ["src/api/types.ts"], rules: { "no-restricted-imports": "off" } },
  {
    // Tests and stories may assert on raw values and build fixtures freely.
    files: ["**/*.test.{ts,tsx}", "**/*.stories.{ts,tsx}", ".storybook/**"],
    rules: { "no-restricted-properties": "off", "@typescript-eslint/no-non-null-assertion": "off" },
  },
  {
    files: ["**/*.mjs"],
    languageOptions: { globals: { ...globals.node, ...globals.vitest } },
  },
  {
    // Parity scripts run in Node and evaluate callbacks in the browser.
    files: ["parity/**/*.mjs"],
    languageOptions: { globals: { ...globals.node, ...globals.browser } },
  },
);
