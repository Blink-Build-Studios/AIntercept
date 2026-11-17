import type { Meta, StoryObj } from "@storybook/react";
import demo from "../../api/samples/person_summary/demo";
import { CoverageLine } from "./CoverageLine";

const meta = { title: "Components/Summary/CoverageLine", component: CoverageLine, parameters: { layout: "padded" } } satisfies Meta<
  typeof CoverageLine
>;
export default meta;
type Story = StoryObj<typeof meta>;

/** Nothing reported missing: the person summary's coverage now that Claude Code's analytics aren't kept (no
 * reports to compare with, so no gap can show). */
export const Complete: Story = { args: { coverage: demo.data.coverage } };
