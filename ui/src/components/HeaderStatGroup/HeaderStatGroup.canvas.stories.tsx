// Parity specimens: the canvas's own content for HeaderStatGroup/default
// (CompSummary.dc.html) and HeaderStatGroup/withHover (CompTableHover.dc.html), measured against the canvas by `make parity`.
import type { Meta, StoryObj } from "@storybook/react";
import { modelCallsTip } from "../../lib/tips";
import { AgentPlacesSheet } from "../AgentSectionHead/CompAgents.canvas.stories";
import { ProvidersSessionSheet } from "../ProviderMark/CompProviders.canvas.stories";
import { HeaderStat } from "../HeaderStat/HeaderStat";
import { HeaderStatGroup } from "./HeaderStatGroup";

export default { title: "Canvas/Summary/HeaderStatGroup", parameters: { layout: "padded" } } satisfies Meta;

export const Canvas: StoryObj = {
  render: () => (
    <div className="flex flex-col items-start gap-3">
      <div data-parity="HeaderStatGroup/default" className="inline-grid items-start p-4">
        <HeaderStatGroup>
          <HeaderStat label="Role" value="admin" />
          <HeaderStat label="Plan" value="max · 20x" />
          <HeaderStat label="Last seen" value="now" live />
        </HeaderStatGroup>
      </div>
      {/* CompTableHover: the group fills its 656px cell. */}
      <div data-parity="HeaderStatGroup/withHover" className="grid items-start" style={{ gridTemplateColumns: "656px" }}>
        <HeaderStatGroup>
          <HeaderStat label="Role" value="admin" />
          <HeaderStat label="Plan" value="max · 20x" />
          <HeaderStat label="Model calls" value="640" qualifier="+94 not captured" qualifierTone="gap" tip={modelCallsTip(640, 94)} data-force="hover" />
        </HeaderStatGroup>
      </div>
      {/* CompAgents: HeaderStatGroup/withAgentExtras. */}
      <AgentPlacesSheet />
      {/* CompProviders: the session header's one wrapping group (§11M 1.10). */}
      <ProvidersSessionSheet />
    </div>
  ),
};
