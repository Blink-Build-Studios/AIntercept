import type { Meta, StoryObj } from "@storybook/react";
import summary from "../../api/samples/person_summary/demo";
import { formatCount } from "../../lib/format";
import { KeyValueList } from "./KeyValueList";
import { sourceTip } from "../../lib/tips";

const fp = summary.data.footprint;
const meta = {
  title: "Components/Summary/KeyValueList",
  component: KeyValueList,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div style={{ width: 448 }}>{Story()}</div>],
} satisfies Meta<typeof KeyValueList>;
export default meta;
type Story = StoryObj<typeof meta>;

const rows = [
  { k: "Prompts", v: fp.prompts === null ? null : formatCount(fp.prompts) },
  { k: "Tool calls", v: fp.toolCalls === null ? null : formatCount(fp.toolCalls), unknownReason: "not extracted yet" },
  { k: "Commands", d: fp.topCommandWords.slice(0, 3).map((w) => `${w.word} ${w.count}`).join(" · "), v: fp.commands },
  { k: "Destructive", d: `${fp.destructive.rmRf} rm -rf · ${fp.destructive.outsideCwd} outside cwd`, v: fp.destructive.total },
  { k: "Commits · pushes", v: `${fp.codeShipped.commits} · ${fp.codeShipped.pushes}` },
  { k: "PRs", v: fp.codeShipped.prs.length ? fp.codeShipped.prs.map((n) => `#${n}`).join(", ") : null, unknownReason: "none linked" },
];

/** Footprint, from the demo sample. */
export const Default: Story = { args: { title: "Footprint · all sessions", accent: "skills", rows } };
export const WithAction: Story = { args: { ...Default.args!, title: "Footprint · all sessions", rows, action: { label: "all requests ›", href: "#requests" } } };
export const Loading: Story = { args: { title: "Capabilities · seen in any session", rows: [], loading: true } };
/** Rows with a source explain it on hover (§6B.7). */
export const RowHover: Story = {
  args: { title: "Request shape", rows: [{ k: "thinking budget", v: "10,000", tip: sourceTip("request body · thinking.budget_tokens", { n: 412, of: 640, per: "calls" }) }] },
};
