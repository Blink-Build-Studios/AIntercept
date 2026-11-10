import type { Meta, StoryObj } from "@storybook/react";
import details from "../../api/samples/request_details/normal";
import { detailRow } from "./drawerData";
import { DetailSection } from "./DetailSection";
import { codexResponseRows } from "../AgentSectionHead/agentSpecimens";

const meta = {
  title: "Components/Drawer/DetailSection",
  component: DetailSection,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div style={{ width: 400 }}>{Story()}</div>],
} satisfies Meta<typeof DetailSection>;
export default meta;
type Story = StoryObj<typeof meta>;

const section = (title: string) => {
  const s = details.data.sections.find((x) => x.title === title) ?? details.data.sections[0];
  if (!s) throw new Error("sample has no sections");
  return { title: s.title, accent: s.accent, rows: s.rows.map(detailRow), className: "border border-line bg-surface" };
};
export const Default: Story = { args: section("Request settings") };
/** Passed checks read ✓ (cross-check & posture). */
export const WithChecks: Story = { args: section("Cross-check & posture") };
/** An agent's own section, after the common ones (§11J screens 2). */
export const Agent: Story = { args: { title: "response", agent: "codex", rows: codexResponseRows } };
