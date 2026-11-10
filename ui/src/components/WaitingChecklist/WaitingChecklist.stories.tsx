import type { Meta, StoryObj } from "@storybook/react";
import probesOnly from "../../api/samples/status/probes_only";
import waiting from "../../api/samples/status/waiting";
import { WaitingChecklist, waitingSteps } from "./WaitingChecklist";

const meta = {
  title: "Components/Chrome/WaitingChecklist",
  component: WaitingChecklist,
  parameters: { layout: "padded" },
  decorators: [(Story) => <div style={{ width: 882 }}>{Story()}</div>],
} satisfies Meta<typeof WaitingChecklist>;
export default meta;
type Story = StoryObj<typeof meta>;

const at = (s: typeof waiting) => ({ steps: waitingSteps(s.data, new Date(s.meta.asOf)), checkedAt: s.meta.asOf });
/** The proxy's probes arrive; no real request yet. */
export const Default: Story = { args: at(probesOnly) };
/** Nothing has arrived yet, not even a probe. */
export const NothingYet: Story = { args: at(waiting) };
