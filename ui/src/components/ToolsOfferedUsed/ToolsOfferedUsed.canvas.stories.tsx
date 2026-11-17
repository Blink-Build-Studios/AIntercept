// Parity specimens: CompTurns.dc.html's ToolsOfferedUsed (handoff §11S), the panel 880px wide,
// and its three rows measured in place.
import type { Meta, StoryObj } from "@storybook/react";
import { ParityTags } from "../../test/ParityTag";
import { ToolsOfferedUsed } from "./ToolsOfferedUsed";
import { sessionTools } from "./specimens";

/** CompTurns' tools panel (also on its sheet story). */
export function ToolsSpecimen() {
  const row = (n: number) => `[data-parity='ToolsOfferedUsed'] [role=row]:nth-child(${n + 1})`;
  return (
    <ParityTags
      targets={[
        { spec: "ToolsOfferedUsed/row", selector: row(1) },
        { spec: "ToolsOfferedUsed/rowMcp", selector: row(2) },
        { spec: "ToolsOfferedUsed/rowUnused", selector: row(3) },
      ]}
    >
      <div data-parity="ToolsOfferedUsed" className="grid items-start" style={{ gridTemplateColumns: "882px" }}>
        <ToolsOfferedUsed rows={sessionTools} changes={2} />
      </div>
    </ParityTags>
  );
}

const meta = { title: "Canvas/Turns/ToolsOfferedUsed", parameters: { layout: "padded" }, excludeStories: ["ToolsSpecimen"] } satisfies Meta;
export default meta;

export const Canvas: StoryObj = { render: () => <ToolsSpecimen /> };
